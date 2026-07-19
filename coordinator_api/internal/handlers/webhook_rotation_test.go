package handlers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rotationAwareMockStore embeds WebhookMockStore and adds the narrow
// rotation-store methods (webhookSecretRotationStore / vcsCredentialRotationStore)
// that webhook_handler.go type-asserts for. Plain WebhookMockStore
// intentionally does NOT implement these, which is what the backward-compat
// tests in webhook_handler_test.go exercise.
type rotationAwareMockStore struct {
	*WebhookMockStore

	ListActiveProjectWebhookSecretsFunc   func(ctx context.Context, projectID, provider string) ([]models.ProjectWebhookSecret, error)
	TouchProjectWebhookSecretLastUsedFunc func(ctx context.Context, id string) error
	touchWebhookSecretCalls               []string

	ListActiveProjectVCSCredentialsFunc   func(ctx context.Context, projectID, provider string) ([]models.ProjectVCSCredential, error)
	TouchProjectVCSCredentialLastUsedFunc func(ctx context.Context, id string) error
	touchVCSCredentialCalls               []string
}

func (m *rotationAwareMockStore) ListActiveProjectWebhookSecrets(ctx context.Context, projectID, provider string) ([]models.ProjectWebhookSecret, error) {
	if m.ListActiveProjectWebhookSecretsFunc != nil {
		return m.ListActiveProjectWebhookSecretsFunc(ctx, projectID, provider)
	}
	return nil, nil
}

func (m *rotationAwareMockStore) TouchProjectWebhookSecretLastUsed(ctx context.Context, id string) error {
	m.touchWebhookSecretCalls = append(m.touchWebhookSecretCalls, id)
	if m.TouchProjectWebhookSecretLastUsedFunc != nil {
		return m.TouchProjectWebhookSecretLastUsedFunc(ctx, id)
	}
	return nil
}

func (m *rotationAwareMockStore) ListActiveProjectVCSCredentials(ctx context.Context, projectID, provider string) ([]models.ProjectVCSCredential, error) {
	if m.ListActiveProjectVCSCredentialsFunc != nil {
		return m.ListActiveProjectVCSCredentialsFunc(ctx, projectID, provider)
	}
	return nil, nil
}

func (m *rotationAwareMockStore) TouchProjectVCSCredentialLastUsed(ctx context.Context, id string) error {
	m.touchVCSCredentialCalls = append(m.touchVCSCredentialCalls, id)
	if m.TouchProjectVCSCredentialLastUsedFunc != nil {
		return m.TouchProjectVCSCredentialLastUsedFunc(ctx, id)
	}
	return nil
}

func rotationTestPRWebhook(t *testing.T, handler *WebhookHandler, mockVCS *MockVCSClient) (*httptest.ResponseRecorder, *models.Job) {
	t.Helper()

	prEvent := &vcs.WebhookEvent{
		Provider:     vcs.GitHub,
		EventType:    "pull_request",
		GenericEvent: vcs.EventPullRequestOpened,
		Repository: vcs.RepositoryInfo{
			FullName: "test-org/test-repo",
			CloneURL: "https://github.com/test-org/test-repo.git",
		},
		PullRequest: &vcs.PullRequestInfo{
			Number:  1,
			Title:   "Test PR",
			Action:  "opened",
			HeadSHA: "sha123",
			HeadRef: "feature",
			BaseRef: "main",
		},
	}
	mockVCS.ParseWebhookFunc = func(r *http.Request) (*vcs.WebhookEvent, error) {
		return prEvent, nil
	}
	handler.AddVCSClient(vcs.GitHub, mockVCS)

	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "sha123", "feature", "main", 1)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	w := httptest.NewRecorder()

	handler.HandleGitHubWebhook(w, req)

	return w, nil
}

