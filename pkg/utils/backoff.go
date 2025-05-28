package utils

import (
	"context"
	"time"
)

type BackoffConfig struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	MaxAttempts  int
	Factor       float64
}

func DefaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     10 * time.Second,
		MaxAttempts:  5,
		Factor:       2.0,
	}
}

func WithRetry(ctx context.Context, config BackoffConfig, operation func() error) error {
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
