package main

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
)

func (w *worker) BuildNotebook() (*unstructured.Unstructured, error) {
	if w.template == nil {
		if err := w.SelectWorkloadTemplate(); err != nil {
			return nil, err
		}
	}
	nb := w.notebook

	notebookObj := w.template.NotebookObject()
	notebookObj.SetName(nb.Name)
	notebookObj.SetNamespace(nb.Namespace)
	labels := notebookObj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["app"] = nb.Name
	notebookObj.SetLabels(labels)

	podSpec, found, err := unstructured.NestedMap(notebookObj.Object, "spec", "template", "spec")
	if err != nil || !found {
		return nil, fmt.Errorf("read notebook template pod spec: %w", err)
	}

	containers, err := templateNamedItems(podSpec, "containers")
	if err != nil {
		return nil, err
	}
	primaryIndex := -1
	for i := range containers {
		if containers[i]["name"] == notebookPrimaryContainerName {
			primaryIndex = i
			break
		}
	}
	if primaryIndex < 0 {
		return nil, fmt.Errorf("notebook template primary container is missing")
	}
	primary := containers[primaryIndex]
	imageName, _ := primary["image"].(string)
	if nb.ImageName != nil && strings.TrimSpace(*nb.ImageName) != "" {
		imageName = strings.TrimSpace(*nb.ImageName)
		w.logger.Info("using custom image from notebook request", "image", imageName)
	}
	primary["name"] = nb.Name
	primary["image"] = imageName
	resources, resourcesFound, err := unstructured.NestedMap(primary, "resources")
	if err != nil {
		return nil, fmt.Errorf("read primary resources: %w", err)
	}
	if !resourcesFound {
		resources = map[string]any{}
	}
	requests, requestsFound, err := unstructured.NestedMap(resources, "requests")
	if err != nil {
		return nil, fmt.Errorf("read primary resource requests: %w", err)
	}
	if !requestsFound {
		requests = map[string]any{}
	}
	limits, limitsFound, err := unstructured.NestedMap(resources, "limits")
	if err != nil {
		return nil, fmt.Errorf("read primary resource limits: %w", err)
	}
	if !limitsFound {
		limits = map[string]any{}
	}
	requests["cpu"] = fmt.Sprintf("%.6f", nb.CPURequest)
	requests["memory"] = nb.MemoryRequest
	limits["cpu"] = fmt.Sprintf("%.6f", nb.CPULimit)
	limits["memory"] = nb.MemoryLimit
	if nb.GPUType != nil && nb.GPURequest != nil && nb.GPULimit != nil && notebookWorkload(nb) == "gpu" {
		requests[*nb.GPUType] = int64(*nb.GPURequest)
		limits[*nb.GPUType] = int64(*nb.GPULimit)
	}
	resources["requests"] = requests
	resources["limits"] = limits
	primary["resources"] = resources

	initContainers, err := templateNamedItems(podSpec, "initContainers")
	if err != nil {
		return nil, err
	}
	workspaceMount, hasWorkspace := w.workspacePVCMount()
	demoConfig := w.template.Spec.Lifecycle.DemoFiles
	flavor := notebookWorkload(nb)
	if demoConfig.Enabled && !hasWorkspace {
		w.logger.Info("no workspace PVC configured, skipping persistent demo file setup")
	} else if demoConfig.Enabled {
		if !usesNotebookImageForDemoInit(imageName) {
			initContainers = append(initContainers, map[string]any{
				"name":    demoInitContainerName,
				"image":   demoConfig.InitImage,
				"command": []any{"/bin/sh", "-c", buildInitImageDemoCopyScript(flavor)},
				"volumeMounts": []any{
					resolvedVolumeMountSpec(workspaceMount, "/home/jovyan"),
				},
			})
		}
		initContainers = append(initContainers, map[string]any{
			"name":    builtInNotebookInitName,
			"image":   imageName,
			"command": []any{"/bin/sh", "-c", buildNotebookImageDemoCopyScript(flavor, notebookImageProjectNotebookDir(imageName))},
			"volumeMounts": []any{
				resolvedVolumeMountSpec(workspaceMount, "/mnt/data"),
			},
		})
	}

	volumes, err := templateNamedItems(podSpec, "volumes")
	if err != nil {
		return nil, err
	}
	values := templateValues{
		Namespace: nb.Namespace, NotebookName: nb.Name,
		PVCName: nb.PVCname, StorageSize: nb.StorageSize,
	}
	volumes, err = renderNotebookStorage(volumes, containers, initContainers, w.omittedVolumes, values)
	if err != nil {
		return nil, err
	}
	if err := w.patchPlatformTokenResources(containers, volumes); err != nil {
		return nil, err
	}
	podSpec["containers"] = mapsToAny(containers)
	podSpec["initContainers"] = mapsToAny(initContainers)
	podSpec["volumes"] = mapsToAny(volumes)
	podSpec["automountServiceAccountToken"] = false

	for _, container := range containers {
		enforceContainerSecurity(container)
	}
	for _, container := range initContainers {
		enforceContainerSecurity(container)
	}
	if err := w.applyInstanceTypeOverride(podSpec); err != nil {
		return nil, err
	}
	if err := unstructured.SetNestedMap(notebookObj.Object, podSpec, "spec", "template", "spec"); err != nil {
		return nil, fmt.Errorf("set rendered notebook pod spec: %w", err)
	}
	if err := validateBuiltNotebook(notebookObj); err != nil {
		return nil, err
	}
	return notebookObj, nil
}