// TestWebhookHandler_Rotation_NewestActiveSecretTriedFirst verifies the
// precedence order: active rotation rows newest-first, before legacy/org/env
// fallbacks, and that a match on the newest row stops the search (no other
// candidate is tried) and stamps last_used_at on that row only.
func TestWebhookHandler_Rotation_NewestActiveSecretTriedFirst(t *testing.T) {
	project := webhookTestProject()
	project.WebhookSecret = "legacy/project:webhook_secret"

	mockStore := &rotationAwareMockStore{
		WebhookMockStore: &WebhookMockStore{
			GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
				return project, nil
			},
		},
		ListActiveProjectWebhookSecretsFunc: func(ctx context.Context, projectID, provider string) ([]models.ProjectWebhookSecret, error) {
			// Store contract: returned oldest-first (created_at ASC).
			return []models.ProjectWebhookSecret{
				{ID: "old-id", ProjectID: projectID, Provider: provider, SecretRef: "webhooks/proj:old", IsActive: true},
				{ID: "new-id", ProjectID: projectID, Provider: provider, SecretRef: "webhooks/proj:new", IsActive: true},
			}, nil
		},
	}

	handler := NewWebhookHandler(mockStore, nil)
	handler.SetTokenResolver(func(ctx context.Context, ref string) (string, error) {
		switch ref {
		case "webhooks/proj:old":
			return "old-secret-fake", nil
		case "webhooks/proj:new":
			return "new-secret-fake", nil
		case "legacy/project:webhook_secret":
			return "legacy-secret-fake", nil
		}
		return "", fmt.Errorf("unexpected secret ref: %s", ref)
	})

	var triedSecrets []string
	mockVCS := &MockVCSClient{
		ValidateWebhookFunc: func(r *http.Request, secret string) error {
			triedSecrets = append(triedSecrets, secret)
			if secret == "new-secret-fake" {
				return nil
			}
			return fmt.Errorf("signature mismatch")
		},
	}

	w, _ := rotationTestPRWebhook(t, handler, mockVCS)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"new-secret-fake"}, triedSecrets, "should match on the first (newest) candidate and stop")
	assert.Equal(t, []string{"new-id"}, mockStore.touchWebhookSecretCalls, "should stamp last_used_at only on the matching rotation row")
	require.Len(t, mockStore.CreateJobCalls, 1)
}

// TestWebhookHandler_Rotation_FallsThroughToOlderActiveRow verifies that
// when the newest rotation row doesn't validate, the next active row is
// tried, and last_used_at is stamped on whichever row actually matched.
func TestWebhookHandler_Rotation_FallsThroughToOlderActiveRow(t *testing.T) {
	project := webhookTestProject()
	project.WebhookSecret = "legacy/project:webhook_secret"

	mockStore := &rotationAwareMockStore{
		WebhookMockStore: &WebhookMockStore{
			GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
				return project, nil
			},
		},
		ListActiveProjectWebhookSecretsFunc: func(ctx context.Context, projectID, provider string) ([]models.ProjectWebhookSecret, error) {
			return []models.ProjectWebhookSecret{
				{ID: "old-id", ProjectID: projectID, Provider: provider, SecretRef: "webhooks/proj:old", IsActive: true},
				{ID: "new-id", ProjectID: projectID, Provider: provider, SecretRef: "webhooks/proj:new", IsActive: true},
			}, nil
		},
	}

	handler := NewWebhookHandler(mockStore, nil)
	handler.SetTokenResolver(func(ctx context.Context, ref string) (string, error) {
		switch ref {
		case "webhooks/proj:old":
			return "old-secret-fake", nil
		case "webhooks/proj:new":
			return "new-secret-fake", nil
		case "legacy/project:webhook_secret":
			return "legacy-secret-fake", nil
		}
		return "", fmt.Errorf("unexpected secret ref: %s", ref)
	})

	var triedSecrets []string
	mockVCS := &MockVCSClient{
		ValidateWebhookFunc: func(r *http.Request, secret string) error {
			triedSecrets = append(triedSecrets, secret)
			if secret == "old-secret-fake" {
				return nil
			}
			return fmt.Errorf("signature mismatch")
		},
	}

	w, _ := rotationTestPRWebhook(t, handler, mockVCS)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"new-secret-fake", "old-secret-fake"}, triedSecrets)
	assert.Equal(t, []string{"old-id"}, mockStore.touchWebhookSecretCalls)
	require.Len(t, mockStore.CreateJobCalls, 1)
}

