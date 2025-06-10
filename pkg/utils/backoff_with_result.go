package utils

import (
	"context"
	"time"
)

func WithExponentialBackoffResult[T any](ctx context.Context, config BackoffConfig, operation func() (T, error)) (T, error) {
	var err error
	var result T
	currentDelay := config.InitialDelay

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		result, err = operation()
		if err == nil {
			return result, nil
		}

		if attempt == config.MaxAttempts {
			return result, err
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(currentDelay):
		}

		currentDelay = time.Duration(float64(currentDelay) * config.Factor)
		if currentDelay > config.MaxDelay {
			currentDelay = config.MaxDelay
		}
	}

	return result, err
}
func WithLinearRetryResult[T any](ctx context.Context, config LinearRetryConfig, operation func() (T, error)) (T, error) {
	var err error
	var result T
	currentDelay := config.InitialDelay

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		result, err = operation()
		if err == nil {
			return result, nil
		}

		if attempt == config.MaxAttempts {
			return result, err
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(currentDelay):
		}

		currentDelay += config.Increment
		if currentDelay > config.MaxDelay {
			currentDelay = config.MaxDelay
		}
	}

	return result, err
}
