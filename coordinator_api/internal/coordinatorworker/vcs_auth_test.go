package coordinatorworker

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
	"github.com/stretchr/testify/require"
)

// TestRunLoop_VCSAuth_WritesGitCredentialsAndMasksToken drives a lease that
// carries a coordinator-resolved vcs_auth end-to-end (WORKERS_PLAN.md Wave 3
// P3c) and asserts:
//   - the expected gitconfig (credential.helper=store, useHttpPath=true) and
//     credentials (git-credential-store lines embedding the token) files are
//     written into the lease's ephemeral workspace at
//     .reactorcide/vcs-auth, matching vcsAuthContainerDir's layout;
//   - the VCS token is injected into JobConfig.VCSAuth for the runner to
//     consume, but is NEVER placed in JobConfig.Env;
//   - a log line that echoes the token verbatim (simulating a checkout
//     command's own output) is masked before ever reaching an AppendLogs
//     chunk -- the same secret-isolation guarantee TestRunLoop_HappyPath
//     enforces for lease.Secrets, extended to lease.vcs_auth;
//   - the credential files are gone once the lease's workspace is cleaned
//     up after the job finishes.
func TestRunLoop_VCSAuth_WritesGitCredentialsAndMasksToken(t *testing.T) {
	const vcsToken = "sooper-seekrit-vcs-token-4f2a"
	stdoutContent := "cloning repo\nusing credential " + vcsToken + " for checkout\ndone\n"

	lease := csilapi.Lease{
		LeaseId:    "lease-vcs-1",
		JobId:      "job-vcs-1",
		Image:      "alpine:latest",
		Command:    []string{"sh", "-c", "echo hi"},
		WorkingDir: "/job",
		Env:        []csilapi.EnvVar{{Key: "PLAIN", Value: "not-a-secret"}},
		Resources:  csilapi.Resources{CpuRequest: "1", CpuLimit: "2", MemoryLimit: "4Gi"},
		RunAsUser:  "1001:1001",
		VcsAuth: &csilapi.VCSAuth{
			Provider: "github",
			Url:      "https://github.com/example/repo.git",
			Username: "x-access-token",
			Token:    vcsToken,
		},
	}

	fc := &fakeClient{}
	fc.RequestJobFunc = singleLeaseThenNone(lease)

	waitBlock := make(chan struct{})
	fr := &fakeRunner{
		Stdout:    io.NopCloser(strings.NewReader(stdoutContent)),
		WaitBlock: waitBlock,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, testConfig(), fc, runnerFactoryFor(fr)) }()

	require.Eventually(t, func() bool { return len(fr.snapshotSpawnCalls()) == 1 }, 5*time.Second, 20*time.Millisecond, "expected SpawnJob to be called")

	spawnCalls := fr.snapshotSpawnCalls()
	require.Len(t, spawnCalls, 1)
	cfg := spawnCalls[0]

	// The token must never be injected as a real env var.
	for k, v := range cfg.Env {
		require.NotContains(t, v, vcsToken, "VCS token must never appear in JobConfig.Env (key %s)", k)
	}
	require.NotEmpty(t, cfg.Env["REACTORCIDE_VCS_AUTH_DIR"])
	require.Equal(t, "true", cfg.Env["REACTORCIDE_VCS_AUTH_CLEANUP"])
	require.Equal(t, "0", cfg.Env["GIT_TERMINAL_PROMPT"])
	require.Equal(t, "not-a-secret", cfg.Env["PLAIN"])

	// JobConfig.VCSAuth carries the credential material for the Kubernetes
	// runner (see internal/worker/kubernetes_runner.go); its SecretValues
	// must contain exactly the resolved token (for the runner's own
	// masking/Secret-materialization), never a placeholder or empty value.
	require.NotNil(t, cfg.VCSAuth)
	require.Equal(t, vcsAuthContainerDir, cfg.VCSAuth.ContainerDir)
	require.Contains(t, cfg.VCSAuth.GitConfig, "helper = store --file "+vcsAuthContainerDir+"/credentials")
	require.Contains(t, cfg.VCSAuth.GitConfig, "useHttpPath = true")
	require.Contains(t, cfg.VCSAuth.Credentials, vcsToken)
	require.Contains(t, cfg.VCSAuth.Credentials, "x-access-token")
	require.Equal(t, []string{vcsToken}, cfg.VCSAuth.SecretValues)

	// The files actually landed on disk under the workspace's
	// .reactorcide/vcs-auth directory (Docker/containerd bind-mount this at
	// /job, so it appears at vcsAuthContainerDir with no runner-specific
	// code -- see vcs_auth.go's prepareVCSAuth doc comment).
	hostDir := filepath.Join(cfg.WorkspaceDir, ".reactorcide", "vcs-auth")
	gitconfigBytes, err := os.ReadFile(filepath.Join(hostDir, "gitconfig"))
	require.NoError(t, err)
	require.Contains(t, string(gitconfigBytes), "[credential]")
	require.Contains(t, string(gitconfigBytes), "helper = store")

	credentialsBytes, err := os.ReadFile(filepath.Join(hostDir, "credentials"))
	require.NoError(t, err)
	require.Contains(t, string(credentialsBytes), vcsToken)
	require.Contains(t, string(credentialsBytes), "github.com")

	// Let the (simulated) container finish and observe the rest of the run.
	close(waitBlock)

	require.Eventually(t, func() bool { return len(fc.snapshotReportResults()) == 1 }, 5*time.Second, 20*time.Millisecond, "expected ReportResult to be called")
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	logChunks := fc.snapshotAppendLogs()
	require.NotEmpty(t, logChunks, "expected at least one AppendLogs call")
	for _, l := range logChunks {
		require.NotContains(t, l.Chunk, vcsToken, "the VCS token must never appear in a shipped log chunk")
	}
	var sawRedacted bool
	for _, l := range logChunks {
		if strings.Contains(l.Chunk, "[REDACTED]") {
			sawRedacted = true
		}
	}
	require.True(t, sawRedacted, "expected the token-bearing line to appear masked, not dropped")

	// Cleanup: the entire ephemeral workspace (including the vcs-auth
	// credential material) is removed once the lease finishes.
	require.Eventually(t, func() bool {
		_, err := os.Stat(hostDir)
		return os.IsNotExist(err)
	}, 5*time.Second, 20*time.Millisecond, "expected the VCS auth directory to be removed once the lease's workspace is cleaned up")
}