func renderNotebookStorage(volumes, containers, initContainers []map[string]any, omitted map[string]struct{}, values templateValues) ([]map[string]any, error) {
	renderedVolumes := make([]map[string]any, 0, len(volumes))
	for _, volume := range volumes {
		name, _ := volume["name"].(string)
		if _, skip := omitted[name]; skip {
			continue
		}
		if pvc, ok := volume["persistentVolumeClaim"].(map[string]any); ok {
			claimName, _ := pvc["claimName"].(string)
			claimName = renderTemplate(claimName, values)
			if errs := validation.IsDNS1123Subdomain(claimName); len(errs) > 0 {
				return nil, fmt.Errorf("PVC volume %s rendered invalid claim name %q: %s", name, claimName, strings.Join(errs, ", "))
			}
			pvc["claimName"] = claimName
			volume["persistentVolumeClaim"] = pvc
		}
		renderedVolumes = append(renderedVolumes, volume)
	}
	for _, group := range [][]map[string]any{containers, initContainers} {
		for _, container := range group {
			if err := renderContainerMounts(container, omitted, values); err != nil {
				return nil, err
			}
		}
	}
	return renderedVolumes, nil
}

func renderContainerMounts(container map[string]any, omitted map[string]struct{}, values templateValues) error {
	mounts, found, err := unstructured.NestedSlice(container, "volumeMounts")
	if err != nil {
		return fmt.Errorf("read container volumeMounts: %w", err)
	}
	if !found {
		return nil
	}
	rendered := make([]any, 0, len(mounts))
	for i, item := range mounts {
		mount, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("volumeMounts[%d] must be an object", i)
		}
		name, _ := mount["name"].(string)
		if _, skip := omitted[name]; skip {
			continue
		}
		if subPath, ok := mount["subPath"].(string); ok && subPath != "" {
			subPath = renderTemplate(subPath, values)
			if strings.HasPrefix(subPath, "/") || strings.Contains(subPath, "..") {
				return fmt.Errorf("volumeMount %s rendered unsafe subPath %q", name, subPath)
			}
			mount["subPath"] = subPath
		}
		rendered = append(rendered, mount)
	}
	container["volumeMounts"] = rendered
	return nil
}

func usesNotebookImageForDemoInit(imageName string) bool {
	switch imageName {
	case "098809772313.dkr.ecr.ap-south-1.amazonaws.com/tgdex/ai-sandbox-cpu-notebook:nha-ps1-v3",
		"098809772313.dkr.ecr.ap-south-1.amazonaws.com/tgdex/ai-sandbox-cpu-notebook:nha-ps2-v4",
		"098809772313.dkr.ecr.ap-south-1.amazonaws.com/tgdex/ai-sandbox-cpu-notebook:nha-ps3-v3":
		return true
	default:
		return false
	}
}

