package k8s

import (
	"context"
	"errors"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	ManagedPVCLabel   = "sandbox-connect.tgdex.io/managed"
	NotebookPVCLabel  = "sandbox-connect.tgdex.io/notebook"
	VolumePVCLabel    = "sandbox-connect.tgdex.io/volume"
	RetentionPVCLabel = "sandbox-connect.tgdex.io/retention"

	RetentionDeleteWithNotebook = "delete-with-notebook"
	RetentionKeep               = "retain"
)

var managedPVCGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}

// DeleteManagedNotebookPVCs removes only worker-created claims explicitly
// labelled for deletion with the named notebook. Platform-owned and retained
// claims never match this selector.
func DeleteManagedNotebookPVCs(ctx context.Context, client dynamic.Interface, namespace, notebookName string) error {
	selector := labels.Set{
		ManagedPVCLabel:   "true",
		NotebookPVCLabel:  notebookName,
		RetentionPVCLabel: RetentionDeleteWithNotebook,
	}.AsSelector().String()

	claims, err := client.Resource(managedPVCGVR).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	policy := metav1.DeletePropagationBackground
	var result error
	for _, claim := range claims.Items {
		err := client.Resource(managedPVCGVR).Namespace(namespace).Delete(ctx, claim.GetName(), metav1.DeleteOptions{PropagationPolicy: &policy})
		if err != nil && !k8serrors.IsNotFound(err) {
			result = errors.Join(result, err)
		}
	}
	return result
}
