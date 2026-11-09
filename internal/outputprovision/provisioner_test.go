package outputprovision

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

const userNS = "8773fed5-ed3e-47bd-8195-fe02e82379fc"

func b64(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }

func writeSecretDir(t *testing.T, values map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	// Mimic kubelet's atomic-writer layout: keys are symlinks into ..data.
	data := filepath.Join(dir, "..2026_11_09_00_00_00.000000000")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(data), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	for key, value := range values {
		if err := os.WriteFile(filepath.Join(data, key), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..data", key), filepath.Join(dir, key)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func namespace(name string, labels map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": name, "labels": labels},
	}}
}

func issuedClient() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/tls",
		"metadata": map[string]any{"name": "output-data-access-mtls-issued", "namespace": "omop-auth"},
		"data":     map[string]any{"ca.crt": b64("CA"), "tls.crt": b64("CERT"), "tls.key": b64("KEY")},
	}}
}

func profileNamespace(name string) *unstructured.Unstructured {
	return namespace(name, map[string]any{"app.kubernetes.io/part-of": "kubeflow-profile"})
}

func newFakeClient(objects ...runtime.Object) *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		namespaceGVR: "NamespaceList", secretGVR: "SecretList", configMapGVR: "ConfigMapList",
		serviceAccountGVR: "ServiceAccountList", roleBindingGVR: "RoleBindingList",
	}, objects...)
}

func testConfig(t *testing.T, mode Mode) Config {
	return Config{
		Mode: mode, ProfileLabelKey: "app.kubernetes.io/part-of", ProfileLabelValue: "kubeflow-profile",
		RunnerServiceAccount: "output-runner", RunnerClusterRole: "output-runner-executor",
		RegistrySecretName:    "nha-registry-cred",
		RegistrySecretDir:     writeSecretDir(t, map[string]string{".dockerconfigjson": `{"auths":{}}`}),
		FileServiceSecretName: "output-file-service",
		FileServiceSecretDir:  writeSecretDir(t, map[string]string{"FILE_SERVICE_BASE_URL": "http://files", "FILE_SERVICE_TOKEN": "upload-token"}),
		ReplacementConfigMapName: "output-replacements-v1", ProductionEnvSecretName: "output-production-env",
		MTLSEnabled: true, MTLSSecretName: "output-data-access-mtls",
		MTLSSourceNamespace: "omop-auth", MTLSSourceSecret: "output-data-access-mtls-issued",
	}
}

func newTestProvisioner(t *testing.T, mode Mode, objects ...runtime.Object) (*Provisioner, *fake.FakeDynamicClient) {
	t.Helper()
	client := newFakeClient(objects...)
	p, err := New(client, testConfig(t, mode), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return p, client
}

func mustGet(t *testing.T, client *fake.FakeDynamicClient, gvr schema.GroupVersionResource, ns, name string) *unstructured.Unstructured {
	t.Helper()
	object, err := client.Resource(gvr).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get %s/%s: %v", gvr.Resource, name, err)
	}
	return object
}

