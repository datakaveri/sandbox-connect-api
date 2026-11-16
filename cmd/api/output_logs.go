package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"sandbox-backend-service/internal/output"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

const (
	outputLogDefaultTailLines = int64(1000)
	outputLogMaxTailLines     = int64(5000)
	outputLogMaxStreamTime    = 5 * time.Minute
	outputLogPollInterval     = time.Second
	outputLogHeartbeat        = 15 * time.Second
	outputLogMaxLineBytes     = 2 << 20
)

var outputLogStages = map[string]int{
	"prepare":   0,
	"convert":   1,
	"configure": 2,
	"execute":   3,
	"upload":    4,
}

type outputPodLogSource interface {
	ListPods(context.Context, string, string) ([]corev1.Pod, error)
	StreamPodLogs(context.Context, string, string, corev1.PodLogOptions) (io.ReadCloser, error)
}

type kubernetesOutputLogSource struct {
	client kubernetes.Interface
}

func newKubernetesOutputLogSource(client kubernetes.Interface) outputPodLogSource {
	if client == nil {
		return nil
	}
	return &kubernetesOutputLogSource{client: client}
}

func (s *kubernetesOutputLogSource) ListPods(ctx context.Context, namespace, selector string) ([]corev1.Pod, error) {
	list, err := s.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (s *kubernetesOutputLogSource) StreamPodLogs(
	ctx context.Context,
	namespace string,
	podName string,
	options corev1.PodLogOptions,
) (io.ReadCloser, error) {
	return s.client.CoreV1().Pods(namespace).GetLogs(podName, &options).Stream(ctx)
}

type OutputLogEvent struct {
	Stage     string `json:"stage"`
	Timestamp string `json:"timestamp,omitempty"`
	Message   string `json:"message"`
}

type OutputLogStatusEvent struct {
	OutputID string `json:"outputId"`
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
}

// streamOutputLogs godoc
// @Summary      Stream an owned output run's live execution logs
// @Description  Streams Server-Sent Events for the prepare, convert, configure, execute, and upload workflow stages. The stream is live-only and may be unavailable after workflow pod cleanup.
// @Tags         outputs
// @Produce      text/event-stream
// @Param        output_id path string true "Output ID"
// @Param        tailLines query int false "Initial lines per workflow pod (1-5000)" default(1000)
// @Success      200 {string} string "Server-Sent Events: status, log, warning, unavailable, complete"
// @Failure      400 {object} Error400
// @Failure      401 {object} Error401
// @Failure      404 {object} Error404
// @Failure      503 {object} Error503
// @Security     BearerAuth
// @Router       /v1/outputs/{output_id}/logs [get]
func (app *application) streamOutputLogs(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	if !app.outputFeatureReady(w, r) {
		return
	}
	if app.outputLogs == nil {
		sendError(w, logger, http.StatusServiceUnavailable, "Output log service is unavailable")
		return
	}
	user, ok := r.Context().Value(UserContextKey).(UserInfo)
	if !ok {
		sendError(w, logger, http.StatusUnauthorized, "Unauthorized")
		return
	}
	outputID, ok := validOutputID(w, r)
	if !ok {
		return
	}
	tailLines, ok := parseOutputLogTailLines(w, r)
	if !ok {
		return
	}
	record, err := app.outputStore.GetOwned(r.Context(), outputID, user.Sub)
	if err != nil {
		handleOutputLookupError(w, r, err)
		return
	}

	deadline := time.Now().Add(outputLogMaxStreamTime)
	if !user.ExpiresAt.IsZero() && user.ExpiresAt.Before(deadline) {
		deadline = user.ExpiresAt
	}
	if !deadline.After(time.Now()) {
		sendError(w, logger, http.StatusUnauthorized, "Token expired")
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()

	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		logger.Warn("failed to disable write deadline for output log stream", "error", err)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		logger.Warn("output log streaming is not supported by response writer", "error", err)
		return
	}

	lastStatus := record.Status
	if err := writeOutputSSEJSON(w, controller, "status", OutputLogStatusEvent{
		OutputID: record.ID,
		Status:   string(record.Status),
	}); err != nil {
		return
	}

	pollTicker := time.NewTicker(outputLogPollInterval)
	heartbeatTicker := time.NewTicker(outputLogHeartbeat)
	defer pollTicker.Stop()
	defer heartbeatTicker.Stop()

	streamedPods := make(map[string]struct{})
	announcedPods := make(map[string]struct{})
	announcedContainers := make(map[string]struct{})
	sawWorkflowPod := false
	firstLookup := true

	for {
		if !firstLookup {
			record, err = app.outputStore.GetOwned(ctx, outputID, user.Sub)
			if err != nil {
				logger.Error("failed to refresh output during log stream", "error", err, "output_id", outputID)
				_ = writeOutputSSEJSON(w, controller, "warning", OutputLogStatusEvent{
					OutputID: outputID,
					Status:   string(lastStatus),
					Message:  "Output status is temporarily unavailable",
				})
				return
			}
		}
		firstLookup = false

		if record.Status != lastStatus {
			lastStatus = record.Status
			if err := writeOutputSSEJSON(w, controller, "status", OutputLogStatusEvent{
				OutputID: record.ID,
				Status:   string(record.Status),
			}); err != nil {
				return
			}
		}

		var validPods []corev1.Pod
		workflowAssigned := record.WorkflowName != nil && record.WorkflowUID != nil &&
			*record.WorkflowName != "" && *record.WorkflowUID != ""
		podLookupComplete := !workflowAssigned
		if workflowAssigned {
			selector := labels.Set{
				"workflows.argoproj.io/workflow": *record.WorkflowName,
			}.AsSelector().String()
			pods, listErr := app.outputLogs.ListPods(ctx, record.Namespace, selector)
			if listErr != nil {
				if ctx.Err() != nil {
					return
				}
				logger.Warn("failed to list output workflow pods", "error", listErr, "output_id", outputID)
			} else {
				validPods = trustedOutputWorkflowPods(pods, *record.WorkflowName, *record.WorkflowUID)
				podLookupComplete = true
				if len(validPods) > 0 {
					sawWorkflowPod = true
				}
				for i := range validPods {
					pod := validPods[i]
					if _, alreadyStreamed := streamedPods[pod.Name]; alreadyStreamed {
						continue
					}
					stage := pod.Labels["output-stage"]
					if _, announced := announcedPods[pod.Name]; !announced {
						createdAt := pod.CreationTimestamp.Time
						if createdAt.IsZero() {
							createdAt = time.Now()
						}
						if err := writeOutputSSEJSON(w, controller, "log", OutputLogEvent{
							Stage: stage, Timestamp: createdAt.UTC().Format(time.RFC3339Nano),
							Message: "[system] Pod created; waiting for main container",
						}); err != nil {
							return
						}
						announcedPods[pod.Name] = struct{}{}
					}
					if startedAt, started := outputPodMainContainerStarted(pod); started {
						if _, announced := announcedContainers[pod.Name]; !announced {
							if err := writeOutputSSEJSON(w, controller, "log", OutputLogEvent{
								Stage: stage, Timestamp: startedAt.UTC().Format(time.RFC3339Nano),
								Message: "[system] Main container started",
							}); err != nil {
								return
							}
							announcedContainers[pod.Name] = struct{}{}
						}
					}
					streamed := app.streamOutputPod(ctx, w, controller, heartbeatTicker.C,
						record.Namespace, pod.Name, stage, tailLines)
					if streamed || outputLogTerminal(record.Status) {
						streamedPods[pod.Name] = struct{}{}
					}
					if !streamed && outputLogTerminal(record.Status) {
						if err := writeOutputSSEJSON(w, controller, "warning", OutputLogEvent{
							Stage: stage, Message: "Logs for this stage are unavailable",
						}); err != nil {
							return
						}
					}
					if ctx.Err() != nil {
						return
					}
				}
			}
		}

		if outputLogTerminal(record.Status) && podLookupComplete &&
			!hasUnstreamedOutputPods(validPods, streamedPods) {
			if !sawWorkflowPod {
				if err := writeOutputSSEJSON(w, controller, "unavailable", OutputLogStatusEvent{
					OutputID: record.ID,
					Status:   string(record.Status),
					Message:  "Live workflow logs are no longer available",
				}); err != nil {
					return
				}
			}
			_ = writeOutputSSEJSON(w, controller, "complete", OutputLogStatusEvent{
				OutputID: record.ID,
				Status:   string(record.Status),
			})
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-heartbeatTicker.C:
			if err := writeOutputSSEComment(w, controller, "keepalive"); err != nil {
				return
			}
		case <-pollTicker.C:
		}
	}
}

func (app *application) streamOutputPod(
	ctx context.Context,
	w http.ResponseWriter,
	controller *http.ResponseController,
	heartbeats <-chan time.Time,
	namespace string,
	podName string,
	stage string,
	tailLines int64,
) bool {
	stream, err := app.outputLogs.StreamPodLogs(ctx, namespace, podName, corev1.PodLogOptions{
		Container:  "main",
		Follow:     true,
		Timestamps: true,
		TailLines:  &tailLines,
	})
	if err != nil {
		slog.Warn("failed to open output pod log stream", "error", err, "pod", podName)
		return false
	}

	podCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan OutputLogEvent)
	done := make(chan error, 1)
	go scanOutputPodLogs(podCtx, stream, stage, events, done)
	for {
		select {
		case <-ctx.Done():
			_ = stream.Close()
			return true
		case <-heartbeats:
			if writeOutputSSEComment(w, controller, "keepalive") != nil {
				_ = stream.Close()
				return true
			}
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			if writeOutputSSEJSON(w, controller, "log", event) != nil {
				_ = stream.Close()
				return true
			}
		case scanErr := <-done:
			if scanErr != nil && ctx.Err() == nil {
				slog.Warn("output pod log stream ended with an error", "error", scanErr, "pod", podName)
				_ = writeOutputSSEJSON(w, controller, "warning", OutputLogEvent{
					Stage: stage, Message: "Log streaming for this stage ended unexpectedly",
				})
			}
			return true
		}
	}
}

