package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newPrincipalOrg(t *testing.T, ctx context.Context, name string) *models.Organization {
	t.Helper()
	org := &models.Organization{Name: name, DisplayName: name}
	require.NoError(t, postgres_store.PostgresStore.CreateOrganization(ctx, org))
	return org
}

func TestPrincipalStore(t *testing.T) {
	ps := postgres_store.PostgresStore

	t.Run("a VCS login becomes one principal", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			first, created, err := ps.EnsureVCSPrincipal(ctx, "github", "Octo-Cat")
			require.NoError(t, err)
			assert.True(t, created)
			assert.Equal(t, models.PrincipalKindHuman, first.Kind)
			again, created, err := ps.EnsureVCSPrincipal(ctx, "GitHub", "octo-cat")
			require.NoError(t, err)
			assert.False(t, created)
			assert.Equal(t, first.PrincipalID, again.PrincipalID)
			resolved, err := ps.ResolvePrincipalIdentity(ctx, models.IdentityTypeVCS, "GITHUB/OCTO-CAT")
			require.NoError(t, err)
			assert.Equal(t, first.PrincipalID, resolved.PrincipalID)
		})
	})

	t.Run("service principals are unique in an org", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			org := newPrincipalOrg(t, ctx, "svc-org")
			principal := &models.Principal{OrgID: &org.OrgID, Name: "deployer"}
			require.NoError(t, ps.CreateServicePrincipal(ctx, principal))
			assert.Equal(t, models.PrincipalKindService, principal.Kind)
			assert.ErrorIs(t, ps.CreateServicePrincipal(ctx, &models.Principal{OrgID: &org.OrgID, Name: "deployer"}), store.ErrAlreadyExists)
			assert.ErrorIs(t, ps.CreateServicePrincipal(ctx, &models.Principal{OrgID: &org.OrgID, Name: "Bad Name"}), store.ErrInvalidInput)
			found, err := ps.GetServicePrincipalByName(ctx, org.OrgID, "deployer")
			require.NoError(t, err)
			assert.Equal(t, principal.PrincipalID, found.PrincipalID)
			listed, err := ps.ListPrincipals(ctx, org.OrgID, models.PrincipalKindService)
			require.NoError(t, err)
			require.Len(t, listed, 1)
			require.NoError(t, ps.SetPrincipalStatus(ctx, principal.PrincipalID, models.PrincipalStatusDisabled))
			disabled, err := ps.GetPrincipal(ctx, principal.PrincipalID)
			require.NoError(t, err)
			assert.False(t, disabled.IsActive())
			assert.ErrorIs(t, ps.SetPrincipalStatus(ctx, models.SystemPrincipalID, models.PrincipalStatusDisabled), store.ErrForbidden)
		})
	})

	t.Run("linking merges a webhook principal and refuses a claimed identity", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			du := &DataUtils{db: tx}
			user, err := du.CreateUser(DataSetup{"Username": "merge-user", "Email": "m@example.com"})
			require.NoError(t, err)
			seen, _, err := ps.EnsureVCSPrincipal(ctx, "github", "merge-login")
			require.NoError(t, err)
			require.NoError(t, ps.LinkPrincipalIdentity(ctx, &models.PrincipalIdentity{IdentityType: models.IdentityTypeVCS,
				IdentityValue: "github/merge-login", PrincipalID: user.UserID}))
			resolved, err := ps.ResolvePrincipalIdentity(ctx, models.IdentityTypeVCS, "github/merge-login")
			require.NoError(t, err)
			assert.Equal(t, user.UserID, resolved.PrincipalID)
			// The old principal resolves to the user now.
			followed, err := ps.GetPrincipal(ctx, seen.PrincipalID)
			require.NoError(t, err)
			assert.Equal(t, user.UserID, followed.PrincipalID)

			org := newPrincipalOrg(t, ctx, "link-org")
			service := &models.Principal{OrgID: &org.OrgID, Name: "bot"}
			require.NoError(t, ps.CreateServicePrincipal(ctx, service))
			err = ps.LinkPrincipalIdentity(ctx, &models.PrincipalIdentity{IdentityType: models.IdentityTypeVCS,
				IdentityValue: "github/merge-login", PrincipalID: service.PrincipalID})
			assert.ErrorIs(t, err, store.ErrConflict)
			assert.ErrorIs(t, ps.LinkPrincipalIdentity(ctx, &models.PrincipalIdentity{IdentityType: models.IdentityTypeUser,
				IdentityValue: user.UserID, PrincipalID: service.PrincipalID}), store.ErrInvalidInput)
			assert.ErrorIs(t, ps.UnlinkPrincipalIdentity(ctx, models.IdentityTypeUser, user.UserID), store.ErrInvalidInput)
			require.NoError(t, ps.UnlinkPrincipalIdentity(ctx, models.IdentityTypeVCS, "github/merge-login"))
		})
	})

	t.Run("linking never merges a principal that has authority", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			du := &DataUtils{db: tx}
			attacker, err := du.CreateUser(DataSetup{"Username": "escalate-user", "Email": "e@example.com"})
			require.NoError(t, err)
			org := newPrincipalOrg(t, ctx, "victim-org")
			service := &models.Principal{OrgID: &org.OrgID, Name: "deployer"}
			require.NoError(t, ps.CreateServicePrincipal(ctx, service))
			require.NoError(t, ps.LinkPrincipalIdentity(ctx, &models.PrincipalIdentity{IdentityType: models.IdentityTypeVCS,
				IdentityValue: "github/victim-bot", PrincipalID: service.PrincipalID}))
			// A webhook principal in a group is not webhook-only.
			seen, _, err := ps.EnsureVCSPrincipal(ctx, "github", "grouped-login")
			require.NoError(t, err)
			group := &models.Group{OrgID: org.OrgID, Name: "release"}
			require.NoError(t, ps.CreateGroup(ctx, group))
			require.NoError(t, ps.AddGroupMemberPrincipal(ctx, group.GroupID, seen.PrincipalID))

			for _, value := range []string{"github/victim-bot", "github/grouped-login"} {
				err := ps.LinkPrincipalIdentity(ctx, &models.PrincipalIdentity{IdentityType: models.IdentityTypeVCS,
					IdentityValue: value, PrincipalID: attacker.UserID})
				assert.ErrorIs(t, err, store.ErrConflict, value)
			}
			owner, err := ps.ResolvePrincipalIdentity(ctx, models.IdentityTypeVCS, "github/victim-bot")
			require.NoError(t, err)
			assert.Equal(t, service.PrincipalID, owner.PrincipalID)
			still, err := ps.GetPrincipal(ctx, service.PrincipalID)
			require.NoError(t, err)
			assert.Equal(t, service.PrincipalID, still.PrincipalID, "the service principal is not merged")
			groups, err := ps.ListGroupsForPrincipal(ctx, attacker.UserID)
			require.NoError(t, err)
			assert.Empty(t, groups)
		})
	})

	t.Run("any principal can be a group member and hold roles", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			org := newPrincipalOrg(t, ctx, "group-org")
			group := &models.Group{OrgID: org.OrgID, Name: "deployers"}
			require.NoError(t, ps.CreateGroup(ctx, group))
			service := &models.Principal{OrgID: &org.OrgID, Name: "deployer"}
			require.NoError(t, ps.CreateServicePrincipal(ctx, service))
			require.NoError(t, ps.AddGroupMemberPrincipal(ctx, group.GroupID, service.PrincipalID))
			require.NoError(t, ps.AddGroupMemberPrincipal(ctx, group.GroupID, service.PrincipalID), "idempotent")
			du := &DataUtils{db: tx}
			user, err := du.CreateUser(DataSetup{"Username": "group-user", "Email": "g@example.com"})
			require.NoError(t, err)
			require.NoError(t, ps.AddGroupMember(ctx, group.GroupID, user.UserID))

			members, err := ps.ListGroupMemberPrincipals(ctx, group.GroupID)
			require.NoError(t, err)
			assert.Len(t, members, 2)
			users, err := ps.ListGroupMembers(ctx, group.GroupID)
			require.NoError(t, err)
			assert.Len(t, users, 1, "the user list holds only users")
			groups, err := ps.ListGroupsForPrincipal(ctx, service.PrincipalID)
			require.NoError(t, err)
			require.Len(t, groups, 1)

			orgID := org.OrgID
			require.NoError(t, ps.CreateRoleAssignment(ctx, &models.RoleAssignment{PrincipalType: models.PrincipalTypeGroup,
				PrincipalID: group.GroupID, ScopeType: models.ScopeTypeOrg, ScopeID: &orgID, Role: models.RoleAdmin}))
			assignments, err := ps.ListRoleAssignmentsForPrincipalID(ctx, service.PrincipalID, []string{group.GroupID})
			require.NoError(t, err)
			require.Len(t, assignments, 1)
			admin, err := authz.NewResolver(ps).IsOrgAdmin(ctx, authz.Identity{PrincipalID: service.PrincipalID}, org.OrgID)
			require.NoError(t, err)
			assert.True(t, admin, "a group role applies to a service principal")
			listed, err := ps.ListPrincipals(ctx, org.OrgID, models.PrincipalKindHuman)
			require.NoError(t, err)
			require.Len(t, listed, 1)
			assert.Equal(t, user.UserID, listed[0].PrincipalID)
		})
	})

	t.Run("a job created without an actor gets the system principal", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			du := &DataUtils{db: tx}
			job, err := du.CreateJob(DataSetup{})
			require.NoError(t, err)
			reloaded, err := ps.GetJobByID(ctx, job.JobID)
			require.NoError(t, err)
			require.NotNil(t, reloaded.ActorPrincipalID)
			assert.Equal(t, models.SystemPrincipalID, *reloaded.ActorPrincipalID)

			// The store reads the trigger's actor back, and a later save of
			// a struct that never had one keeps it.
			created := &models.Job{OrgID: reloaded.OrgID, Name: "fresh", JobCommand: "true"}
			require.NoError(t, ps.CreateJob(ctx, created))
			require.NotNil(t, created.ActorPrincipalID)
			assert.Equal(t, models.SystemPrincipalID, *created.ActorPrincipalID)
			stale := *created
			stale.ActorPrincipalID, stale.ActorIdentity = nil, ""
			stale.Status = "queued"
			require.NoError(t, ps.UpdateJob(ctx, &stale))
			afterSave, err := ps.GetJobByID(ctx, created.JobID)
			require.NoError(t, err)
			require.NotNil(t, afterSave.ActorPrincipalID)
			assert.Equal(t, models.SystemPrincipalID, *afterSave.ActorPrincipalID)
		})
	})

	t.Run("the resolver returns every linked identity", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			du := &DataUtils{db: tx}
			user, err := du.CreateUser(DataSetup{"Username": "resolver-user", "Email": "r@example.com"})
			require.NoError(t, err)
			require.NoError(t, ps.LinkPrincipalIdentity(ctx, &models.PrincipalIdentity{IdentityType: models.IdentityTypeLinkKeys,
				IdentityValue: "Resolver@Example.com", PrincipalID: user.UserID}))
			require.NoError(t, ps.LinkPrincipalIdentity(ctx, &models.PrincipalIdentity{IdentityType: models.IdentityTypeVCS,
				IdentityValue: "github/resolver", PrincipalID: user.UserID}))
			resolver := principals.NewResolver(ps, 0)
			resolution, err := resolver.Resolve(ctx, models.IdentityTypeVCS, "github/Resolver")
			require.NoError(t, err)
			assert.Equal(t, user.UserID, resolution.Principal.PrincipalID)
			assert.True(t, resolution.HasIdentity(models.IdentityTypeLinkKeys, "resolver@example.com"))
			assert.Equal(t, user.UserID, resolution.UserID())
			subjects := principals.Subjects(resolution, principals.SubjectContext{})
			assert.Contains(t, subjects, "identity:linkkeys:resolver@example.com")
			assert.Contains(t, subjects, "vcs_user:github/resolver")
			assert.Contains(t, subjects, "principal:"+user.UserID)
		})
	})
}

