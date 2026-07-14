package main

import (
	"encoding/json"
	"fmt"
	"os"
	pathpkg "path"
	"regexp"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"
)

const (
	runtimeConfigAPIVersion       = "sandbox-connect/v1alpha1"
	defaultExternalPVCWaitTimeout = 120 * time.Second
	managedPVCSourceType          = "managed"
	existingPVCSourceType         = "existing"
	retentionDeleteWithNotebook   = "DeleteWithNotebook"
	retentionRetain               = "Retain"
	defaultLegacyWorkspaceMount   = "data-volume"
	defaultLegacyWorkspacePath    = "/home/jovyan"
)

var templateTokenPattern = regexp.MustCompile(`\{[^{}]+\}`)

var supportedTemplateTokens = map[string]struct{}{
	"{namespace}":    {},
	"{notebookName}": {},
	"{pvcName}":      {},
	"{storageSize}":  {},
}

type RuntimeConfig struct {
	APIVersion string                    `json:"apiVersion"`
	Defaults   RuntimeConfigDefaults     `json:"defaults,omitempty"`
	Workloads  map[string]WorkloadPolicy `json:"workloads"`
	source     string
}

type RuntimeConfigDefaults struct {
	ExternalPVCWaitTimeout string `json:"externalPVCWaitTimeout,omitempty"`
}

type WorkloadPolicy struct {
	Scheduling SchedulingPolicy `json:"scheduling,omitempty"`
	PVCMounts  []PVCMountConfig `json:"pvcMounts,omitempty"`
}

type SchedulingPolicy struct {
	NodeSelector         map[string]string          `json:"nodeSelector,omitempty"`
	Affinity             map[string]any             `json:"affinity,omitempty"`
	Tolerations          []map[string]any           `json:"tolerations,omitempty"`
	InstanceTypeOverride InstanceTypeOverrideConfig `json:"instanceTypeOverride,omitempty"`
}