func scanOutputPodLogs(
	ctx context.Context,
	stream io.ReadCloser,
	stage string,
	events chan<- OutputLogEvent,
	done chan<- error,
) {
	defer stream.Close()
	defer close(events)
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), outputLogMaxLineBytes)
	for scanner.Scan() {
		timestamp, message := splitKubernetesLogLine(scanner.Text())
		event := OutputLogEvent{Stage: stage, Timestamp: timestamp, Message: message}
		select {
		case events <- event:
		case <-ctx.Done():
			done <- ctx.Err()
			return
		}
	}
	done <- scanner.Err()
}

func splitKubernetesLogLine(line string) (string, string) {
	parts := strings.SplitN(line, " ", 2)
	if len(parts) == 2 {
		if _, err := time.Parse(time.RFC3339Nano, parts[0]); err == nil {
			return parts[0], parts[1]
		}
	}
	return "", line
}

func trustedOutputWorkflowPods(pods []corev1.Pod, workflowName, workflowUID string) []corev1.Pod {
	trusted := make([]corev1.Pod, 0, len(pods))
	for i := range pods {
		pod := pods[i]
		stage := pod.Labels["output-stage"]
		if _, allowed := outputLogStages[stage]; !allowed {
			continue
		}
		if pod.Labels["workflows.argoproj.io/workflow"] != workflowName {
			continue
		}
		owned := false
		for _, owner := range pod.OwnerReferences {
			if owner.APIVersion == "argoproj.io/v1alpha1" && owner.Kind == "Workflow" &&
				owner.Name == workflowName && string(owner.UID) == workflowUID {
				owned = true
				break
			}
		}
		if owned {
			trusted = append(trusted, pod)
		}
	}
	sort.SliceStable(trusted, func(i, j int) bool {
		iStage := outputLogStages[trusted[i].Labels["output-stage"]]
		jStage := outputLogStages[trusted[j].Labels["output-stage"]]
		if iStage != jStage {
			return iStage < jStage
		}
		if !trusted[i].CreationTimestamp.Equal(&trusted[j].CreationTimestamp) {
			return trusted[i].CreationTimestamp.Before(&trusted[j].CreationTimestamp)
		}
		return trusted[i].Name < trusted[j].Name
	})
	return trusted
}

