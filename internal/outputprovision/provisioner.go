// Package outputprovision reconciles the namespace-local prerequisites that an
// output Workflow needs in a participant namespace: the runner ServiceAccount,
// its executor-only token Secret and RBAC binding, registry/upload credentials,
// the data-access mTLS client Secret, and create-only execution configuration.
//
// It replaces the manual prepare-output-namespace.py procedure. Credential
// values are never logged; change descriptions name objects only.
package outputprovision

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Mode selects whether the provisioner is disabled, only reports drift, or writes.
type Mode string

const (
	ModeOff    Mode = "off"
	ModeDryRun Mode = "dry-run"
	ModeApply  Mode = "apply"
)

const (
	// ManagedLabel marks objects this provisioner created or reconciles.
	ManagedLabel = "sandbox-connect.tgdex.io/output-provisioner"

	serviceAccountTokenType   = "kubernetes.io/service-account-token"
	serviceAccountAnnotation  = "kubernetes.io/service-account.name"
	dockerConfigJSONKey       = ".dockerconfigjson"
	dockerConfigJSONType      = "kubernetes.io/dockerconfigjson"
	replacementsKey           = "replacements.json"
	defaultReplacementsJSON   = `{"optional":{},"required":{},"version":"v1"}`
	rbacGroup                 = "rbac.authorization.k8s.io"
	legacyRunnerRoleKind      = "Role"
	clusterRoleKind           = "ClusterRole"
	serviceAccountSubjectKind = "ServiceAccount"
)

var (
	namespaceGVR      = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	secretGVR         = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	configMapGVR      = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	serviceAccountGVR = schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}
	roleBindingGVR    = schema.GroupVersionResource{Group: rbacGroup, Version: "v1", Resource: "rolebindings"}

	// ErrNamespaceNotEligible means the namespace must never receive output prerequisites.
	ErrNamespaceNotEligible = errors.New("namespace is not eligible for output provisioning")
	// ErrSourceUnavailable means a source credential is missing or malformed; an operator must fix it.
	ErrSourceUnavailable = errors.New("output provisioning source credential is unavailable")
	// ErrConflict means an existing object cannot be reconciled without manual intervention.
	ErrConflict = errors.New("existing namespace object conflicts with output provisioning")

	reservedNamespaces = map[string]bool{
		"default": true, "kube-system": true, "kube-public": true, "kube-node-lease": true,
		"cert-manager": true, "omop-auth": true, "argo": true, "istio-system": true,
		"kubeflow": true, "auth": true,
	}
)

// Config names every object and source the provisioner touches.
type Config struct {
	Mode Mode

	// Namespaces carrying this label are eligible; AllowedNamespaces are eligible regardless.
	ProfileLabelKey   string
	ProfileLabelValue string
	AllowedNamespaces []string

	RunnerServiceAccount string
	// RunnerClusterRole is bound in each namespace to the runner ServiceAccount.
	RunnerClusterRole string

	RegistrySecretName    string
	RegistrySecretDir     string
	FileServiceSecretName string
	FileServiceSecretDir  string

	ReplacementConfigMapName string
	ProductionEnvSecretName  string

	MTLSEnabled         bool
	MTLSSecretName      string
	MTLSSourceNamespace string
	MTLSSourceSecret    string
}

