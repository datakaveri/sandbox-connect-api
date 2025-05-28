package utils

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDefaultBackoffConfig(t *testing.T) {
	config := DefaultBackoffConfig()
	
	if config.InitialDelay != 100*time.Millisecond {
		t.Errorf("expected InitialDelay to be 100ms, got %v", config.InitialDelay)
	}
	if config.MaxDelay != 10*time.Second {
		t.Errorf("expected MaxDelay to be 10s, got %v", config.MaxDelay)
	}
	if config.MaxAttempts != 5 {
		t.Errorf("expected MaxAttempts to be 5, got %d", config.MaxAttempts)
	}
	if config.Factor != 2.0 {
		t.Errorf("expected Factor to be 2.0, got %f", config.Factor)
	}
}

func TestWithRetry_Success(t *testing.T) {
	attempts := 0
	operation := func() error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary error")
		}
		return nil
	}

	config := BackoffConfig{
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		MaxAttempts:  5,
		Factor:       2.0,
	}

	ctx := context.Background()
	err := WithRetry(ctx, config, operation)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestWithRetry_MaxAttemptsExceeded(t *testing.T) {
	attempts := 0
	operation := func() error {
		attempts++
		return errors.New("persistent error")
	}

	config := BackoffConfig{
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		MaxAttempts:  3,
		Factor:       2.0,
	}

	ctx := context.Background()
	err := WithRetry(ctx, config, operation)

	if err == nil {
		t.Error("expected error, got nil")
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestWithRetry_ContextCancellation(t *testing.T) {
	attempts := 0
	operation := func() error {
		attempts++
		return errors.New("temporary error")
	}

	config := BackoffConfig{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     1 * time.Second,
		MaxAttempts:  5,
		Factor:       2.0,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := WithRetry(ctx, config, operation)

	if err == nil {
		t.Error("expected context cancellation error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected deadline exceeded error, got %v", err)
	}
}
