package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var runtimeAssetSecretNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func normalizeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func validateRuntimeAssetURL(rawURL, fieldName string, requireGitHub bool) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be a valid URL", fieldName)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("%s must use https", fieldName)
	}
	if parsed.User != nil {
		return fmt.Errorf("%s must not include credentials", fieldName)
	}
	if requireGitHub && !strings.EqualFold(parsed.Hostname(), "github.com") {
		return fmt.Errorf("%s must be a GitHub HTTPS URL", fieldName)
	}
	return nil
}

func validateRuntimeAssetSecretName(name string) error {
	if len(name) > 253 || !runtimeAssetSecretNameRE.MatchString(name) {
		return fmt.Errorf("gitTokenSecretName must be a valid Kubernetes DNS label")
	}
	return nil
}

type runtimeAssetFields struct {
	FileURL            *string
	GitURL             *string
	GitAccessToken     *string
	GitTokenSecretName *string
}

func normalizeAndValidateRuntimeAssetFields(fields *runtimeAssetFields) error {
	fields.FileURL = normalizeOptionalString(fields.FileURL)
	fields.GitURL = normalizeOptionalString(fields.GitURL)
	fields.GitAccessToken = normalizeOptionalString(fields.GitAccessToken)
	fields.GitTokenSecretName = normalizeOptionalString(fields.GitTokenSecretName)

	if fields.FileURL != nil {
		if err := validateRuntimeAssetURL(*fields.FileURL, "fileUrl", false); err != nil {
			return err
		}
	}
	if fields.GitURL != nil {
		if err := validateRuntimeAssetURL(*fields.GitURL, "gitUrl", true); err != nil {
			return err
		}
	}
	if fields.GitAccessToken != nil {
		if fields.GitURL == nil {
			return fmt.Errorf("gitAccessToken requires gitUrl")
		}
		if fields.GitTokenSecretName != nil {
			return fmt.Errorf("use either gitAccessToken or gitTokenSecretName, not both")
		}
	}
	if fields.GitTokenSecretName != nil {
		if fields.GitURL == nil {
			return fmt.Errorf("gitTokenSecretName requires gitUrl")
		}
		if err := validateRuntimeAssetSecretName(*fields.GitTokenSecretName); err != nil {
			return err
		}
	}
	return nil
}

func normalizeAndValidateRuntimeAssets(req *CreateBookingRequest) error {
	fields := runtimeAssetFields{
		FileURL:            req.FileURL,
		GitURL:             req.GitURL,
		GitAccessToken:     req.GitAccessToken,
		GitTokenSecretName: req.GitTokenSecretName,
	}
	if err := normalizeAndValidateRuntimeAssetFields(&fields); err != nil {
		return err
	}
	req.FileURL = fields.FileURL
	req.GitURL = fields.GitURL
	req.GitAccessToken = fields.GitAccessToken
	req.GitTokenSecretName = fields.GitTokenSecretName
	return nil
}

func normalizeAndValidateNotebookRuntimeAssets(req *NotebookRequest) error {
	fields := runtimeAssetFields{
		FileURL:            req.FileURL,
		GitURL:             req.GitURL,
		GitAccessToken:     req.GitAccessToken,
		GitTokenSecretName: req.GitTokenSecretName,
	}
	if err := normalizeAndValidateRuntimeAssetFields(&fields); err != nil {
		return err
	}
	req.FileURL = fields.FileURL
	req.GitURL = fields.GitURL
	req.GitAccessToken = fields.GitAccessToken
	req.GitTokenSecretName = fields.GitTokenSecretName
	return nil
}

func gitAccessTokenSecretName(notebookName string) string {
	return notebookName + "-git-token"
}

func (app *application) createOrUpdateGitAccessTokenSecret(ctx context.Context, namespace, secretName, token string) error {
	secretGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}
	encodedToken := base64.StdEncoding.EncodeToString([]byte(token))
	newSecret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      secretName,
			"namespace": namespace,
			"labels": map[string]any{
				"sandbox-connect/runtime-git-token": "true",
			},
		},
		"type": "Opaque",
		"data": map[string]any{
			"token": encodedToken,
		},
	}}

	existing, err := app.k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			_, err = app.k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Create(ctx, newSecret, metav1.CreateOptions{})
			return err
		}
		return err
	}

	data, found, err := unstructured.NestedStringMap(existing.Object, "data")
	if err != nil || !found {
		data = map[string]string{}
	}
	data["token"] = encodedToken
	unstructured.SetNestedStringMap(existing.Object, data, "data")
	labels := existing.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["sandbox-connect/runtime-git-token"] = "true"
	existing.SetLabels(labels)
	_, err = app.k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func (app *application) verifyGitTokenSecret(ctx context.Context, namespace, secretName string) error {
	secretGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}
	secret, err := app.k8sClient.Dynamic.Resource(secretGVR).Namespace(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return fmt.Errorf("gitTokenSecretName secret not found")
		}
		return fmt.Errorf("failed to verify gitTokenSecretName secret: %w", err)
	}
	data, found, err := unstructured.NestedStringMap(secret.Object, "data")
	if err != nil || !found {
		return fmt.Errorf("gitTokenSecretName secret must contain token key")
	}
	if _, ok := data["token"]; !ok {
		return fmt.Errorf("gitTokenSecretName secret must contain token key")
	}
	return nil
}
