package main

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"sandbox-backend-service/pkg/constants"
	"strings"
	"time"

	"github.com/google/uuid"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var runtimeInjectionPodGVR = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "pods",
}

func (nb Notebook) HasRuntimeAssets() bool {
	return nonEmptyString(nb.FileURL) || nonEmptyString(nb.GitURL)
}

func nonEmptyString(value *string) bool {
	return value != nil && strings.TrimSpace(*value) != ""
}

func newRuntimeInjectionPodName(notebookName string) string {
	suffix := uuid.NewString()[:8]
	base := notebookName
	maxBaseLen := 63 - len("-inject-") - len(suffix)
	if len(base) > maxBaseLen {
		base = strings.TrimRight(base[:maxBaseLen], "-")
	}
	if base == "" {
		base = "notebook"
	}
	return fmt.Sprintf("%s-inject-%s", base, suffix)
}

func runtimeAssetFileName(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "downloaded-file"
	}
	name := path.Base(parsed.Path)
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return "downloaded-file"
	}
	return name
}

func runtimeAssetRepoDir(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "repository"
	}
	name := path.Base(strings.TrimSuffix(parsed.Path, "/"))
	name = strings.TrimSuffix(name, ".git")
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return "repository"
	}
	return name
}

func buildRuntimeInjectionScript() string {
	return `set -eu
mkdir -p /workspace

if [ -n "${FILE_URL:-}" ]; then
  echo "[runtime-inject] downloading file"
  wget -O "/workspace/${FILE_NAME}" "${FILE_URL}"
  chown 1000:1000 "/workspace/${FILE_NAME}"
fi

if [ -n "${GIT_URL:-}" ]; then
  echo "[runtime-inject] cloning repository"
  if [ -n "${GIT_TOKEN:-}" ]; then
    git config --global url."https://x-access-token:${GIT_TOKEN}@github.com/".insteadOf "https://github.com/"
  fi
  rm -rf "/workspace/${GIT_CLONE_DIR}"
  git clone --depth 1 "${GIT_URL}" "/workspace/${GIT_CLONE_DIR}"
  chown -R 1000:1000 "/workspace/${GIT_CLONE_DIR}"
fi
`
}

func (w *worker) buildRuntimeInjectionPod(podName string) (*unstructured.Unstructured, error) {
	nb := w.notebook
	workspace, ok := w.workspacePVCMount()
	if !ok {
		return nil, fmt.Errorf("runtime injection requires a configured workspace PVC mount")
	}
	env := []any{}
	if nonEmptyString(nb.FileURL) {
		env = append(env,
			map[string]any{"name": "FILE_URL", "value": strings.TrimSpace(*nb.FileURL)},
			map[string]any{"name": "FILE_NAME", "value": runtimeAssetFileName(*nb.FileURL)},
		)
	}
	if nonEmptyString(nb.GitURL) {
		env = append(env,
			map[string]any{"name": "GIT_URL", "value": strings.TrimSpace(*nb.GitURL)},
			map[string]any{"name": "GIT_CLONE_DIR", "value": runtimeAssetRepoDir(*nb.GitURL)},
		)
	}
	if nonEmptyString(nb.GitTokenSecretName) {
		env = append(env, map[string]any{
			"name": "GIT_TOKEN",
			"valueFrom": map[string]any{
				"secretKeyRef": map[string]any{
					"name": strings.TrimSpace(*nb.GitTokenSecretName),
					"key":  "token",
				},
			},
		})
	}

	podSpec := map[string]any{
		"restartPolicy": "Never",
		"volumes": []any{map[string]any{
			"name": workspace.Name,
			"persistentVolumeClaim": map[string]any{
				"claimName": workspace.ClaimName,
			},
		}},
		"containers": []any{map[string]any{
			"name":    "runtime-injector",
			"image":   w.template.Spec.Lifecycle.RuntimeInjection.Image,
			"command": []any{"/bin/sh", "-c", buildRuntimeInjectionScript()},
			"env":     env,
			"securityContext": map[string]any{
				"privileged":               false,
				"allowPrivilegeEscalation": false,
			},
			"volumeMounts": []any{resolvedVolumeMountSpec(workspace, "/workspace")},
		}},
	}
	if err := w.applyHelperScheduling(podSpec); err != nil {
		return nil, err
	}

	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      podName,
			"namespace": nb.Namespace,
			"labels": map[string]any{
				"app":                               nb.Name,
				"sandbox-connect/runtime-injection": "true",
			},
		},
		"spec": podSpec,
	}}, nil
}

func (w *worker) CreateRuntimeInjectionPod(podName string) error {
	logger := w.logger.With("operation", "CreateRuntimeInjectionPod", "pod", podName)
	ctx, cancel := WithTimeoutContext(context.Background(), K8sCreationTimeout)
	defer cancel()

	pod, err := w.buildRuntimeInjectionPod(podName)
	if err != nil {
		return err
	}
	return WithK8sRetry(ctx, logger, func() (constants.ShouldContinue, error) {
		_, err := w.app.k8sClient.Dynamic.Resource(runtimeInjectionPodGVR).Namespace(w.notebook.Namespace).Create(ctx, pod, metav1.CreateOptions{})
		if k8serrors.IsAlreadyExists(err) {
			logger.Warn("runtime injection pod already exists, will not retry", "error", err)
			return constants.RetryStop, err
		}
		if err != nil {
			logger.Warn("failed to create runtime injection pod, will retry", "error", err)
			return constants.RetryContinue, err
		}
		return constants.RetryStop, nil
	})
}

func (w *worker) WaitRuntimeInjectionPod(podName string) error {
	logger := w.logger.With("operation", "WaitRuntimeInjectionPod", "pod", podName)
	ctx, cancel := WithTimeoutContext(context.Background(), RuntimeInjectionTimeout)
	defer cancel()

	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("runtime injection pod timed out after %v", RuntimeInjectionTimeout)
		case <-ticker.C:
			pod, err := w.app.k8sClient.Dynamic.Resource(runtimeInjectionPodGVR).Namespace(w.notebook.Namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				if k8serrors.IsNotFound(err) {
					return fmt.Errorf("runtime injection pod disappeared")
				}
				logger.Warn("failed to get runtime injection pod status", "error", err)
				continue
			}
			phase, found, err := unstructured.NestedString(pod.Object, "status", "phase")
			if err != nil || !found {
				continue
			}
			switch phase {
			case "Succeeded":
				logger.Info("runtime injection pod succeeded")
				return nil
			case "Failed":
				return fmt.Errorf("runtime injection pod failed")
			}
		}
	}
}

func (w *worker) DeleteRuntimeInjectionPod(podName string) error {
	return w.DeletePod(podName)
}