// Validate checks that an enabled provisioner has every name and source it needs.
func (c Config) Validate() error {
	switch c.Mode {
	case ModeOff:
		return nil
	case ModeDryRun, ModeApply:
	default:
		return fmt.Errorf("output provisioning mode must be off, dry-run, or apply")
	}
	required := map[string]string{
		"profile label key": c.ProfileLabelKey, "profile label value": c.ProfileLabelValue,
		"runner service account": c.RunnerServiceAccount, "runner cluster role": c.RunnerClusterRole,
		"registry secret name": c.RegistrySecretName, "registry secret directory": c.RegistrySecretDir,
		"file service secret name": c.FileServiceSecretName, "file service secret directory": c.FileServiceSecretDir,
		"replacement config map": c.ReplacementConfigMapName, "production env secret": c.ProductionEnvSecretName,
	}
	if c.MTLSEnabled {
		required["mTLS secret name"] = c.MTLSSecretName
		required["mTLS source namespace"] = c.MTLSSourceNamespace
		required["mTLS source secret"] = c.MTLSSourceSecret
	}
	var missing []string
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("output provisioning requires: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Result lists what changed (or would change in dry-run) for one namespace.
type Result struct {
	Namespace string
	Changes   []string
}

// Provisioner reconciles output prerequisites in participant namespaces.
type Provisioner struct {
	kube   dynamic.Interface
	config Config
	logger *slog.Logger
	mu     sync.Mutex
}

// New returns a provisioner, or nil when the mode is off.
func New(kube dynamic.Interface, config Config, logger *slog.Logger) (*Provisioner, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.Mode == ModeOff {
		return nil, nil
	}
	if kube == nil || logger == nil {
		return nil, fmt.Errorf("Kubernetes client and logger are required")
	}
	return &Provisioner{kube: kube, config: config, logger: logger.With("component", "output-provisioner")}, nil
}

// Mode reports the configured mode.
func (p *Provisioner) Mode() Mode { return p.config.Mode }

type sources struct {
	registry    map[string]any
	fileService map[string]any
	mtls        map[string]any
}

// EnsureNamespace reconciles one namespace. In dry-run mode nothing is written.
func (p *Provisioner) EnsureNamespace(ctx context.Context, namespace string) (Result, error) {
	src, err := p.loadSources(ctx)
	if err != nil {
		return Result{Namespace: namespace}, err
	}
	return p.ensure(ctx, namespace, src)
}

// EnsureAll reconciles every eligible namespace, continuing past per-namespace failures.
func (p *Provisioner) EnsureAll(ctx context.Context) ([]Result, error) {
	src, err := p.loadSources(ctx)
	if err != nil {
		return nil, err
	}
	names, err := p.eligibleNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	var results []Result
	var errs error
	for _, namespace := range names {
		result, err := p.ensure(ctx, namespace, src)
		results = append(results, result)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("%s: %w", namespace, err))
		}
	}
	return results, errs
}

func (p *Provisioner) eligibleNamespaces(ctx context.Context) ([]string, error) {
	selector := p.config.ProfileLabelKey + "=" + p.config.ProfileLabelValue
	list, err := p.kube.Resource(namespaceGVR).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list profile namespaces: %w", err)
	}
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if !seen[name] && !reservedNamespaces[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for index := range list.Items {
		if list.Items[index].GetDeletionTimestamp() == nil {
			add(list.Items[index].GetName())
		}
	}
	for _, name := range p.config.AllowedNamespaces {
		add(name)
	}
	sort.Strings(names)
	return names, nil
}

func (p *Provisioner) checkEligible(ctx context.Context, namespace string) error {
	if namespace == "" || reservedNamespaces[namespace] {
		return fmt.Errorf("%w: %q is reserved", ErrNamespaceNotEligible, namespace)
	}
	object, err := p.kube.Resource(namespaceGVR).Get(ctx, namespace, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return fmt.Errorf("%w: %q does not exist", ErrNamespaceNotEligible, namespace)
	}
	if err != nil {
		return fmt.Errorf("get namespace: %w", err)
	}
	if object.GetDeletionTimestamp() != nil {
		return fmt.Errorf("%w: %q is terminating", ErrNamespaceNotEligible, namespace)
	}
	for _, allowed := range p.config.AllowedNamespaces {
		if allowed == namespace {
			return nil
		}
	}
	if object.GetLabels()[p.config.ProfileLabelKey] != p.config.ProfileLabelValue {
		return fmt.Errorf("%w: %q is not a profile namespace", ErrNamespaceNotEligible, namespace)
	}
	return nil
}