func nestedSliceOrEmpty(object map[string]any, field string) ([]any, error) {
	values, found, err := unstructured.NestedSlice(object, field)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", field, err)
	}
	if !found {
		return []any{}, nil
	}
	return values, nil
}

func mapsToAny(values []map[string]any) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

func enforceContainerSecurity(container map[string]any) {
	security, _ := container["securityContext"].(map[string]any)
	if security == nil {
		security = map[string]any{}
	}
	security["privileged"] = false
	security["procMount"] = "Default"
	security["allowPrivilegeEscalation"] = false
	container["securityContext"] = security
}

func validateBuiltNotebook(notebook *unstructured.Unstructured) error {
	if errs := validation.IsDNS1123Subdomain(notebook.GetName()); len(errs) > 0 {
		return fmt.Errorf("rendered notebook name %q is invalid: %s", notebook.GetName(), strings.Join(errs, ", "))
	}
	if errs := validation.IsDNS1123Label(notebook.GetNamespace()); len(errs) > 0 {
		return fmt.Errorf("rendered notebook namespace %q is invalid: %s", notebook.GetNamespace(), strings.Join(errs, ", "))
	}
	podSpec, found, err := unstructured.NestedMap(notebook.Object, "spec", "template", "spec")
	if err != nil || !found {
		return fmt.Errorf("read rendered notebook pod spec: %w", err)
	}
	volumes, err := templateNamedItems(podSpec, "volumes")
	if err != nil {
		return err
	}
	volumeNames := map[string]struct{}{}
	for _, volume := range volumes {
		name, err := requiredItemName(volume, "rendered volume")
		if err != nil {
			return err
		}
		if _, exists := volumeNames[name]; exists {
			return fmt.Errorf("rendered notebook has duplicate volume name %q", name)
		}
		volumeNames[name] = struct{}{}
	}
	for _, field := range []string{"containers", "initContainers"} {
		items, err := templateNamedItems(podSpec, field)
		if err != nil {
			return err
		}
		names := map[string]struct{}{}
		for _, item := range items {
			name, err := requiredItemName(item, "rendered "+field)
			if err != nil {
				return err
			}
			if _, exists := names[name]; exists {
				return fmt.Errorf("rendered notebook has duplicate %s name %q", field, name)
			}
			names[name] = struct{}{}
			if err := validateBuiltContainer(item, field+" "+name, volumeNames); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateBuiltContainer(container map[string]any, location string, volumeNames map[string]struct{}) error {
	mounts, found, err := unstructured.NestedSlice(container, "volumeMounts")
	if err != nil {
		return fmt.Errorf("read rendered %s volumeMounts: %w", location, err)
	}
	seenNames := map[string]struct{}{}
	seenPaths := map[string]struct{}{}
	if found {
		for i, item := range mounts {
			mount, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("rendered %s volumeMounts[%d] must be an object", location, i)
			}
			name, _ := mount["name"].(string)
			if _, exists := volumeNames[name]; !exists {
				return fmt.Errorf("rendered %s volumeMount %q has no matching volume", location, name)
			}
			if _, exists := seenNames[name]; exists {
				return fmt.Errorf("rendered %s has duplicate volumeMount name %q", location, name)
			}
			seenNames[name] = struct{}{}
			mountPath, _ := mount["mountPath"].(string)
			if mountPath == "" {
				return fmt.Errorf("rendered %s volumeMount %q has no mountPath", location, name)
			}
			if _, exists := seenPaths[mountPath]; exists {
				return fmt.Errorf("rendered %s has duplicate mountPath %q", location, mountPath)
			}
			seenPaths[mountPath] = struct{}{}
		}
	}
	env, found, err := unstructured.NestedSlice(container, "env")
	if err != nil {
		return fmt.Errorf("read rendered %s env: %w", location, err)
	}
	if found {
		seenEnv := map[string]struct{}{}
		for i, item := range env {
			entry, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("rendered %s env[%d] must be an object", location, i)
			}
			name, _ := entry["name"].(string)
			if name == "" {
				return fmt.Errorf("rendered %s env[%d] has no name", location, i)
			}
			if _, exists := seenEnv[name]; exists {
				return fmt.Errorf("rendered %s has duplicate env name %q", location, name)
			}
			seenEnv[name] = struct{}{}
		}
	}
	return nil
}