func outputPodMainContainerStarted(pod corev1.Pod) (time.Time, bool) {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != "main" {
			continue
		}
		if status.State.Running != nil {
			return status.State.Running.StartedAt.Time, true
		}
		if status.State.Terminated != nil {
			return status.State.Terminated.StartedAt.Time, true
		}
	}
	return time.Time{}, false
}

func hasUnstreamedOutputPods(pods []corev1.Pod, streamed map[string]struct{}) bool {
	for i := range pods {
		if _, ok := streamed[pods[i].Name]; !ok {
			return true
		}
	}
	return false
}

func outputLogTerminal(status output.Status) bool {
	return status == output.StatusPendingApproval || status == output.StatusFailed
}

func parseOutputLogTailLines(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("tailLines"))
	if raw == "" {
		return outputLogDefaultTailLines, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 || value > outputLogMaxTailLines {
		sendError(w, getLogger(r), http.StatusBadRequest, "tailLines must be between 1 and 5000")
		return 0, false
	}
	return value, true
}

func isOutputLogStreamRequest(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	return len(parts) == 4 && parts[0] == "v1" && parts[1] == "outputs" &&
		parts[2] != "" && parts[3] == "logs"
}

func writeOutputSSEJSON(
	w io.Writer,
	controller *http.ResponseController,
	event string,
	payload any,
) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	return controller.Flush()
}

func writeOutputSSEComment(w io.Writer, controller *http.ResponseController, comment string) error {
	if _, err := fmt.Fprintf(w, ": %s\n\n", comment); err != nil {
		return err
	}
	return controller.Flush()
}
