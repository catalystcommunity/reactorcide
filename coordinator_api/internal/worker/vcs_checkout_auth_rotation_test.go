package worker

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// vcsRotationMockStore embeds MockStore (from corndogs_worker_test.go) and
// adds the project lookup + rotation-store methods needed to exercise
// resolveVCSCheckoutToken's rotation-aware precedence.
type vcsRotationMockStore struct {
	*MockStore

	project *models.Project

	ListActiveProjectVCSCredentialsFunc func(ctx context.Context, projectID, provider string) ([]models.ProjectVCSCredential, error)
	touchCalls                          []string
}

func (m *vcsRotationMockStore) GetProjectByID(ctx context.Context, projectID string) (*models.Project, error) {
	if m.project != nil && m.project.ProjectID == projectID {
		return m.project, nil
	}
	return nil, nil
}

func (m *vcsRotationMockStore) ListActiveProjectVCSCredentials(ctx context.Context, projectID, provider string) ([]models.ProjectVCSCredential, error) {
	if m.ListActiveProjectVCSCredentialsFunc != nil {
		return m.ListActiveProjectVCSCredentialsFunc(ctx, projectID, provider)
	}
	return nil, nil
}

func (m *vcsRotationMockStore) TouchProjectVCSCredentialLastUsed(ctx context.Context, id string) error {
	m.touchCalls = append(m.touchCalls, id)
	return nil
}

// setupLocalSecretsProvider initializes a throwaway local secrets store for
// tests that need resolveSecretRefForUser to actually resolve a path:key
// reference. Values used by tests are obviously-fake fixtures, never real
// credentials.
func setupLocalSecretsProvider(t *testing.T) (path, password string) {
	t.Helper()
	tempDir := t.TempDir()
	password = "test-password-not-real"
	storage := secrets.NewStorageWithPath(tempDir)
	if err := storage.Init(password, false); err != nil {
		t.Fatalf("failed to init secrets storage: %v", err)
	}
	return tempDir, password
}

func TestResolveVCSCheckoutToken_RotationRowWinsOverLegacy(t *testing.T) {
	secretsPath, secretsPassword := setupLocalSecretsProvider(t)

	provider, err := secrets.NewLocalProvider(secretsPath, secretsPassword)
	if err != nil {
		t.Fatalf("failed to create local provider: %v", err)
	}
	ctx := context.Background()
	if err := provider.Set(ctx, "vcs/proj", "rotated", "rotated-token-fake"); err != nil {
		t.Fatalf("failed to seed rotated secret: %v", err)
	}
	if err := provider.Set(ctx, "vcs/proj", "legacy", "legacy-token-fake"); err != nil {
		t.Fatalf("failed to seed legacy secret: %v", err)
	}

	project := &models.Project{
		ProjectID:      "proj-1",
		VCSTokenSecret: "vcs/proj:legacy",
	}

	mockStore := &vcsRotationMockStore{
		MockStore: &MockStore{},
		project:   project,
		ListActiveProjectVCSCredentialsFunc: func(ctx context.Context, projectID, providerName string) ([]models.ProjectVCSCredential, error) {
			return []models.ProjectVCSCredential{
				{ID: "rotation-id", ProjectID: projectID, Provider: providerName, SecretRef: "vcs/proj:rotated", IsActive: true},
			}, nil
		},
	}

	jp := &JobProcessor{
		store: mockStore,
		config: &JobProcessorConfig{
			SecretsStorageType:   "local",
			SecretsLocalPath:     secretsPath,
			SecretsLocalPassword: secretsPassword,
		},
	}

	job := &models.Job{JobID: "job-1", UserID: "user-1", ProjectID: &project.ProjectID}

	token, ok, err := jp.resolveVCSCheckoutToken(ctx, job, vcs.GitHub)
	if err != nil {
		t.Fatalf("resolveVCSCheckoutToken failed: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if token != "rotated-token-fake" {
		t.Fatalf("expected the active rotation row's secret to win over the legacy ref, got %q", token)
	}
	if len(mockStore.touchCalls) != 1 || mockStore.touchCalls[0] != "rotation-id" {
		t.Fatalf("expected TouchProjectVCSCredentialLastUsed called once with rotation-id, got %v", mockStore.touchCalls)
	}
}

func TestResolveVCSCheckoutToken_NoActiveRotationRows_FallsBackToLegacy(t *testing.T) {
	// Simulates every rotation row for this project+provider being
	// deactivated (or never created): ListActiveProjectVCSCredentials
	// returns none, so resolution must fall back to the legacy ref, and no
	// last_used stamp should occur since no rotation row was used.
	secretsPath, secretsPassword := setupLocalSecretsProvider(t)
	provider, err := secrets.NewLocalProvider(secretsPath, secretsPassword)
	if err != nil {
		t.Fatalf("failed to create local provider: %v", err)
	}
	ctx := context.Background()
	if err := provider.Set(ctx, "vcs/proj", "legacy", "legacy-token-fake"); err != nil {
		t.Fatalf("failed to seed legacy secret: %v", err)
	}

	project := &models.Project{
		ProjectID:      "proj-2",
		VCSTokenSecret: "vcs/proj:legacy",
	}

	mockStore := &vcsRotationMockStore{
		MockStore: &MockStore{},
		project:   project,
		ListActiveProjectVCSCredentialsFunc: func(ctx context.Context, projectID, providerName string) ([]models.ProjectVCSCredential, error) {
			return nil, nil
		},
	}

	jp := &JobProcessor{
		store: mockStore,
		config: &JobProcessorConfig{
			SecretsStorageType:   "local",
			SecretsLocalPath:     secretsPath,
			SecretsLocalPassword: secretsPassword,
		},
	}

	job := &models.Job{JobID: "job-2", UserID: "user-1", ProjectID: &project.ProjectID}

	token, ok, err := jp.resolveVCSCheckoutToken(ctx, job, vcs.GitHub)
	if err != nil {
		t.Fatalf("resolveVCSCheckoutToken failed: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if token != "legacy-token-fake" {
		t.Fatalf("expected fallback to legacy secret, got %q", token)
	}
	if len(mockStore.touchCalls) != 0 {
		t.Fatalf("expected no TouchProjectVCSCredentialLastUsed calls when no rotation row was used, got %v", mockStore.touchCalls)
	}
}
