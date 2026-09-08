package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

const (
	notebookTemplateAPIVersion    = "sandbox-connect/v1alpha1"
	notebookTemplateKind          = "SandboxNotebookTemplate"
	defaultExternalPVCWaitTimeout = 120 * time.Second
	managedPVCSourceType          = "managed"
	existingPVCSourceType         = "existing"
	retentionDeleteWithNotebook   = "DeleteWithNotebook"
	retentionRetain               = "Retain"
	notebookPrimaryContainerName  = "notebook"
	demoInitContainerName         = "init-demo-ipynb"
	builtInNotebookInitName       = "extract-built-in-notebooks"
	platformTokenSidecarName      = "platform-token-sidecar"
	sharedWorkspaceVolumeName     = "shared-workspace"
)

var templateTokenPattern = regexp.MustCompile(`{[^{}]+}`)

var supportedTemplateTokens = map[string]struct{}{
	"{namespace}":    {},
	"{notebookName}": {},
	"{pvcName}":      {},
	"{storageSize}":  {},
}

type SandboxNotebookTemplate struct {
	APIVersion   string                      `json:"apiVersion"`
	Kind         string                      `json:"kind"`
	Metadata     SandboxTemplateMetadata     `json:"metadata"`
	Spec         SandboxNotebookTemplateSpec `json:"spec"`
	source       string
	pvcTemplates map[string]map[string]any
}

type SandboxTemplateMetadata struct {
	Name string `json:"name"`
}

type SandboxNotebookTemplateSpec struct {
	Lifecycle NotebookLifecycleConfig `json:"lifecycle"`
	Notebook  map[string]any          `json:"notebook"`
}

type NotebookLifecycleConfig struct {
	ExternalPVCWaitTimeout string                       `json:"externalPVCWaitTimeout,omitempty"`
	WorkspaceVolumeName    string                       `json:"workspaceVolumeName,omitempty"`
	VolumePolicies         []VolumeLifecyclePolicy      `json:"volumePolicies,omitempty"`
	InstanceTypeOverride   InstanceTypeOverrideConfig   `json:"instanceTypeOverride,omitempty"`
	RuntimeInjection       RuntimeInjectionConfig       `json:"runtimeInjection"`
	DemoFiles              DemoFilesConfig              `json:"demoFiles,omitempty"`
	PlatformToken          *PlatformTokenTemplateConfig `json:"platformToken,omitempty"`
}

type VolumeLifecyclePolicy struct {
	Name     string                `json:"name"`
	Required *bool                 `json:"required,omitempty"`
	Managed  *ManagedVolumePolicy  `json:"managed,omitempty"`
	Existing *ExistingVolumePolicy `json:"existing,omitempty"`
}

type ManagedVolumePolicy struct {
	RetentionPolicy string `json:"retentionPolicy"`
}

type ExistingVolumePolicy struct {
	WaitForBound *bool                 `json:"waitForBound,omitempty"`
	Expected     *PVCExpectationConfig `json:"expected,omitempty"`
}

type PVCExpectationConfig struct {
	AccessModes []string `json:"accessModes,omitempty"`
	VolumeMode  string   `json:"volumeMode,omitempty"`
}

