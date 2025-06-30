package main

import (
	"context"
	"time"
)

func (ps *profileSync) failedAAARequest(ctx context.Context, profileID string, cost float64, requestTime string,
	statusCode int, errorMessage string, payload []byte) error {
	logger := ps.logger.With("user_id", profileID)
	logger.Debug("Logging failed AAA request", "status_code", statusCode)
	_, err := ps.pgPool.Exec(ctx, `
		INSERT INTO failed_aaa_requests (
			user_id, 
			cost, 
			requested_at, 
			error_message, 
			status_code, 
			request_payload
		) VALUES ($1, $2, $3, $4, $5, $6)`,
		profileID, cost, requestTime, errorMessage, statusCode, payload)

	if err != nil {
		logger.Error("Failed to log failed AAA request", "error", err)
		return err
	}

	logger.Info("Successfully logged failed AAA request")
	return nil
}

func (ps *profileSync) markResolvedAAARequests(ctx context.Context, profileID string) error {
	logger := ps.logger.With("user_id", profileID)
	logger.Info("Marking resolved AAA requests")

	now := time.Now().UTC()

	_, err := ps.pgPool.Exec(ctx, `
		UPDATE failed_aaa_requests 
		SET resolved = true, resolved_at = $2
		WHERE user_id = $1 AND resolved = false`,
		profileID, now)

	if err != nil {
		logger.Error("Failed to mark AAA requests as resolved", "error", err)
		return err
	}

	logger.Info("Successfully marked AAA requests as resolved")
	return nil
}
