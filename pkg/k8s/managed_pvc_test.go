package k8s

import (
	"context"
	"testing"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
)

func testPVC(namespace, name string, labels map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"namespace": namespace, "name": name, "labels": labels},
	}}
}

func TestDeleteManagedNotebookPVCsPreservesExternalAndRetainedClaims(t *testing.T) {
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(),
		testPVC("user-ns", "delete-me", map[string]any{
			ManagedPVCLabel: "true", NotebookPVCLabel: "demo", RetentionPVCLabel: RetentionDeleteWithNotebook,
		}),
		testPVC("user-ns", "retained", map[string]any{
			ManagedPVCLabel: "true", RetentionPVCLabel: RetentionKeep,
		}),
		testPVC("user-ns", "external", map[string]any{}),
	)

	if err := DeleteManagedNotebookPVCs(context.Background(), client, "user-ns", "demo"); err != nil {
		t.Fatalf("DeleteManagedNotebookPVCs returned error: %v", err)
	}
	if _, err := client.Resource(managedPVCGVR).Namespace("user-ns").Get(context.Background(), "delete-me", metav1.GetOptions{}); !k8serrors.IsNotFound(err) {
		t.Fatalf("delete-with-notebook PVC still exists or returned unexpected error: %v", err)
	}
	for _, name := range []string{"retained", "external"} {
		if _, err := client.Resource(managedPVCGVR).Namespace("user-ns").Get(context.Background(), name, metav1.GetOptions{}); err != nil {
			t.Fatalf("PVC %s should have been preserved: %v", name, err)
		}
	}
}
