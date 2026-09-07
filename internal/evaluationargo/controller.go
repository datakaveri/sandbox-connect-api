package evaluationargo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"sandbox-backend-service/internal/evaluation"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	workflowGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}
	podGVR      = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	jobGVR      = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
)

type Queue interface {
	ClaimNextEvaluation(context.Context, string, time.Duration) (evaluation.Record, bool, error)
	SetWorkflow(context.Context, string, string, string, string) error
	SetPhase(context.Context, string, string, evaluation.Status, time.Duration) error
	CompleteEvaluation(context.Context, string, string, json.RawMessage) error
	FailEvaluation(context.Context, string, string, string, string) error
	ClaimNextApproval(context.Context, string, time.Duration) (evaluation.ApprovalWork, bool, error)
	SetCopyJob(context.Context, string, string, string) error
	RenewApprovalClaim(context.Context, string, string, time.Duration) error
	CompleteApproval(context.Context, string, string) error
	FailApproval(context.Context, string, string, string, string) error
}

type ControllerConfig struct {
	WorkerID         string
	PollInterval     time.Duration
	PVCWaitTimeout   time.Duration
	ClaimLease       time.Duration
	MaxManifestBytes int
	MaxManifestFiles int
	Workflow         WorkflowConfig
	CopyJob          CopyJobConfig
}

type Controller struct {
	queue  Queue
	kube   dynamic.Interface
	config ControllerConfig
	logger *slog.Logger
}

func NewController(queue Queue, kube dynamic.Interface, config ControllerConfig, logger *slog.Logger) (*Controller, error) {
	if queue == nil || kube == nil || logger == nil {
		return nil, fmt.Errorf("queue, Kubernetes client, and logger are required")
	}
	if config.WorkerID == "" || config.PollInterval <= 0 || config.PVCWaitTimeout <= 0 ||
		config.ClaimLease <= config.PollInterval || config.MaxManifestBytes <= 0 || config.MaxManifestFiles <= 0 {
		return nil, fmt.Errorf("invalid controller timing, identity, or manifest limit configuration")
	}
	if err := config.Workflow.Validate(); err != nil {
		return nil, err
	}
	if err := config.CopyJob.Validate(); err != nil {
		return nil, err
	}
	return &Controller{queue: queue, kube: kube, config: config, logger: logger}, nil
}

func (c *Controller) RunOnce(ctx context.Context) error {
	record, found, err := c.queue.ClaimNextEvaluation(ctx, c.config.WorkerID, c.config.ClaimLease)
	if err != nil {
		return fmt.Errorf("claim evaluation: %w", err)
	}
	if found {
		if err := c.reconcileEvaluation(ctx, record); err != nil {
			return err
		}
	}
	work, found, err := c.queue.ClaimNextApproval(ctx, c.config.WorkerID, c.config.ClaimLease)
	if err != nil {
		return fmt.Errorf("claim approval: %w", err)
	}
	if found {
		return c.reconcileApproval(ctx, work)
	}
	return nil
}

func (c *Controller) reconcileEvaluation(ctx context.Context, record evaluation.Record) error {
	logger := c.logger.With("evaluation_id", record.ID, "namespace", record.Namespace)
	if record.WorkflowName == nil {
		waitCtx, cancel := context.WithTimeout(ctx, c.config.PVCWaitTimeout)
		err := c.waitForPVCRelease(waitCtx, record)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("wait for source PVC release: %w", err)
		}
		if err != nil {
			return c.failEvaluation(ctx, record.ID, "pvc_release_timeout",
				"The notebook storage was not released before the evaluation timeout", err)
		}
		workflow, err := BuildWorkflow(record, c.config.Workflow)
		if err != nil {
			return c.failEvaluation(ctx, record.ID, "workflow_configuration_invalid", "Evaluation configuration is invalid", err)
		}
		created, err := c.kube.Resource(workflowGVR).Namespace(record.Namespace).
			Create(ctx, workflow, metav1.CreateOptions{})
		if k8serrors.IsAlreadyExists(err) {
			created, err = c.kube.Resource(workflowGVR).Namespace(record.Namespace).
				Get(ctx, workflow.GetName(), metav1.GetOptions{})
		}
		if err != nil {
			return c.failEvaluation(ctx, record.ID, "workflow_create_failed", "The evaluation workflow could not be started", err)
		}
		if err := verifyEvaluationObject(created, record.ID, "workflow"); err != nil {
			return c.failEvaluation(ctx, record.ID, "workflow_identity_mismatch", "The evaluation workflow identity is invalid", err)
		}
		uid := string(created.GetUID())
		if err := c.queue.SetWorkflow(ctx, record.ID, c.config.WorkerID, created.GetName(), uid); err != nil {
			return fmt.Errorf("record workflow identity: %w", err)
		}
		record.WorkflowName = stringPointer(created.GetName())
		record.WorkflowUID = stringPointer(uid)
		logger.Info("evaluation workflow created", "workflow", created.GetName(), "workflow_uid", uid)
	}
	return c.watchWorkflow(ctx, record)
}