func (p *Provisioner) loadSources(ctx context.Context) (sources, error) {
	registry, err := readSecretDir(p.config.RegistrySecretDir)
	if err != nil {
		return sources{}, fmt.Errorf("%w: registry: %v", ErrSourceUnavailable, err)
	}
	if _, ok := registry[dockerConfigJSONKey]; !ok {
		return sources{}, fmt.Errorf("%w: registry source lacks %s", ErrSourceUnavailable, dockerConfigJSONKey)
	}
	fileService, err := readSecretDir(p.config.FileServiceSecretDir)
	if err != nil {
		return sources{}, fmt.Errorf("%w: file service: %v", ErrSourceUnavailable, err)
	}
	src := sources{registry: registry, fileService: fileService}
	if !p.config.MTLSEnabled {
		return src, nil
	}
	issued, err := p.kube.Resource(secretGVR).Namespace(p.config.MTLSSourceNamespace).
		Get(ctx, p.config.MTLSSourceSecret, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return sources{}, fmt.Errorf("%w: issued mTLS client Secret is missing", ErrSourceUnavailable)
	}
	if err != nil {
		return sources{}, fmt.Errorf("get issued mTLS client Secret: %w", err)
	}
	data, _, _ := unstructured.NestedMap(issued.Object, "data")
	aliases := map[string]any{}
	for target, source := range map[string]string{"ca.crt": "ca.crt", "client.crt": "tls.crt", "client.key": "tls.key"} {
		value, ok := data[source].(string)
		if !ok || value == "" {
			return sources{}, fmt.Errorf("%w: issued mTLS client Secret lacks %s", ErrSourceUnavailable, source)
		}
		aliases[target] = value
	}
	src.mtls = aliases
	return src, nil
}

// readSecretDir reads a mounted Secret volume into base64 Secret data.
// Kubelet's internal "..data"/"..<timestamp>" entries are skipped.
func readSecretDir(dir string) (map[string]any, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read mounted Secret directory")
	}
	data := map[string]any{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "..") {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path) // follows kubelet symlinks
		if err != nil || info.IsDir() {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read mounted Secret key")
		}
		data[name] = base64.StdEncoding.EncodeToString(content)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("mounted Secret directory is empty")
	}
	return data, nil
}

func (p *Provisioner) ensure(ctx context.Context, namespace string, src sources) (Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := Result{Namespace: namespace}
	if err := p.checkEligible(ctx, namespace); err != nil {
		return result, err
	}
	steps := []func() (string, error){
		func() (string, error) {
			return p.ensureSecret(ctx, namespace, p.config.RegistrySecretName, dockerConfigJSONType, src.registry)
		},
		func() (string, error) {
			return p.ensureSecret(ctx, namespace, p.config.FileServiceSecretName, "Opaque", src.fileService)
		},
		func() (string, error) { return p.ensureServiceAccount(ctx, namespace) },
		func() (string, error) { return p.ensureRoleBinding(ctx, namespace) },
		func() (string, error) { return p.ensureTokenSecret(ctx, namespace) },
		func() (string, error) {
			return p.createIfMissing(ctx, configMapGVR, p.object("v1", "ConfigMap", namespace, p.config.ReplacementConfigMapName,
				map[string]any{"data": map[string]any{replacementsKey: defaultReplacementsJSON}}))
		},
		func() (string, error) {
			return p.createIfMissing(ctx, secretGVR, p.object("v1", "Secret", namespace, p.config.ProductionEnvSecretName,
				map[string]any{"type": "Opaque"}))
		},
	}
	if p.config.MTLSEnabled {
		steps = append(steps, func() (string, error) {
			return p.ensureSecret(ctx, namespace, p.config.MTLSSecretName, "Opaque", src.mtls)
		})
	}
	for _, step := range steps {
		change, err := step()
		if err != nil {
			return result, err
		}
		if change != "" {
			result.Changes = append(result.Changes, change)
		}
	}
	if len(result.Changes) > 0 {
		p.logger.Info("output namespace reconciled", "namespace", namespace,
			"mode", string(p.config.Mode), "changes", result.Changes)
	}
	return result, nil
}

func (p *Provisioner) object(apiVersion, kind, namespace, name string, fields map[string]any) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{
			"name": name, "namespace": namespace,
			"labels": map[string]any{ManagedLabel: "true"},
		},
	}
	for key, value := range fields {
		object[key] = value
	}
	return &unstructured.Unstructured{Object: object}
}

func (p *Provisioner) dryRun() bool { return p.config.Mode != ModeApply }

func describe(action string, object *unstructured.Unstructured) string {
	return action + " " + object.GetKind() + "/" + object.GetName()
}

