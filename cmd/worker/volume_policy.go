package main

import (
	"context"
	"fmt"
	"log/slog"
	pathpkg "path"
	"sandbox-backend-service/pkg/constants"
	sandboxk8s "sandbox-backend-service/pkg/k8s"
	"sandbox-backend-service/pkg/utils"
	"strings"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	managedLabelKey   = sandboxk8s.ManagedPVCLabel
	notebookLabelKey  = sandboxk8s.NotebookPVCLabel
	volumeLabelKey    = sandboxk8s.VolumePVCLabel
	retentionLabelKey = sandboxk8s.RetentionPVCLabel
	retentionDelete   = sandboxk8s.RetentionDeleteWithNotebook
	retentionKeep     = sandboxk8s.RetentionKeep
)

var workerPVCGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}

func notebookWorkload(nb Notebook) string {
	if utils.CheckGPUResource(nb.GPUType, nb.GPURequest, nb.GPULimit) {
		return "gpu"
	}
	return "cpu"
}

func (w *worker) SelectWorkloadPolicy() error {
	workload := notebookWorkload(w.notebook)
	policy, ok := w.app.runtimeConfig.Workloads[workload]
	if !ok {
		return fmt.Errorf("no runtime policy configured for workload %s", workload)
	}
	w.policy = policy
	w.logger.Info("selected worker runtime policy", "workload", workload, "pvc_mount_count", len(policy.PVCMounts))
	return nil
}

func (w *worker) PreparePVCMounts() error {
	if err := w.SelectWorkloadPolicy(); err != nil {
		return err
	}

	resolved, err := w.resolvePVCMounts()
	if err != nil {
		return err
	}
	w.resolvedPVCMounts = nil
	availableMounts := map[string]ResolvedPVCMount{}

	// Validate platform-owned claims first, avoiding managed PVC churn when the
	// namespace storage prerequisites are not ready yet.
	for _, mount := range resolved {
		if mount.Managed {
			continue
		}
		if mount.NFS != nil {
			availableMounts[mount.Name] = mount
			continue
		}
		cfg := w.mountConfigByName(mount.Name)
		available, err := w.waitForExistingPVC(cfg, mount)
		if err != nil {
			return err
		}
		if available {
			availableMounts[mount.Name] = mount
		}
	}

	for _, mount := range resolved {
		if !mount.Managed {
			continue
		}
		if err := w.ensureManagedPVC(mount); err != nil {
			return err
		}
		w.resolvedPVCMounts = append(w.resolvedPVCMounts, mount)
		availableMounts[mount.Name] = mount
	}

	// Preserve configuration order in the generated pod while retaining the
	// progressive managed-mount list above for failure cleanup.
	w.resolvedPVCMounts = w.resolvedPVCMounts[:0]
	for _, mount := range resolved {
		if available, ok := availableMounts[mount.Name]; ok {
			w.resolvedPVCMounts = append(w.resolvedPVCMounts, available)
		}
	}

	if w.notebook.HasRuntimeAssets() {
		if _, ok := w.workspacePVCMount(); !ok {
			return fmt.Errorf("runtime assets require a workspace PVC mount for workload %s", notebookWorkload(w.notebook))
		}
	}
	return nil
}

func (w *worker) resolvePVCMounts() ([]ResolvedPVCMount, error) {
	values := templateValues{
		Namespace: w.notebook.Namespace, NotebookName: w.notebook.Name,
		PVCName: w.notebook.PVCname, StorageSize: w.notebook.StorageSize,
	}
	resolved := make([]ResolvedPVCMount, 0, len(w.policy.PVCMounts))
	for _, cfg := range w.policy.PVCMounts {
		claimName := ""
		if cfg.Source.Type != nfsVolumeSourceType {
			claimName = renderTemplate(cfg.Source.ClaimNameTemplate, values)
			if errs := validation.IsDNS1123Subdomain(claimName); len(errs) > 0 {
				return nil, fmt.Errorf("PVC mount %s rendered invalid claim name %q: %s", cfg.Name, claimName, strings.Join(errs, ", "))
			}
		}
		subPath := renderTemplate(cfg.SubPathTemplate, values)
		if subPath != "" && (pathpkg.IsAbs(subPath) || strings.Contains(subPath, "..") || pathpkg.Clean(subPath) != subPath) {
			return nil, fmt.Errorf("PVC mount %s rendered unsafe subPath %q", cfg.Name, subPath)
		}
		var spec map[string]any
		if cfg.Source.Type == managedPVCSourceType {
			rendered, ok := renderConfigValue(cfg.Source.Spec, values).(map[string]any)
			if !ok {
				return nil, fmt.Errorf("PVC mount %s has invalid managed spec", cfg.Name)
			}
			spec = rendered
		}
		resolved = append(resolved, ResolvedPVCMount{
			Name: cfg.Name, ClaimName: claimName, MountPath: cfg.MountPath,
			SubPath: subPath, ReadOnly: cfg.ReadOnly, Workspace: cfg.Workspace,
			Managed: cfg.Source.Type == managedPVCSourceType, NFS: cfg.Source.NFS,
			RetentionPolicy: cfg.Source.RetentionPolicy, Spec: spec,
		})
	}
	return resolved, nil
}

