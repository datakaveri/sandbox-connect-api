package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sandbox-backend-service/pkg/constants"
)

var dashboardNotebookGVR = schema.GroupVersionResource{Group: "kubeflow.org", Version: "v1beta1", Resource: "notebooks"}

type dashboardNotebookKey struct{ namespace, name string }
type dashboardRuntimeSnapshot struct {
	Notebooks  map[dashboardNotebookKey]*unstructured.Unstructured
	ObservedAt *time.Time
	Stale      bool
	// Absence is confirmed only for the DB identities observed before this list.
	// Include the row ID so reusing a sandbox name does not reuse an old absence.
	ConfirmedMissing map[dashboardNotebookKey]string
}

type dashboardRuntimeCache struct {
	mu         sync.Mutex
	checkedAt  time.Time
	snapshot   dashboardRuntimeSnapshot
	refreshing chan struct{}
}

func (app *application) dashboardRuntime(ctx context.Context, now time.Time, expected ...dashboardNotebook) dashboardRuntimeSnapshot {
	if ctx.Err() != nil {
		return dashboardRuntimeSnapshot{}
	}
	c := &app.dashboardCache
	for {
		c.mu.Lock()
		if ctx.Err() != nil {
			c.mu.Unlock()
			return dashboardRuntimeSnapshot{}
		}
		if !c.checkedAt.IsZero() && now.Sub(c.checkedAt) < 30*time.Second && !c.snapshot.needsAbsenceRefresh(expected) {
			snapshot := c.snapshot
			c.mu.Unlock()
			if snapshot.ObservedAt != nil && now.Sub(*snapshot.ObservedAt) > 60*time.Second {
				return dashboardRuntimeSnapshot{ObservedAt: snapshot.ObservedAt}
			}
			return snapshot
		}
		if c.refreshing != nil {
			refreshing := c.refreshing
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return dashboardRuntimeSnapshot{}
			case <-refreshing:
				continue
			}
		}
		c.refreshing = make(chan struct{})
		c.mu.Unlock()
		break
	}
	defer func() {
		c.mu.Lock()
		close(c.refreshing)
		c.refreshing = nil
		c.mu.Unlock()
	}()
	// Only complete list results replace the cache. Partial pages cannot prove absence.
	callerCtx := ctx
	ctx, cancel := context.WithTimeout(callerCtx, 10*time.Second)
	defer cancel()
	items := map[dashboardNotebookKey]*unstructured.Unstructured{}
	var err error
	if app.k8sClient == nil || app.k8sClient.Dynamic == nil {
		err = fmt.Errorf("missing kubernetes client")
	} else {
		continuation := ""
		for {
			list, listErr := app.k8sClient.Dynamic.Resource(dashboardNotebookGVR).List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
			if listErr != nil {
				err = listErr
				break
			}
			for i := range list.Items {
				n := list.Items[i].DeepCopy()
				items[dashboardNotebookKey{n.GetNamespace(), n.GetName()}] = n
			}
			if list.GetContinue() == "" {
				break
			}
			if list.GetContinue() == continuation {
				err = fmt.Errorf("repeated kubernetes continuation")
				break
			}
			continuation = list.GetContinue()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// A canceled caller says nothing about Kubernetes health. Preserve the shared
	// snapshot and retry eligibility for the next request instead of caching its error.
	if callerCtx.Err() != nil || errors.Is(err, context.Canceled) {
		snapshot := c.snapshot
		if snapshot.ObservedAt != nil && now.Sub(*snapshot.ObservedAt) > 60*time.Second {
			return dashboardRuntimeSnapshot{ObservedAt: snapshot.ObservedAt}
		}
		snapshot.Stale = snapshot.Notebooks != nil
		return snapshot
	}
	c.checkedAt = now
	if err == nil {
		observed := now.UTC()
		missing := map[dashboardNotebookKey]string{}
		for _, n := range expected {
			key := dashboardNotebookKey{n.Namespace, n.Name}
			if n.LatestEvent == constants.StatusNotebookApplied && n.ID != "" && items[key] == nil {
				missing[key] = n.ID
			}
		}
		c.snapshot = dashboardRuntimeSnapshot{Notebooks: items, ObservedAt: &observed, ConfirmedMissing: missing}
	} else if c.snapshot.ObservedAt != nil && now.Sub(*c.snapshot.ObservedAt) <= 60*time.Second {
		c.snapshot.Stale = true
	} else {
		// Retain the last observation timestamp, but never serve arbitrarily old data.
		c.snapshot = dashboardRuntimeSnapshot{ObservedAt: c.snapshot.ObservedAt}
	}
	return c.snapshot
}

func (s dashboardRuntimeSnapshot) needsAbsenceRefresh(expected []dashboardNotebook) bool {
	// Failed refreshes retain the normal retry cooldown. They cannot confirm absence.
	if s.Notebooks == nil || s.Stale {
		return false
	}
	for _, n := range expected {
		key := dashboardNotebookKey{n.Namespace, n.Name}
		if n.LatestEvent == constants.StatusNotebookApplied && s.Notebooks[key] == nil &&
			(n.ID == "" || s.ConfirmedMissing[key] != n.ID) {
			return true
		}
	}
	return false
}

func dashboardStoredResources(n dashboardNotebook) (*DashboardSandboxResources, error) {
	cpu, err := resource.ParseQuantity(n.CPU)
	if err != nil || cpu.Sign() < 0 {
		return nil, fmt.Errorf("invalid cpu request")
	}
	mem, err := resource.ParseQuantity(n.Memory)
	if err != nil || mem.Sign() < 0 {
		return nil, fmt.Errorf("invalid memory request")
	}
	cores := cpu.AsApproximateFloat64()
	if math.IsInf(cores, 0) || math.IsNaN(cores) || n.GPURequest < 0 {
		return nil, fmt.Errorf("invalid resource quantity")
	}
	r := &DashboardSandboxResources{CPU: cores, Memory: mem.Value(), GPU: []DashboardGPUUnits{}}
	if n.ResourceType == "gpu" {
		if n.GPUType == nil || !strings.Contains(*n.GPUType, "/") {
			return nil, fmt.Errorf("missing GPU resource name")
		}
		if n.GPURequest > 0 {
			r.GPU = append(r.GPU, DashboardGPUUnits{ResourceName: *n.GPUType, Units: n.GPURequest})
		}
	}
	return r, nil
}

func dashboardResourcesMatch(n dashboardNotebook, obj *unstructured.Unstructured) bool {
	if obj == nil {
		return false
	}
	raw, found, err := unstructured.NestedMap(obj.Object, "spec", "template", "spec")
	if err != nil || !found {
		return false
	}
	var pod corev1.PodSpec
	if runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &pod) != nil {
		return false
	}
	var primary *corev1.Container
	for i := range pod.Containers {
		if pod.Containers[i].Name == n.Name {
			primary = &pod.Containers[i]
			break
		}
	}
	// Older templates may retain the literal primary name rather than the worker's
	// sandbox-specific name. Check that only after the current worker convention.
	if primary == nil {
		for i := range pod.Containers {
			if pod.Containers[i].Name == "notebook" {
				primary = &pod.Containers[i]
				break
			}
		}
	}
	if primary == nil && len(pod.Containers) == 1 {
		primary = &pod.Containers[0]
	}
	if primary == nil {
		return false
	}
	cpu, err := resource.ParseQuantity(n.CPU)
	if err != nil {
		return false
	}
	mem, err := resource.ParseQuantity(n.Memory)
	if err != nil {
		return false
	}
	liveCPU, liveMemory := primary.Resources.Requests[corev1.ResourceCPU], primary.Resources.Requests[corev1.ResourceMemory]
	if cpu.Cmp(liveCPU) != 0 || mem.Cmp(liveMemory) != 0 {
		return false
	}
	for name, qty := range primary.Resources.Requests {
		if (IsValidGPUResource(string(name)) || strings.Contains(string(name), "/mig-") || (n.GPUType != nil && string(name) == *n.GPUType)) && qty.Sign() > 0 {
			if n.GPUType == nil || string(name) != *n.GPUType {
				return false
			}
		}
	}
	if n.GPUType != nil && n.ResourceType == "gpu" {
		gpu := primary.Resources.Requests[corev1.ResourceName(*n.GPUType)]
		if gpu.Cmp(*resource.NewQuantity(n.GPURequest, resource.DecimalSI)) != 0 {
			return false
		}
	}
	return true
}
