package worker

import (
	"errors"
	"fmt"
	"testing"
)

func TestPodStartupError(t *testing.T) {
	tests := []struct {
		name           string
		reason         string
		message        string
		expectedString string
	}{
		{
			name:           "ImagePullBackOff with message",
			reason:         "ImagePullBackOff",
			message:        "Back-off pulling image \"invalid:image\"",
			expectedString: "pod failed to start: ImagePullBackOff - Back-off pulling image \"invalid:image\"",
		},
		{
			name:           "ErrImagePull without message",
			reason:         "ErrImagePull",
			message:        "",
			expectedString: "pod failed to start: ErrImagePull",
		},
		{
			name:           "CreateContainerConfigError with message",
			reason:         "CreateContainerConfigError",
			message:        "container config invalid",
			expectedString: "pod failed to start: CreateContainerConfigError - container config invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &PodStartupError{
				Reason:  tt.reason,
				Message: tt.message,
			}

			if err.Error() != tt.expectedString {
				t.Errorf("expected error string %q, got %q", tt.expectedString, err.Error())
			}
		})
	}
}

func TestIsPodStartupError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "PodStartupError returns true",
			err:      &PodStartupError{Reason: "ImagePullBackOff", Message: "test"},
			expected: true,
		},
		{
			name:     "wrapped PodStartupError returns true",
			err:      fmt.Errorf("failed to get pod for job: %w", &PodStartupError{Reason: "ErrImagePull", Message: "test"}),
			expected: true,
		},
		{
			name:     "double wrapped PodStartupError returns true",
			err:      fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &PodStartupError{Reason: "CrashLoopBackOff", Message: "test"})),
			expected: true,
		},
		{
			name:     "regular error returns false",
			err:      errors.New("some error"),
			expected: false,
		},
		{
			name:     "nil error returns false",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsPodStartupError(tt.err)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}