func (p *Provisioner) create(ctx context.Context, gvr schema.GroupVersionResource, object *unstructured.Unstructured) (string, error) {
	change := describe("create", object)
	if p.dryRun() {
		return change, nil
	}
	_, err := p.kube.Resource(gvr).Namespace(object.GetNamespace()).Create(ctx, object, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("create %s/%s: %w", object.GetKind(), object.GetName(), err)
	}
	return change, nil
}

func (p *Provisioner) update(ctx context.Context, gvr schema.GroupVersionResource, object *unstructured.Unstructured) (string, error) {
	change := describe("update", object)
	if p.dryRun() {
		return change, nil
	}
	_, err := p.kube.Resource(gvr).Namespace(object.GetNamespace()).Update(ctx, object, metav1.UpdateOptions{})
	if err != nil {
		return "", fmt.Errorf("update %s/%s: %w", object.GetKind(), object.GetName(), err)
	}
	return change, nil
}

func (p *Provisioner) get(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	object, err := p.kube.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get %s/%s: %w", gvr.Resource, name, err)
	}
	return object, nil
}

// createIfMissing never modifies an existing object; administrators own its content.
func (p *Provisioner) createIfMissing(ctx context.Context, gvr schema.GroupVersionResource, desired *unstructured.Unstructured) (string, error) {
	existing, err := p.get(ctx, gvr, desired.GetNamespace(), desired.GetName())
	if err != nil || existing != nil {
		return "", err
	}
	return p.create(ctx, gvr, desired)
}

func markManaged(object *unstructured.Unstructured) bool {
	labels := object.GetLabels()
	if labels[ManagedLabel] == "true" {
		return false
	}
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ManagedLabel] = "true"
	object.SetLabels(labels)
	return true
}

// ensureSecret keeps a credential Secret identical to its source data.
func (p *Provisioner) ensureSecret(ctx context.Context, namespace, name, secretType string, data map[string]any) (string, error) {
	existing, err := p.get(ctx, secretGVR, namespace, name)
	if err != nil {
		return "", err
	}
	if existing == nil {
		return p.create(ctx, secretGVR, p.object("v1", "Secret", namespace, name,
			map[string]any{"type": secretType, "data": copyMap(data)}))
	}
	if existingType, _, _ := unstructured.NestedString(existing.Object, "type"); existingType != secretType {
		return "", fmt.Errorf("%w: Secret/%s has type %q, expected %q", ErrConflict, name, existingType, secretType)
	}
	current, _, _ := unstructured.NestedMap(existing.Object, "data")
	labelled := markManaged(existing)
	if reflect.DeepEqual(current, data) && !labelled {
		return "", nil
	}
	existing.Object["data"] = copyMap(data)
	return p.update(ctx, secretGVR, existing)
}

func (p *Provisioner) ensureServiceAccount(ctx context.Context, namespace string) (string, error) {
	name := p.config.RunnerServiceAccount
	pullSecret := map[string]any{"name": p.config.RegistrySecretName}
	existing, err := p.get(ctx, serviceAccountGVR, namespace, name)
	if err != nil {
		return "", err
	}
	if existing == nil {
		return p.create(ctx, serviceAccountGVR, p.object("v1", "ServiceAccount", namespace, name,
			map[string]any{"imagePullSecrets": []any{pullSecret}}))
	}
	pulls, _, _ := unstructured.NestedSlice(existing.Object, "imagePullSecrets")
	hasPull := false
	for _, item := range pulls {
		if entry, ok := item.(map[string]any); ok && entry["name"] == p.config.RegistrySecretName {
			hasPull = true
		}
	}
	labelled := markManaged(existing)
	if hasPull && !labelled {
		return "", nil
	}
	if !hasPull {
		existing.Object["imagePullSecrets"] = append(pulls, pullSecret)
	}
	return p.update(ctx, serviceAccountGVR, existing)
}