// TestWebhookHandler_Rotation_FallsBackToLegacyWhenNoRotationRowMatches
// verifies that when rotation rows exist but none validate, resolution
// falls through to the legacy ref, and no last_used stamp happens (the
// match wasn't a rotation row).
func TestWebhookHandler_Rotation_FallsBackToLegacyWhenNoRotationRowMatches(t *testing.T) {
	project := webhookTestProject()
	project.WebhookSecret = "legacy/project:webhook_secret"

	mockStore := &rotationAwareMockStore{
		WebhookMockStore: &WebhookMockStore{
			GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
				return project, nil
			},
		},
		ListActiveProjectWebhookSecretsFunc: func(ctx context.Context, projectID, provider string) ([]models.ProjectWebhookSecret, error) {
			return []models.ProjectWebhookSecret{
				{ID: "old-id", ProjectID: projectID, Provider: provider, SecretRef: "webhooks/proj:old", IsActive: true},
			}, nil
		},
	}

	handler := NewWebhookHandler(mockStore, nil)
	handler.SetTokenResolver(func(ctx context.Context, ref string) (string, error) {
		switch ref {
		case "webhooks/proj:old":
			return "old-secret-fake", nil
		case "legacy/project:webhook_secret":
			return "legacy-secret-fake", nil
		}
		return "", fmt.Errorf("unexpected secret ref: %s", ref)
	})

	mockVCS := &MockVCSClient{
		ValidateWebhookFunc: func(r *http.Request, secret string) error {
			if secret == "legacy-secret-fake" {
				return nil
			}
			return fmt.Errorf("signature mismatch")
		},
	}

	w, _ := rotationTestPRWebhook(t, handler, mockVCS)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, mockStore.touchWebhookSecretCalls, "legacy match must not stamp a rotation row")
	require.Len(t, mockStore.CreateJobCalls, 1)
}

// TestWebhookHandler_Rotation_NoActiveRows_BackwardCompatible verifies that
// a store implementing the rotation interface but reporting zero active
// rows behaves exactly like the pre-rotation single-secret path.
func TestWebhookHandler_Rotation_NoActiveRows_BackwardCompatible(t *testing.T) {
	project := webhookTestProject()
	project.WebhookSecret = "legacy/project:webhook_secret"

	mockStore := &rotationAwareMockStore{
		WebhookMockStore: &WebhookMockStore{
			GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
				return project, nil
			},
		},
		ListActiveProjectWebhookSecretsFunc: func(ctx context.Context, projectID, provider string) ([]models.ProjectWebhookSecret, error) {
			return nil, nil
		},
	}

	handler := NewWebhookHandler(mockStore, nil)
	handler.SetTokenResolver(func(ctx context.Context, ref string) (string, error) {
		if ref == "legacy/project:webhook_secret" {
			return "legacy-secret-fake", nil
		}
		return "", fmt.Errorf("unexpected secret ref: %s", ref)
	})

	var triedSecrets []string
	mockVCS := &MockVCSClient{
		ValidateWebhookFunc: func(r *http.Request, secret string) error {
			triedSecrets = append(triedSecrets, secret)
			if secret == "legacy-secret-fake" {
				return nil
			}
			return fmt.Errorf("signature mismatch")
		},
	}

	w, _ := rotationTestPRWebhook(t, handler, mockVCS)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"legacy-secret-fake"}, triedSecrets)
	assert.Empty(t, mockStore.touchWebhookSecretCalls)
	require.Len(t, mockStore.CreateJobCalls, 1)
}

// TestWebhookHandler_Rotation_DeactivatedRowNeverTried verifies that a
// secret value which is NOT among the rows returned by
// ListActiveProjectWebhookSecrets (simulating a deactivated row filtered out
// by the store's `WHERE is_active` clause) can never validate a webhook,
// even if an attacker replays a signature computed with that old secret.
func TestWebhookHandler_Rotation_DeactivatedRowNeverTried(t *testing.T) {
	project := webhookTestProject()
	project.WebhookSecret = "" // no legacy fallback: isolate rotation behavior

	const deactivatedSecret = "deactivated-secret-fake"

	mockStore := &rotationAwareMockStore{
		WebhookMockStore: &WebhookMockStore{
			GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
				return project, nil
			},
		},
		ListActiveProjectWebhookSecretsFunc: func(ctx context.Context, projectID, provider string) ([]models.ProjectWebhookSecret, error) {
			// Only the still-active row is returned; the deactivated row is
			// absent, exactly as the store's is_active filter guarantees.
			return []models.ProjectWebhookSecret{
				{ID: "active-id", ProjectID: projectID, Provider: provider, SecretRef: "webhooks/proj:active", IsActive: true},
			}, nil
		},
	}

	handler := NewWebhookHandler(mockStore, nil)
	handler.SetTokenResolver(func(ctx context.Context, ref string) (string, error) {
		if ref == "webhooks/proj:active" {
			return "active-secret-fake", nil
		}
		return "", fmt.Errorf("unexpected secret ref: %s", ref)
	})

	mockVCS := &MockVCSClient{
		ValidateWebhookFunc: func(r *http.Request, secret string) error {
			if secret == deactivatedSecret {
				// Would only succeed if the deactivated secret incorrectly
				// leaked into the candidate list.
				return nil
			}
			return fmt.Errorf("signature mismatch")
		},
	}

	w, _ := rotationTestPRWebhook(t, handler, mockVCS)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, mockStore.touchWebhookSecretCalls)
	assert.Empty(t, mockStore.CreateJobCalls)
}

