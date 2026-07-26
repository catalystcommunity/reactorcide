package worker

import (
	"runtime"
	"strings"
	"testing"
)

func TestGetSupportedBackends_IncludesVM(t *testing.T) {
	backends := GetSupportedBackends()
	found := false
	for _, b := range backends {
		if b == BackendVM {
			found = true
		}
	}
	if !found {
		t.Fatalf("GetSupportedBackends() = %v, want it to include %q", backends, BackendVM)
	}
}

func TestIsBackendSupported_VM(t *testing.T) {
	if !IsBackendSupported("vm") {
		t.Fatal(`IsBackendSupported("vm") = false, want true (it's a recognized backend name on every OS)`)
	}
}

// TestIsBackendImplemented_VM_MatchesOS documents the honest split this
// task calls for: "vm" is a supported backend name everywhere, but only
// actually implemented (a real VMLifecycle) on darwin/windows once
// VM-3/VM-4 land -- see vmrunner.newVMLifecycle.
func TestIsBackendImplemented_VM_MatchesOS(t *testing.T) {
	want := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	if got := IsBackendImplemented("vm"); got != want {
		t.Fatalf("IsBackendImplemented(\"vm\") = %v, want %v on GOOS=%s", got, want, runtime.GOOS)
	}
}

// TestNewJobRunner_VM_OnUnsupportedOS covers this task's Linux-testable
// requirement: NewJobRunner("vm") on an OS with no VMLifecycle must return
// a clear, actionable error rather than a generic failure or a nil
// runner/nil error pair.
func TestNewJobRunner_VM_OnUnsupportedOS(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("darwin/windows have a stub VMLifecycle that succeeds at construction (VM-3/VM-4 not landed yet) -- this test only covers the 'no lifecycle at all' OS path")
	}

	runner, err := NewJobRunner("vm")
	if err == nil {
		t.Fatal("expected an error constructing the vm backend on this OS")
	}
	if runner != nil {
		t.Fatalf("expected nil runner alongside the error, got %T", runner)
	}
	if !strings.Contains(err.Error(), "vm backend not supported on this OS") {
		t.Fatalf("error = %q, want it to explain the OS is unsupported", err.Error())
	}
}

func TestToVMJobConfig_MapsRelevantFields(t *testing.T) {
	config := &JobConfig{
		Image:       "test.img",
		Command:     []string{"sh", "-c", "true"},
		Env:         map[string]string{"K": "V"},
		CPURequest:  "500m",
		CPULimit:    "1",
		MemoryLimit: "1Gi",
		JobID:       "job-x",
		// Fields with no VM-guest equivalent yet -- must NOT cause a panic
		// or be silently expected to do anything.
		WorkspaceDir: "/some/workspace",
	}

	vc := toVMJobConfig(config)

	if vc.Image != config.Image {
		t.Errorf("Image = %q, want %q", vc.Image, config.Image)
	}
	if len(vc.Command) != 3 || vc.Command[2] != "true" {
		t.Errorf("Command = %v, want %v", vc.Command, config.Command)
	}
	if vc.Env["K"] != "V" {
		t.Errorf("Env[K] = %q, want %q", vc.Env["K"], "V")
	}
	if vc.CPURequest != config.CPURequest || vc.CPULimit != config.CPULimit || vc.MemoryLimit != config.MemoryLimit {
		t.Errorf("resource fields = (%q, %q, %q), want (%q, %q, %q)",
			vc.CPURequest, vc.CPULimit, vc.MemoryLimit,
			config.CPURequest, config.CPULimit, config.MemoryLimit)
	}
	if vc.JobID != config.JobID {
		t.Errorf("JobID = %q, want %q", vc.JobID, config.JobID)
	}
}

func TestLoadVMConfig_Defaults(t *testing.T) {
	t.Setenv("REACTORCIDE_VM_IMAGE_DIR", "")
	t.Setenv("REACTORCIDE_VM_SSH_USER", "")
	t.Setenv("REACTORCIDE_VM_SSH_PASSWORD", "")
	t.Setenv("REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE", "")

	cfg, err := LoadVMConfig()
	if err != nil {
		t.Fatalf("LoadVMConfig: %v", err)
	}
	if cfg.ImageDir == "" {
		t.Error("expected a non-empty default ImageDir")
	}
	if cfg.SSHUser == "" {
		t.Error("expected a non-empty default SSHUser")
	}
}
