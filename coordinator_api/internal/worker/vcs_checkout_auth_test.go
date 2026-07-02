package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestPrepareVCSCheckoutAuth_UsesGlobalTokenWithoutEnvValue(t *testing.T) {
	previous := config.VCSGitHubToken
	config.VCSGitHubToken = "test-token-123"
	t.Cleanup(func() { config.VCSGitHubToken = previous })

	workspace := t.TempDir()
	jp := &JobProcessor{store: &MockStore{}, config: &JobProcessorConfig{SecretsStorageType: "none"}}
	env := map[string]string{
		"REACTORCIDE_SOURCE_URL":    "https://github.com/example/repo.git",
		"REACTORCIDE_CI_SOURCE_URL": "https://github.com/example/ci.git",
	}
	job := &models.Job{JobID: "job-1", UserID: "user-1"}

	auth, err := jp.prepareVCSCheckoutAuth(context.Background(), job, env, workspace)
	if err != nil {
		t.Fatalf("prepareVCSCheckoutAuth failed: %v", err)
	}
	if auth == nil {
		t.Fatal("expected auth config")
	}
	if auth.ContainerDir != vcsAuthContainerDir {
		t.Fatalf("unexpected container dir %q", auth.ContainerDir)
	}
	if !strings.Contains(auth.GitConfig, "credential") || !strings.Contains(auth.GitConfig, "useHttpPath = true") {
		t.Fatalf("gitconfig does not configure credential helper with useHttpPath: %s", auth.GitConfig)
	}
	if !strings.Contains(auth.Credentials, "github.com/example/repo.git") {
		t.Fatalf("credentials missing source repo entry: %s", auth.Credentials)
	}
	if !strings.Contains(auth.Credentials, "github.com/example/ci.git") {
		t.Fatalf("credentials missing CI repo entry: %s", auth.Credentials)
	}
	if got := env["GIT_CONFIG_GLOBAL"]; got != "" {
		t.Fatalf("prepareVCSCheckoutAuth should not mutate env, got GIT_CONFIG_GLOBAL=%q", got)
	}

	credentialsPath := filepath.Join(workspace, ".reactorcide", "vcs-auth", "credentials")
	if _, err := os.Stat(credentialsPath); err != nil {
		t.Fatalf("credentials file was not written: %v", err)
	}
	cleanupVCSCheckoutAuth(workspace)
	if _, err := os.Stat(credentialsPath); !os.IsNotExist(err) {
		t.Fatalf("expected credentials cleanup, stat err=%v", err)
	}
}