// TestWebhookHandler_Rotation_VCSCredential_StatusClientPrefersRotationRow
// verifies getStatusClient prefers the highest-precedence active
// project_vcs_credentials rotation row over the legacy project ref, and
// stamps last_used_at on success.
func TestWebhookHandler_Rotation_VCSCredential_StatusClientPrefersRotationRow(t *testing.T) {
	project := webhookTestProject()
	project.VCSTokenSecret = "legacy/project:vcs_token"

	mockStore := &rotationAwareMockStore{
		WebhookMockStore: &WebhookMockStore{},
		ListActiveProjectVCSCredentialsFunc: func(ctx context.Context, projectID, provider string) ([]models.ProjectVCSCredential, error) {
			return []models.ProjectVCSCredential{
				{ID: "old-cred", ProjectID: projectID, Provider: provider, SecretRef: "vcs/proj:old", IsActive: true},
				{ID: "new-cred", ProjectID: projectID, Provider: provider, SecretRef: "vcs/proj:new", IsActive: true},
			}, nil
		},
	}

	handler := NewWebhookHandler(mockStore, nil)
	handler.SetTokenResolver(func(ctx context.Context, ref string) (string, error) {
		switch ref {
		case "vcs/proj:new":
			return "new-token-fake", nil
		case "vcs/proj:old":
			return "old-token-fake", nil
		case "legacy/project:vcs_token":
			return "legacy-token-fake", nil
		}
		return "", fmt.Errorf("unexpected secret ref: %s", ref)
	})

	fallback := &MockVCSClient{}
	var usedToken string
	handler.SetClientFactory(func(provider vcs.Provider, token string) (vcs.Client, error) {
		usedToken = token
		return &MockVCSClient{}, nil
	})

	client := handler.getStatusClient(context.Background(), project, vcs.GitHub, fallback)

	require.NotSame(t, fallback, client)
	assert.Equal(t, "new-token-fake", usedToken, "should prefer the highest-precedence (most recently created) active rotation row")
	assert.Equal(t, []string{"new-cred"}, mockStore.touchVCSCredentialCalls)
}

// withGlobalWebhookSecret sets config.VCSGitHubSecret for the duration of a
// test and restores it afterward, so tier-exclusivity tests can simulate a
// configured global/env fallback secret without leaking state across tests.
func withGlobalWebhookSecret(t *testing.T, secret string) {
	t.Helper()
	old := config.VCSGitHubSecret
	config.VCSGitHubSecret = secret
	t.Cleanup(func() { config.VCSGitHubSecret = old })
}

// TestWebhookHandler_TierExclusivity_ProjectSecretBlocksGlobalFallback is a
// regression test for the tier-exclusivity bug: when a project has its own
// dedicated webhook secret configured, the shared global/env secret must
// NEVER be accepted as an alternate valid signature — otherwise anyone who
// knows the broadly-shared global secret could forge webhooks for a project
// that deliberately opted into its own dedicated secret.
func TestWebhookHandler_TierExclusivity_ProjectSecretBlocksGlobalFallback(t *testing.T) {
	project := webhookTestProject()
	project.WebhookSecret = "legacy/project:webhook_secret" // project tier present

	withGlobalWebhookSecret(t, "global-secret-fake") // tier 3 also configured

	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
	}
	handler := NewWebhookHandler(mockStore, nil)
	handler.SetTokenResolver(func(ctx context.Context, ref string) (string, error) {
		if ref == "legacy/project:webhook_secret" {
			return "project-secret-fake", nil
		}
		return "", fmt.Errorf("unexpected secret ref: %s", ref)
	})

	// The incoming request is only signed with the global secret — the
	// project tier's own secret does not validate it.
	mockVCS := &MockVCSClient{
		ValidateWebhookFunc: func(r *http.Request, secret string) error {
			if secret == "global-secret-fake" {
				return nil
			}
			return fmt.Errorf("signature mismatch")
		},
	}

	w, _ := rotationTestPRWebhook(t, handler, mockVCS)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "global secret must not be tried once a project-tier secret is configured")
	assert.Empty(t, mockStore.CreateJobCalls)
}