// ensureRoleBinding binds the shared runner ClusterRole. A legacy binding to the
// identically scoped namespace Role created by the manual script is accepted,
// because roleRef is immutable and replacing it would briefly revoke access.
func (p *Provisioner) ensureRoleBinding(ctx context.Context, namespace string) (string, error) {
	name := p.config.RunnerServiceAccount
	subject := map[string]any{"kind": serviceAccountSubjectKind, "name": name, "namespace": namespace}
	existing, err := p.get(ctx, roleBindingGVR, namespace, name)
	if err != nil {
		return "", err
	}
	if existing == nil {
		return p.create(ctx, roleBindingGVR, p.object(rbacGroup+"/v1", "RoleBinding", namespace, name, map[string]any{
			"subjects": []any{subject},
			"roleRef":  map[string]any{"apiGroup": rbacGroup, "kind": clusterRoleKind, "name": p.config.RunnerClusterRole},
		}))
	}
	kind, _, _ := unstructured.NestedString(existing.Object, "roleRef", "kind")
	roleName, _, _ := unstructured.NestedString(existing.Object, "roleRef", "name")
	validRef := (kind == clusterRoleKind && roleName == p.config.RunnerClusterRole) ||
		(kind == legacyRunnerRoleKind && roleName == name)
	if !validRef {
		return "", fmt.Errorf("%w: RoleBinding/%s references %s/%s", ErrConflict, name, kind, roleName)
	}
	subjects, _, _ := unstructured.NestedSlice(existing.Object, "subjects")
	if len(subjects) == 1 && reflect.DeepEqual(subjects[0], subject) {
		return "", nil
	}
	existing.Object["subjects"] = []any{subject}
	return p.update(ctx, roleBindingGVR, existing)
}

// ensureTokenSecret creates Argo's executor-only token Secret; Kubernetes fills its token.
func (p *Provisioner) ensureTokenSecret(ctx context.Context, namespace string) (string, error) {
	name := p.config.RunnerServiceAccount + ".service-account-token"
	existing, err := p.get(ctx, secretGVR, namespace, name)
	if err != nil {
		return "", err
	}
	if existing == nil {
		desired := p.object("v1", "Secret", namespace, name, map[string]any{"type": serviceAccountTokenType})
		desired.SetAnnotations(map[string]string{serviceAccountAnnotation: p.config.RunnerServiceAccount})
		return p.create(ctx, secretGVR, desired)
	}
	secretType, _, _ := unstructured.NestedString(existing.Object, "type")
	if secretType != serviceAccountTokenType || existing.GetAnnotations()[serviceAccountAnnotation] != p.config.RunnerServiceAccount {
		return "", fmt.Errorf("%w: Secret/%s is not the runner token Secret", ErrConflict, name)
	}
	return "", nil
}

func copyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// Workflow adapts the provisioner to the output controller's just-in-time hook.
// In dry-run mode drift and errors are logged but never block a workflow.
type Workflow struct{ P *Provisioner }

// EnsureNamespace reconciles the namespace before a workflow is created.
func (w Workflow) EnsureNamespace(ctx context.Context, namespace string) error {
	result, err := w.P.EnsureNamespace(ctx, namespace)
	if w.P.dryRun() {
		if err != nil {
			w.P.logger.Warn("dry-run output namespace check failed", "namespace", namespace, "error", err)
		} else if len(result.Changes) > 0 {
			w.P.logger.Warn("dry-run output namespace has drift", "namespace", namespace, "changes", result.Changes)
		}
		return nil
	}
	return err
}

// PermanentProvisioningError reports errors that retrying cannot fix.
func (Workflow) PermanentProvisioningError(err error) bool {
	return errors.Is(err, ErrNamespaceNotEligible) || errors.Is(err, ErrSourceUnavailable) || errors.Is(err, ErrConflict)
}

// Run reconciles all eligible namespaces now and then on every tick until ctx ends.
func (p *Provisioner) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		results, err := p.EnsureAll(ctx)
		changed := 0
		for _, result := range results {
			if len(result.Changes) > 0 {
				changed++
			}
		}
		if err != nil && ctx.Err() == nil {
			p.logger.Error("output namespace reconciliation incomplete", "error", err,
				"namespaces", len(results), "changed", changed, "mode", string(p.config.Mode))
		} else if ctx.Err() == nil {
			p.logger.Info("output namespace reconciliation complete",
				"namespaces", len(results), "changed", changed, "mode", string(p.config.Mode))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
