package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryWithBackoff(t *testing.T) {
	tests := []struct {
		name           string
		config         *RetryConfig
		operation      string
		attempts       int
		finalError     error
		expectError    bool
		expectAttempts int
	}{
		{
			name: "successful on first attempt",
			config: &RetryConfig{
				MaxRetries:    3,
				InitialDelay:  10 * time.Millisecond,
				MaxDelay:      100 * time.Millisecond,
				BackoffFactor: 2.0,
			},
			operation:      "test_op",
			attempts:       1,
			finalError:     nil,
			expectError:    false,
			expectAttempts: 1,
		},
		{
			name: "successful after retry",
			config: &RetryConfig{
				MaxRetries:    3,
				InitialDelay:  10 * time.Millisecond,
				MaxDelay:      100 * time.Millisecond,
				BackoffFactor: 2.0,
			},
			operation:      "test_op",
			attempts:       2,
			finalError:     nil,
			expectError:    false,
			expectAttempts: 2,
		},
		{
			name: "max retries exceeded",
			config: &RetryConfig{
				MaxRetries:    2,
				InitialDelay:  10 * time.Millisecond,
				MaxDelay:      100 * time.Millisecond,
				BackoffFactor: 2.0,
			},
			operation:      "test_op",
			attempts:       10, // More than max retries
			finalError:     &RetryableError{Err: errors.New("test error"), Retryable: true},
			expectError:    true,
			expectAttempts: 3, // Initial attempt + 2 retries
		},
		{
			name: "non-retryable error",
			config: &RetryConfig{
				MaxRetries:    3,
				InitialDelay:  10 * time.Millisecond,
				MaxDelay:      100 * time.Millisecond,
				BackoffFactor: 2.0,
			},
			operation:      "test_op",
			attempts:       1,
			finalError:     &RetryableError{Err: errors.New("non-retryable"), Retryable: false},
			expectError:    true,
			expectAttempts: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			attemptCount := 0

			err := RetryWithBackoffCounter(ctx, tt.config, tt.operation, func(attempt int) error {
				attemptCount++
				if attemptCount < tt.attempts {
					// Return retryable error until we reach the desired attempt
					return &RetryableError{Err: errors.New("temp error"), Retryable: true}
				}
				return tt.finalError
			})

			if tt.expectError && err == nil {
				t.Errorf("expected error but got nil")
			} else if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if attemptCount != tt.expectAttempts {
				t.Errorf("expected %d attempts, got %d", tt.expectAttempts, attemptCount)
			}
		})
	}
}

func TestRetryWithContextCancellation(t *testing.T) {
	config := &RetryConfig{
		MaxRetries:    3,
		InitialDelay:  100 * time.Millisecond,
		MaxDelay:      1 * time.Second,
		BackoffFactor: 2.0,
	}

	ctx, cancel := context.WithCancel(context.Background())
	attemptCount := 0

	// Cancel context after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := RetryWithBackoff(ctx, config, "test_op", func() error {
		attemptCount++
		return &RetryableError{Err: errors.New("temp error"), Retryable: true}
	})

	if err == nil {
		t.Error("expected error due to context cancellation")
	}

	if !errors.Is(err, context.Canceled) && attemptCount > 1 {
		t.Errorf("expected context.Canceled error or single attempt, got: %v, attempts: %d", err, attemptCount)
	}
}

func TestClassifyExecutionError(t *testing.T) {
	tests := []struct {
		name      string
		exitCode  int
		expectErr bool
		retryable bool
	}{
		{
			name:      "success",
			exitCode:  0,
			expectErr: false,
			retryable: false,
		},
		{
			name:      "container runtime error",
			exitCode:  125,
			expectErr: true,
			retryable: true,
		},
		{
			name:      "permission denied",
			exitCode:  126,
			expectErr: true,
			retryable: false,
		},
		{
			name:      "command not found",
			exitCode:  127,
			expectErr: true,
			retryable: false,
		},
		{
			name:      "killed (OOM)",
			exitCode:  137,
			expectErr: true,
			retryable: true,
		},
		{
			name:      "terminated",
			exitCode:  143,
			expectErr: true,
			retryable: false,
		},
		{
			name:      "application error",
			exitCode:  1,
			expectErr: true,
			retryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ClassifyExecutionError(nil, tt.exitCode)

			if tt.expectErr && result == nil {
				t.Error("expected error but got nil")
			} else if !tt.expectErr && result != nil {
				t.Errorf("unexpected error: %v", result)
			}

			if result != nil && result.Retryable != tt.retryable {
				t.Errorf("expected retryable=%v, got %v", tt.retryable, result.Retryable)
			}
		})
	}
}

func TestBackoffDelay(t *testing.T) {
	config := &RetryConfig{
		MaxRetries:     3,
		InitialDelay:   100 * time.Millisecond,
		MaxDelay:       1 * time.Second,
		BackoffFactor:  2.0,
		JitterFraction: 0.0, // No jitter for predictable testing
	}

	tests := []struct {
		attempt     int
		expectedMin time.Duration
		expectedMax time.Duration
	}{
		{0, 100 * time.Millisecond, 100 * time.Millisecond},
		{1, 200 * time.Millisecond, 200 * time.Millisecond},
		{2, 400 * time.Millisecond, 400 * time.Millisecond},
		{3, 800 * time.Millisecond, 800 * time.Millisecond},
		{4, 1 * time.Second, 1 * time.Second}, // Should be capped at MaxDelay
		{5, 1 * time.Second, 1 * time.Second}, // Should still be capped
	}

	for _, tt := range tests {
		t.Run(t.Name(), func(t *testing.T) {
			delay := calculateBackoffDelay(tt.attempt, config)
			if delay < tt.expectedMin || delay > tt.expectedMax {
				t.Errorf("attempt %d: expected delay between %v and %v, got %v",
					tt.attempt, tt.expectedMin, tt.expectedMax, delay)
			}
		})
	}
}

func TestJitter(t *testing.T) {
	baseDuration := 100 * time.Millisecond
	fraction := 0.1

	// Run multiple times to test randomness
	for i := 0; i < 10; i++ {
		jittered := addJitter(baseDuration, fraction)

		minExpected := baseDuration
		maxExpected := baseDuration + time.Duration(float64(baseDuration)*fraction)

		if jittered < minExpected || jittered > maxExpected {
			t.Errorf("jittered duration %v outside expected range [%v, %v]",
				jittered, minExpected, maxExpected)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{
			name:      "nil error",
			err:       nil,
			retryable: false,
		},
		{
			name:      "retryable error",
			err:       &RetryableError{Err: errors.New("test"), Retryable: true},
			retryable: true,
		},
		{
			name:      "non-retryable error",
			err:       &RetryableError{Err: errors.New("test"), Retryable: false},
			retryable: false,
		},
		{
			name:      "context cancelled",
			err:       context.Canceled,
			retryable: false,
		},
		{
			name:      "context deadline exceeded",
			err:       context.DeadlineExceeded,
			retryable: false,
		},
		{
			name:      "regular error",
			err:       errors.New("regular error"),
			retryable: false, // Default behavior for unknown errors
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsRetryable(tt.err)
			if result != tt.retryable {
				t.Errorf("expected retryable=%v, got %v", tt.retryable, result)
			}
		})
	}
}