func renderConfigValue(value any, values templateValues) any {
	switch typed := value.(type) {
	case string:
		return renderTemplate(typed, values)
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = renderConfigValue(item, values)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = renderConfigValue(item, values)
		}
		return result
	default:
		return typed
	}
}

func (w *worker) mountConfigByName(name string) PVCMountConfig {
	for _, mount := range w.policy.PVCMounts {
		if mount.Name == name {
			return mount
		}
	}
	return PVCMountConfig{}
}

func (w *worker) waitForExistingPVC(cfg PVCMountConfig, mount ResolvedPVCMount) (bool, error) {
	timeout, err := w.app.runtimeConfig.ExternalPVCWaitTimeout()
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for {
		pvc, getErr := w.app.k8sClient.Dynamic.Resource(workerPVCGVR).Namespace(w.notebook.Namespace).Get(ctx, mount.ClaimName, metav1.GetOptions{})
		if getErr == nil {
			if err := validateExistingPVC(pvc, cfg.Source.Expected); err != nil {
				if !cfg.IsRequired() {
					w.logger.Warn("optional PVC is incompatible and will be omitted", "volume", mount.Name, "claim", mount.ClaimName, "error", err)
					return false, nil
				}
				return false, fmt.Errorf("required PVC %s (%s) is incompatible: %w", mount.Name, mount.ClaimName, err)
			}
			if !cfg.Source.ShouldWaitForBound() {
				return true, nil
			}
			phase, _, _ := unstructured.NestedString(pvc.Object, "status", "phase")
			if phase == "Bound" {
				return true, nil
			}
		} else if !k8serrors.IsNotFound(getErr) {
			w.logger.Warn("failed checking existing PVC", "volume", mount.Name, "claim", mount.ClaimName, "error", getErr)
		}

		select {
		case <-ctx.Done():
			if !cfg.IsRequired() {
				w.logger.Warn("optional PVC unavailable and will be omitted", "volume", mount.Name, "claim", mount.ClaimName, "timeout", timeout)
				return false, nil
			}
			return false, fmt.Errorf("required PVC %s (%s) was not available within %s", mount.Name, mount.ClaimName, timeout)
		case <-ticker.C:
		}
	}
}

func validateExistingPVC(pvc *unstructured.Unstructured, expected *PVCExpectationConfig) error {
	if timestamp := pvc.GetDeletionTimestamp(); timestamp != nil {
		return fmt.Errorf("PVC is terminating")
	}
	if expected == nil {
		return nil
	}
	if len(expected.AccessModes) > 0 {
		actual, _, err := unstructured.NestedStringSlice(pvc.Object, "spec", "accessModes")
		if err != nil {
			return fmt.Errorf("read accessModes: %w", err)
		}
		actualSet := map[string]struct{}{}
		for _, mode := range actual {
			actualSet[mode] = struct{}{}
		}
		for _, mode := range expected.AccessModes {
			if _, ok := actualSet[mode]; !ok {
				return fmt.Errorf("missing expected access mode %s", mode)
			}
		}
	}
	if expected.VolumeMode != "" {
		actual, found, err := unstructured.NestedString(pvc.Object, "spec", "volumeMode")
		if err != nil {
			return fmt.Errorf("read volumeMode: %w", err)
		}
		if !found || actual == "" {
			actual = "Filesystem"
		}
		if actual != expected.VolumeMode {
			return fmt.Errorf("volumeMode is %s, expected %s", actual, expected.VolumeMode)
		}
	}
	return nil
}