func principalRequest(t *testing.T, ctx context.Context, method, path, auth string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Buffer
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewBuffer(data)
	} else {
		reader = bytes.NewBuffer(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, path, reader)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rr := httptest.NewRecorder()
	GetTestMux().ServeHTTP(rr, req)
	return rr
}

// TestServicePrincipalAPI covers the no-login flow: the admin token creates a
// service principal, issues it a token, and gives it roles. The token's
// authority is its capabilities intersected with the principal's roles, and
// a job it submits records the principal as the actor.
func TestServicePrincipalAPI(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		admin, err := du.CreateUser(DataSetup{"Username": "sp-admin", "Email": "sp@example.com"})
		require.NoError(t, err)
		adminAuth, err := createTokenAuthHeader(ctx, tx, admin.UserID)
		require.NoError(t, err)
		org := newPrincipalOrg(t, ctx, "sp-org")

		rr := principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/sp-org/principals", adminAuth,
			map[string]string{"name": "release-bot", "display_name": "Release Bot"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var created map[string]any
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
		principalID := created["principal_id"].(string)
		assert.Contains(t, created["subjects"], "service:sp-org/release-bot")

		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/sp-org/principals/release-bot/tokens", adminAuth,
			map[string]any{"name": "ci", "capabilities": []string{tokencaps.JobsSubmit, tokencaps.JobsRead}})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var issued struct {
			Token     string `json:"token"`
			TokenInfo struct {
				TokenID string `json:"token_id"`
			} `json:"token_info"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &issued))
		botAuth := "Bearer " + issued.Token

		submit := map[string]any{"name": "bot job", "source_type": "git", "source_url": "https://github.com/test/repo.git",
			"job_command": "echo hi", "org": "sp-org"}
		// No roles yet: the token has no organization.
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/jobs", botAuth, submit)
		assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())

		// A member role gives read capabilities only.
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/sp-org/principals/release-bot/roles", adminAuth,
			map[string]string{"role": "member"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/jobs", botAuth, submit)
		assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())

		// The admin role through a group gives the issued capabilities.
		group := &models.Group{OrgID: org.OrgID, Name: "admins"}
		require.NoError(t, postgres_store.PostgresStore.CreateGroup(ctx, group))
		orgID := org.OrgID
		require.NoError(t, postgres_store.PostgresStore.CreateRoleAssignment(ctx, &models.RoleAssignment{PrincipalType: models.PrincipalTypeGroup,
			PrincipalID: group.GroupID, ScopeType: models.ScopeTypeOrg, ScopeID: &orgID, Role: models.RoleAdmin}))
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/sp-org/principals/release-bot/groups", adminAuth,
			map[string]string{"group": "admins"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/jobs", botAuth, submit)
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var job struct {
			JobID string `json:"job_id"`
			Actor struct {
				PrincipalID string `json:"principal_id"`
				Kind        string `json:"kind"`
				DisplayName string `json:"display_name"`
				Identity    string `json:"identity"`
			} `json:"actor"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &job))
		assert.Equal(t, principalID, job.Actor.PrincipalID)
		assert.Equal(t, models.PrincipalKindService, job.Actor.Kind)
		assert.Equal(t, "Release Bot", job.Actor.DisplayName)
		assert.Equal(t, "token:"+issued.TokenInfo.TokenID, job.Actor.Identity)
		stored, err := postgres_store.PostgresStore.GetJobByID(ctx, job.JobID)
		require.NoError(t, err)
		require.NotNil(t, stored.ActorPrincipalID)
		assert.Equal(t, principalID, *stored.ActorPrincipalID)

		// The token cannot manage tokens: it was not issued that capability.
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/sp-org/principals/release-bot/tokens", botAuth,
			map[string]any{"name": "escalate"})
		assert.Equal(t, http.StatusForbidden, rr.Code)

		// resolve-principal finds the principal by its token identity.
		query := "?type=token&value=" + url.QueryEscape(issued.TokenInfo.TokenID)
		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/principals/resolve"+query, adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), principalID)
		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/principals/resolve"+query, botAuth, nil)
		assert.Equal(t, http.StatusForbidden, rr.Code, "resolve without an org needs global authority")

		// A disabled principal's token stops working.
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/sp-org/principals/release-bot/disable", adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/jobs", botAuth, nil)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})
}

