package utils

import (
	"context"
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

func WithExponentialBackoff(ctx context.Context, config BackoffConfig, operation func() error) error {
	var err error
	currentDelay := config.InitialDelay

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		if err = operation(); err == nil {
			return nil
		}

		if attempt == config.MaxAttempts {
			return err
		}

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

func WithLinearRetry(ctx context.Context, config LinearRetryConfig, operation func() error) error {
	var err error
	currentDelay := config.InitialDelay

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		if err = operation(); err == nil {
			return nil
		}

		if attempt == config.MaxAttempts {
			return err
		}

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
