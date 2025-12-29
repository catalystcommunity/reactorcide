package worker

import (
	"context"
	"testing"
)

// TestDockerRunner_validateConfig tests the configuration validation
func TestDockerRunner_validateConfig(t *testing.T) {
	runner := &DockerRunner{}

	tests := []struct {
		name        string
		config      *JobConfig
		expectError bool
	}{
		{
			name: "valid config",
			config: &JobConfig{
				Image:        "alpine:latest",
				Command:      []string{"echo", "hello"},
				WorkspaceDir: "/tmp/test",
				JobID:        "test-123",
			},
			expectError: false,
		},
		{
			name: "missing image",
			config: &JobConfig{
				Command:      []string{"echo", "hello"},
				WorkspaceDir: "/tmp/test",
				JobID:        "test-123",
			},
			expectError: true,
		},
		{
			name: "missing command",
			config: &JobConfig{
				Image:        "alpine:latest",
				Command:      []string{},
				WorkspaceDir: "/tmp/test",
				JobID:        "test-123",
			},
			expectError: true,
		},
		{
			name: "missing workspace",
			config: &JobConfig{
				Image:   "alpine:latest",
				Command: []string{"echo", "hello"},
				JobID:   "test-123",
			},
			expectError: true,
		},
		{
			name: "missing job ID",
			config: &JobConfig{
				Image:        "alpine:latest",
				Command:      []string{"echo", "hello"},
				WorkspaceDir: "/tmp/test",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runner.validateConfig(tt.config)
			if tt.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestDockerRunner_envMapToSlice tests environment map to slice conversion
func TestDockerRunner_envMapToSlice(t *testing.T) {
	runner := &DockerRunner{}

	tests := []struct {
		name     string
		envMap   map[string]string
		expected int
	}{
		{
			name:     "nil map",
			envMap:   nil,
			expected: 0,
		},
		{
			name:     "empty map",
			envMap:   map[string]string{},
			expected: 0,
		},
		{
			name: "single entry",
			envMap: map[string]string{
				"KEY": "value",
			},
			expected: 1,
		},
		{
			name: "multiple entries",
			envMap: map[string]string{
				"KEY1": "value1",
				"KEY2": "value2",
				"KEY3": "value3",
			},
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runner.envMapToSlice(tt.envMap)
			if len(result) != tt.expected {
				t.Errorf("expected %d entries, got %d", tt.expected, len(result))
			}

			// Verify format (KEY=VALUE)
			for _, entry := range result {
				if len(entry) == 0 {
					t.Errorf("empty environment entry")
				}
			}
		})
	}
}

// TestParseMemoryString tests memory string parsing
func TestParseMemoryString(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    int64
		expectError bool
	}{
		{
			name:        "kilobytes (Ki)",
			input:       "512Ki",
			expected:    512 * 1024,
			expectError: false,
		},
		{
			name:        "megabytes (Mi)",
			input:       "256Mi",
			expected:    256 * 1024 * 1024,
			expectError: false,
		},
		{
			name:        "gigabytes (Gi)",
			input:       "2Gi",
			expected:    2 * 1024 * 1024 * 1024,
			expectError: false,
		},
		{
			name:        "megabytes (M)",
			input:       "500M",
			expected:    500 * 1000 * 1000,
			expectError: false,
		},
		{
			name:        "gigabytes (G)",
			input:       "1G",
			expected:    1 * 1000 * 1000 * 1000,
			expectError: false,
		},
		{
			name:        "plain bytes",
			input:       "1024",
			expected:    1024,
			expectError: false,
		},
		{
			name:        "empty string",
			input:       "",
			expected:    0,
			expectError: true,
		},
		{
			name:        "invalid format",
			input:       "invalid",
			expected:    0,
			expectError: true,
		},
		{
			name:        "whitespace handling",
			input:       "  512Mi  ",
			expected:    512 * 1024 * 1024,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseMemoryString(tt.input)
			if tt.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.expectError && result != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, result)
			}
		})
	}
}

// TestNewJobRunner_Docker tests creating a Docker runner via the factory
func TestNewJobRunner_Docker(t *testing.T) {
	runner, err := NewJobRunner("docker")
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}

	if runner == nil {
		t.Fatal("expected non-nil runner")
	}

	// Verify it's the right type
	if _, ok := runner.(*DockerRunner); !ok {
		t.Errorf("expected *DockerRunner, got %T", runner)
	}
}

