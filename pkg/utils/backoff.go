package utils

import (
	"context"
	"log/slog"
	"sandbox-backend-service/pkg/constants"
	"time"
)

type LinearRetryConfig struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	MaxAttempts  int
	Increment    time.Duration
}
type BackoffConfig struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	MaxAttempts  int
	Factor       float64
}

func WithExponentialBackoff(ctx context.Context, config BackoffConfig, logger *slog.Logger,
	operation func() (constants.ShouldContinue, error)) error {
	var err error
	currentDelay := config.InitialDelay

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		shouldContinue, operationErr := operation()
		err = operationErr
		if shouldContinue == constants.RetryStop || attempt == config.MaxAttempts || err == nil {
			return err
		}
		logger.Warn("Operation failed, will retry", "attempt", attempt, "maxAttempts", config.MaxAttempts, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(currentDelay):
		}
		currentDelay = time.Duration(float64(currentDelay) * config.Factor)
		if currentDelay > config.MaxDelay {
			currentDelay = config.MaxDelay
		}
	}

	return err
}
func WithLinearRetry(ctx context.Context, config LinearRetryConfig, logger *slog.Logger,
	operation func() (constants.ShouldContinue, error)) error {
	var err error
	currentDelay := config.InitialDelay

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		shouldContinue, operationErr := operation()
		err = operationErr
		if shouldContinue == constants.RetryStop || attempt == config.MaxAttempts || err == nil {
			return err
		}
		logger.Warn("Operation failed, will retry", "attempt", attempt, "maxAttempts", config.MaxAttempts, "error", err)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(currentDelay):
		}

		currentDelay = currentDelay + config.Increment
		if currentDelay > config.MaxDelay {
			currentDelay = config.MaxDelay
		}
	}
	return err
}
