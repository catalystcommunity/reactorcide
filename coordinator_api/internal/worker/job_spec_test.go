package worker

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadJobSpec tests loading job specifications from files
func TestLoadJobSpec(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "jobspec-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name        string
		content     string
		filename    string
		expectError bool
		checkName   string
		checkImage  string
	}{
		{
			name: "valid yaml job",
			content: `name: test-job
command: echo hello
image: alpine:latest
environment:
  FOO: bar
`,
			filename:    "test.yaml",
			expectError: false,
			checkName:   "test-job",
			checkImage:  "alpine:latest",
		},
		{
			name: "valid json job",
			content: `{
  "name": "json-job",
  "command": "echo hello",
  "image": "ubuntu:22.04"
}`,
			filename:    "test.json",
			expectError: false,
			checkName:   "json-job",
			checkImage:  "ubuntu:22.04",
		},
		{
			name: "missing command",
			content: `name: no-command
image: alpine:latest
`,
			filename:    "nocommand.yaml",
			expectError: true,
		},
		{
			name: "default image",
			content: `name: default-image
command: echo hello
`,
			filename:    "defaultimage.yaml",
			expectError: false,
			checkImage:  DefaultRunnerImage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(tmpDir, tt.filename)
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatalf("failed to write test file: %v", err)
			}

			spec, err := LoadJobSpec(path)
			if tt.expectError {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.checkName != "" && spec.Name != tt.checkName {
				t.Errorf("name = %q, want %q", spec.Name, tt.checkName)
			}
			if tt.checkImage != "" && spec.Image != tt.checkImage {
				t.Errorf("image = %q, want %q", spec.Image, tt.checkImage)
			}
		})
	}
}

// TestParseCommand tests command parsing with shell quoting
func TestParseCommand(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "simple command",
			input:    "echo hello",
			expected: []string{"echo", "hello"},
		},
		{
			name:     "single quotes",
			input:    "sh -c 'echo hello world'",
			expected: []string{"sh", "-c", "echo hello world"},
		},
		{
			name:     "double quotes",
			input:    `echo "hello world"`,
			expected: []string{"echo", "hello world"},
		},
		{
			name:     "mixed quotes",
			input:    `sh -c "echo 'hello'"`,
			expected: []string{"sh", "-c", "echo 'hello'"},
		},
		{
			name:     "escaped characters",
			input:    `echo hello\ world`,
			expected: []string{"echo", "hello world"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseCommand(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("got %d args, want %d: %v vs %v", len(result), len(tt.expected), result, tt.expected)
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("arg[%d] = %q, want %q", i, v, tt.expected[i])
				}
			}
		})
	}
}