// TestWebhookHandler_TierExclusivity_OrgSecretBlocksGlobalFallback mirrors
// the project-tier test above one tier up: when no project-tier secret is
// configured but the project owner's org-level secret is, the global/env
// fallback must not be consulted either.
func TestWebhookHandler_TierExclusivity_OrgSecretBlocksGlobalFallback(t *testing.T) {
	project := webhookTestProject()
	project.WebhookSecret = "" // no project tier
	ownerID := "owner-1"
	project.UserID = &ownerID

	withGlobalWebhookSecret(t, "global-secret-fake")

	owner := &models.User{
		UserID:         ownerID,
		WebhookSecrets: models.JSONB{"github": "org/owner:webhook_secret"},
	}

	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
		GetUserByIDFunc: func(ctx context.Context, userID string) (*models.User, error) {
			if userID == ownerID {
				return owner, nil
			}
			return nil, fmt.Errorf("unexpected user id: %s", userID)
		},
	}
	handler := NewWebhookHandler(mockStore, nil)
	handler.SetTokenResolver(func(ctx context.Context, ref string) (string, error) {
		if ref == "org/owner:webhook_secret" {
			return "org-secret-fake", nil
		}
		return "", fmt.Errorf("unexpected secret ref: %s", ref)
	})

	mockVCS := &MockVCSClient{
		ValidateWebhookFunc: func(r *http.Request, secret string) error {
			if secret == "global-secret-fake" {
				return nil
			}
			return fmt.Errorf("signature mismatch")
		},
	}

	w, _ := rotationTestPRWebhook(t, handler, mockVCS)

	assert.Equal(t, http.StatusUnauthorized, w.Code, "global secret must not be tried once an org-tier secret is configured")
	assert.Empty(t, mockStore.CreateJobCalls)
}

// TestWebhookHandler_TierExclusivity_NoProjectTier_OrgTierWorks verifies the
// org tier is actually reachable (not just exclusionary) when the project
// tier is empty: a valid signature against the org-level secret succeeds.
func TestWebhookHandler_TierExclusivity_NoProjectTier_OrgTierWorks(t *testing.T) {
	project := webhookTestProject()
	project.WebhookSecret = ""
	ownerID := "owner-1"
	project.UserID = &ownerID

	owner := &models.User{
		UserID:         ownerID,
		WebhookSecrets: models.JSONB{"github": "org/owner:webhook_secret"},
	}

	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
		GetUserByIDFunc: func(ctx context.Context, userID string) (*models.User, error) {
			if userID == ownerID {
				return owner, nil
			}
			return nil, fmt.Errorf("unexpected user id: %s", userID)
		},
	}
	handler := NewWebhookHandler(mockStore, nil)
	handler.SetTokenResolver(func(ctx context.Context, ref string) (string, error) {
		if ref == "org/owner:webhook_secret" {
			return "org-secret-fake", nil
		}
		return "", fmt.Errorf("unexpected secret ref: %s", ref)
	})

	mockVCS := &MockVCSClient{
		ValidateWebhookFunc: func(r *http.Request, secret string) error {
			if secret == "org-secret-fake" {
				return nil
			}
			return fmt.Errorf("signature mismatch")
		},
	}

	w, _ := rotationTestPRWebhook(t, handler, mockVCS)

	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, mockStore.CreateJobCalls, 1)
}

// TestWebhookHandler_TierExclusivity_NothingConfigured_EnvFallbackWorks
// verifies the global/env tier is reachable when no project- or org-tier
// secret is configured at all.
func TestWebhookHandler_TierExclusivity_NothingConfigured_EnvFallbackWorks(t *testing.T) {
	project := webhookTestProject()
	project.WebhookSecret = "" // no project tier, no owner configured

	withGlobalWebhookSecret(t, "global-secret-fake")

	mockStore := &WebhookMockStore{
		GetProjectByRepoURLFunc: func(ctx context.Context, repoURL string) (*models.Project, error) {
			return project, nil
		},
	}
	handler := NewWebhookHandler(mockStore, nil)
	handler.SetTokenResolver(func(ctx context.Context, ref string) (string, error) {
		return "", fmt.Errorf("unexpected secret ref: %s", ref)
	})

	mockVCS := &MockVCSClient{
		ValidateWebhookFunc: func(r *http.Request, secret string) error {
			if secret == "global-secret-fake" {
				return nil
			}
			return fmt.Errorf("signature mismatch")
		},
	}

	w, _ := rotationTestPRWebhook(t, handler, mockVCS)

	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, mockStore.CreateJobCalls, 1)
}