type InstanceTypeOverrideConfig struct {
	Enabled     bool   `json:"enabled,omitempty"`
	SelectorKey string `json:"selectorKey,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type PVCMountConfig struct {
	Name            string          `json:"name"`
	MountPath       string          `json:"mountPath"`
	ReadOnly        bool            `json:"readOnly,omitempty"`
	Workspace       bool            `json:"workspace,omitempty"`
	Required        *bool           `json:"required,omitempty"`
	SubPathTemplate string          `json:"subPathTemplate,omitempty"`
	Source          PVCSourceConfig `json:"source"`
}

type PVCSourceConfig struct {
	Type              string                `json:"type"`
	ClaimNameTemplate string                `json:"claimNameTemplate"`
	WaitForBound      *bool                 `json:"waitForBound,omitempty"`
	RetentionPolicy   string                `json:"retentionPolicy,omitempty"`
	Spec              map[string]any        `json:"spec,omitempty"`
	Expected          *PVCExpectationConfig `json:"expected,omitempty"`
}

type PVCExpectationConfig struct {
	AccessModes []string `json:"accessModes,omitempty"`
	VolumeMode  string   `json:"volumeMode,omitempty"`
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
	Spec            map[string]any
}

type templateValues struct {
	Namespace    string
	NotebookName string
	PVCName      string
	StorageSize  string
}

func (m PVCMountConfig) IsRequired() bool {
	return m.Required == nil || *m.Required
}

func (s PVCSourceConfig) ShouldWaitForBound() bool {
	return s.WaitForBound == nil || *s.WaitForBound
}

func LoadRuntimeConfig(env Env) (RuntimeConfig, error) {
	if strings.TrimSpace(env.RuntimeConfigPath) == "" {
		cfg, err := legacyRuntimeConfig(env)
		if err != nil {
			return RuntimeConfig{}, err
		}
		cfg.source = "legacy-environment"
		return cfg, nil
	}

	raw, err := os.ReadFile(env.RuntimeConfigPath)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("read %s: %w", env.RuntimeConfigPath, err)
	}
	jsonData, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("convert runtime config to JSON: %w", err)
	}

	var cfg RuntimeConfig
	decoder := json.NewDecoder(strings.NewReader(string(jsonData)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return RuntimeConfig{}, fmt.Errorf("decode runtime config: %w", err)
	}
	if err := validateRuntimeConfig(cfg); err != nil {
		return RuntimeConfig{}, err
	}
	cfg.source = env.RuntimeConfigPath
	return cfg, nil
}

func legacyRuntimeConfig(env Env) (RuntimeConfig, error) {
	cpuTypes := splitNonEmpty(env.CPU_NODE_INSTANCE_TYPES)
	if len(cpuTypes) == 0 {
		return RuntimeConfig{}, fmt.Errorf("WORKER_CPU_NODE_INSTANCE_TYPES is required when WORKER_RUNTIME_CONFIG_PATH is not set")
	}
	if strings.TrimSpace(env.STORAGE_CLASS_NAME) == "" {
		return RuntimeConfig{}, fmt.Errorf("WORKER_STORAGE_CLASS_NAME is required when WORKER_RUNTIME_CONFIG_PATH is not set")
	}

	legacyMount := PVCMountConfig{
		Name:      defaultLegacyWorkspaceMount,
		MountPath: defaultLegacyWorkspacePath,
		Workspace: true,
		Source: PVCSourceConfig{
			Type:              managedPVCSourceType,
			ClaimNameTemplate: "{pvcName}",
			RetentionPolicy:   retentionDeleteWithNotebook,
			Spec: map[string]any{
				"accessModes":      []any{"ReadWriteOnce"},
				"storageClassName": env.STORAGE_CLASS_NAME,
				"resources": map[string]any{
					"requests": map[string]any{"storage": "{storageSize}"},
				},
			},
		},
	}

	cpuValues := make([]any, len(cpuTypes))
	for i, value := range cpuTypes {
		cpuValues[i] = value
	}
	cpuPolicy := WorkloadPolicy{
		Scheduling: SchedulingPolicy{Affinity: map[string]any{
			"nodeAffinity": map[string]any{
				"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
					"nodeSelectorTerms": []any{map[string]any{
						"matchExpressions": []any{map[string]any{
							"key": "node.kubernetes.io/instance-type", "operator": "In", "values": cpuValues,
						}},
					}},
				},
			},
		}},
		PVCMounts: []PVCMountConfig{legacyMount},
	}

	gpuSelector := map[string]string{}
	gpuTypes := splitNonEmpty(env.GPU_NODE_INSTANCE_TYPES)
	if len(gpuTypes) > 0 {
		gpuSelector["node.kubernetes.io/instance-type"] = gpuTypes[0]
	} else if value := strings.TrimSpace(env.GPU_NODE_INSTANCE_TYPE); value != "" {
		gpuSelector["node.kubernetes.io/instance-type"] = value
	}
	gpuPolicy := WorkloadPolicy{
		Scheduling: SchedulingPolicy{
			NodeSelector: gpuSelector,
			InstanceTypeOverride: InstanceTypeOverrideConfig{
				Enabled: true, SelectorKey: "node.kubernetes.io/instance-type", Required: len(gpuSelector) == 0,
			},
		},
		PVCMounts: []PVCMountConfig{legacyMount},
	}

	cfg := RuntimeConfig{
		APIVersion: runtimeConfigAPIVersion,
		Defaults:   RuntimeConfigDefaults{ExternalPVCWaitTimeout: defaultExternalPVCWaitTimeout.String()},
		Workloads:  map[string]WorkloadPolicy{"cpu": cpuPolicy, "gpu": gpuPolicy},
	}
	return cfg, validateRuntimeConfig(cfg)
}

func validateRuntimeConfig(cfg RuntimeConfig) error {
	if cfg.APIVersion != runtimeConfigAPIVersion {
		return fmt.Errorf("unsupported runtime config apiVersion %q", cfg.APIVersion)
	}
	if len(cfg.Workloads) == 0 {
		return fmt.Errorf("runtime config must define at least one workload")
	}
	if _, err := cfg.ExternalPVCWaitTimeout(); err != nil {
		return err
	}

	workloadNames := make([]string, 0, len(cfg.Workloads))
	for name := range cfg.Workloads {
		workloadNames = append(workloadNames, name)
	}
	sort.Strings(workloadNames)
	for _, name := range workloadNames {
		if name != "cpu" && name != "gpu" {
			return fmt.Errorf("unsupported workload %q: expected cpu or gpu", name)
		}
		if err := validateWorkloadPolicy(name, cfg.Workloads[name]); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkloadPolicy(workload string, policy WorkloadPolicy) error {
	for key, value := range policy.Scheduling.NodeSelector {
		if errs := validation.IsQualifiedName(key); len(errs) > 0 {
			return fmt.Errorf("workload %s has invalid nodeSelector key %q: %s", workload, key, strings.Join(errs, ", "))
		}
		if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
			return fmt.Errorf("workload %s has invalid nodeSelector value %q: %s", workload, value, strings.Join(errs, ", "))
		}
	}
	if override := policy.Scheduling.InstanceTypeOverride; override.Enabled {
		if strings.TrimSpace(override.SelectorKey) == "" {
			return fmt.Errorf("workload %s instanceTypeOverride.selectorKey is required when enabled", workload)
		}
		if errs := validation.IsQualifiedName(override.SelectorKey); len(errs) > 0 {
			return fmt.Errorf("workload %s has invalid instance type selector key: %s", workload, strings.Join(errs, ", "))
		}
	}

	seenNames := map[string]struct{}{}
	seenPaths := map[string]struct{}{}
	workspaceCount := 0
	for i, mount := range policy.PVCMounts {
		location := fmt.Sprintf("workload %s pvcMounts[%d]", workload, i)
		if errs := validation.IsDNS1123Label(mount.Name); len(errs) > 0 {
			return fmt.Errorf("%s has invalid name %q: %s", location, mount.Name, strings.Join(errs, ", "))
		}
		if _, exists := seenNames[mount.Name]; exists {
			return fmt.Errorf("workload %s has duplicate PVC mount name %q", workload, mount.Name)
		}
		seenNames[mount.Name] = struct{}{}
		if !pathpkg.IsAbs(mount.MountPath) || pathpkg.Clean(mount.MountPath) != mount.MountPath {
			return fmt.Errorf("%s mountPath must be a clean absolute path", location)
		}
		if _, exists := seenPaths[mount.MountPath]; exists {
			return fmt.Errorf("workload %s has duplicate mountPath %q", workload, mount.MountPath)
		}
		seenPaths[mount.MountPath] = struct{}{}
		if mount.Workspace {
			workspaceCount++
			if mount.ReadOnly {
				return fmt.Errorf("%s workspace mount must be writable", location)
			}
		}
		if err := validateTemplate(mount.Source.ClaimNameTemplate); err != nil {
			return fmt.Errorf("%s claimNameTemplate: %w", location, err)
		}
		if strings.TrimSpace(mount.Source.ClaimNameTemplate) == "" {
			return fmt.Errorf("%s claimNameTemplate is required", location)
		}
		if mount.SubPathTemplate != "" {
			if err := validateTemplate(mount.SubPathTemplate); err != nil {
				return fmt.Errorf("%s subPathTemplate: %w", location, err)
			}
			if pathpkg.IsAbs(mount.SubPathTemplate) || strings.Contains(mount.SubPathTemplate, "..") {
				return fmt.Errorf("%s subPathTemplate must be a safe relative path", location)
			}
		}

		switch mount.Source.Type {
		case existingPVCSourceType:
			if mount.Source.RetentionPolicy != "" || mount.Source.Spec != nil {
				return fmt.Errorf("%s existing source cannot define retentionPolicy or spec", location)
			}
		case managedPVCSourceType:
			if !mount.IsRequired() {
				return fmt.Errorf("%s managed source cannot be optional", location)
			}
			if mount.Source.RetentionPolicy != retentionDeleteWithNotebook && mount.Source.RetentionPolicy != retentionRetain {
				return fmt.Errorf("%s managed source retentionPolicy must be %s or %s", location, retentionDeleteWithNotebook, retentionRetain)
			}
			if len(mount.Source.Spec) == 0 {
				return fmt.Errorf("%s managed source spec is required", location)
			}
		default:
			return fmt.Errorf("%s source.type must be existing or managed", location)
		}
	}
	if workspaceCount > 1 {
		return fmt.Errorf("workload %s defines more than one workspace PVC mount", workload)
	}
	return nil
}

func (cfg RuntimeConfig) ExternalPVCWaitTimeout() (time.Duration, error) {
	value := strings.TrimSpace(cfg.Defaults.ExternalPVCWaitTimeout)
	if value == "" {
		return defaultExternalPVCWaitTimeout, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("defaults.externalPVCWaitTimeout must be a positive duration")
	}
	return duration, nil
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

func splitNonEmpty(value string) []string {
	result := []string{}
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