// TestLegacyServiceTokenKeepsAuthority checks that a token made the old way
// (no principal roles) keeps its capabilities after the migration gave it a
// service principal.
func TestLegacyServiceTokenKeepsAuthority(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		org := newPrincipalOrg(t, ctx, "legacy-org")
		value := "legacy-service-token-value"
		token := &models.APIToken{TokenHash: checkauth.HashAPIToken(value), Name: "Legacy CI", IsActive: true,
			SubjectType: "service_token", OwnerOrgID: &org.OrgID, OrganizationIDs: []string{org.OrgID},
			Capabilities: []string{tokencaps.JobsSubmit, tokencaps.JobsRead}}
		require.NoError(t, postgres_store.PostgresStore.CreateAPIToken(ctx, token))
		stored, _, err := postgres_store.PostgresStore.ValidateAPIToken(ctx, value)
		require.NoError(t, err)
		require.NotNil(t, stored.PrincipalID, "the trigger made a service principal")
		assert.False(t, stored.IntersectRoles)

		rr := principalRequest(t, ctx, http.MethodPost, "/api/v1/jobs", "Bearer "+value, map[string]any{"name": "legacy",
			"source_type": "git", "source_url": "https://github.com/test/repo.git", "job_command": "echo hi", "org": "legacy-org"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), *stored.PrincipalID)
	})
}

