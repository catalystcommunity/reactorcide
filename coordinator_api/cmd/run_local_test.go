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

	// Check standard container environment variables
	if config.Env["REACTORCIDE_IN_CONTAINER"] != "true" {
		t.Errorf("expected REACTORCIDE_IN_CONTAINER=true, got %q", config.Env["REACTORCIDE_IN_CONTAINER"])
	}
	if config.Env["REACTORCIDE_CODE_DIR"] != "/job/src" {
		t.Errorf("expected REACTORCIDE_CODE_DIR=/job/src, got %q", config.Env["REACTORCIDE_CODE_DIR"])
	}
	if config.Env["REACTORCIDE_JOB_DIR"] != "/job/src" {
		t.Errorf("expected REACTORCIDE_JOB_DIR=/job/src, got %q", config.Env["REACTORCIDE_JOB_DIR"])
	}
}

func TestToJobConfig_CustomCodeAndJobDirs(t *testing.T) {
	spec := &worker.JobSpec{
		Name:       "custom-paths",
		Command:    "pwd",
		Image:      "alpine:latest",
		CodeDir:    "/job/code",
		JobDir:     "/job/code/subdir",
		WorkingDir: "/job/code/subdir",
	}

	config := spec.ToJobConfig("/tmp/workspace", "job-123", "test-queue")

	if config.SourceMountPath != "/job/code" {
		t.Errorf("expected source mount path '/job/code', got %q", config.SourceMountPath)
	}
	if config.WorkingDir != "/job/code/subdir" {
		t.Errorf("expected working dir '/job/code/subdir', got %q", config.WorkingDir)
	}
	if config.Env["REACTORCIDE_CODE_DIR"] != "/job/code" {
		t.Errorf("expected REACTORCIDE_CODE_DIR=/job/code, got %q", config.Env["REACTORCIDE_CODE_DIR"])
	}
	if config.Env["REACTORCIDE_JOB_DIR"] != "/job/code/subdir" {
		t.Errorf("expected REACTORCIDE_JOB_DIR=/job/code/subdir, got %q", config.Env["REACTORCIDE_JOB_DIR"])
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

func TestJobSpec_RunLocalBlock(t *testing.T) {
	tempDir := t.TempDir()
	jobFile := filepath.Join(tempDir, "test.yaml")

	content := `name: test-job
command: echo hello
run_local:
  as_runner: true
  user: "1001:1001"
`
	if err := os.WriteFile(jobFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	spec, err := worker.LoadJobSpec(jobFile)
	if err != nil {
		t.Fatalf("LoadJobSpec failed: %v", err)
	}
	if spec.RunLocal == nil {
		t.Fatal("expected run_local block to be parsed")
	}
	if !spec.RunLocal.AsRunner {
		t.Error("expected run_local.as_runner=true")
	}
	if spec.RunLocal.User != "1001:1001" {
		t.Errorf("expected run_local.user '1001:1001', got %q", spec.RunLocal.User)
	}
}

func TestMergeJobSpecs_RunLocalOverlay(t *testing.T) {
	base := &worker.JobSpec{
		Name:     "base",
		Command:  "echo hi",
		RunLocal: &worker.RunLocalSpec{AsRunner: true},
	}
	overlay := &worker.JobSpec{
		RunLocal: &worker.RunLocalSpec{User: "5000:5000"},
	}

	merged, _ := worker.MergeJobSpecs(base, []*worker.JobSpec{overlay}, []string{"overlay.yaml"})
	if merged.RunLocal == nil {
		t.Fatal("expected run_local to survive merge")
	}
	if merged.RunLocal.User != "5000:5000" {
		t.Errorf("expected overlay to override user, got %q", merged.RunLocal.User)
	}
	// Base run_local with no overlay should pass through unchanged.
	mergedNoOverlay, _ := worker.MergeJobSpecs(base, nil, nil)
	if mergedNoOverlay.RunLocal == nil || !mergedNoOverlay.RunLocal.AsRunner {
		t.Error("expected base run_local.as_runner to pass through merge")
	}
}

func TestResolveCodeSourceFromArgs(t *testing.T) {
	tests := []struct {
		name        string
		codeURL     string
		codeRef     string
		prNum       int
		wantNil     bool
		wantErr     bool
		wantURL     string
		wantRef     string
		wantHeadRef string
	}{
		{
			name:    "no flags returns nil",
			wantNil: true,
		},
		{
			name:        "code-url alone resolves",
			codeURL:     "https://github.com/fork-owner/repo.git",
			codeRef:     "lilac/text-overflow",
			wantURL:     "https://github.com/fork-owner/repo.git",
			wantRef:     "lilac/text-overflow",
			wantHeadRef: "lilac/text-overflow",
		},
		{
			name:        "code-url without ref",
			codeURL:     "https://github.com/owner/repo.git",
			wantURL:     "https://github.com/owner/repo.git",
			wantRef:     "",
			wantHeadRef: "",
		},
		{
			name:    "pr and code-url are mutually exclusive",
			codeURL: "https://github.com/owner/repo.git",
			prNum:   60,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCodeSourceFromArgs(tt.codeURL, tt.codeRef, tt.prNum)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil result, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected resolved source, got nil")
			}
			if got.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", got.URL, tt.wantURL)
			}
			if got.Ref != tt.wantRef {
				t.Errorf("Ref = %q, want %q", got.Ref, tt.wantRef)
			}
			if got.HeadRef != tt.wantHeadRef {
				t.Errorf("HeadRef = %q, want %q", got.HeadRef, tt.wantHeadRef)
			}
		})
	}
}

