package outputargo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"sandbox-backend-service/internal/output"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	workflowGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}
	podGVR      = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
)

type Queue interface {
	ClaimNextOutput(context.Context, string, time.Duration) (output.Record, bool, error)
	SetWorkflow(context.Context, string, string, string, string) error
	SetPhase(context.Context, string, string, output.Status, time.Duration) error
	CompleteOutput(context.Context, string, string, json.RawMessage) error
	FailOutput(context.Context, string, string, string, string) error
	ClaimNextApproval(context.Context, string, time.Duration) (output.ApprovalWork, bool, error)
	SetPublishing(context.Context, string, string, time.Duration) error
	CompleteApproval(context.Context, string, string, output.PublicationResult) error
	FailApproval(context.Context, string, string, string, string) error
}

type Publisher interface {
	Publish(context.Context, output.ApprovalWork) (output.PublicationResult, error)
}

type ControllerConfig struct {
	WorkerID         string
	PollInterval     time.Duration
	PVCWaitTimeout   time.Duration
	ClaimLease       time.Duration
	MaxManifestBytes int
	MaxManifestFiles int
	MaxFileBytes     int64
	MaxOutputBytes   int64
	Workflow         WorkflowConfig
}

type Controller struct {
	queue     Queue
	publisher Publisher
	kube      dynamic.Interface
	config    ControllerConfig
	logger    *slog.Logger
}

func NewController(queue Queue, publisher Publisher, kube dynamic.Interface, config ControllerConfig, logger *slog.Logger) (*Controller, error) {
	if queue == nil || publisher == nil || kube == nil || logger == nil {
		return nil, fmt.Errorf("queue, publisher, Kubernetes client, and logger are required")
	}
	if config.WorkerID == "" || config.PollInterval <= 0 || config.PVCWaitTimeout <= 0 ||
		config.ClaimLease <= config.PollInterval || config.MaxManifestBytes <= 0 ||
		config.MaxManifestFiles <= 0 || config.MaxFileBytes <= 0 ||
		config.MaxOutputBytes <= 0 || config.MaxFileBytes > config.MaxOutputBytes {
		return nil, fmt.Errorf("invalid controller timing, identity, or output limit configuration")
	}
	config.Workflow.Limits = output.Limits{MaxManifestBytes: config.MaxManifestBytes, MaxManifestFiles: config.MaxManifestFiles, MaxFileBytes: config.MaxFileBytes, MaxOutputBytes: config.MaxOutputBytes}
	if err := config.Workflow.Validate(); err != nil {
		return nil, err
	}
	return &Controller{queue: queue, publisher: publisher, kube: kube, config: config, logger: logger}, nil
}