// TestLegacyVCSIdentityRouteNeedsGlobalAdmin: POST /api/v1/vcs-identities
// links a VCS login to a user (a human), whose subjects apply in every
// organization. An org admin of the named organization may not do it; the
// admin token may. A login of another principal is a conflict.
func TestLegacyVCSIdentityRouteNeedsGlobalAdmin(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		org := newPrincipalOrg(t, ctx, "legacy-link-org")
		orgAdmin, err := du.CreateUser(DataSetup{"Username": "legacy-org-admin", "Email": "loa@example.com"})
		require.NoError(t, err)
		orgID := org.OrgID
		require.NoError(t, postgres_store.PostgresStore.CreateRoleAssignment(ctx, &models.RoleAssignment{PrincipalType: models.PrincipalTypeUser,
			PrincipalID: orgAdmin.UserID, ScopeType: models.ScopeTypeOrg, ScopeID: &orgID, Role: models.RoleAdmin}))
		orgAdminValue := "legacy-org-admin-token"
		_, err = du.CreateAPIToken(DataSetup{"UserID": orgAdmin.UserID, "Name": "org admin", "TokenHash": checkauth.HashAPIToken(orgAdminValue),
			"IsActive": true, "SubjectType": "user_token", "AllOrganizations": true, "AllCapabilities": true})
		require.NoError(t, err)
		victim, err := du.CreateUser(DataSetup{"Username": "legacy-victim", "Email": "lv@example.com"})
		require.NoError(t, err)
		adminAuth, err := createTokenAuthHeader(ctx, tx, orgAdmin.UserID)
		require.NoError(t, err)

		body := map[string]string{"organization": "legacy-link-org", "provider": "github", "external_subject": "legacy-login", "username": "legacy-victim"}
		rr := principalRequest(t, ctx, http.MethodPost, "/api/v1/vcs-identities", "Bearer "+orgAdminValue, body)
		assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())

		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/vcs-identities", adminAuth, body)
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		owner, err := postgres_store.PostgresStore.ResolvePrincipalIdentity(ctx, models.IdentityTypeVCS, "github/legacy-login")
		require.NoError(t, err)
		assert.Equal(t, victim.UserID, owner.PrincipalID)

		// A login of a service principal is not moved to a user.
		service := &models.Principal{OrgID: &orgID, Name: "legacy-bot"}
		require.NoError(t, postgres_store.PostgresStore.CreateServicePrincipal(ctx, service))
		require.NoError(t, postgres_store.PostgresStore.LinkPrincipalIdentity(ctx, &models.PrincipalIdentity{IdentityType: models.IdentityTypeVCS,
			IdentityValue: "github/legacy-bot", PrincipalID: service.PrincipalID}))
		// The principals REST route has the same rule.
		require.NoError(t, postgres_store.PostgresStore.CreateRoleAssignment(ctx, &models.RoleAssignment{PrincipalType: models.PrincipalTypeUser,
			PrincipalID: victim.UserID, ScopeType: models.ScopeTypeOrg, ScopeID: &orgID, Role: models.RoleMember}))
		link := map[string]string{"identity_type": "vcs", "identity_value": "github/legacy-other"}
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/legacy-link-org/principals/"+victim.UserID+"/identities", "Bearer "+orgAdminValue, link)
		assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/legacy-link-org/principals/legacy-bot/identities", "Bearer "+orgAdminValue, link)
		assert.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

		// Last: the refused link aborts the test transaction.
		body["external_subject"] = "legacy-bot"
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/vcs-identities", adminAuth, body)
		assert.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	})
}