func TestParseUserGroup(t *testing.T) {
	tests := []struct {
		input   string
		wantUID int
		wantGID int
		wantErr bool
	}{
		{"1001:1001", 1001, 1001, false},
		{"1001", 1001, 1001, false},
		{"5000:6000", 5000, 6000, false},
		{" 1001 : 1001 ", 1001, 1001, false},
		{"1001:", 1001, 1001, false},
		{"runner", runnerUID, runnerGID, false},
		{"RUNNER", runnerUID, runnerGID, false},
		{"abc", 0, 0, true},
		{"1001:xyz", 0, 0, true},
		{"", 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			uid, gid, err := parseUserGroup(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if uid != tt.wantUID || gid != tt.wantGID {
				t.Errorf("parseUserGroup(%q) = %d:%d, want %d:%d", tt.input, uid, gid, tt.wantUID, tt.wantGID)
			}
		})
	}
}

func TestResolveRunAsUserFromArgs(t *testing.T) {
	hostUID, hostGID := hostRunAsUser()

	tests := []struct {
		name         string
		userFlag     string
		asRunnerFlag bool
		specUser     string
		specAsRunner bool
		runAsUser    string
		wantUID      int
		wantGID      int
		wantAsRunner bool
		wantErr      bool
	}{
		{
			name:    "default falls back to host uid",
			wantUID: hostUID, wantGID: hostGID, wantAsRunner: false,
		},
		{
			name: "as-runner flag pins 1001", asRunnerFlag: true,
			wantUID: runnerUID, wantGID: runnerGID, wantAsRunner: true,
		},
		{
			name: "user flag pins explicit uid", userFlag: "5000:6000",
			wantUID: 5000, wantGID: 6000, wantAsRunner: false,
		},
		{
			name: "user flag 1001 is treated as runner", userFlag: "1001:1001",
			wantUID: runnerUID, wantGID: runnerGID, wantAsRunner: true,
		},
		{
			name: "yaml as_runner applies when no flags", specAsRunner: true,
			wantUID: runnerUID, wantGID: runnerGID, wantAsRunner: true,
		},
		{
			name: "yaml user applies when no flags", specUser: "1001:1001",
			wantUID: runnerUID, wantGID: runnerGID, wantAsRunner: true,
		},
		{
			name: "yaml symbolic user runner", specUser: "runner",
			wantUID: runnerUID, wantGID: runnerGID, wantAsRunner: true,
		},
		{
			name: "symbolic user host resolves to host uid", userFlag: "host",
			wantUID: hostUID, wantGID: hostGID, wantAsRunner: false,
		},
		{
			name: "symbolic user root resolves to zero", userFlag: "root",
			wantUID: 0, wantGID: 0, wantAsRunner: false,
		},
		{
			name: "run_as user applies when no local override", runAsUser: "root",
			wantUID: 0, wantGID: 0, wantAsRunner: false,
		},
		{
			name: "run_local user overrides run_as user", specUser: "host", runAsUser: "root",
			wantUID: hostUID, wantGID: hostGID, wantAsRunner: false,
		},
		{
			name: "cli user overrides yaml", userFlag: "5000:5000", specAsRunner: true,
			wantUID: 5000, wantGID: 5000, wantAsRunner: false,
		},
		{
			name: "cli as-runner overrides yaml user", asRunnerFlag: true, specUser: "5000:5000",
			wantUID: runnerUID, wantGID: runnerGID, wantAsRunner: true,
		},
		{
			name: "as-runner and user together error", asRunnerFlag: true, userFlag: "1001:1001",
			wantErr: true,
		},
		{
			name: "invalid user flag errors", userFlag: "nope",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid, gid, asRunner, err := resolveRunAsUserFromArgs(tt.userFlag, tt.asRunnerFlag, tt.specUser, tt.specAsRunner, tt.runAsUser)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if uid != tt.wantUID || gid != tt.wantGID {
				t.Errorf("uid:gid = %d:%d, want %d:%d", uid, gid, tt.wantUID, tt.wantGID)
			}
			if asRunner != tt.wantAsRunner {
				t.Errorf("asRunner = %v, want %v", asRunner, tt.wantAsRunner)
			}
		})
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
