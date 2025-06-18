package main

import (
	"context"
	"fmt"
	"sandbox-backend-service/pkg/constants"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (ps *profileSync) addProfileToDb(profile KubeflowProfile) error {
	ctx, cancel := context.WithTimeout(ps.rootCtx, 30*time.Second)
	defer cancel()

	return WithDBRetry(ctx, ps.logger, func() (constants.ShouldContinue, error) {
		_, err := ps.pgPool.Exec(ctx, `
		INSERT INTO profiles (user_id, email)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO NOTHING`,
			profile.UserID, profile.Email)
		if err != nil {
			return constants.RetryContinue, fmt.Errorf("failed to insert profile into DB: %v", err)
		}
		return constants.RetryStop, nil
	})
}

func (ps *profileSync) addProfileToK8s(profile Profile) error {
	ctx, cancel := context.WithTimeout(ps.rootCtx, 30*time.Second)
	defer cancel()
	profileGVR := schema.GroupVersionResource{
		Group:    "kubeflow.org",
		Version:  "v1",
		Resource: "profiles",
	}
	profileObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubeflow.org/v1",
			"kind":       "Profile",
			"metadata": map[string]interface{}{
				"name": profile.UserID,
			},
			"spec": map[string]interface{}{
				"owner": map[string]interface{}{
					"kind": "User",
					"name": profile.Email,
				},
			},
		},
	}

	return WithK8sRetry(ctx, ps.logger, func() (constants.ShouldContinue, error) {
		_, err := ps.dynamicClient.Dynamic.Resource(profileGVR).Create(ctx, profileObj, metav1.CreateOptions{})
		if err != nil {
			if k8serrors.IsAlreadyExists(err) {
				ps.logger.Info("profile already exists in K8s", "user_id", profile.UserID)
				return constants.RetryStop, nil
			}
			return constants.RetryContinue, fmt.Errorf("failed to create profile in K8s: %v", err)
		}
		return constants.RetryStop, nil
	})
}

func (ps *profileSync) updateOrphanProfile(profile Profile, missingFromK8s bool) error {
	ctx, cancel := context.WithTimeout(ps.rootCtx, 30*time.Second)
	defer cancel()

	return WithDBRetry(ctx, ps.logger, func() (constants.ShouldContinue, error) {
		_, err := ps.pgPool.Exec(ctx, `
		INSERT INTO orphan_profiles (user_id, email, missing_from_k8s)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET
		missing_from_k8s = $3,
		updated_at = CURRENT_TIMESTAMP`,
			profile.UserID, profile.Email, missingFromK8s)
		if err != nil {
			return constants.RetryContinue, fmt.Errorf("failed to update orphan profile: %v", err)
		}
		return constants.RetryStop, nil
	})
}