type InstanceTypeOverrideConfig struct {
	SelectorKey string `json:"selectorKey,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type RuntimeInjectionConfig struct {
	Image string `json:"image"`
}

type DemoFilesConfig struct {
	Enabled   bool   `json:"enabled,omitempty"`
	InitImage string `json:"initImage,omitempty"`
}

type PlatformTokenTemplateConfig struct {
	SessionAPIBaseURL string `json:"sessionAPIBaseURL"`
}

type ResolvedPVCMount struct {
	Name            string
	ClaimName       string
	MountPath       string
	SubPath         string
	ReadOnly        bool
	Workspace       bool
	Managed         bool
	RetentionPolicy string
	PVCTemplate     map[string]any
}

type templateValues struct {
	Namespace    string
	NotebookName string
	PVCName      string
	StorageSize  string
}

func (p VolumeLifecyclePolicy) IsRequired() bool {
	return p.Required == nil || *p.Required
}

func (p ExistingVolumePolicy) ShouldWaitForBound() bool {
	return p.WaitForBound == nil || *p.WaitForBound
}

func LoadSandboxNotebookTemplate(templatePath, expectedWorkload string) (*SandboxNotebookTemplate, error) {
	if strings.TrimSpace(templatePath) == "" {
		return nil, fmt.Errorf("notebook template path for workload %s is required", expectedWorkload)
	}
	file, err := os.Open(templatePath)
	if err != nil {
		return nil, fmt.Errorf("open notebook template %s: %w", templatePath, err)
	}
	defer file.Close()
	decoder := k8syaml.NewYAMLOrJSONDecoder(file, 4096)
	documents := make([]map[string]any, 0, 2)
	for {
		var candidate map[string]any
		if err := decoder.Decode(&candidate); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode notebook template YAML: %w", err)
		}
		if candidate != nil {
			documents = append(documents, candidate)
		}
	}
	if len(documents) == 0 {
		return nil, fmt.Errorf("notebook template bundle is empty")
	}

	jsonData, err := json.Marshal(documents[0])
	if err != nil {
		return nil, fmt.Errorf("encode notebook template: %w", err)
	}
	var template SandboxNotebookTemplate
	strictDecoder := json.NewDecoder(strings.NewReader(string(jsonData)))
	strictDecoder.DisallowUnknownFields()
	if err := strictDecoder.Decode(&template); err != nil {
		return nil, fmt.Errorf("decode notebook template: %w", err)
	}

	template.source = templatePath
	template.pvcTemplates = make(map[string]map[string]any, len(documents)-1)
	for i, document := range documents[1:] {
		pvc := &unstructured.Unstructured{Object: document}
		name := strings.TrimSpace(pvc.GetName())
		if name == "" {
			return nil, fmt.Errorf("PVC template document %d metadata.name is required", i+2)
		}
		if _, exists := template.pvcTemplates[name]; exists {
			return nil, fmt.Errorf("duplicate PVC template document %q", name)
		}
		template.pvcTemplates[name] = document
	}
	if err := validateSandboxNotebookTemplate(&template, expectedWorkload); err != nil {
		return nil, err
	}
	return template.DeepCopy(), nil
}

func (t *SandboxNotebookTemplate) DeepCopy() *SandboxNotebookTemplate {
	if t == nil {
		return nil
	}
	raw, err := json.Marshal(t)
	if err != nil {
		panic(fmt.Sprintf("deep copy notebook template: %v", err))
	}
	var result SandboxNotebookTemplate
	if err := json.Unmarshal(raw, &result); err != nil {
		panic(fmt.Sprintf("deep copy notebook template: %v", err))
	}
	result.source = t.source
	result.pvcTemplates = make(map[string]map[string]any, len(t.pvcTemplates))
	for name, pvcTemplate := range t.pvcTemplates {
		result.pvcTemplates[name] = runtimeJSONDeepCopy(pvcTemplate)
	}
	return &result
}

func (t *SandboxNotebookTemplate) NotebookObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: runtimeJSONDeepCopy(t.Spec.Notebook)}
}

func runtimeJSONDeepCopy(value map[string]any) map[string]any {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("copy JSON object: %v", err))
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		panic(fmt.Sprintf("copy JSON object: %v", err))
	}
	return result
}

func (t *SandboxNotebookTemplate) ExternalPVCWaitTimeout() (time.Duration, error) {
	value := strings.TrimSpace(t.Spec.Lifecycle.ExternalPVCWaitTimeout)
	if value == "" {
		return defaultExternalPVCWaitTimeout, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("lifecycle.externalPVCWaitTimeout must be a positive duration")
	}
	return duration, nil
}

func (t *SandboxNotebookTemplate) VolumePolicy(name string) (VolumeLifecyclePolicy, bool) {
	for _, policy := range t.Spec.Lifecycle.VolumePolicies {
		if policy.Name == name {
			return policy, true
		}
	}
	return VolumeLifecyclePolicy{}, false
}

func (t *SandboxNotebookTemplate) PVCTemplate(name string) (map[string]any, bool) {
	pvcTemplate, exists := t.pvcTemplates[name]
	if !exists {
		return nil, false
	}
	return runtimeJSONDeepCopy(pvcTemplate), true
}

func validateSandboxNotebookTemplate(template *SandboxNotebookTemplate, expectedWorkload string) error {
	if template.APIVersion != notebookTemplateAPIVersion {
		return fmt.Errorf("notebook template apiVersion must be %s", notebookTemplateAPIVersion)
	}
	if template.Kind != notebookTemplateKind {
		return fmt.Errorf("notebook template kind must be %s", notebookTemplateKind)
	}
	if template.Metadata.Name != expectedWorkload {
		return fmt.Errorf("notebook template metadata.name must be %q", expectedWorkload)
	}
	if _, err := template.ExternalPVCWaitTimeout(); err != nil {
		return err
	}

	notebook := &unstructured.Unstructured{Object: template.Spec.Notebook}
	if notebook.GetAPIVersion() != "kubeflow.org/v1beta1" || notebook.GetKind() != "Notebook" {
		return fmt.Errorf("spec.notebook must be a kubeflow.org/v1beta1 Notebook")
	}
	if notebook.GetName() != "" || notebook.GetNamespace() != "" {
		return fmt.Errorf("spec.notebook metadata.name and metadata.namespace must be absent")
	}
	if _, exists := notebook.GetLabels()["app"]; exists {
		return fmt.Errorf("spec.notebook metadata.labels.app is worker-managed")
	}
	if err := validateNotebookTokenLocations(template.Spec.Notebook, nil); err != nil {
		return err
	}

	podSpec, found, err := unstructured.NestedMap(notebook.Object, "spec", "template", "spec")
	if err != nil || !found {
		return fmt.Errorf("spec.notebook must define spec.template.spec")
	}
	if automount, found, err := unstructured.NestedBool(podSpec, "automountServiceAccountToken"); err != nil {
		return fmt.Errorf("embedded Notebook automountServiceAccountToken must be boolean: %w", err)
	} else if found && automount {
		return fmt.Errorf("embedded Notebook automountServiceAccountToken must be false")
	}
	for _, field := range []string{"hostNetwork", "hostPID", "hostIPC"} {
		if enabled, _, err := unstructured.NestedBool(podSpec, field); err != nil {
			return fmt.Errorf("embedded Notebook %s must be boolean: %w", field, err)
		} else if enabled {
			return fmt.Errorf("embedded Notebook cannot enable %s", field)
		}
	}
	if security, found, err := unstructured.NestedMap(podSpec, "securityContext"); err != nil {
		return fmt.Errorf("read embedded Notebook securityContext: %w", err)
	} else if found {
		if value, ok := security["runAsNonRoot"].(bool); ok && !value {
			return fmt.Errorf("embedded Notebook securityContext.runAsNonRoot cannot be false")
		}
		if profile, ok := security["seccompProfile"].(map[string]any); ok && profile["type"] == "Unconfined" {
			return fmt.Errorf("embedded Notebook seccompProfile cannot be Unconfined")
		}
	}
	volumes, err := templateNamedItems(podSpec, "volumes")
	if err != nil {
		return err
	}
	volumeByName := map[string]map[string]any{}
	for _, volume := range volumes {
		name, err := requiredItemName(volume, "volume")
		if err != nil {
			return err
		}
		if _, exists := volumeByName[name]; exists {
			return fmt.Errorf("embedded Notebook has duplicate volume name %q", name)
		}
		volumeByName[name] = volume
	}

	containers, err := templateNamedItems(podSpec, "containers")
	if err != nil {
		return err
	}
	initContainers, err := templateNamedItems(podSpec, "initContainers")
	if err != nil {
		return err
	}
	primary, err := validateTemplateContainers(containers, initContainers, volumeByName)
	if err != nil {
		return err
	}
	image, _ := primary["image"].(string)
	if strings.TrimSpace(image) == "" {
		return fmt.Errorf("embedded Notebook primary container must define a default image")
	}

	policyNames := map[string]struct{}{}
	managedPolicyNames := map[string]struct{}{}
	for i, policy := range template.Spec.Lifecycle.VolumePolicies {
		location := fmt.Sprintf("lifecycle.volumePolicies[%d]", i)
		if _, exists := policyNames[policy.Name]; exists {
			return fmt.Errorf("duplicate lifecycle volume policy %q", policy.Name)
		}
		policyNames[policy.Name] = struct{}{}
		volume, exists := volumeByName[policy.Name]
		if !exists {
			return fmt.Errorf("%s references missing Notebook volume %q", location, policy.Name)
		}
		if _, found, err := unstructured.NestedMap(volume, "persistentVolumeClaim"); err != nil || !found {
			return fmt.Errorf("%s must reference a persistentVolumeClaim volume", location)
		}
		if (policy.Managed == nil) == (policy.Existing == nil) {
			return fmt.Errorf("%s must define exactly one of managed or existing", location)
		}
		claimName, found, err := unstructured.NestedString(volume, "persistentVolumeClaim", "claimName")
		if err != nil || !found || strings.TrimSpace(claimName) == "" {
			return fmt.Errorf("%s Notebook volume must define persistentVolumeClaim.claimName", location)
		}
		if err := validateTemplate(claimName); err != nil {
			return fmt.Errorf("%s claimName: %w", location, err)
		}
		if policy.Managed != nil {
			if !policy.IsRequired() {
				return fmt.Errorf("%s managed volume cannot be optional", location)
			}
			if policy.Managed.RetentionPolicy != retentionDeleteWithNotebook && policy.Managed.RetentionPolicy != retentionRetain {
				return fmt.Errorf("%s managed.retentionPolicy must be %s or %s", location, retentionDeleteWithNotebook, retentionRetain)
			}
			pvcTemplate, exists := template.pvcTemplates[policy.Name]
			if !exists {
				return fmt.Errorf("%s has no matching PVC template document %q", location, policy.Name)
			}
			managedPolicyNames[policy.Name] = struct{}{}
			if err := validateManagedPVCTemplate(pvcTemplate, policy.Name); err != nil {
				return fmt.Errorf("PVC template document %q: %w", policy.Name, err)
			}
			if err := validateConfigTemplates(pvcTemplate); err != nil {
				return fmt.Errorf("PVC template document %q: %w", policy.Name, err)
			}
		}
	}
	for name := range template.pvcTemplates {
		if _, exists := managedPolicyNames[name]; !exists {
			return fmt.Errorf("PVC template document %q has no matching managed volume policy", name)
		}
	}
	for name, volume := range volumeByName {
		if _, found, _ := unstructured.NestedMap(volume, "persistentVolumeClaim"); found {
			if _, exists := policyNames[name]; !exists {
				return fmt.Errorf("persistentVolumeClaim volume %q must have a lifecycle volume policy", name)
			}
		}
	}

	primaryMounts, err := containerVolumeMounts(primary)
	if err != nil {
		return err
	}
	for _, mount := range primaryMounts {
		if subPath, ok := mount["subPath"].(string); ok && subPath != "" {
			if err := validateTemplate(subPath); err != nil {
				return fmt.Errorf("primary volumeMount %s subPath: %w", mount["name"], err)
			}
			if pathpkg.IsAbs(subPath) || strings.Contains(subPath, "..") {
				return fmt.Errorf("primary volumeMount %s subPath must be a safe relative path", mount["name"])
			}
		}
	}
	if workspace := strings.TrimSpace(template.Spec.Lifecycle.WorkspaceVolumeName); workspace != "" {
		volume, exists := volumeByName[workspace]
		if !exists {
			return fmt.Errorf("lifecycle.workspaceVolumeName references missing volume %q", workspace)
		}
		if _, found, _ := unstructured.NestedMap(volume, "persistentVolumeClaim"); !found {
			return fmt.Errorf("lifecycle.workspaceVolumeName must reference a persistentVolumeClaim volume")
		}
		mount, exists := findMountByName(primaryMounts, workspace)
		if !exists {
			return fmt.Errorf("workspace volume %q must be mounted by the primary container", workspace)
		}
		if readOnly, _ := mount["readOnly"].(bool); readOnly {
			return fmt.Errorf("workspace volume %q must be writable", workspace)
		}
	}
	if strings.TrimSpace(template.Spec.Lifecycle.RuntimeInjection.Image) == "" {
		return fmt.Errorf("lifecycle.runtimeInjection.image is required")
	}
	if template.Spec.Lifecycle.DemoFiles.Enabled && strings.TrimSpace(template.Spec.Lifecycle.DemoFiles.InitImage) == "" {
		return fmt.Errorf("lifecycle.demoFiles.initImage is required when enabled")
	}
	override := template.Spec.Lifecycle.InstanceTypeOverride
	if override.Required && strings.TrimSpace(override.SelectorKey) == "" {
		return fmt.Errorf("lifecycle.instanceTypeOverride.selectorKey is required when instance type is required")
	}
	if override.SelectorKey != "" {
		if errs := validation.IsQualifiedName(override.SelectorKey); len(errs) > 0 {
			return fmt.Errorf("lifecycle.instanceTypeOverride.selectorKey is invalid: %s", strings.Join(errs, ", "))
		}
	}
	if err := validatePlatformTokenTemplate(template, podSpec, containers, volumeByName); err != nil {
		return err
	}
	return nil
}

func validateManagedPVCTemplate(template map[string]any, expectedName string) error {
	if len(template) == 0 {
		return fmt.Errorf("is required")
	}
	pvc := &unstructured.Unstructured{Object: template}
	for field := range template {
		if field != "apiVersion" && field != "kind" && field != "metadata" && field != "spec" {
			return fmt.Errorf("field %s is unsupported", field)
		}
	}
	if pvc.GetAPIVersion() != "v1" || pvc.GetKind() != "PersistentVolumeClaim" {
		return fmt.Errorf("must be a v1/PersistentVolumeClaim")
	}
	metadata, found, err := unstructured.NestedMap(template, "metadata")
	if err != nil {
		return fmt.Errorf("metadata must be an object: %w", err)
	}
	if !found {
		return fmt.Errorf("metadata is required")
	}
	for field := range metadata {
		if field != "name" && field != "labels" && field != "annotations" {
			return fmt.Errorf("metadata.%s is worker-managed or unsupported", field)
		}
	}
	if pvc.GetName() != expectedName {
		return fmt.Errorf("metadata.name must match managed volume policy %q", expectedName)
	}
	if _, _, err := unstructured.NestedStringMap(template, "metadata", "labels"); err != nil {
		return fmt.Errorf("metadata.labels must contain only string values: %w", err)
	}
	if _, _, err := unstructured.NestedStringMap(template, "metadata", "annotations"); err != nil {
		return fmt.Errorf("metadata.annotations must contain only string values: %w", err)
	}
	spec, found, err := unstructured.NestedMap(template, "spec")
	if err != nil {
		return fmt.Errorf("spec must be an object: %w", err)
	}
	if !found || len(spec) == 0 {
		return fmt.Errorf("spec is required")
	}
	return nil
}

func validateTemplateContainers(containers, initContainers []map[string]any, volumes map[string]map[string]any) (map[string]any, error) {
	if len(containers) == 0 {
		return nil, fmt.Errorf("embedded Notebook must define at least one container")
	}
	primaryCount := 0
	var primary map[string]any
	names := map[string]struct{}{}
	for _, container := range containers {
		name, err := requiredItemName(container, "container")
		if err != nil {
			return nil, err
		}
		if _, exists := names[name]; exists {
			return nil, fmt.Errorf("embedded Notebook has duplicate container name %q", name)
		}
		names[name] = struct{}{}
		if name == notebookPrimaryContainerName {
			primaryCount++
			primary = container
		}
		if err := validateTemplateContainer(container, "container "+name, volumes); err != nil {
			return nil, err
		}
	}
	if primaryCount != 1 {
		return nil, fmt.Errorf("embedded Notebook must define exactly one primary container named %q", notebookPrimaryContainerName)
	}
	initNames := map[string]struct{}{}
	for _, container := range initContainers {
		name, err := requiredItemName(container, "init container")
		if err != nil {
			return nil, err
		}
		if _, exists := initNames[name]; exists {
			return nil, fmt.Errorf("embedded Notebook has duplicate init container name %q", name)
		}
		if _, exists := names[name]; exists {
			return nil, fmt.Errorf("embedded Notebook reuses container name %q for an init container", name)
		}
		if name == demoInitContainerName || name == builtInNotebookInitName {
			return nil, fmt.Errorf("embedded Notebook init container name %q is worker-reserved", name)
		}
		initNames[name] = struct{}{}
		if err := validateTemplateContainer(container, "init container "+name, volumes); err != nil {
			return nil, err
		}
	}
	return primary, nil
}

func validateTemplateContainer(container map[string]any, location string, volumes map[string]map[string]any) error {
	if security, ok := container["securityContext"].(map[string]any); ok {
		if value, ok := security["privileged"].(bool); ok && value {
			return fmt.Errorf("embedded Notebook %s cannot enable privileged mode", location)
		}
		if value, ok := security["allowPrivilegeEscalation"].(bool); ok && value {
			return fmt.Errorf("embedded Notebook %s cannot allow privilege escalation", location)
		}
		if value, ok := security["procMount"].(string); ok && value != "" && value != "Default" {
			return fmt.Errorf("embedded Notebook %s procMount must be Default", location)
		}
	}
	mounts, err := containerVolumeMounts(container)
	if err != nil {
		return fmt.Errorf("read embedded Notebook %s volumeMounts: %w", location, err)
	}
	seenNames := map[string]struct{}{}
	seenPaths := map[string]struct{}{}
	for i, mount := range mounts {
		name, _ := mount["name"].(string)
		if name == "" {
			return fmt.Errorf("embedded Notebook %s volumeMounts[%d] must have a name", location, i)
		}
		if _, exists := volumes[name]; !exists {
			return fmt.Errorf("embedded Notebook %s volumeMount %q has no matching volume", location, name)
		}
		if _, exists := seenNames[name]; exists {
			return fmt.Errorf("embedded Notebook %s has duplicate volumeMount name %q", location, name)
		}
		seenNames[name] = struct{}{}
		mountPath, _ := mount["mountPath"].(string)
		if !pathpkg.IsAbs(mountPath) || pathpkg.Clean(mountPath) != mountPath {
			return fmt.Errorf("embedded Notebook %s volumeMount %q must have a clean absolute mountPath", location, name)
		}
		if _, exists := seenPaths[mountPath]; exists {
			return fmt.Errorf("embedded Notebook %s has duplicate mountPath %q", location, mountPath)
		}
		seenPaths[mountPath] = struct{}{}
	}
	return nil
}

func validatePlatformTokenTemplate(template *SandboxNotebookTemplate, podSpec map[string]any, containers []map[string]any, volumes map[string]map[string]any) error {
	config := template.Spec.Lifecycle.PlatformToken
	sidecar, hasSidecar := findNamedItem(containers, platformTokenSidecarName)
	_, hasRefreshVolume := volumes[platformRefreshTokenVolumeName]
	_, hasCacheVolume := volumes[platformTokenCacheVolumeName]
	if config == nil {
		if hasSidecar || hasRefreshVolume || hasCacheVolume {
			return fmt.Errorf("platform token resources require lifecycle.platformToken")
		}
		return nil
	}
	if strings.TrimSpace(config.SessionAPIBaseURL) == "" {
		return fmt.Errorf("lifecycle.platformToken.sessionAPIBaseURL is required")
	}
	if !hasSidecar || !hasRefreshVolume || !hasCacheVolume {
		return fmt.Errorf("platform token configuration requires sidecar and token volumes in embedded Notebook")
	}
	primary, _ := findNamedItem(containers, notebookPrimaryContainerName)
	for _, requiredEnv := range []string{"MAHAAGX_TOKEN_FILE", "MAHAAGX_FILE_API_BASE_URL"} {
		if !containerHasEnv(primary, requiredEnv) {
			return fmt.Errorf("primary container must define platform token env %s", requiredEnv)
		}
	}
	for _, requiredEnv := range []string{"BOOTSTRAP_TOKEN_FILE", "READY_ADDRESS", "TOKEN_SESSION_URL", "EXPECTED_USER_ID"} {
		if !containerHasEnv(sidecar, requiredEnv) {
			return fmt.Errorf("platform token sidecar must define env %s", requiredEnv)
		}
	}
	_ = podSpec
	return nil
}

func templateNamedItems(object map[string]any, field string) ([]map[string]any, error) {
	items, found, err := unstructured.NestedSlice(object, field)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", field, err)
	}
	if !found {
		return nil, nil
	}
	result := make([]map[string]any, 0, len(items))
	for i, item := range items {
		value, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be an object", field, i)
		}
		result = append(result, value)
	}
	return result, nil
}

func requiredItemName(item map[string]any, kind string) (string, error) {
	name, ok := item["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("%s must have a non-empty name", kind)
	}
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return "", fmt.Errorf("%s name %q is invalid: %s", kind, name, strings.Join(errs, ", "))
	}
	return name, nil
}

func containerVolumeMounts(container map[string]any) ([]map[string]any, error) {
	items, found, err := unstructured.NestedSlice(container, "volumeMounts")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	result := make([]map[string]any, 0, len(items))
	for i, item := range items {
		mount, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("volumeMounts[%d] must be an object", i)
		}
		result = append(result, mount)
	}
	return result, nil
}

func findNamedItem(items []map[string]any, name string) (map[string]any, bool) {
	for _, item := range items {
		if item["name"] == name {
			return item, true
		}
	}
	return nil, false
}

func findMountByName(items []map[string]any, name string) (map[string]any, bool) {
	return findNamedItem(items, name)
}

func containerHasEnv(container map[string]any, name string) bool {
	env, found, _ := unstructured.NestedSlice(container, "env")
	if !found {
		return false
	}
	for _, item := range env {
		entry, ok := item.(map[string]any)
		if ok && entry["name"] == name {
			return true
		}
	}
	return false
}

func validateNotebookTokenLocations(value any, path []string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if err := validateNotebookTokenLocations(item, append(path, key)); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := validateNotebookTokenLocations(item, append(path, "[]")); err != nil {
				return err
			}
		}
	case string:
		if !strings.ContainsAny(typed, "{}") {
			return nil
		}
		allowed := len(path) >= 2 && path[len(path)-1] == "claimName" && path[len(path)-2] == "persistentVolumeClaim"
		if !allowed && len(path) >= 3 && path[len(path)-1] == "subPath" && path[len(path)-3] == "volumeMounts" {
			allowed = true
		}
		if !allowed {
			return fmt.Errorf("template tokens are not allowed at spec.notebook.%s", strings.Join(path, "."))
		}
		if err := validateTemplate(typed); err != nil {
			return fmt.Errorf("spec.notebook.%s: %w", strings.Join(path, "."), err)
		}
	}
	return nil
}

func validateConfigTemplates(value any) error {
	switch typed := value.(type) {
	case string:
		if strings.ContainsAny(typed, "{}") {
			return validateTemplate(typed)
		}
	case map[string]any:
		for _, item := range typed {
			if err := validateConfigTemplates(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := validateConfigTemplates(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateTemplate(value string) error {
	for _, token := range templateTokenPattern.FindAllString(value, -1) {
		if _, ok := supportedTemplateTokens[token]; !ok {
			return fmt.Errorf("unsupported template token %s", token)
		}
	}
	withoutTokens := templateTokenPattern.ReplaceAllString(value, "")
	if strings.ContainsAny(withoutTokens, "{}") {
		return fmt.Errorf("malformed template")
	}
	return nil
}

func renderTemplate(value string, values templateValues) string {
	replacer := strings.NewReplacer(
		"{namespace}", values.Namespace,
		"{notebookName}", values.NotebookName,
		"{pvcName}", values.PVCName,
		"{storageSize}", values.StorageSize,
	)
	return replacer.Replace(value)
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
