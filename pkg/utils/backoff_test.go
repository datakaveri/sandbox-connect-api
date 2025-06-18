package utils

import (
	"context"
	"errors"
	"log/slog"
	"sandbox-backend-service/pkg/constants"
	"testing"
	"time"
)

func TestBackoffConfigs(t *testing.T) {
	t.Run("Exponential Backoff Config", func(t *testing.T) {
		config := BackoffConfig{
			InitialDelay: 100 * time.Millisecond,
			MaxDelay:     5 * time.Second,
			MaxAttempts:  5,
			Factor:       2.0,
		}

		if config.InitialDelay != 100*time.Millisecond {
			t.Errorf("expected InitialDelay to be 100ms, got %v", config.InitialDelay)
		}
		if config.MaxDelay != 5*time.Second {
			t.Errorf("expected MaxDelay to be 5s, got %v", config.MaxDelay)
		}
		if config.MaxAttempts != 5 {
			t.Errorf("expected MaxAttempts to be 5, got %d", config.MaxAttempts)
		}
		if config.Factor != 2.0 {
			t.Errorf("expected Factor to be 2.0, got %f", config.Factor)
		}
	})

	t.Run("Linear Backoff Config", func(t *testing.T) {
		config := LinearRetryConfig{
			InitialDelay: 50 * time.Millisecond,
			MaxDelay:     500 * time.Millisecond,
			MaxAttempts:  3,
			Increment:    100 * time.Millisecond,
		}

		if config.InitialDelay != 50*time.Millisecond {
			t.Errorf("expected InitialDelay to be 50ms, got %v", config.InitialDelay)
		}
		if config.MaxDelay != 500*time.Millisecond {
			t.Errorf("expected MaxDelay to be 500ms, got %v", config.MaxDelay)
		}
		if config.MaxAttempts != 3 {
			t.Errorf("expected MaxAttempts to be 3, got %d", config.MaxAttempts)
		}
		if config.Increment != 100*time.Millisecond {
			t.Errorf("expected Increment to be 100ms, got %v", config.Increment)
		}
	})
}

func TestWithExponentialBackoff_Success(t *testing.T) {
	attempts := 0
	logger := slog.Default()
	operation := func() (constants.ShouldContinue, error) {
		attempts++
		if attempts < 3 {
			return constants.RetryContinue, errors.New("temporary error")
		}
		return constants.RetryContinue, nil
	}

	config := BackoffConfig{
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		MaxAttempts:  5,
		Factor:       2.0,
	}

	ctx := context.Background()
	err := WithExponentialBackoff(ctx, config, logger, operation)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestWithExponentialBackoff_MaxAttemptsExceeded(t *testing.T) {
	attempts := 0
	logger := slog.Default()
	operation := func() (constants.ShouldContinue, error) {
		attempts++
		return constants.RetryContinue, errors.New("persistent error")
	}

	config := BackoffConfig{
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		MaxAttempts:  3,
		Factor:       2.0,
	}

	ctx := context.Background()
	err := WithExponentialBackoff(ctx, config, logger, operation)

	if err == nil {
		t.Error("expected error, got nil")
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestWithExponentialBackoff_ContextCancellation(t *testing.T) {
	attempts := 0
	logger := slog.Default()
	operation := func() (constants.ShouldContinue, error) {
		attempts++
		return constants.RetryContinue, errors.New("temporary error")
	}

	config := BackoffConfig{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     1 * time.Second,
		MaxAttempts:  5,
		Factor:       2.0,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := WithExponentialBackoff(ctx, config, logger, operation)

	if err == nil {
		t.Error("expected context cancellation error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected deadline exceeded error, got %v", err)
	}
}

// Linear Retry Tests
func TestWithLinearRetry_Success(t *testing.T) {
	attempts := 0
	logger := slog.Default()
	operation := func() (constants.ShouldContinue, error) {
		attempts++
		if attempts < 3 {
			return constants.RetryContinue, errors.New("temporary error")
		}
		return constants.RetryContinue, nil
	}

	config := LinearRetryConfig{
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		MaxAttempts:  5,
		Increment:    20 * time.Millisecond,
	}

	ctx := context.Background()
	err := WithLinearRetry(ctx, config, logger, operation)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestWithLinearRetry_MaxAttemptsExceeded(t *testing.T) {
	attempts := 0
	logger := slog.Default()
	operation := func() (constants.ShouldContinue, error) {
		attempts++
		return constants.RetryContinue, errors.New("persistent error")
	}

	config := LinearRetryConfig{
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		MaxAttempts:  3,
		Increment:    20 * time.Millisecond,
	}

	ctx := context.Background()
	err := WithLinearRetry(ctx, config, logger, operation)

	if err == nil {
		t.Error("expected error, got nil")
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestWithLinearRetry_ContextCancellation(t *testing.T) {
	attempts := 0
	logger := slog.Default()
	operation := func() (constants.ShouldContinue, error) {
		attempts++
		return constants.RetryContinue, errors.New("temporary error")
	}

	config := LinearRetryConfig{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     1 * time.Second,
		MaxAttempts:  5,
		Increment:    200 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := WithLinearRetry(ctx, config, logger, operation)

	if err == nil {
		t.Error("expected context cancellation error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected deadline exceeded error, got %v", err)
	}
}

func TestWithExponentialBackoff_EarlyReturn(t *testing.T) {
	attempts := 0
	logger := slog.Default()
	operation := func() (constants.ShouldContinue, error) {
		attempts++
		if attempts == 2 {
			return constants.RetryStop, errors.New("critical error, stop retrying")
		}
		return constants.RetryContinue, errors.New("temporary error")
	}

	config := BackoffConfig{
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		MaxAttempts:  5,
		Factor:       2.0,
	}

	ctx := context.Background()
	err := WithExponentialBackoff(ctx, config, logger, operation)

	if err == nil {
		t.Error("expected error, got nil")
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestWithLinearRetry_EarlyReturn(t *testing.T) {
	attempts := 0
	logger := slog.Default()
	operation := func() (constants.ShouldContinue, error) {
		attempts++
		if attempts == 2 {
			// On second attempt, signal to stop retrying
			return constants.RetryStop, errors.New("critical error, stop retrying")
		}
		return constants.RetryContinue, errors.New("temporary error")
	}

	config := LinearRetryConfig{
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		MaxAttempts:  5,
		Increment:    20 * time.Millisecond,
	}

	ctx := context.Background()
	err := WithLinearRetry(ctx, config, logger, operation)

	if err == nil {
		t.Error("expected error, got nil")
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}