// TestRunLoop_NoVCSAuth_NoCredentialFilesWritten asserts a lease with no
// vcs_auth (the common case) results in no JobConfig.VCSAuth and no
// REACTORCIDE_VCS_AUTH_* env vars, exercising the "absent" path.
func TestRunLoop_NoVCSAuth_NoCredentialFilesWritten(t *testing.T) {
	lease := csilapi.Lease{
		LeaseId: "lease-no-vcs",
		JobId:   "job-no-vcs",
		Image:   "alpine:latest",
		Command: []string{"true"},
	}

	fc := &fakeClient{}
	fc.RequestJobFunc = singleLeaseThenNone(lease)
	fr := &fakeRunner{}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, testConfig(), fc, runnerFactoryFor(fr)) }()

	require.Eventually(t, func() bool { return len(fc.snapshotReportResults()) == 1 }, 5*time.Second, 20*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	spawnCalls := fr.snapshotSpawnCalls()
	require.Len(t, spawnCalls, 1)
	require.Nil(t, spawnCalls[0].VCSAuth)
	_, ok := spawnCalls[0].Env["REACTORCIDE_VCS_AUTH_DIR"]
	require.False(t, ok, "no REACTORCIDE_VCS_AUTH_DIR should be set when the lease carries no vcs_auth")
}
