package main

import (
	"context"
	"sandbox-backend-service/pkg/utils"
	"time"
)

func WithK8sRetry(ctx context.Context, operation func() error) error {
	return utils.WithExponentialBackoff(ctx, k8sRetryConfig, operation)
}

func WithDBRetry(ctx context.Context, operation func() error) error {
	return utils.WithExponentialBackoff(ctx, dbRetryConfig, operation)
}
func WithExternalApiRetry(ctx context.Context, operation func() error) error {
	return utils.WithExponentialBackoff(ctx, externalApiRetryConfig, operation)
}
func WithTimeoutContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}
