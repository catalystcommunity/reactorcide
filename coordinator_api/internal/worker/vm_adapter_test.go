package worker

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker/vmrunner"
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

func TestVMRunnerAdapterLocalImagesAllowEmptyPrefetch(t *testing.T) {
	adapter := &vmRunnerAdapter{}
	if err := adapter.PrefetchImages(context.Background(), nil); err != nil {
		t.Fatalf("empty prefetch with local images: %v", err)
	}
	if err := adapter.PrefetchImages(context.Background(), []string{"registry.example/image:tag"}); err == nil {
		t.Fatal("configured OCI prefetch with local images succeeded")
	}
}

func TestIsBackendSupported_VM(t *testing.T) {
	if !IsBackendSupported("vm") {
		t.Fatal(`IsBackendSupported("vm") = false, want true (it's a recognized backend name on every OS)`)
	}
}

// TestIsBackendImplemented_VM_MatchesOS verifies that the VM backend is
// available only on hosts that provide a VM lifecycle.
func TestIsBackendImplemented_VM_MatchesOS(t *testing.T) {
	want := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	if got := IsBackendImplemented("vm"); got != want {
		t.Fatalf("IsBackendImplemented(\"vm\") = %v, want %v on GOOS=%s", got, want, runtime.GOOS)
	}
}

// TestNewJobRunner_VM_OnUnsupportedOS verifies the error on an unsupported
// host operating system.
func TestNewJobRunner_VM_OnUnsupportedOS(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("this test covers only host operating systems without a VM lifecycle")
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
		Image:        "test.img",
		Command:      []string{"sh", "-c", "true"},
		Env:          map[string]string{"K": "V"},
		CPURequest:   "500m",
		CPULimit:     "1",
		MemoryLimit:  "1Gi",
		JobID:        "job-x",
		WorkspaceDir: "/some/workspace",
	}

	vc := toVMJobConfig(config, "runner", "darwin")

	if vc.Image != config.Image {
		t.Errorf("Image = %q, want %q", vc.Image, config.Image)
	}
	if len(vc.Command) != 3 || vc.Command[2] != "true" {
		t.Errorf("Command = %v, want %v", vc.Command, config.Command)
	}
	if vc.Env["K"] != "V" {
		t.Errorf("Env[K] = %q, want %q", vc.Env["K"], "V")
	}
	if vc.Env["HOME"] != "/Users/runner" {
		t.Errorf("Env[HOME] = %q, want %q", vc.Env["HOME"], "/Users/runner")
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

func TestToVMJobConfig_StagesVCSAuthForWindowsGuest(t *testing.T) {
	config := &JobConfig{
		Image:      "windows.img",
		Command:    []string{"runnerlib", "run"},
		WorkingDir: "/job",
		Env: map[string]string{
			"REACTORCIDE_VCS_AUTH_DIR":  "/job/.reactorcide/vcs-auth",
			"GIT_CONFIG_GLOBAL":         "/job/.reactorcide/vcs-auth/gitconfig",
			"REACTORCIDE_CODE_DIR":      "/job/src",
			"REACTORCIDE_TRIGGERS_FILE": "/job/triggers.json",
		},
		SourceDir:       `C:\host\source`,
		SourceMountPath: "/job/src",
		VCSAuth: &VCSAuthConfig{
			ContainerDir: "/job/.reactorcide/vcs-auth",
			GitConfig:    "git-config-data",
			Credentials:  "credential-data",
		},
		JobID: "job-windows",
	}

	vmConfig := toVMJobConfig(config, "runner", "windows")
	if vmConfig.Platform != vmrunner.GuestPlatformWindows {
		t.Fatalf("Platform = %q", vmConfig.Platform)
	}
	if vmConfig.WorkingDir != `C:/reactorcide/job` {
		t.Fatalf("WorkingDir = %q", vmConfig.WorkingDir)
	}
	if vmConfig.Env["REACTORCIDE_VCS_AUTH_DIR"] != `C:/reactorcide/job/.reactorcide/vcs-auth` {
		t.Fatalf("VCS auth dir = %q", vmConfig.Env["REACTORCIDE_VCS_AUTH_DIR"])
	}
	if vmConfig.Env["REACTORCIDE_CODE_DIR"] != `C:/reactorcide/job/src` {
		t.Fatalf("code dir = %q", vmConfig.Env["REACTORCIDE_CODE_DIR"])
	}
	if vmConfig.Env["REACTORCIDE_TRIGGERS_FILE"] != `C:/reactorcide/job/triggers.json` {
		t.Fatalf("triggers file = %q", vmConfig.Env["REACTORCIDE_TRIGGERS_FILE"])
	}
	if len(vmConfig.Trees) != 1 || vmConfig.Trees[0].SourcePath != `C:\host\source` || vmConfig.Trees[0].Destination != `C:/reactorcide/job/src` {
		t.Fatalf("Trees = %+v", vmConfig.Trees)
	}
	if len(vmConfig.Files) != 2 || string(vmConfig.Files[0].Data) != "git-config-data" || string(vmConfig.Files[1].Data) != "credential-data" {
		t.Fatalf("Files = %+v", vmConfig.Files)
	}
	for _, file := range vmConfig.Files {
		if file.Mode != 0o600 {
			t.Fatalf("file mode = %o", file.Mode)
		}
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