// TestHasSecretRefs tests secret reference detection
func TestHasSecretRefs(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"${secret:path:key}", true},
		{"prefix${secret:path:key}suffix", true},
		{"no secret here", false},
		{"${env:VAR}", false},
		{"${secret:a/b/c:mykey}", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := HasSecretRefs(tt.input)
			if result != tt.expected {
				t.Errorf("HasSecretRefs(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// TestHasEnvRefs tests environment variable reference detection
func TestHasEnvRefs(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"${env:HOME}", true},
		{"prefix${env:PATH}suffix", true},
		{"no env here", false},
		{"${secret:path:key}", false},
		{"${env:MY_VAR}", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := HasEnvRefs(tt.input)
			if result != tt.expected {
				t.Errorf("HasEnvRefs(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// TestResolveEnvRefs tests environment variable resolution
func TestResolveEnvRefs(t *testing.T) {
	// Set up test environment
	os.Setenv("TEST_VAR", "test-value")
	os.Setenv("ANOTHER_VAR", "another-value")
	defer os.Unsetenv("TEST_VAR")
	defer os.Unsetenv("ANOTHER_VAR")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single env ref",
			input:    "${env:TEST_VAR}",
			expected: "test-value",
		},
		{
			name:     "env ref with prefix",
			input:    "prefix-${env:TEST_VAR}",
			expected: "prefix-test-value",
		},
		{
			name:     "multiple env refs",
			input:    "${env:TEST_VAR}-${env:ANOTHER_VAR}",
			expected: "test-value-another-value",
		},
		{
			name:     "unset env var",
			input:    "${env:UNSET_VAR}",
			expected: "",
		},
		{
			name:     "no env refs",
			input:    "plain text",
			expected: "plain text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResolveEnvRefs(tt.input)
			if result != tt.expected {
				t.Errorf("ResolveEnvRefs(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestResolveEnvInMap tests map-wide environment resolution
func TestResolveEnvInMap(t *testing.T) {
	os.Setenv("TEST_USER", "testuser")
	defer os.Unsetenv("TEST_USER")

	input := map[string]string{
		"USER":     "${env:TEST_USER}",
		"STATIC":   "static-value",
		"COMBINED": "user-${env:TEST_USER}-end",
	}

	result := ResolveEnvInMap(input)

	expected := map[string]string{
		"USER":     "testuser",
		"STATIC":   "static-value",
		"COMBINED": "user-testuser-end",
	}

	for k, v := range expected {
		if result[k] != v {
			t.Errorf("result[%q] = %q, want %q", k, result[k], v)
		}
	}
}

// TestMergeJobSpecs tests overlay merging functionality
func TestMergeJobSpecs(t *testing.T) {
	base := &JobSpec{
		Name:    "base-job",
		Command: "echo base",
		Image:   "base-image:latest",
		Environment: map[string]string{
			"BASE_VAR":   "base-value",
			"SHARED_VAR": "base-shared",
		},
		Capabilities: []string{"docker"},
	}

	overlay := &JobSpec{
		Name: "overlay-job",
		Environment: map[string]string{
			"OVERLAY_VAR": "overlay-value",
			"SHARED_VAR":  "overlay-shared",
		},
	}

	result, overrides := MergeJobSpecs(base, []*JobSpec{overlay}, []string{"overlay.yaml"})

	// Check merged values
	if result.Name != "overlay-job" {
		t.Errorf("name = %q, want %q", result.Name, "overlay-job")
	}
	if result.Command != "echo base" {
		t.Errorf("command should be from base: got %q", result.Command)
	}
	if result.Image != "base-image:latest" {
		t.Errorf("image should be from base: got %q", result.Image)
	}

	// Check merged environment
	if result.Environment["BASE_VAR"] != "base-value" {
		t.Errorf("BASE_VAR should be preserved")
	}
	if result.Environment["OVERLAY_VAR"] != "overlay-value" {
		t.Errorf("OVERLAY_VAR should be added")
	}
	if result.Environment["SHARED_VAR"] != "overlay-shared" {
		t.Errorf("SHARED_VAR should be overridden by overlay")
	}

	// Check capabilities preserved
	if len(result.Capabilities) != 1 || result.Capabilities[0] != "docker" {
		t.Errorf("capabilities should be preserved from base")
	}

	// No secret overrides in this case
	if len(overrides) != 0 {
		t.Errorf("expected no secret overrides, got %d", len(overrides))
	}
}

// TestMergeJobSpecs_SecretOverride tests detection of secret overrides
func TestMergeJobSpecs_SecretOverride(t *testing.T) {
	base := &JobSpec{
		Name:    "base-job",
		Command: "echo test",
		Environment: map[string]string{
			"PASSWORD": "${secret:vault/path:password}",
		},
	}

	overlay := &JobSpec{
		Environment: map[string]string{
			"PASSWORD": "plaintext-password",
		},
	}

	_, overrides := MergeJobSpecs(base, []*JobSpec{overlay}, []string{"overlay.yaml"})

	if len(overrides) != 1 {
		t.Fatalf("expected 1 secret override, got %d", len(overrides))
	}

	override := overrides[0]
	if override.Key != "PASSWORD" {
		t.Errorf("override key = %q, want PASSWORD", override.Key)
	}
	if override.OldValue != "${secret:vault/path:password}" {
		t.Errorf("override old value incorrect")
	}
	if override.NewValue != "plaintext-password" {
		t.Errorf("override new value incorrect")
	}
	if override.OverlayFile != "overlay.yaml" {
		t.Errorf("override file = %q, want overlay.yaml", override.OverlayFile)
	}
}

// TestMergeJobSpecs_MultipleOverlays tests layering multiple overlays
func TestMergeJobSpecs_MultipleOverlays(t *testing.T) {
	base := &JobSpec{
		Name:    "base",
		Command: "echo base",
		Environment: map[string]string{
			"VAR": "base",
		},
	}

	overlay1 := &JobSpec{
		Environment: map[string]string{
			"VAR": "overlay1",
		},
	}

	overlay2 := &JobSpec{
		Environment: map[string]string{
			"VAR": "overlay2",
		},
	}

	// overlay2 is applied last, so it should win
	result, _ := MergeJobSpecs(base, []*JobSpec{overlay1, overlay2}, []string{"o1.yaml", "o2.yaml"})

	if result.Environment["VAR"] != "overlay2" {
		t.Errorf("VAR = %q, want overlay2 (last overlay wins)", result.Environment["VAR"])
	}
}

// TestLoadJobSpecWithOverlays tests the full overlay loading flow
func TestLoadJobSpecWithOverlays(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "overlay-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create base job file
	baseContent := `name: base-job
command: echo hello
image: alpine:latest
environment:
  REGISTRY_USER: "${env:REGISTRY_USER}"
  REGISTRY_PASSWORD: "${secret:vault:password}"
`
	basePath := filepath.Join(tmpDir, "job.yaml")
	if err := os.WriteFile(basePath, []byte(baseContent), 0644); err != nil {
		t.Fatalf("failed to write base file: %v", err)
	}

	// Create overlay file
	overlayContent := `environment:
  REGISTRY_USER: override-user
`
	overlayPath := filepath.Join(tmpDir, "overlay.yaml")
	if err := os.WriteFile(overlayPath, []byte(overlayContent), 0644); err != nil {
		t.Fatalf("failed to write overlay file: %v", err)
	}

	spec, overrides, err := LoadJobSpecWithOverlays(basePath, []string{overlayPath})
	if err != nil {
		t.Fatalf("LoadJobSpecWithOverlays failed: %v", err)
	}

	if spec.Name != "base-job" {
		t.Errorf("name = %q, want base-job", spec.Name)
	}
	if spec.Environment["REGISTRY_USER"] != "override-user" {
		t.Errorf("REGISTRY_USER should be overridden")
	}
	if spec.Environment["REGISTRY_PASSWORD"] != "${secret:vault:password}" {
		t.Errorf("REGISTRY_PASSWORD should be preserved")
	}

	// No secret overrides (REGISTRY_USER was env ref, not secret)
	if len(overrides) != 0 {
		t.Errorf("expected no secret overrides, got %d", len(overrides))
	}
}

// TestToJobConfig tests conversion from JobSpec to JobConfig
func TestToJobConfig(t *testing.T) {
	spec := &JobSpec{
		Name:    "test-job",
		Command: "sh -c 'echo hello'",
		Image:   "alpine:latest",
		Environment: map[string]string{
			"MY_VAR": "my-value",
		},
		Capabilities:   []string{"docker", "gpu"},
		WorkingDir:     "/custom/dir",
		TimeoutSeconds: 300,
		CPULimit:       "2",
		MemoryLimit:    "512Mi",
	}

	config := spec.ToJobConfig("/workspace", "job-123", "default")

	if config.Image != "alpine:latest" {
		t.Errorf("image = %q, want alpine:latest", config.Image)
	}
	if len(config.Command) != 3 {
		t.Errorf("command should have 3 parts, got %d", len(config.Command))
	}
	if config.WorkspaceDir != "/workspace" {
		t.Errorf("workspace = %q, want /workspace", config.WorkspaceDir)
	}
	if config.WorkingDir != "/custom/dir" {
		t.Errorf("workingDir = %q, want /custom/dir", config.WorkingDir)
	}
	if config.JobID != "job-123" {
		t.Errorf("jobID = %q, want job-123", config.JobID)
	}
	if config.QueueName != "default" {
		t.Errorf("queueName = %q, want default", config.QueueName)
	}

	// Check environment includes both user vars and job metadata
	if config.Env["MY_VAR"] != "my-value" {
		t.Errorf("MY_VAR not in env")
	}
	if config.Env["REACTORCIDE_JOB_ID"] != "job-123" {
		t.Errorf("REACTORCIDE_JOB_ID not set")
	}

	// Check capabilities
	if len(config.Capabilities) != 2 {
		t.Errorf("expected 2 capabilities, got %d", len(config.Capabilities))
	}
}
