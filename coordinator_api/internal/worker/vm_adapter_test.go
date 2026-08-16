package worker

import (
	"context"
	"os"
	"path/filepath"
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
		WorkspaceDir: t.TempDir(),
	}

	vc, err := toVMJobConfig(config, "runner", "darwin")
	if err != nil {
		t.Fatal(err)
	}

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
			"REACTORCIDE_CI_SOURCE_DIR": "/job/ci",
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

	workspace := t.TempDir()
	config.WorkspaceDir = workspace
	varsFile := filepath.Join(t.TempDir(), "workflow-vars.json")
	if err := os.WriteFile(varsFile, []byte(`{"channel":"stable"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config.ExtraMounts = []string{varsFile + ":/job/workflow-vars.json:ro"}
	config.Env["RC_WF_VARS_FILE"] = "/job/workflow-vars.json"
	config.Env["RC_WF_OUTPUT_FILE"] = "/job/workflow-output.json"
	vmConfig, err := toVMJobConfig(config, "runner", "windows")
	if err != nil {
		t.Fatal(err)
	}
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
	if vmConfig.Env["REACTORCIDE_CI_SOURCE_DIR"] != `C:/reactorcide/job/ci` {
		t.Fatalf("CI source dir = %q", vmConfig.Env["REACTORCIDE_CI_SOURCE_DIR"])
	}
	if vmConfig.Env["REACTORCIDE_TRIGGERS_FILE"] != `C:/reactorcide/job/triggers.json` {
		t.Fatalf("triggers file = %q", vmConfig.Env["REACTORCIDE_TRIGGERS_FILE"])
	}
	if vmConfig.Env["RC_WF_VARS_FILE"] != `C:/reactorcide/job/workflow-vars.json` || vmConfig.Env["RC_WF_OUTPUT_FILE"] != `C:/reactorcide/job/workflow-output.json` {
		t.Fatalf("workflow paths = %q, %q", vmConfig.Env["RC_WF_VARS_FILE"], vmConfig.Env["RC_WF_OUTPUT_FILE"])
	}
	if len(vmConfig.Trees) != 2 || vmConfig.Trees[1].SourcePath != `C:\host\source` || vmConfig.Trees[1].Destination != `C:/reactorcide/job/src` {
		t.Fatalf("Trees = %+v", vmConfig.Trees)
	}
	if len(vmConfig.Files) != 3 || string(vmConfig.Files[0].Data) != "git-config-data" || string(vmConfig.Files[1].Data) != "credential-data" || string(vmConfig.Files[2].Data) != `{"channel":"stable"}` {
		t.Fatalf("Files = %+v", vmConfig.Files)
	}
	for _, file := range vmConfig.Files {
		if file.Mode != 0o600 {
			t.Fatalf("file mode = %o", file.Mode)
		}
	}
	if len(vmConfig.Results) != 2 || vmConfig.Results[0].SourcePath != `C:/reactorcide/job/workflow-output.json` || vmConfig.Results[0].DestinationPath != filepath.Join(workspace, "workflow-output.json") {
		t.Fatalf("Results = %+v", vmConfig.Results)
	}
}

func TestVMJobInputMountSupportsWindowsHostPaths(t *testing.T) {
	host, guest, ok := vmJobInputMount(`D:\reactorcide\ci:/job/ci:ro`)
	if !ok || host != `D:\reactorcide\ci` || guest != "/job/ci" {
		t.Fatalf("mount = %q, %q, %v", host, guest, ok)
	}
	if _, _, ok := vmJobInputMount(`/tmp/passwd:/etc/passwd:ro`); ok {
		t.Fatal("accepted a mount outside /job")
	}
}

func TestLoadVMConfig_Defaults(t *testing.T) {
	ConfigureVMPlainHTTPRegistries(nil)
	t.Cleanup(func() { ConfigureVMPlainHTTPRegistries(nil) })
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

func TestLoadVMConfigPlainHTTPIsCLIOnly(t *testing.T) {
	ConfigureVMPlainHTTPRegistries(nil)
	t.Cleanup(func() { ConfigureVMPlainHTTPRegistries(nil) })
	t.Setenv("REACTORCIDE_VM_IMAGE_REGISTRY_PLAIN_HTTP", "ignored.example:5000")

	cfg, err := LoadVMConfig()
	if err != nil {
		t.Fatalf("LoadVMConfig: %v", err)
	}
	if len(cfg.OCIPlainHTTPRegistries) != 0 {
		t.Fatalf("environment enabled plain HTTP: %v", cfg.OCIPlainHTTPRegistries)
	}

	ConfigureVMPlainHTTPRegistries([]string{"explicit.example:5000"})
	cfg, err = LoadVMConfig()
	if err != nil {
		t.Fatalf("LoadVMConfig after explicit configuration: %v", err)
	}
	if len(cfg.OCIPlainHTTPRegistries) != 1 || cfg.OCIPlainHTTPRegistries[0] != "explicit.example:5000" {
		t.Fatalf("explicit plain HTTP registries = %v", cfg.OCIPlainHTTPRegistries)
	}
}