func TestEnsureNamespaceProvisionsFreshProfileNamespace(t *testing.T) {
	p, client := newTestProvisioner(t, ModeApply, profileNamespace(userNS), issuedClient())
	result, err := p.EnsureNamespace(context.Background(), userNS)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 8 {
		t.Fatalf("expected 8 creations, got %v", result.Changes)
	}

	registry := mustGet(t, client, secretGVR, userNS, "nha-registry-cred")
	if registry.Object["type"] != dockerConfigJSONType {
		t.Fatalf("registry Secret type = %v", registry.Object["type"])
	}
	if data, _, _ := unstructured.NestedMap(registry.Object, "data"); len(data) != 1 || data[".dockerconfigjson"] != b64(`{"auths":{}}`) {
		t.Fatalf("registry Secret data must contain only the mounted key, got keys %v", keys(data))
	}
	mtls := mustGet(t, client, secretGVR, userNS, "output-data-access-mtls")
	data, _, _ := unstructured.NestedMap(mtls.Object, "data")
	if data["client.crt"] != b64("CERT") || data["client.key"] != b64("KEY") || data["ca.crt"] != b64("CA") {
		t.Fatalf("mTLS aliases were not mapped from cert-manager keys")
	}

	sa := mustGet(t, client, serviceAccountGVR, userNS, "output-runner")
	pulls, _, _ := unstructured.NestedSlice(sa.Object, "imagePullSecrets")
	if len(pulls) != 1 || pulls[0].(map[string]any)["name"] != "nha-registry-cred" {
		t.Fatalf("runner ServiceAccount pull secrets = %v", pulls)
	}
	binding := mustGet(t, client, roleBindingGVR, userNS, "output-runner")
	if kind, _, _ := unstructured.NestedString(binding.Object, "roleRef", "kind"); kind != "ClusterRole" {
		t.Fatalf("roleRef kind = %s", kind)
	}
	token := mustGet(t, client, secretGVR, userNS, "output-runner.service-account-token")
	if token.Object["type"] != serviceAccountTokenType || token.GetAnnotations()[serviceAccountAnnotation] != "output-runner" {
		t.Fatalf("executor token Secret is malformed")
	}
	mustGet(t, client, configMapGVR, userNS, "output-replacements-v1")
	mustGet(t, client, secretGVR, userNS, "output-production-env")

	again, err := p.EnsureNamespace(context.Background(), userNS)
	if err != nil || len(again.Changes) != 0 {
		t.Fatalf("second reconcile should be a no-op, got %v, %v", again.Changes, err)
	}
}

func TestEnsureNamespacePreservesAdminOwnedConfiguration(t *testing.T) {
	custom := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "output-replacements-v1", "namespace": userNS},
		"data":     map[string]any{"replacements.json": `{"version":"v1","required":{"a":"b"},"optional":{}}`},
	}}
	env := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
		"metadata": map[string]any{"name": "output-production-env", "namespace": userNS},
		"data":     map[string]any{"DB_NAME": b64("prod")},
	}}
	p, client := newTestProvisioner(t, ModeApply, profileNamespace(userNS), issuedClient(), custom, env)
	if _, err := p.EnsureNamespace(context.Background(), userNS); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, client, configMapGVR, userNS, "output-replacements-v1")
	if value, _, _ := unstructured.NestedString(got.Object, "data", "replacements.json"); value != custom.Object["data"].(map[string]any)["replacements.json"] {
		t.Fatalf("replacement map was overwritten")
	}
	gotEnv := mustGet(t, client, secretGVR, userNS, "output-production-env")
	if value, _, _ := unstructured.NestedString(gotEnv.Object, "data", "DB_NAME"); value != b64("prod") {
		t.Fatalf("production environment was overwritten")
	}
}

func TestEnsureNamespaceRefreshesRotatedCredentials(t *testing.T) {
	stale := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
		"metadata": map[string]any{"name": "output-data-access-mtls", "namespace": userNS},
		"data":     map[string]any{"ca.crt": b64("CA"), "client.crt": b64("OLD"), "client.key": b64("OLDKEY")},
	}}
	p, client := newTestProvisioner(t, ModeApply, profileNamespace(userNS), issuedClient(), stale)
	if _, err := p.EnsureNamespace(context.Background(), userNS); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, client, secretGVR, userNS, "output-data-access-mtls")
	if value, _, _ := unstructured.NestedString(got.Object, "data", "client.crt"); value != b64("CERT") {
		t.Fatalf("renewed client certificate was not published")
	}
	if got.GetLabels()[ManagedLabel] != "true" {
		t.Fatalf("adopted Secret should be labelled as managed")
	}
}

