package test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestNoLoginPrincipalTokenDefaults covers owner decision B: on a no-login
// instance (REACTORCIDE_UI_AUTH_MODE=none) a service principal token issued
// with no capability list gets all capabilities and all organizations, and
// roles do not narrow it. An explicit list keeps the normal, role-narrowed
// token. A login mode never gives the wide default.
func TestNoLoginPrincipalTokenDefaults(t *testing.T) {
	origMode := config.UIAuthMode
	defer func() { config.UIAuthMode = origMode }()

	type issuedToken struct {
		Token     string `json:"token"`
		TokenInfo struct {
			TokenID      string   `json:"token_id"`
			Capabilities []string `json:"capabilities"`
		} `json:"token_info"`
	}
	issue := func(t *testing.T, ctx context.Context, adminAuth, org, principal string, body map[string]any) issuedToken {
		t.Helper()
		rr := principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/"+org+"/principals/"+principal+"/tokens", adminAuth, body)
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var issued issuedToken
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &issued))
		return issued
	}
	setup := func(t *testing.T, ctx context.Context, tx *gorm.DB, org string) string {
		t.Helper()
		du := &DataUtils{db: tx}
		admin, err := du.CreateUser(DataSetup{"Username": org + "-admin", "Email": org + "@example.com"})
		require.NoError(t, err)
		adminAuth, err := createTokenAuthHeader(ctx, tx, admin.UserID)
		require.NoError(t, err)
		newPrincipalOrg(t, ctx, org)
		rr := principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/"+org+"/principals", adminAuth,
			map[string]string{"name": "bot"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		return adminAuth
	}
	submit := func(org string) map[string]any {
		return map[string]any{"name": "bot job", "source_type": "git", "source_url": "https://github.com/test/repo.git",
			"job_command": "echo hi", "org": org}
	}

	t.Run("no-login mode: omitted capabilities give all capabilities and all organizations", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			config.UIAuthMode = config.UIAuthModeNone
			adminAuth := setup(t, ctx, tx, "nl-org")
			other := newPrincipalOrg(t, ctx, "nl-other")

			issued := issue(t, ctx, adminAuth, "nl-org", "bot", map[string]any{"name": "all"})
			assert.Equal(t, []string{tokencaps.All}, issued.TokenInfo.Capabilities)
			stored, err := postgres_store.PostgresStore.GetAPITokenByID(ctx, issued.TokenInfo.TokenID)
			require.NoError(t, err)
			assert.Equal(t, "instance_token", stored.SubjectType)
			assert.True(t, stored.AllOrganizations)
			assert.True(t, stored.AllCapabilities)
			assert.False(t, stored.IntersectRoles)
			require.NotNil(t, stored.PrincipalID, "the token stays bound to its principal")

			botAuth := "Bearer " + issued.Token
			// No roles, and the existing checks still pass: its own org, another
			// org, and a global operation.
			rr := principalRequest(t, ctx, http.MethodPost, "/api/v1/jobs", botAuth, submit("nl-org"))
			assert.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
			rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/jobs", botAuth, submit(other.Name))
			assert.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
			rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/principals/resolve?type=token&value="+issued.TokenInfo.TokenID, botAuth, nil)
			assert.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

			// An explicit list is honoured: the normal role-narrowed token.
			narrow := issue(t, ctx, adminAuth, "nl-org", "bot", map[string]any{"name": "narrow",
				"capabilities": []string{tokencaps.JobsSubmit, tokencaps.JobsRead}})
			stored, err = postgres_store.PostgresStore.GetAPITokenByID(ctx, narrow.TokenInfo.TokenID)
			require.NoError(t, err)
			assert.Equal(t, "service_token", stored.SubjectType)
			assert.False(t, stored.AllOrganizations)
			assert.True(t, stored.IntersectRoles)
			rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/jobs", "Bearer "+narrow.Token, submit("nl-org"))
			assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())

			// A caller without all capabilities cannot issue the wide token.
			rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/nl-org/principals/bot/tokens",
				"Bearer "+narrow.Token, map[string]any{"name": "escalate"})
			assert.Equal(t, http.StatusForbidden, rr.Code)
		})
	})

	for _, mode := range []string{config.UIAuthModeLocalRP, config.UIAuthModeRP} {
		t.Run("login mode "+mode+": omitted capabilities stay org-scoped and role-narrowed", func(t *testing.T) {
			RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
				config.UIAuthMode = mode
				adminAuth := setup(t, ctx, tx, "lm-org")
				issued := issue(t, ctx, adminAuth, "lm-org", "bot", map[string]any{"name": "all"})
				stored, err := postgres_store.PostgresStore.GetAPITokenByID(ctx, issued.TokenInfo.TokenID)
				require.NoError(t, err)
				assert.Equal(t, "service_token", stored.SubjectType)
				assert.False(t, stored.AllOrganizations)
				assert.True(t, stored.AllCapabilities)
				assert.True(t, stored.IntersectRoles)
				rr := principalRequest(t, ctx, http.MethodPost, "/api/v1/jobs", "Bearer "+issued.Token, submit("lm-org"))
				assert.Equal(t, http.StatusForbidden, rr.Code, "no roles: no authority with logins enabled")
			})
		})
	}
}

// TestNoLoginInstanceAdminApprovals covers owner decision 1: on a no-login
// instance only, a wide service principal token gives pull request commands
// (REST POST /api/v1/approvals) as the instance administrator. With logins,
// or when the login history guard refused mode none, it gets 403.
func TestNoLoginInstanceAdminApprovals(t *testing.T) {
	origMode, origGuard := config.UIAuthMode, config.NoLoginGuardPassed()
	defer func() {
		config.UIAuthMode = origMode
		config.SetNoLoginGuardPassed(origGuard)
	}()

	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		config.UIAuthMode = config.UIAuthModeNone
		config.SetNoLoginGuardPassed(true)
		du := &DataUtils{db: tx}
		admin, err := du.CreateUser(DataSetup{"Username": "nla-admin", "Email": "nla@example.com"})
		require.NoError(t, err)
		adminAuth, err := createTokenAuthHeader(ctx, tx, admin.UserID)
		require.NoError(t, err)
		org := newPrincipalOrg(t, ctx, "nla-org")
		require.NoError(t, postgres_store.PostgresStore.CreateProject(ctx, &models.Project{Name: "nla-app",
			RepoURL: "github.com/acme/nla-app", OrgID: org.OrgID}))
		rr := principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/nla-org/principals", adminAuth,
			map[string]string{"name": "bot"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/nla-org/principals/bot/tokens", adminAuth,
			map[string]any{"name": "wide"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var issued struct {
			Token string `json:"token"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &issued))
		wide := "Bearer " + issued.Token
		body := map[string]any{"organization": "nla-org", "project": "nla-app", "pr_number": 3, "command": "approve-ci"}

		// The command passes the token check. It then fails because the test
		// coordinator has no VCS client to read the pull request.
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/approvals", wide, body)
		assert.NotEqual(t, http.StatusForbidden, rr.Code, rr.Body.String())

		config.SetNoLoginGuardPassed(false)
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/approvals", wide, body)
		assert.Equal(t, http.StatusForbidden, rr.Code, "the guard refused mode none: no instance administrator")

		config.SetNoLoginGuardPassed(true)
		config.UIAuthMode = config.UIAuthModeRP
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/approvals", wide, body)
		assert.Equal(t, http.StatusForbidden, rr.Code, "a login mode: the wide token has no person")
	})
}
