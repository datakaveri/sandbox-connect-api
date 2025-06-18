package main

import (
	"context"
	"log/slog"
	"sandbox-backend-service/pkg/constants"
	"sandbox-backend-service/pkg/utils"
	"time"
)

func WithK8sRetry(ctx context.Context, logger *slog.Logger, operation func() (constants.ShouldContinue, error)) error {
	return utils.WithExponentialBackoff(ctx, k8sRetryConfig, logger, operation)
}

func WithDBRetry(ctx context.Context, logger *slog.Logger, operation func() (constants.ShouldContinue, error)) error {
	return utils.WithExponentialBackoff(ctx, dbRetryConfig, logger, operation)
}
func WithExternalApiRetry(ctx context.Context, logger *slog.Logger, operation func() (constants.ShouldContinue, error)) error {
	return utils.WithExponentialBackoff(ctx, externalApiRetryConfig, logger, operation)
}
func WithTimeoutContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}