func (c *Controller) RunOnce(ctx context.Context) error {
	record, found, err := c.queue.ClaimNextOutput(ctx, c.config.WorkerID, c.config.ClaimLease)
	if err != nil {
		return fmt.Errorf("claim output: %w", err)
	}
	if found {
		if err := c.reconcileOutput(ctx, record); err != nil {
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

func (c *Controller) reconcileOutput(ctx context.Context, record output.Record) error {
	logger := c.logger.With("output_id", record.ID, "namespace", record.Namespace)
	if record.WorkflowName == nil {
		waitCtx, cancel := context.WithTimeout(ctx, c.config.PVCWaitTimeout)
		err := c.waitForPVCRelease(waitCtx, record)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("wait for source PVC release: %w", err)
		}
		if err != nil {
			return c.failOutput(ctx, record.ID, "pvc_release_timeout",
				"The notebook storage was not released before the output timeout", err)
		}
		workflow, err := BuildWorkflow(record, c.config.Workflow)
		if err != nil {
			return c.failOutput(ctx, record.ID, "workflow_configuration_invalid", "Output configuration is invalid", err)
		}
		created, err := c.kube.Resource(workflowGVR).Namespace(record.Namespace).
			Create(ctx, workflow, metav1.CreateOptions{})
		if k8serrors.IsAlreadyExists(err) {
			created, err = c.kube.Resource(workflowGVR).Namespace(record.Namespace).
				Get(ctx, workflow.GetName(), metav1.GetOptions{})
		}
		if err != nil {
			return c.failOutput(ctx, record.ID, "workflow_create_failed", "The output workflow could not be started", err)
		}
		if err := verifyOutputObject(created, record.ID, "workflow"); err != nil {
			return c.failOutput(ctx, record.ID, "workflow_identity_mismatch", "The output workflow identity is invalid", err)
		}
		uid := string(created.GetUID())
		if err := c.queue.SetWorkflow(ctx, record.ID, c.config.WorkerID, created.GetName(), uid); err != nil {
			return fmt.Errorf("record workflow identity: %w", err)
		}
		record.WorkflowName = stringPointer(created.GetName())
		record.WorkflowUID = stringPointer(uid)
		logger.Info("output workflow created", "workflow", created.GetName(), "workflow_uid", uid)
	}
	return c.watchWorkflow(ctx, record)
}

func (c *Controller) watchWorkflow(ctx context.Context, record output.Record) error {
	for {
		workflow, err := c.kube.Resource(workflowGVR).Namespace(record.Namespace).
			Get(ctx, *record.WorkflowName, metav1.GetOptions{})
		if err != nil {
			if !k8serrors.IsNotFound(err) {
				return fmt.Errorf("get output workflow: %w", err)
			}
			return c.failOutput(ctx, record.ID, "workflow_unavailable", "The output workflow is unavailable", err)
		}
		if err := verifyWorkflowIdentity(workflow, record); err != nil {
			return c.failOutput(ctx, record.ID, "workflow_identity_mismatch", "The output workflow identity is invalid", err)
		}
		phase, _, _ := unstructured.NestedString(workflow.Object, "status", "phase")
		switch phase {
		case "Succeeded":
			manifest, err := workflowManifest(workflow, c.config.MaxManifestBytes,
				c.config.MaxManifestFiles, c.config.MaxFileBytes, c.config.MaxOutputBytes, record.ReviewPrefix)
			if err != nil {
				return c.failOutput(ctx, record.ID, "output_manifest_invalid", "The output manifest is invalid", err)
			}
			return c.queue.CompleteOutput(ctx, record.ID, c.config.WorkerID, manifest)
		case "Failed", "Error":
			return c.failOutput(ctx, record.ID, "workflow_failed", "The output workflow failed", errors.New(phase))
		default:
			status := workflowStatus(workflow)
			if err := c.queue.SetPhase(ctx, record.ID, c.config.WorkerID, status, c.config.ClaimLease); err != nil {
				return fmt.Errorf("update output phase: %w", err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.config.PollInterval):
		}
	}
}

func (c *Controller) waitForPVCRelease(ctx context.Context, record output.Record) error {
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
		if err := c.queue.SetPhase(ctx, record.ID, c.config.WorkerID, output.StatusWaitingForPVC, c.config.ClaimLease); err != nil {
			return fmt.Errorf("renew output claim while waiting for PVC: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.config.PollInterval):
		}
	}
}

func (c *Controller) reconcileApproval(ctx context.Context, work output.ApprovalWork) error {
	if err := c.queue.SetPublishing(ctx, work.Output.ID, c.config.WorkerID, c.config.ClaimLease); err != nil {
		return fmt.Errorf("mark approval as publishing: %w", err)
	}
	result, err := c.publisher.Publish(ctx, work)
	if err != nil {
		return c.failApproval(ctx, work.Output.ID, "publication_failed",
			"The approved output could not be published", err)
	}
	if result.WorkspacePrefix != work.Approval.WorkspacePrefix {
		return c.failApproval(ctx, work.Output.ID, "publication_identity_mismatch",
			"The approved output publication identity is invalid",
			fmt.Errorf("workspace prefix %q does not match expected prefix", result.WorkspacePrefix))
	}
	if err := c.queue.CompleteApproval(ctx, work.Output.ID, c.config.WorkerID, result); err != nil {
		return fmt.Errorf("complete output approval: %w", err)
	}
	c.logger.Info("approved output published", "output_id", work.Output.ID,
		"workspace_manifest_key", result.WorkspaceManifestKey)
	return nil
}

func (c *Controller) failOutput(ctx context.Context, id, code, safeMessage string, cause error) error {
	c.logger.Error("output failed", "output_id", id, "error", cause, "error_code", code)
	if err := c.queue.FailOutput(ctx, id, c.config.WorkerID, code, safeMessage); err != nil {
		return fmt.Errorf("record output failure after %v: %w", cause, err)
	}
	return nil
}

func (c *Controller) failApproval(ctx context.Context, id, code, safeMessage string, cause error) error {
	c.logger.Error("approval publication failed", "output_id", id, "error", cause, "error_code", code)
	if err := c.queue.FailApproval(ctx, id, c.config.WorkerID, code, safeMessage); err != nil {
		return fmt.Errorf("record approval failure after %v: %w", cause, err)
	}
	return nil
}

func workflowStatus(workflow *unstructured.Unstructured) output.Status {
	nodes, _, _ := unstructured.NestedMap(workflow.Object, "status", "nodes")
	ordered := []struct {
		name   string
		status output.Status
	}{{"upload", output.StatusUploading}, {"execute", output.StatusExecuting},
		{"configure", output.StatusConfiguring}, {"convert", output.StatusConverting},
		{"prepare", output.StatusPreparing}}
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
	return output.StatusPreparing
}

func workflowManifest(workflow *unstructured.Unstructured, maxBytes, maxFiles int, maxFileBytes, maxOutputBytes int64, reviewPrefix string) (json.RawMessage, error) {
	parameters, _, _ := unstructured.NestedSlice(workflow.Object, "status", "outputs", "parameters")
	if len(parameters) == 0 {
		nodes, _, _ := unstructured.NestedMap(workflow.Object, "status", "nodes")
		for _, rawNode := range nodes {
			node, ok := rawNode.(map[string]any)
			if !ok || node["name"] != workflow.GetName() {
				continue
			}
			parameters, _, _ = unstructured.NestedSlice(node, "outputs", "parameters")
			break
		}
	}
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
		manifest, err := output.ParseManifest(raw, maxBytes, maxFiles, maxFileBytes, maxOutputBytes)
		if err != nil {
			return nil, err
		}
		if err := output.ValidateManifestPrefix(manifest, reviewPrefix); err != nil {
			return nil, err
		}
		return raw, nil
	}
	return nil, fmt.Errorf("manifest output parameter is missing")
}

func verifyOutputObject(object *unstructured.Unstructured, outputID, kind string) error {
	if object.GetLabels()["sandbox-connect.tgdex.io/output-id"] != outputID {
		return fmt.Errorf("%s %q is not owned by output %s", kind, object.GetName(), outputID)
	}
	return nil
}

func verifyWorkflowIdentity(workflow *unstructured.Unstructured, record output.Record) error {
	if err := verifyOutputObject(workflow, record.ID, "workflow"); err != nil {
		return err
	}
	if record.WorkflowUID != nil && *record.WorkflowUID != "" && string(workflow.GetUID()) != *record.WorkflowUID {
		return fmt.Errorf("workflow %q UID does not match the recorded UID", workflow.GetName())
	}
	return nil
}

func stringPointer(value string) *string { return &value }
