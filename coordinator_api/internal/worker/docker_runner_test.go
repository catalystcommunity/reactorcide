package worker

import (
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
		{"docker", true},      // fully implemented
		{"containerd", true},  // fully implemented
		{"kubernetes", true},  // fully implemented
		{"auto", true},        // fully implemented (auto-detection)
		{"invalid", false},
		{"DOCKER", true},       // case insensitive
		{"KUBERNETES", true},   // case insensitive
		{"CONTAINERD", true},   // case insensitive
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
	if len(backends) != 4 {
		t.Errorf("expected 4 supported backends, got %d", len(backends))
	}

	expectedBackends := map[RunnerBackend]bool{
		BackendAuto:       true,
		BackendDocker:     true,
		BackendContainerd: true,
		BackendKubernetes: true,
	}

	for _, backend := range backends {
		if !expectedBackends[backend] {
			t.Errorf("unexpected backend in list: %s", backend)
		}
	}
}

// TestJobConfig_SourceDir tests that SourceDir field is properly set on JobConfig
func TestJobConfig_SourceDir(t *testing.T) {
	config := &JobConfig{
		Image:        "alpine:latest",
		Command:      []string{"echo", "hello"},
		WorkspaceDir: "/tmp/workspace",
		SourceDir:    "/home/user/project",
		JobID:        "test-123",
	}

	if config.SourceDir != "/home/user/project" {
		t.Errorf("SourceDir = %q, want /home/user/project", config.SourceDir)
	}

	// Validate that SourceDir doesn't affect normal validation
	runner := &DockerRunner{}
	if err := runner.validateConfig(config); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}

	// SourceDir is optional - empty should also pass validation
	config.SourceDir = ""
	if err := runner.validateConfig(config); err != nil {
		t.Errorf("unexpected validation error with empty SourceDir: %v", err)
	}
}

// TestContainerdRunner_validateConfig tests the configuration validation for containerd
func TestContainerdRunner_validateConfig(t *testing.T) {
	runner := &ContainerdRunner{
		containers: make(map[string]*containerProcess),
	}

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

// TestNewJobRunner_Containerd tests creating a Containerd runner via the factory
func TestNewJobRunner_Containerd(t *testing.T) {
	runner, err := NewJobRunner("containerd")
	if err != nil {
		t.Skipf("Containerd not available: %v", err)
	}

	if runner == nil {
		t.Fatal("expected non-nil runner")
	}

	// Verify it's the right type
	if _, ok := runner.(*ContainerdRunner); !ok {
		t.Errorf("expected *ContainerdRunner, got %T", runner)
	}
}
