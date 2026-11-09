package outputargo

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"sandbox-backend-service/internal/output"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

var errPermanent = errors.New("permanent")

type stubProvisioner struct {
	err   error
	calls []string
}

func (s *stubProvisioner) EnsureNamespace(_ context.Context, namespace string) error {
	s.calls = append(s.calls, namespace)
	return s.err
}
func (*stubProvisioner) PermanentProvisioningError(err error) bool { return errors.Is(err, errPermanent) }

type failRecordingQueue struct {
	publicationQueue
	failCode string
}

func (q *failRecordingQueue) FailOutput(_ context.Context, _, _, code, _ string) error {
	q.failCode = code
	return nil
}

func hookController(queue Queue, provisioner NamespaceProvisioner) *Controller {
	kube := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{podGVR: "PodList"})
	c := &Controller{
		queue: queue, kube: kube,
		config: ControllerConfig{WorkerID: "worker-1", ClaimLease: time.Minute, PollInterval: time.Second, PVCWaitTimeout: time.Second},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	c.SetProvisioner(provisioner)
	return c
}

func TestReconcileOutputFailsOnPermanentProvisioningError(t *testing.T) {
	queue := &failRecordingQueue{}
	provisioner := &stubProvisioner{err: errPermanent}
	record := output.Record{ID: "output-1", Namespace: "user-ns", SourcePVC: "pvc"}
	if err := hookController(queue, provisioner).reconcileOutput(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if queue.failCode != "namespace_provisioning_failed" || len(provisioner.calls) != 1 || provisioner.calls[0] != "user-ns" {
		t.Fatalf("fail code %q, calls %v", queue.failCode, provisioner.calls)
	}
}

func TestReconcileOutputRetriesTransientProvisioningError(t *testing.T) {
	queue := &failRecordingQueue{}
	record := output.Record{ID: "output-1", Namespace: "user-ns", SourcePVC: "pvc"}
	err := hookController(queue, &stubProvisioner{err: errors.New("apiserver timeout")}).reconcileOutput(context.Background(), record)
	if err == nil || queue.failCode != "" {
		t.Fatalf("transient error should be returned for retry, got err=%v fail=%q", err, queue.failCode)
	}
}
