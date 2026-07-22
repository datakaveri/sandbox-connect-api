package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	platformTokenNotebookLabel       = "sandbox-connect/notebook-name"
	defaultPlatformTokenReadyPort    = 8081
	defaultPlatformTokenReadyTimeout = 40 * time.Second
	platformTokenReadyPollInterval   = 500 * time.Millisecond
)

var platformTokenPodGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

type platformTokenReadyResponse struct {
	Status    string `json:"status"`
	SessionID string `json:"sessionId"`
}

func (app *application) waitForPlatformTokenReady(ctx context.Context, namespace, notebookName, sessionID string) error {
	timeout := time.Duration(app.env.PlatformTokenReadyTimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = defaultPlatformTokenReadyTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	client := &http.Client{
		Transport: transport,
		Timeout:   2 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	for {
		ready, err := app.isPlatformTokenReady(waitCtx, client, namespace, notebookName, sessionID)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}

		timer := time.NewTimer(platformTokenReadyPollInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("wait for platform token readiness: %w", waitCtx.Err())
		case <-timer.C:
		}
	}
}

func (app *application) isPlatformTokenReady(ctx context.Context, client *http.Client, namespace, notebookName, sessionID string) (bool, error) {
	selector := labels.Set{platformTokenNotebookLabel: notebookName}.String()
	pods, err := app.k8sClient.Dynamic.Resource(platformTokenPodGVR).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return false, fmt.Errorf("list notebook pods for platform token readiness: %w", err)
	}

	port := app.env.PlatformTokenReadyPort
	if port <= 0 {
		port = defaultPlatformTokenReadyPort
	}
	for i := range pods.Items {
		podIP, found, err := unstructured.NestedString(pods.Items[i].Object, "status", "podIP")
		if err != nil || !found || net.ParseIP(podIP) == nil {
			continue
		}
		readyURL := url.URL{
			Scheme: "http",
			Host:   net.JoinHostPort(podIP, strconv.Itoa(port)),
			Path:   "/readyz",
		}
		query := readyURL.Query()
		query.Set("sessionId", sessionID)
		readyURL.RawQuery = query.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL.String(), nil)
		if err != nil {
			return false, err
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var status platformTokenReadyResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&status)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK && decodeErr == nil && status.Status == "ready" && status.SessionID == sessionID {
			return true, nil
		}
	}
	return false, nil
}
