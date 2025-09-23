package worker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
)

// RetryConfig holds configuration for retry logic
type RetryConfig struct {
	MaxRetries     int           // Maximum number of retry attempts
	InitialDelay   time.Duration // Initial delay between retries
	MaxDelay       time.Duration // Maximum delay between retries
	BackoffFactor  float64       // Exponential backoff factor (e.g., 2.0)
	JitterFraction float64       // Fraction of delay to add as random jitter (0.0-1.0)
}

// DefaultRetryConfig returns the default retry configuration
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:     3,
		InitialDelay:   1 * time.Second,
		MaxDelay:       30 * time.Second,
		BackoffFactor:  2.0,
		JitterFraction: 0.1,
	}
}

// RetryableError represents an error that can be retried
type RetryableError struct {
	Err       error
	Retryable bool
	Reason    string
}

func (e *RetryableError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%v (reason: %s, retryable: %v)", e.Err, e.Reason, e.Retryable)
	}
	return fmt.Sprintf("%v (retryable: %v)", e.Err, e.Retryable)
}

func (e *RetryableError) Unwrap() error {
	return e.Err
}

// IsRetryable checks if an error is retryable
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	var retryableErr *RetryableError
	if errors.As(err, &retryableErr) {
		return retryableErr.Retryable
	}

	// Check for transient errors that should be retried
	// This could be expanded to check for specific error types or messages
	return isTransientError(err)
}

// isTransientError checks if an error is likely transient
func isTransientError(err error) bool {
	if err == nil {
		return false
	}

	// Check for context errors (not retryable)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// TODO: Add checks for specific transient errors:
	// - Network timeouts
	// - Temporary database connection errors
	// - Container pull errors
	// - Resource temporarily unavailable

	return false
}

// RetryWithBackoffCounter executes a function with exponential backoff retry logic and provides attempt counter
func RetryWithBackoffCounter(ctx context.Context, config *RetryConfig, operation string, fn func(attempt int) error) error {
	if config == nil {
		config = DefaultRetryConfig()
	}

	var lastErr error
	delay := config.InitialDelay

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		// Check context before attempting
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context cancelled before attempt %d: %w", attempt+1, err)
		}

		// Execute the function with attempt counter
		if err := fn(attempt); err != nil {
			lastErr = err

			// Check if the error is retryable
			if !IsRetryable(err) {
				logging.Log.WithField("operation", operation).
					WithField("attempt", attempt+1).
					WithError(err).
					Warn("Non-retryable error encountered")
				return err
			}

			// Check if we've exhausted retries
			if attempt >= config.MaxRetries {
				logging.Log.WithField("operation", operation).
					WithField("attempts", attempt+1).
					WithError(err).
					Error("Max retries exceeded")
				return fmt.Errorf("operation %s failed after %d attempts: %w", operation, attempt+1, err)
			}

			// Calculate next delay with exponential backoff
			if attempt > 0 {
				delay = time.Duration(float64(delay) * config.BackoffFactor)
				if delay > config.MaxDelay {
					delay = config.MaxDelay
				}
			}

			// Add jitter to prevent thundering herd
			jitteredDelay := addJitter(delay, config.JitterFraction)

			logging.Log.WithField("operation", operation).
				WithField("attempt", attempt+1).
				WithField("delay", jitteredDelay).
				WithError(err).
				Info("Retrying operation after delay")

			// Wait before retrying
			select {
			case <-time.After(jitteredDelay):
				// Continue to next attempt
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during retry delay: %w", ctx.Err())
			}
		} else {
			// Success
			if attempt > 0 {
				logging.Log.WithField("operation", operation).
					WithField("attempt", attempt+1).
					Info("Operation succeeded after retry")
			}
			return nil
		}
	}

	return lastErr
}

// RetryWithBackoff executes a function with exponential backoff retry logic
func RetryWithBackoff(ctx context.Context, config *RetryConfig, operation string, fn func() error) error {
	return RetryWithBackoffCounter(ctx, config, operation, func(_ int) error {
		return fn()
	})
}

// addJitter adds random jitter to a duration
func addJitter(d time.Duration, fraction float64) time.Duration {
	if fraction <= 0 {
		return d
	}
	if fraction > 1 {
		fraction = 1
	}

	jitter := time.Duration(rand.Float64() * float64(d) * fraction)
	return d + jitter
}

// calculateBackoffDelay calculates the delay for a given retry attempt
func calculateBackoffDelay(attempt int, config *RetryConfig) time.Duration {
	if attempt <= 0 {
		return config.InitialDelay
	}

	delay := time.Duration(float64(config.InitialDelay) * math.Pow(config.BackoffFactor, float64(attempt)))
	if delay > config.MaxDelay {
		delay = config.MaxDelay
	}

	return addJitter(delay, config.JitterFraction)
}

// ClassifyExecutionError classifies an execution error as retryable or not
func ClassifyExecutionError(err error, exitCode int) *RetryableError {
	if err == nil && exitCode == 0 {
		return nil
	}

	// Non-zero exit codes are generally not retryable unless they indicate
	// a transient issue
	if exitCode != 0 {
		// Check for specific exit codes that might be retryable
		switch exitCode {
		case 125: // Docker run error (e.g., cannot find container)
			return &RetryableError{
				Err:       fmt.Errorf("container execution error (exit code %d)", exitCode),
				Retryable: true,
				Reason:    "Container runtime error",
			}
		case 126: // Permission denied or cannot execute
			return &RetryableError{
				Err:       fmt.Errorf("execution permission error (exit code %d)", exitCode),
				Retryable: false,
				Reason:    "Permission denied",
			}
		case 127: // Command not found
			return &RetryableError{
				Err:       fmt.Errorf("command not found (exit code %d)", exitCode),
				Retryable: false,
				Reason:    "Command not found",
			}
		case 137: // Killed (often due to OOM)
			return &RetryableError{
				Err:       fmt.Errorf("process killed (exit code %d)", exitCode),
				Retryable: true,
				Reason:    "Process killed (possibly OOM)",
			}
		case 143: // Terminated by SIGTERM
			return &RetryableError{
				Err:       fmt.Errorf("process terminated (exit code %d)", exitCode),
				Retryable: false,
				Reason:    "Process terminated",
			}
		default:
			// Most application-level errors are not retryable
			return &RetryableError{
				Err:       fmt.Errorf("job failed with exit code %d", exitCode),
				Retryable: false,
				Reason:    "Application error",
			}
		}
	}

	// If there's an error but no exit code, check if it's transient
	if err != nil {
		if isTransientError(err) {
			return &RetryableError{
				Err:       err,
				Retryable: true,
				Reason:    "Transient error",
			}
		}
		return &RetryableError{
			Err:       err,
			Retryable: false,
		}
	}

	return nil
}