func (w *worker) ensureManagedPVC(mount ResolvedPVCMount) error {
	logger := w.logger.With("operation", "EnsureManagedPVC", "volume", mount.Name, "claim", mount.ClaimName)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), PVCTerminatingWaitTimeout)
	defer waitCancel()

	for {
		existing, err := w.app.k8sClient.Dynamic.Resource(workerPVCGVR).Namespace(w.notebook.Namespace).Get(waitCtx, mount.ClaimName, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			break
		}
		if err != nil {
			return fmt.Errorf("get managed PVC %s: %w", mount.ClaimName, err)
		}
		if existing.GetDeletionTimestamp() != nil {
			select {
			case <-waitCtx.Done():
				return fmt.Errorf("managed PVC %s remained terminating", mount.ClaimName)
			case <-time.After(PollInterval):
				continue
			}
		}
		if err := w.validateManagedPVCOwnership(existing, mount); err != nil {
			return err
		}
		logger.Info("reusing compatible managed PVC")
		return nil
	}

	labels := map[string]any{
		managedLabelKey: "true",
		volumeLabelKey:  mount.Name,
	}
	if mount.RetentionPolicy == retentionDeleteWithNotebook {
		labels[notebookLabelKey] = w.notebook.Name
		labels[retentionLabelKey] = retentionDelete
	} else {
		labels[retentionLabelKey] = retentionKeep
	}
	pvc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": mount.ClaimName, "namespace": w.notebook.Namespace, "labels": labels},
		"spec":     runtime.DeepCopyJSONValue(mount.Spec),
	}}
	ctx, cancel := context.WithTimeout(context.Background(), K8sCreationTimeout)
	defer cancel()
	return WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		_, err := w.app.k8sClient.Dynamic.Resource(workerPVCGVR).Namespace(w.notebook.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
		if k8serrors.IsAlreadyExists(err) {
			return constants.RetryStop, err
		}
		if err != nil {
			return constants.RetryContinue, err
		}
		return constants.RetryStop, nil
	})
}

func (w *worker) validateManagedPVCOwnership(pvc *unstructured.Unstructured, mount ResolvedPVCMount) error {
	labels := pvc.GetLabels()
	if labels[managedLabelKey] != "true" || labels[volumeLabelKey] != mount.Name {
		return fmt.Errorf("PVC %s already exists and is not owned by this configured volume", mount.ClaimName)
	}
	if mount.RetentionPolicy == retentionDeleteWithNotebook && labels[notebookLabelKey] != w.notebook.Name {
		return fmt.Errorf("PVC %s is owned by another notebook", mount.ClaimName)
	}
	return nil
}

func (w *worker) workspacePVCMount() (ResolvedPVCMount, bool) {
	for _, mount := range w.resolvedPVCMounts {
		if mount.Workspace {
			return mount, true
		}
	}
	return ResolvedPVCMount{}, false
}

func resolvedVolumeMountSpec(mount ResolvedPVCMount, mountPath string) map[string]any {
	spec := map[string]any{
		"name": mount.Name, "mountPath": mountPath, "readOnly": mount.ReadOnly,
	}
	if mount.SubPath != "" {
		spec["subPath"] = mount.SubPath
	}
	return spec
}

func (w *worker) DeletePreparedManagedPVCs() error {
	var firstErr error
	for _, mount := range w.resolvedPVCMounts {
		if !mount.Managed || mount.RetentionPolicy != retentionDeleteWithNotebook {
			continue
		}
		if err := w.deletePVCByName(mount.ClaimName, w.logger); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (w *worker) deletePVCByName(name string, logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), K8sDeletionTimeout)
	defer cancel()
	policy := metav1.DeletePropagationBackground
	err := w.app.k8sClient.Dynamic.Resource(workerPVCGVR).Namespace(w.notebook.Namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy})
	if k8serrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		logger.Error("failed deleting managed PVC", "claim", name, "error", err)
	}
	return err
}

func (w *worker) applyScheduling(spec map[string]any) error {
	policy := w.policy.Scheduling
	selector := map[string]any{}
	for key, value := range policy.NodeSelector {
		selector[key] = value
	}
	if override := policy.InstanceTypeOverride; override.Enabled {
		instanceType := ""
		if w.notebook.InstanceType != nil {
			instanceType = strings.TrimSpace(*w.notebook.InstanceType)
		}
		if instanceType == "" && override.Required {
			return fmt.Errorf("instance type is required by the %s workload runtime policy", notebookWorkload(w.notebook))
		}
		if instanceType != "" {
			selector[override.SelectorKey] = instanceType
		}
	}
	if len(selector) > 0 {
		spec["nodeSelector"] = selector
	}
	if len(policy.Affinity) > 0 {
		spec["affinity"] = runtime.DeepCopyJSONValue(policy.Affinity)
	}
	if len(policy.Tolerations) > 0 {
		tolerations := make([]any, len(policy.Tolerations))
		for i, value := range policy.Tolerations {
			tolerations[i] = runtime.DeepCopyJSONValue(value)
		}
		spec["tolerations"] = tolerations
	}
	return nil
}
