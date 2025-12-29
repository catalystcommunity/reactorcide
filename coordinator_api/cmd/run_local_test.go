package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
)

func TestLoadJobSpec_YAML(t *testing.T) {
	// Create temp file
	tempDir := t.TempDir()
	jobFile := filepath.Join(tempDir, "test.yaml")

	content := `name: test-job
command: echo hello
environment:
  FOO: bar
`
	if err := os.WriteFile(jobFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	spec, err := worker.LoadJobSpec(jobFile)
	if err != nil {
		t.Fatalf("LoadJobSpec failed: %v", err)
	}

	if spec.Name != "test-job" {
		t.Errorf("expected name 'test-job', got %q", spec.Name)
	}
	if spec.Command != "echo hello" {
		t.Errorf("expected command 'echo hello', got %q", spec.Command)
	}
	if spec.Environment["FOO"] != "bar" {
		t.Errorf("expected FOO=bar, got %q", spec.Environment["FOO"])
	}
}

func TestLoadJobSpec_JSON(t *testing.T) {
	tempDir := t.TempDir()
	jobFile := filepath.Join(tempDir, "test.json")

	content := `{
  "name": "test-job",
  "command": "echo hello",
  "environment": {
    "FOO": "bar"
  }
}`
	if err := os.WriteFile(jobFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	spec, err := worker.LoadJobSpec(jobFile)
	if err != nil {
		t.Fatalf("LoadJobSpec failed: %v", err)
	}

	if spec.Name != "test-job" {
		t.Errorf("expected name 'test-job', got %q", spec.Name)
	}
	if spec.Command != "echo hello" {
		t.Errorf("expected command 'echo hello', got %q", spec.Command)
	}
}

func TestLoadJobSpec_MissingCommand(t *testing.T) {
	tempDir := t.TempDir()
	jobFile := filepath.Join(tempDir, "test.yaml")

	content := `name: test-job
environment:
  FOO: bar
`
	if err := os.WriteFile(jobFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := worker.LoadJobSpec(jobFile)
	if err == nil {
		t.Fatal("expected error for missing command")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("expected error about command, got %v", err)
	}
}

func TestLoadJobSpec_DefaultName(t *testing.T) {
	tempDir := t.TempDir()
	jobFile := filepath.Join(tempDir, "my-job.yaml")

	content := `command: echo hello`
	if err := os.WriteFile(jobFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	spec, err := worker.LoadJobSpec(jobFile)
	if err != nil {
		t.Fatalf("LoadJobSpec failed: %v", err)
	}

	if spec.Name != "my-job.yaml" {
		t.Errorf("expected name 'my-job.yaml', got %q", spec.Name)
	}
}

func TestSecretMasker(t *testing.T) {
	masker := secrets.NewMasker()
	masker.RegisterSecret("secret123")
	masker.RegisterSecret("password456")

	tests := []struct {
		input    string
		expected string
	}{
		{"no secrets here", "no secrets here"},
		{"the secret is secret123", "the secret is [REDACTED]"},
		{"password456 is the password", "[REDACTED] is the password"},
		{"both secret123 and password456", "both [REDACTED] and [REDACTED]"},
		{"secret123secret123", "[REDACTED][REDACTED]"},
	}

	for _, tt := range tests {
		result := masker.MaskString(tt.input)
		if result != tt.expected {
			t.Errorf("MaskString(%q) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}

func TestSecretMasker_ShortStrings(t *testing.T) {
	masker := secrets.NewMasker()
	masker.RegisterSecret("ab") // Too short, should not be registered

	result := masker.MaskString("ab is here")
	if result != "ab is here" {
		t.Errorf("short strings should not be masked, got %q", result)
	}
}

func TestSecretRefPattern(t *testing.T) {
	tests := []struct {
		input       string
		shouldMatch bool
		path        string
		key         string
	}{
		{"${secret:path:key}", true, "path", "key"},
		{"${secret:project/test:API_KEY}", true, "project/test", "API_KEY"},
		{"${secret:my-path:my_key}", true, "my-path", "my_key"},
		{"normal value", false, "", ""},
		{"${other:path:key}", false, "", ""},
	}

	for _, tt := range tests {
		matches := worker.SecretRefPattern.FindStringSubmatch(tt.input)
		if tt.shouldMatch {
			if len(matches) != 3 {
				t.Errorf("expected match for %q", tt.input)
				continue
			}
			if matches[1] != tt.path {
				t.Errorf("expected path %q, got %q", tt.path, matches[1])
			}
			if matches[2] != tt.key {
				t.Errorf("expected key %q, got %q", tt.key, matches[2])
			}
		} else {
			if len(matches) > 0 {
				t.Errorf("expected no match for %q", tt.input)
			}
		}
	}
}

func TestResolveSecretRefs_NoSecret(t *testing.T) {
	// No secret reference, should return as-is
	result, err := worker.ResolveSecretRefs("normal value", func(path, key string) (string, error) {
		t.Fatal("getter should not be called for non-secret values")
		return "", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "normal value" {
		t.Errorf("expected unchanged value, got %q", result)
	}
}

func TestResolveSecretRefs_WithSecret(t *testing.T) {
	result, err := worker.ResolveSecretRefs("token=${secret:project/test:API_KEY}", func(path, key string) (string, error) {
		if path == "project/test" && key == "API_KEY" {
			return "my-secret-value", nil
		}
		return "", nil
	})
	if err != nil {
		t.Fatalf("ResolveSecretRefs failed: %v", err)
	}

	if result != "token=my-secret-value" {
		t.Errorf("expected 'token=my-secret-value', got %q", result)
	}
}

func TestHasSecretRefs(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"${secret:path:key}", true},
		{"prefix ${secret:path:key} suffix", true},
		{"normal value", false},
		{"${other:path:key}", false},
	}

	for _, tt := range tests {
		result := worker.HasSecretRefs(tt.input)
		if result != tt.expected {
			t.Errorf("HasSecretRefs(%q) = %v, expected %v", tt.input, result, tt.expected)
		}
	}
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"echo hello", []string{"echo", "hello"}},
		{"sh -c 'echo hello'", []string{"sh", "-c", "echo hello"}},
		{`sh -c "echo hello"`, []string{"sh", "-c", "echo hello"}},
		{`sh -c 'echo "hello world"'`, []string{"sh", "-c", `echo "hello world"`}},
		{`sh -c "echo 'hello world'"`, []string{"sh", "-c", "echo 'hello world'"}},
		{"cmd arg1 arg2 arg3", []string{"cmd", "arg1", "arg2", "arg3"}},
		{`echo "multi word arg"`, []string{"echo", "multi word arg"}},
		{`escaped\ space`, []string{"escaped space"}},
	}

	for _, tt := range tests {
		result := worker.ParseCommand(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("ParseCommand(%q): got %d args, expected %d\n  got: %v\n  expected: %v",
				tt.input, len(result), len(tt.expected), result, tt.expected)
			continue
		}
		for i := range result {
			if result[i] != tt.expected[i] {
				t.Errorf("ParseCommand(%q)[%d]: got %q, expected %q",
					tt.input, i, result[i], tt.expected[i])
			}
		}
	}
}

func TestToJobConfig(t *testing.T) {
	spec := &worker.JobSpec{
		Name:    "test-job",
		Command: "sh -c 'echo hello'",
		Image:   "alpine:latest",
		Environment: map[string]string{
			"FOO": "bar",
		},
		WorkingDir: "/custom",
	}

	config := spec.ToJobConfig("/tmp/workspace", "job-123", "test-queue")

	if config.Image != "alpine:latest" {
		t.Errorf("expected image 'alpine:latest', got %q", config.Image)
	}

	// Command should be parsed
	expectedCmd := []string{"sh", "-c", "echo hello"}
	if len(config.Command) != len(expectedCmd) {
		t.Errorf("expected %d command args, got %d", len(expectedCmd), len(config.Command))
	}

	if config.WorkspaceDir != "/tmp/workspace" {
		t.Errorf("expected workspace '/tmp/workspace', got %q", config.WorkspaceDir)
	}

	if config.WorkingDir != "/custom" {
		t.Errorf("expected working dir '/custom', got %q", config.WorkingDir)
	}

	if config.JobID != "job-123" {
		t.Errorf("expected job ID 'job-123', got %q", config.JobID)
	}

	if config.QueueName != "test-queue" {
		t.Errorf("expected queue 'test-queue', got %q", config.QueueName)
	}

	// Check environment has job metadata added
	if config.Env["FOO"] != "bar" {
		t.Errorf("expected FOO=bar, got %q", config.Env["FOO"])
	}
	if config.Env["REACTORCIDE_JOB_ID"] != "job-123" {
		t.Errorf("expected REACTORCIDE_JOB_ID=job-123, got %q", config.Env["REACTORCIDE_JOB_ID"])
	}
}

func TestJobSpec_WithSource(t *testing.T) {
	tempDir := t.TempDir()
	jobFile := filepath.Join(tempDir, "test.yaml")

	content := `name: test-job
command: echo hello
source:
  type: git
  url: https://github.com/example/repo.git
  ref: main
`
	if err := os.WriteFile(jobFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	spec, err := worker.LoadJobSpec(jobFile)
	if err != nil {
		t.Fatalf("LoadJobSpec failed: %v", err)
	}

	if spec.Source == nil {
		t.Fatal("expected source to be non-nil")
	}
	if spec.Source.Type != "git" {
		t.Errorf("expected type 'git', got %q", spec.Source.Type)
	}
	if spec.Source.URL != "https://github.com/example/repo.git" {
		t.Errorf("unexpected URL: %q", spec.Source.URL)
	}
	if spec.Source.Ref != "main" {
		t.Errorf("expected ref 'main', got %q", spec.Source.Ref)
	}
}

func TestIsSensitiveKey(t *testing.T) {
	tests := []struct {
		key      string
		expected bool
	}{
		{"API_TOKEN", true},
		{"MY_SECRET", true},
		{"PASSWORD", true},
		{"AUTH_KEY", true},
		{"NORMAL_VAR", false},
		{"MY_CONFIG", false},
	}

	for _, tt := range tests {
		result := isSensitiveKey(tt.key)
		if result != tt.expected {
			t.Errorf("isSensitiveKey(%q) = %v, expected %v", tt.key, result, tt.expected)
		}
	}
}
