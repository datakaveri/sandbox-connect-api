package main

import (
	"context"
	"fmt"
	"sandbox-backend-service/pkg/constants"
	"time"

	"github.com/jackc/pgx/v5"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func getCreationTimestampFromUnstructured(obj map[string]interface{}) (time.Time, error) {
	meta, ok := obj["metadata"].(map[string]interface{})
	if !ok {
		return time.Time{}, fmt.Errorf("metadata not found in object")
	}
	ts, ok := meta["creationTimestamp"].(string)
	if !ok {
		return time.Time{}, fmt.Errorf("creationTimestamp not found in metadata")
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse creationTimestamp: %w", err)
	}
	return t, nil
}

func (ps *profileSync) addProfileToDbFull(profile KubeflowProfile, createdAtInK8s time.Time) (Profile, error) {
	ctx, cancel := context.WithTimeout(ps.rootCtx, 30*time.Second)
	defer cancel()

	var dbProfile Profile
	err := WithDBRetry(ctx, ps.logger, func() (constants.ShouldContinue, error) {
		row := ps.pgPool.QueryRow(ctx, `
			INSERT INTO profiles (user_id, email, aaa_and_opencost_synced_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (user_id) DO NOTHING
			RETURNING id, user_id, email, total_paid_credit, last_sync_balance, can_create_gpu_notebook, aaa_and_opencost_synced_at, pending_deduction
		`, profile.UserID, profile.Email, createdAtInK8s)

		var id string
		var userID string
		var email string
		var totalCredit float64
		var lastSyncBalance float64
		var canCreateGpuNotebook bool
		var aaaAndOpenCostSyncedAt time.Time
		var pendingDeduction float64

		err := row.Scan(&id, &userID, &email, &totalCredit, &lastSyncBalance, &canCreateGpuNotebook, &aaaAndOpenCostSyncedAt, &pendingDeduction)
		if err == pgx.ErrNoRows {
			row = ps.pgPool.QueryRow(ctx, `
				SELECT id, user_id, email, total_paid_credit, last_sync_balance, can_create_gpu_notebook, aaa_and_opencost_synced_at, pending_deduction
				FROM profiles WHERE user_id = $1
			`, profile.UserID)
			err = row.Scan(&id, &userID, &email, &totalCredit, &lastSyncBalance, &canCreateGpuNotebook, &aaaAndOpenCostSyncedAt, &pendingDeduction)
		}

		if err != nil {
			return constants.RetryContinue, fmt.Errorf("failed to insert or fetch profile from DB: %v", err)
		}

		dbProfile = Profile{
			ProfileID:              id,
			UserID:                 userID,
			Email:                  email,
			TotalPaidCredit:        totalCredit,
			LastSyncBalance:        lastSyncBalance,
			CanCreateGpuNotebook:   canCreateGpuNotebook,
			AaaAndOpenCostSyncedAt: aaaAndOpenCostSyncedAt,
			PendingDeduction:       pendingDeduction,
		}

		return constants.RetryStop, nil
	})

	return dbProfile, err
}

func (ps *profileSync) addProfileToK8s(profile Profile) (KubeflowProfile, error) {
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

	err := WithK8sRetry(ctx, ps.logger, func() (constants.ShouldContinue, error) {
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
	if err != nil {
		return KubeflowProfile{}, err
	}
	return KubeflowProfile{
		UserID:    profile.UserID,
		Email:     profile.Email,
		CreatedAt: time.Now().UTC(), // Use UTC for consistency
	}, nil
}