func (c *Controller) watchWorkflow(ctx context.Context, record evaluation.Record) error {
	for {
		workflow, err := c.kube.Resource(workflowGVR).Namespace(record.Namespace).
			Get(ctx, *record.WorkflowName, metav1.GetOptions{})
		if err != nil {
			if !k8serrors.IsNotFound(err) {
				return fmt.Errorf("get evaluation workflow: %w", err)
			}
			return c.failEvaluation(ctx, record.ID, "workflow_unavailable", "The evaluation workflow is unavailable", err)
		}
		if err := verifyWorkflowIdentity(workflow, record); err != nil {
			return c.failEvaluation(ctx, record.ID, "workflow_identity_mismatch", "The evaluation workflow identity is invalid", err)
		}
		phase, _, _ := unstructured.NestedString(workflow.Object, "status", "phase")
		switch phase {
		case "Succeeded":
			manifest, err := workflowManifest(workflow, c.config.MaxManifestBytes, c.config.MaxManifestFiles)
			if err != nil {
				return c.failEvaluation(ctx, record.ID, "output_manifest_invalid", "The evaluation output manifest is invalid", err)
			}
			return c.queue.CompleteEvaluation(ctx, record.ID, c.config.WorkerID, manifest)
		case "Failed", "Error":
			return c.failEvaluation(ctx, record.ID, "workflow_failed", "The evaluation workflow failed", errors.New(phase))
		default:
			status := workflowStatus(workflow)
			if err := c.queue.SetPhase(ctx, record.ID, c.config.WorkerID, status, c.config.ClaimLease); err != nil {
				return fmt.Errorf("update evaluation phase: %w", err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.config.PollInterval):
		}
	}
}

func (c *Controller) waitForPVCRelease(ctx context.Context, record evaluation.Record) error {
	for {
		pods, err := c.kube.Resource(podGVR).Namespace(record.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		inUse := false
		for index := range pods.Items {
			phase, _, _ := unstructured.NestedString(pods.Items[index].Object, "status", "phase")
			if phase == "Succeeded" || phase == "Failed" {
				continue
			}
			volumes, _, _ := unstructured.NestedSlice(pods.Items[index].Object, "spec", "volumes")
			for _, item := range volumes {
				volume, ok := item.(map[string]any)
				if !ok {
					continue
				}
				mountedClaim, _, _ := unstructured.NestedString(volume, "persistentVolumeClaim", "claimName")
				if mountedClaim == record.SourcePVC {
					inUse = true
					break
				}
			}
		}
		if !inUse {
			return nil
		}
		if err := c.queue.SetPhase(ctx, record.ID, c.config.WorkerID, evaluation.StatusWaitingForPVC, c.config.ClaimLease); err != nil {
			return fmt.Errorf("renew evaluation claim while waiting for PVC: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.config.PollInterval):
		}
	}
}

func (c *Controller) reconcileApproval(ctx context.Context, work evaluation.ApprovalWork) error {
	jobName := work.Approval.CopyJobName
	if jobName == nil {
		job, err := BuildCopyJob(work, c.config.CopyJob)
		if err != nil {
			return c.failApproval(ctx, work.Evaluation.ID, "copy_configuration_invalid", "Approval copy configuration is invalid", err)
		}
		created, err := c.kube.Resource(jobGVR).Namespace(work.Evaluation.Namespace).
			Create(ctx, job, metav1.CreateOptions{})
		if k8serrors.IsAlreadyExists(err) {
			created, err = c.kube.Resource(jobGVR).Namespace(work.Evaluation.Namespace).
				Get(ctx, job.GetName(), metav1.GetOptions{})
		}
		if err != nil {
			return c.failApproval(ctx, work.Evaluation.ID, "copy_job_create_failed", "The approved output copy could not be started", err)
		}
		if err := verifyCopyJobIdentity(created, work); err != nil {
			return c.failApproval(ctx, work.Evaluation.ID, "copy_job_identity_mismatch", "The approved output copy identity is invalid", err)
		}
		if err := c.queue.SetCopyJob(ctx, work.Evaluation.ID, c.config.WorkerID, created.GetName()); err != nil {
			return err
		}
		jobName = stringPointer(created.GetName())
	}
	for {
		job, err := c.kube.Resource(jobGVR).Namespace(work.Evaluation.Namespace).
			Get(ctx, *jobName, metav1.GetOptions{})
		if err != nil {
			if !k8serrors.IsNotFound(err) {
				return fmt.Errorf("get approval copy job: %w", err)
			}
			return c.failApproval(ctx, work.Evaluation.ID, "copy_job_unavailable", "The approved output copy is unavailable", err)
		}
		if err := verifyCopyJobIdentity(job, work); err != nil {
			return c.failApproval(ctx, work.Evaluation.ID, "copy_job_identity_mismatch", "The approved output copy identity is invalid", err)
		}
		succeeded, _, _ := unstructured.NestedInt64(job.Object, "status", "succeeded")
		failed, _, _ := unstructured.NestedInt64(job.Object, "status", "failed")
		if succeeded > 0 {
			return c.queue.CompleteApproval(ctx, work.Evaluation.ID, c.config.WorkerID)
		}
		if failed > 0 {
			return c.failApproval(ctx, work.Evaluation.ID, "copy_job_failed", "The approved output copy failed", errors.New("job failed"))
		}
		if err := c.queue.RenewApprovalClaim(ctx, work.Evaluation.ID, c.config.WorkerID, c.config.ClaimLease); err != nil {
			return fmt.Errorf("renew approval claim: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.config.PollInterval):
		}
	}
}

func (c *Controller) failEvaluation(ctx context.Context, id, code, safeMessage string, cause error) error {
	c.logger.Error("evaluation failed", "evaluation_id", id, "error", cause, "error_code", code)
	if err := c.queue.FailEvaluation(ctx, id, c.config.WorkerID, code, safeMessage); err != nil {
		return fmt.Errorf("record evaluation failure after %v: %w", cause, err)
	}
	return nil
}

func (c *Controller) failApproval(ctx context.Context, id, code, safeMessage string, cause error) error {
	c.logger.Error("approval copy failed", "evaluation_id", id, "error", cause, "error_code", code)
	if err := c.queue.FailApproval(ctx, id, c.config.WorkerID, code, safeMessage); err != nil {
		return fmt.Errorf("record approval failure after %v: %w", cause, err)
	}
	return nil
}

func workflowStatus(workflow *unstructured.Unstructured) evaluation.Status {
	nodes, _, _ := unstructured.NestedMap(workflow.Object, "status", "nodes")
	ordered := []struct {
		name   string
		status evaluation.Status
	}{{"upload", evaluation.StatusUploading}, {"execute", evaluation.StatusExecuting},
		{"configure", evaluation.StatusConfiguring}, {"convert", evaluation.StatusConverting},
		{"prepare", evaluation.StatusPreparing}}
	for _, stage := range ordered {
		for _, rawNode := range nodes {
			node, ok := rawNode.(map[string]any)
			if !ok {
				continue
			}
			templateName, _, _ := unstructured.NestedString(node, "templateName")
			phase, _, _ := unstructured.NestedString(node, "phase")
			if templateName == stage.name && (phase == "Running" || phase == "Succeeded") {
				return stage.status
			}
		}
	}
	return evaluation.StatusPreparing
}

func workflowManifest(workflow *unstructured.Unstructured, maxBytes, maxFiles int) (json.RawMessage, error) {
	parameters, _, _ := unstructured.NestedSlice(workflow.Object, "status", "outputs", "parameters")
	for _, rawParameter := range parameters {
		parameter, ok := rawParameter.(map[string]any)
		if !ok {
			continue
		}
		if parameter["name"] != "manifest-json" {
			continue
		}
		value, _ := parameter["value"].(string)
		raw := json.RawMessage(value)
		if _, err := evaluation.ParseManifest(raw, maxBytes, maxFiles); err != nil {
			return nil, err
		}
		return raw, nil
	}
	return nil, fmt.Errorf("manifest output parameter is missing")
}

func verifyEvaluationObject(object *unstructured.Unstructured, evaluationID, kind string) error {
	if object.GetLabels()["sandbox-connect.tgdex.io/evaluation-id"] != evaluationID {
		return fmt.Errorf("%s %q is not owned by evaluation %s", kind, object.GetName(), evaluationID)
	}
	return nil
}

func verifyCopyJobIdentity(job *unstructured.Unstructured, work evaluation.ApprovalWork) error {
	if err := verifyEvaluationObject(job, work.Evaluation.ID, "copy job"); err != nil {
		return err
	}
	expectedAttempt := fmt.Sprintf("%d", work.Approval.AttemptCount)
	if job.GetLabels()["sandbox-connect.tgdex.io/approval-attempt"] != expectedAttempt {
		return fmt.Errorf("copy job %q does not belong to approval attempt %s", job.GetName(), expectedAttempt)
	}
	return nil
}

func verifyWorkflowIdentity(workflow *unstructured.Unstructured, record evaluation.Record) error {
	if err := verifyEvaluationObject(workflow, record.ID, "workflow"); err != nil {
		return err
	}
	if record.WorkflowUID != nil && *record.WorkflowUID != "" && string(workflow.GetUID()) != *record.WorkflowUID {
		return fmt.Errorf("workflow %q UID does not match the recorded UID", workflow.GetName())
	}
	return nil
}

func stringPointer(value string) *string { return &value }