func TestEnsureNamespaceAcceptsLegacyRunnerRoleBinding(t *testing.T) {
	legacy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding",
		"metadata": map[string]any{"name": "output-runner", "namespace": userNS},
		"subjects": []any{map[string]any{"kind": "ServiceAccount", "name": "output-runner", "namespace": userNS}},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "output-runner"},
	}}
	p, _ := newTestProvisioner(t, ModeApply, profileNamespace(userNS), issuedClient(), legacy)
	result, err := p.EnsureNamespace(context.Background(), userNS)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range result.Changes {
		if change == "update RoleBinding/output-runner" || change == "create RoleBinding/output-runner" {
			t.Fatalf("legacy binding should be accepted unchanged")
		}
	}
}

func TestEnsureNamespaceRejectsForeignRoleBinding(t *testing.T) {
	foreign := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding",
		"metadata": map[string]any{"name": "output-runner", "namespace": userNS},
		"subjects": []any{map[string]any{"kind": "ServiceAccount", "name": "output-runner", "namespace": userNS}},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "admin"},
	}}
	p, _ := newTestProvisioner(t, ModeApply, profileNamespace(userNS), issuedClient(), foreign)
	if _, err := p.EnsureNamespace(context.Background(), userNS); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestEnsureNamespaceRefusesIneligibleNamespaces(t *testing.T) {
	p, client := newTestProvisioner(t, ModeApply, namespace("unlabelled", nil), namespace("kube-system", nil), issuedClient())
	for _, name := range []string{"unlabelled", "kube-system", "missing"} {
		if _, err := p.EnsureNamespace(context.Background(), name); !errors.Is(err, ErrNamespaceNotEligible) {
			t.Fatalf("%s: expected not eligible, got %v", name, err)
		}
	}
	if list, _ := client.Resource(secretGVR).Namespace("unlabelled").List(context.Background(), metav1.ListOptions{}); len(list.Items) != 0 {
		t.Fatalf("ineligible namespace received objects")
	}
}

func TestDryRunReportsWithoutWriting(t *testing.T) {
	p, client := newTestProvisioner(t, ModeDryRun, profileNamespace(userNS), issuedClient())
	result, err := p.EnsureNamespace(context.Background(), userNS)
	if err != nil || len(result.Changes) == 0 {
		t.Fatalf("dry-run should report pending creations, got %v, %v", result.Changes, err)
	}
	if list, _ := client.Resource(secretGVR).Namespace(userNS).List(context.Background(), metav1.ListOptions{}); len(list.Items) != 0 {
		t.Fatalf("dry-run wrote %d Secrets", len(list.Items))
	}
}

func TestEnsureAllCoversProfileAndAllowedNamespaces(t *testing.T) {
	p, client := newTestProvisioner(t, ModeApply, profileNamespace(userNS), profileNamespace("other-user"),
		namespace("sandbox", nil), namespace("unlabelled", nil), issuedClient())
	p.config.AllowedNamespaces = []string{"sandbox"}
	results, err := p.EnsureAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 namespaces, got %d", len(results))
	}
	mustGet(t, client, serviceAccountGVR, "sandbox", "output-runner")
	if _, err := client.Resource(serviceAccountGVR).Namespace("unlabelled").Get(context.Background(), "output-runner", metav1.GetOptions{}); err == nil {
		t.Fatalf("unlabelled namespace was provisioned")
	}
}

func TestMissingSourceIsPermanent(t *testing.T) {
	p, _ := newTestProvisioner(t, ModeApply, profileNamespace(userNS))
	_, err := p.EnsureNamespace(context.Background(), userNS)
	if !errors.Is(err, ErrSourceUnavailable) || !(Workflow{P: p}).PermanentProvisioningError(err) {
		t.Fatalf("expected permanent source error, got %v", err)
	}
}

func TestOffModeDisablesProvisioner(t *testing.T) {
	p, err := New(nil, Config{Mode: ModeOff}, nil)
	if err != nil || p != nil {
		t.Fatalf("off mode should return nil provisioner, got %v, %v", p, err)
	}
	if _, err := New(nil, Config{Mode: "sometimes"}, nil); err == nil {
		t.Fatal("unknown mode should be rejected")
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}
