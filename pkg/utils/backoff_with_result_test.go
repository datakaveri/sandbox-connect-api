package utils

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWithExponentialBackoffResult(t *testing.T) {
	config := BackoffConfig{
		InitialDelay: 1 * time.Millisecond,
		MaxDelay:     10 * time.Millisecond,
		MaxAttempts:  3,
		Factor:       2.0,
	}

	t.Run("Success on first attempt", func(t *testing.T) {
		attempts := 0
		expectedResult := "success"

		result, err := WithExponentialBackoffResult(context.Background(), config, func() (string, error) {
			attempts++
			return expectedResult, nil
		})

		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result != expectedResult {
			t.Errorf("Expected result %v, got %v", expectedResult, result)
		}
		if attempts != 1 {
			t.Errorf("Expected 1 attempt, got %d", attempts)
		}
	})

	t.Run("Success after retries", func(t *testing.T) {
		attempts := 0
		expectedResult := "success after retry"
		expectedError := errors.New("temporary error")

		result, err := WithExponentialBackoffResult(context.Background(), config, func() (string, error) {
			attempts++
			if attempts < 3 {
				return "", expectedError
			}
			return expectedResult, nil
		})

		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result != expectedResult {
			t.Errorf("Expected result %v, got %v", expectedResult, result)
		}
		if attempts != 3 {
			t.Errorf("Expected 3 attempts, got %d", attempts)
		}
	})

	t.Run("Failure after max attempts", func(t *testing.T) {
		attempts := 0
		expectedError := errors.New("persistent error")

		result, err := WithExponentialBackoffResult(context.Background(), config, func() (string, error) {
			attempts++
			return "failure", expectedError
		})

		if err != expectedError {
			t.Errorf("Expected error %v, got %v", expectedError, err)
		}
		if result != "failure" {
			t.Errorf("Expected result 'failure', got %v", result)
		}
		if attempts != config.MaxAttempts {
			t.Errorf("Expected %d attempts, got %d", config.MaxAttempts, attempts)
		}
	})

	t.Run("Context cancellation", func(t *testing.T) {
		attempts := 0
		ctx, cancel := context.WithCancel(context.Background())
		
		// Cancel after first attempt
		go func() {
			time.Sleep(2 * time.Millisecond)
			cancel()
		}()

		_, err := WithExponentialBackoffResult(ctx, config, func() (string, error) {
			attempts++
			return "", errors.New("temporary error")
		})

		if !errors.Is(err, context.Canceled) {
			t.Errorf("Expected context.Canceled error, got %v", err)
		}
	})
}

func TestWithLinearRetryResult(t *testing.T) {
	config := LinearRetryConfig{
		InitialDelay: 1 * time.Millisecond,
		MaxDelay:     10 * time.Millisecond,
		MaxAttempts:  3,
		Increment:    2 * time.Millisecond,
	}

	t.Run("Success on first attempt", func(t *testing.T) {
		attempts := 0
		expectedResult := 42

		result, err := WithLinearRetryResult(context.Background(), config, func() (int, error) {
			attempts++
			return expectedResult, nil
		})

		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result != expectedResult {
			t.Errorf("Expected result %v, got %v", expectedResult, result)
		}
		if attempts != 1 {
			t.Errorf("Expected 1 attempt, got %d", attempts)
		}
	})

	t.Run("Success after retries", func(t *testing.T) {
		attempts := 0
		expectedResult := 42
		expectedError := errors.New("temporary error")

		result, err := WithLinearRetryResult(context.Background(), config, func() (int, error) {
			attempts++
			if attempts < 3 {
				return 0, expectedError
			}
			return expectedResult, nil
		})

		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result != expectedResult {
			t.Errorf("Expected result %v, got %v", expectedResult, result)
		}
		if attempts != 3 {
			t.Errorf("Expected 3 attempts, got %d", attempts)
		}
	})
}

// Test with a custom struct type to verify generics work properly
type TestStruct struct {
	Name  string
	Value int
}

func TestWithExponentialBackoffResultCustomType(t *testing.T) {
	config := BackoffConfig{
		InitialDelay: 1 * time.Millisecond,
		MaxDelay:     10 * time.Millisecond,
		MaxAttempts:  3,
		Factor:       2.0,
	}

	t.Run("Custom struct type", func(t *testing.T) {
		expectedResult := TestStruct{Name: "test", Value: 123}

		result, err := WithExponentialBackoffResult(context.Background(), config, func() (TestStruct, error) {
			return expectedResult, nil
		})

		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result.Name != expectedResult.Name || result.Value != expectedResult.Value {
			t.Errorf("Expected result %+v, got %+v", expectedResult, result)
		}
	})
}