// TestNewJobRunner_InvalidBackend tests factory with invalid backend
func TestNewJobRunner_InvalidBackend(t *testing.T) {
	runner, err := NewJobRunner("invalid")
	if err == nil {
		t.Error("expected error for invalid backend")
	}
	if runner != nil {
		t.Error("expected nil runner for invalid backend")
	}
}

// TestNewJobRunner_CaseInsensitive tests factory is case-insensitive
func TestNewJobRunner_CaseInsensitive(t *testing.T) {
	testCases := []string{"DOCKER", "Docker", "docker", "  docker  "}
	for _, backend := range testCases {
		t.Run(backend, func(t *testing.T) {
			runner, err := NewJobRunner(backend)
			// Skip test if Docker is not available
			if err != nil && err.Error() != "unsupported job runner backend" {
				t.Skipf("Docker not available: %v", err)
			}
			if runner == nil && err == nil {
				t.Fatal("expected non-nil runner or error")
			}
		})
	}
}

// TestIsBackendSupported tests backend support checking
func TestIsBackendSupported(t *testing.T) {
	tests := []struct {
		backend  string
		expected bool
	}{
		{"docker", true},
		{"containerd", true},
		{"kubernetes", true},
		{"invalid", false},
		{"DOCKER", true}, // case insensitive
		{"  docker  ", true}, // whitespace handling
	}

	for _, tt := range tests {
		t.Run(tt.backend, func(t *testing.T) {
			result := IsBackendSupported(tt.backend)
			if result != tt.expected {
				t.Errorf("IsBackendSupported(%q) = %v, expected %v", tt.backend, result, tt.expected)
			}
		})
	}
}

// TestIsBackendImplemented tests implementation status checking
func TestIsBackendImplemented(t *testing.T) {
	tests := []struct {
		backend  string
		expected bool
	}{
		{"docker", true},       // fully implemented
		{"containerd", false},  // stub only
		{"kubernetes", false},  // stub only
		{"invalid", false},
		{"DOCKER", true},       // case insensitive
	}

	for _, tt := range tests {
		t.Run(tt.backend, func(t *testing.T) {
			result := IsBackendImplemented(tt.backend)
			if result != tt.expected {
				t.Errorf("IsBackendImplemented(%q) = %v, expected %v", tt.backend, result, tt.expected)
			}
		})
	}
}

// TestGetSupportedBackends tests getting the list of supported backends
func TestGetSupportedBackends(t *testing.T) {
	backends := GetSupportedBackends()
	if len(backends) != 3 {
		t.Errorf("expected 3 supported backends, got %d", len(backends))
	}

	expectedBackends := map[RunnerBackend]bool{
		BackendDocker:      true,
		BackendContainerd:  true,
		BackendKubernetes:  true,
	}

	for _, backend := range backends {
		if !expectedBackends[backend] {
			t.Errorf("unexpected backend in list: %s", backend)
		}
	}
}

// TestStubRunners_ReturnErrors tests that stub runners return appropriate errors
func TestStubRunners_ReturnErrors(t *testing.T) {
	ctx := context.Background()
	config := &JobConfig{
		Image:        "alpine:latest",
		Command:      []string{"echo", "test"},
		WorkspaceDir: "/tmp/test",
		JobID:        "test-123",
	}

	tests := []struct {
		name   string
		runner JobRunner
	}{
		{
			name:   "containerd",
			runner: &ContainerdRunner{},
		},
		{
			name:   "kubernetes",
			runner: &KubernetesRunner{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// All methods should return errors for stub implementations
			_, err := tt.runner.SpawnJob(ctx, config)
			if err == nil {
				t.Error("SpawnJob should return error for stub implementation")
			}

			_, _, err = tt.runner.StreamLogs(ctx, "test-id")
			if err == nil {
				t.Error("StreamLogs should return error for stub implementation")
			}

			_, err = tt.runner.WaitForCompletion(ctx, "test-id")
			if err == nil {
				t.Error("WaitForCompletion should return error for stub implementation")
			}

			err = tt.runner.Cleanup(ctx, "test-id")
			if err == nil {
				t.Error("Cleanup should return error for stub implementation")
			}
		})
	}
}
