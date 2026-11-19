package uiapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// tokenAuthzFixture is the world of TestSessionAndTokenCallers: one
// organization with an admin, a global admin, and one of each resource the
// operations under test read or change.
type tokenAuthzFixture struct {
	deps                                    *Deps
	st                                      *fakeStore
	projectID, groupID, poolID, globalPool  string
	workerID, enrollmentID, queueID, roleID string
	webhookID, vcsID, grantID               string
}

func newTokenAuthzFixtureFor(t *testing.T, tc tokenAuthzCase) *tokenAuthzFixture {
	t.Helper()
	f := newTokenAuthzFixture(t)
	if tc.wrap != nil {
		tc.wrap(f)
	}
	return f
}

func newTokenAuthzFixture(t *testing.T) *tokenAuthzFixture {
	t.Helper()
	withAuthMode(t, config.UIAuthModeLocalRP)
	deps, st := newTestDeps(t)
	ctx := context.Background()
	requireOK(t, st.CreateOrganization(ctx, &models.Organization{OrgID: "org-1", Name: "acme"}))
	requireOK(t, st.CreateOrganization(ctx, &models.Organization{OrgID: "org-2", Name: "other"}))
	st.putUser(models.User{UserID: "admin-1"})
	st.putUser(models.User{UserID: "gadmin-1"})
	st.putUser(models.User{UserID: "member-1"})
	st.putUser(models.User{UserID: "nobody-1"})
	seedOrgAdmin(st, "admin-1", "org-1")
	seedOrgAdmin(st, "admin-1", "org-2")
	seedGlobalAdmin(st, "gadmin-1")
	f := &tokenAuthzFixture{deps: deps, st: st}
	f.projectID = st.putProject(models.Project{UserID: strPtr("org-1"), OrgID: "org-1", Name: "p"}).ProjectID
	group := &models.Group{OrgID: "org-1", Name: "g"}
	requireOK(t, st.CreateGroup(ctx, group))
	f.groupID = group.GroupID
	requireOK(t, st.AddGroupMember(ctx, group.GroupID, "member-1"))
	f.poolID = st.putPool(models.WorkerPool{OrgID: strPtr("org-1"), Name: "pool"}).PoolID
	f.globalPool = st.putPool(models.WorkerPool{Name: "global"}).PoolID
	f.workerID = st.putWorker(models.Worker{WorkerID: "w-1", PoolID: f.poolID, WorkerKey: "k", OS: "linux", Arch: "amd64"}).WorkerID
	enrollment, err := st.CreatePoolEnrollmentToken(ctx, f.poolID, "e", []byte("hash"))
	requireOK(t, err)
	f.enrollmentID = enrollment.TokenID
	f.queueID = st.putQueue(models.Queue{DisplayName: "q"}).QueueID
	orgID := "org-1"
	role := &models.RoleAssignment{PrincipalType: models.PrincipalTypeUser, PrincipalID: "member-1",
		ScopeType: models.ScopeTypeOrg, ScopeID: &orgID, Role: models.RoleMember}
	requireOK(t, st.CreateRoleAssignment(ctx, role))
	f.roleID = role.AssignmentID
	webhook := &models.ProjectWebhookSecret{ProjectID: f.projectID, Provider: "github", Name: "w", SecretRef: "x:w", IsActive: true}
	requireOK(t, st.CreateProjectWebhookSecret(ctx, webhook))
	f.webhookID = webhook.ID
	vcs := &models.ProjectVCSCredential{ProjectID: f.projectID, Provider: "github", Name: "v", SecretRef: "x:v", IsActive: true}
	requireOK(t, st.CreateProjectVCSCredential(ctx, vcs))
	f.vcsID = vcs.ID
	grant := &models.SecretGrant{UserID: "org-1", Name: "grant", SecretPathMatch: "exact", SecretPathPattern: "a/b"}
	requireOK(t, st.CreateSecretGrant(ctx, grant))
	f.grantID = grant.GrantID
	return f
}

// userToken stores a user-delegated API token of userID and returns its
// value. orgs nil means every organization.
func (f *tokenAuthzFixture) userToken(userID string, orgs []string, capabilities ...string) context.Context {
	value := "token-" + userID
	for _, c := range capabilities {
		value += "-" + c
	}
	f.st.apiTokens[value] = models.APIToken{TokenID: value + "-id", SubjectType: "user_token", UserID: userID,
		AllOrganizations: orgs == nil, OrganizationIDs: orgs, Capabilities: capabilities}
	return WithAuthToken(context.Background(), value)
}

// fullToken stores a user-delegated token of userID issued with all
// capabilities and all organizations: it has the full authority of its user.
func (f *tokenAuthzFixture) fullToken(userID string) context.Context {
	value := "full-token-" + userID
	f.st.apiTokens[value] = models.APIToken{TokenID: value + "-id", SubjectType: "user_token", UserID: userID,
		AllOrganizations: true, AllCapabilities: true}
	return WithAuthToken(context.Background(), value)
}

// tokenAuthzCase is one operation that accepts a session and a token.
type tokenAuthzCase struct {
	name string
	// global: the operation needs a global admin (a token for every
	// organization); otherwise an admin of org-1.
	global bool
	// capability is a token capability that allows the operation.
	capability string
	call       func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error
	// tokenOrgs are the organizations of an allowed org token (default
	// org-1).
	tokenOrgs []string
	// tokenCapabilities are the capabilities of the allowed token (default
	// capability alone); withoutCapabilities those of the refused token
	// (default jobs:read).
	tokenCapabilities   []string
	withoutCapabilities []string
	// wrap replaces the store of the fixture with a fake that has the
	// store of the operation.
	wrap func(f *tokenAuthzFixture)
}

func tokenAuthzCases() []tokenAuthzCase {
	org := "org-1"
	scopeOrg := models.ScopeTypeOrg
	return []tokenAuthzCase{
		// Workers: a read needs workers:read, a change workers:manage.
		{"list-workers", false, tokencaps.WorkersRead, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListWorkers(ctx, csilapi.ListWorkersRequest{PoolId: &f.poolID})
			return err
		}, nil, nil, nil, nil},
		{"list-workers all pools", true, tokencaps.WorkersRead, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListWorkers(ctx, csilapi.ListWorkersRequest{})
			return err
		}, nil, nil, nil, nil},
		{"set-worker-status", false, tokencaps.WorkersManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.SetWorkerStatus(ctx, csilapi.SetWorkerStatusRequest{WorkerId: f.workerID, Status: models.WorkerStatusDisabled})
			return err
		}, nil, nil, nil, nil},
		{"drain-worker", false, tokencaps.WorkersManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.DrainWorker(ctx, csilapi.DrainWorkerRequest{WorkerId: f.workerID})
			return err
		}, nil, nil, nil, nil},
		{"list-pools", false, tokencaps.WorkersRead, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListPools(ctx, csilapi.ListPoolsRequest{OrgId: &org})
			return err
		}, nil, nil, nil, nil},
		{"create-pool", false, tokencaps.WorkersManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.CreatePool(ctx, csilapi.CreatePoolRequest{OrgId: &org, Name: "new"})
			return err
		}, nil, nil, nil, nil},
		{"create-pool global", true, tokencaps.WorkersManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.CreatePool(ctx, csilapi.CreatePoolRequest{Name: "new"})
			return err
		}, nil, nil, nil, nil},
		{"update-pool", false, tokencaps.WorkersManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.UpdatePool(ctx, csilapi.UpdatePoolRequest{PoolId: f.poolID, Name: strPtr("renamed")})
			return err
		}, nil, nil, nil, nil},
		{"delete-pool", false, tokencaps.WorkersManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.DeletePool(ctx, csilapi.DeletePoolRequest{PoolId: f.poolID})
			return err
		}, nil, nil, nil, nil},
		{"create-enrollment-token", false, tokencaps.WorkersManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.CreateEnrollmentToken(ctx, csilapi.CreateEnrollmentTokenRequest{PoolId: f.poolID})
			return err
		}, nil, nil, nil, nil},
		{"list-enrollment-tokens", false, tokencaps.WorkersManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListEnrollmentTokens(ctx, csilapi.ListEnrollmentTokensRequest{PoolId: f.poolID})
			return err
		}, nil, nil, nil, nil},
		{"deactivate-enrollment-token", false, tokencaps.WorkersManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.DeactivateEnrollmentToken(ctx, csilapi.DeactivateEnrollmentTokenRequest{TokenId: f.enrollmentID, ConfirmPermanent: boolPtr(true)})
			return err
		}, nil, nil, nil, nil},
		{"list-queues", true, tokencaps.WorkersRead, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListQueues(ctx, csilapi.ListQueuesRequest{})
			return err
		}, nil, nil, nil, nil},
		{"rename-queue", true, tokencaps.WorkersManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.RenameQueue(ctx, csilapi.RenameQueueRequest{QueueId: f.queueID, DisplayName: "renamed"})
			return err
		}, nil, nil, nil, nil},
		{"delete-queue", true, tokencaps.WorkersManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.DeleteQueue(ctx, csilapi.DeleteQueueRequest{QueueId: f.queueID})
			return err
		}, nil, nil, nil, nil},
		// Groups, users, roles: organizations:read for a read,
		// organizations:manage for a change.
		{"list-groups", false, tokencaps.OrganizationsRead, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListGroups(ctx, csilapi.ListGroupsRequest{OrgId: org})
			return err
		}, nil, nil, nil, nil},
		{"create-group", false, tokencaps.OrganizationsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.CreateGroup(ctx, csilapi.CreateGroupRequest{OrgId: org, Name: "new"})
			return err
		}, nil, nil, nil, nil},
		{"update-group", false, tokencaps.OrganizationsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.UpdateGroup(ctx, csilapi.UpdateGroupRequest{GroupId: f.groupID, Name: strPtr("renamed")})
			return err
		}, nil, nil, nil, nil},
		{"delete-group", false, tokencaps.OrganizationsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.DeleteGroup(ctx, csilapi.DeleteGroupRequest{GroupId: f.groupID})
			return err
		}, nil, nil, nil, nil},
		{"add-group-member", false, tokencaps.OrganizationsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.AddGroupMember(ctx, csilapi.AddGroupMemberRequest{GroupId: f.groupID, UserId: "admin-1"})
			return err
		}, nil, nil, nil, nil},
		{"remove-group-member", false, tokencaps.OrganizationsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.RemoveGroupMember(ctx, csilapi.RemoveGroupMemberRequest{GroupId: f.groupID, UserId: "member-1"})
			return err
		}, nil, nil, nil, nil},
		{"list-group-members", false, tokencaps.OrganizationsRead, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListGroupMembers(ctx, csilapi.ListGroupMembersRequest{GroupId: f.groupID})
			return err
		}, nil, nil, nil, nil},
		{"list-users", false, tokencaps.OrganizationsRead, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListUsers(ctx, csilapi.ListUsersRequest{OrgId: org})
			return err
		}, nil, nil, nil, nil},
		{"set-group-priority-shift", false, tokencaps.PoliciesManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.SetGroupPriorityShift(ctx, csilapi.SetGroupPriorityShiftRequest{GroupId: f.groupID, Shift: 1})
			return err
		}, nil, nil, nil, nil},
		{"list-role-assignments", false, tokencaps.OrganizationsRead, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListRoleAssignments(ctx, csilapi.ListRoleAssignmentsRequest{ScopeType: &scopeOrg, ScopeId: &org})
			return err
		}, nil, nil, nil, nil},
		{"assign-role", false, tokencaps.OrganizationsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.AssignRole(ctx, csilapi.AssignRoleRequest{PrincipalType: models.PrincipalTypeUser, PrincipalId: "member-1",
				ScopeType: models.ScopeTypeOrg, ScopeId: &org, Role: models.RoleAdmin})
			return err
		}, nil, nil, nil, nil},
		{"revoke-role", false, tokencaps.OrganizationsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.RevokeRole(ctx, csilapi.RevokeRoleRequest{AssignmentId: f.roleID})
			return err
		}, nil, nil, nil, nil},
		// Projects.
		{"create-project", false, tokencaps.ProjectsCreate, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.CreateProject(ctx, csilapi.CreateProjectRequest{OrgId: org, Name: "new", RepoUrl: "https://example.invalid/r"})
			return err
		}, nil, nil, nil, nil},
		{"update-project", false, tokencaps.ProjectsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.UpdateProject(ctx, csilapi.UpdateProjectRequest{ProjectId: f.projectID, Description: strPtr("d")})
			return err
		}, nil, nil, nil, nil},
		{"delete-project", false, tokencaps.ProjectsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.DeleteProject(ctx, csilapi.DeleteProjectRequest{ProjectId: f.projectID})
			return err
		}, nil, nil, nil, nil},
		// Organizations and shed levels.
		{"delete-org", false, tokencaps.OrganizationsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.DeleteOrg(ctx, csilapi.DeleteOrgRequest{Name: "acme", Replacement: "other", Confirm: true})
			return err
		}, []string{"org-1", "org-2"}, nil, nil, nil},
		{"get-shed-state", false, tokencaps.OrganizationsRead, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.GetShedState(ctx, csilapi.GetShedStateRequest{OrgId: &org})
			return err
		}, nil, nil, nil, nil},
		{"set-shed-level", false, tokencaps.OrganizationsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.SetShedLevel(ctx, csilapi.SetShedLevelRequest{OrgId: &org, Level: models.ShedLevelDegraded})
			return err
		}, nil, nil, nil, nil},
		{"set-shed-level global", true, tokencaps.OrganizationsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.SetShedLevel(ctx, csilapi.SetShedLevelRequest{Level: models.ShedLevelDegraded})
			return err
		}, nil, nil, nil, nil},
		{"set-shed-actions", false, tokencaps.OrganizationsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.SetShedActions(ctx, csilapi.SetShedActionsRequest{OrgId: org, Reset: boolPtr(true)})
			return err
		}, nil, nil, nil, nil},
		// Webhook secrets and VCS credentials: secrets:manage.
		{"list-webhook-secrets", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListWebhookSecrets(ctx, csilapi.ListWebhookSecretsRequest{ProjectId: f.projectID})
			return err
		}, nil, nil, nil, nil},
		{"add-webhook-secret", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.AddWebhookSecret(ctx, csilapi.AddWebhookSecretRequest{ProjectId: f.projectID, Provider: "github", Name: "new", Value: "v"})
			return err
		}, nil, nil, nil, nil},
		{"deactivate-webhook-secret", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.DeactivateWebhookSecret(ctx, csilapi.DeactivateWebhookSecretRequest{Id: f.webhookID})
			return err
		}, nil, nil, nil, nil},
		{"delete-webhook-secret", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.DeleteWebhookSecret(ctx, csilapi.DeleteWebhookSecretRequest{Id: f.webhookID})
			return err
		}, nil, nil, nil, nil},
		{"list-vcs-credentials", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListVcsCredentials(ctx, csilapi.ListVcsCredentialsRequest{ProjectId: f.projectID})
			return err
		}, nil, nil, nil, nil},
		{"add-vcs-credential", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.AddVcsCredential(ctx, csilapi.AddVcsCredentialRequest{ProjectId: f.projectID, Provider: "github", Name: "new", Value: "v"})
			return err
		}, nil, nil, nil, nil},
		{"deactivate-vcs-credential", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.DeactivateVcsCredential(ctx, csilapi.DeactivateVcsCredentialRequest{Id: f.vcsID})
			return err
		}, nil, nil, nil, nil},
		{"delete-vcs-credential", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.DeleteVcsCredential(ctx, csilapi.DeleteVcsCredentialRequest{Id: f.vcsID})
			return err
		}, nil, nil, nil, nil},
		// Secrets and secret grants: secrets:manage.
		{"set-secret", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.SetSecret(ctx, csilapi.SetSecretRequest{OrgId: org, Path: "a/b", Key: "k", Value: "v"})
			return err
		}, nil, nil, nil, nil},
		{"delete-secret", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.DeleteSecret(ctx, csilapi.DeleteSecretRequest{OrgId: org, Path: "a/b", Key: "k"})
			return err
		}, nil, nil, nil, nil},
		{"list-secret-paths", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListSecretPaths(ctx, csilapi.ListSecretPathsRequest{OrgId: org})
			return err
		}, nil, nil, nil, nil},
		{"list-secret-grants", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListSecretGrants(ctx, csilapi.ListSecretGrantsRequest{OrgId: org})
			return err
		}, nil, nil, nil, nil},
		{"create-secret-grant", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.CreateSecretGrant(ctx, csilapi.CreateSecretGrantRequest{OrgId: org, Name: "new", SecretPathMatch: "exact", SecretPathPattern: "a/c"})
			return err
		}, nil, nil, nil, nil},
		{"update-secret-grant", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.UpdateSecretGrant(ctx, csilapi.UpdateSecretGrantRequest{GrantId: f.grantID, Description: strPtr("d")})
			return err
		}, nil, nil, nil, nil},
		{"delete-secret-grant", false, tokencaps.SecretsManage, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.DeleteSecretGrant(ctx, csilapi.DeleteSecretGrantRequest{GrantId: f.grantID})
			return err
		}, nil, nil, nil, nil},
		// Global admin surface: organizations:manage and every organization.
		{"get-global-settings", true, tokencaps.OrganizationsRead, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.GetGlobalSettings(ctx, csilapi.GetGlobalSettingsRequest{})
			return err
		}, nil, nil, nil, nil},
		{"list-trusted-identities", true, tokencaps.OrganizationsRead, func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
			_, err := ui.ListTrustedIdentities(ctx, csilapi.ListTrustedIdentitiesRequest{})
			return err
		}, nil, nil, nil, nil},
	}
}

func isPermissionCode(err error) bool {
	se, ok := err.(*ServiceErr)
	return ok && (se.Code == "unauthorized" || se.Code == "forbidden" || se.Code == "permission_denied")
}

// requireRefused asserts a refusal: "forbidden", or "permission_denied" for
// the operations that use that code (ref classes, priority lanes).
func requireRefused(t *testing.T, err error) {
	t.Helper()
	se, ok := err.(*ServiceErr)
	if !ok || (se.Code != "forbidden" && se.Code != "permission_denied") {
		t.Fatalf("err = %v, want forbidden or permission_denied", err)
	}
}

// TestSessionAndTokenCallers checks each operation that used to need a
// browser session. A session and an API token are equal callers: the API
// enforces the permission. For a token that is the role of its owner
// intersected with the capability and the organizations of the token.
func TestSessionAndTokenCallers(t *testing.T) {
	for _, tc := range append(tokenAuthzCases(), tokenGapCases()...) {
		t.Run(tc.name, func(t *testing.T) {
			admin := "admin-1"
			var orgs []string
			if tc.global {
				admin = "gadmin-1"
			} else {
				orgs = []string{"org-1"}
				if tc.tokenOrgs != nil {
					orgs = tc.tokenOrgs
				}
			}

			t.Run("session allowed", func(t *testing.T) {
				f := newTokenAuthzFixtureFor(t, tc)
				err := tc.call(NewUiService(f.deps), mintSessionCtx(t, f.deps, admin), f)
				if isPermissionCode(err) {
					t.Fatalf("session refused: %v", err)
				}
			})
			t.Run("token with capability allowed", func(t *testing.T) {
				f := newTokenAuthzFixtureFor(t, tc)
				allowed := tc.tokenCapabilities
				if allowed == nil {
					allowed = []string{tc.capability}
				}
				err := tc.call(NewUiService(f.deps), f.userToken(admin, orgs, allowed...), f)
				if isPermissionCode(err) {
					t.Fatalf("token with %s refused: %v", tc.capability, err)
				}
			})
			t.Run("all-capabilities token for all orgs allowed like the session", func(t *testing.T) {
				f := newTokenAuthzFixtureFor(t, tc)
				err := tc.call(NewUiService(f.deps), f.fullToken(admin), f)
				if isPermissionCode(err) {
					t.Fatalf("all-capabilities token refused: %v", err)
				}
			})
			t.Run("token without capability refused", func(t *testing.T) {
				f := newTokenAuthzFixtureFor(t, tc)
				without := tc.withoutCapabilities
				if without == nil {
					without = []string{tokencaps.JobsRead}
				}
				err := tc.call(NewUiService(f.deps), f.userToken(admin, orgs, without...), f)
				requireRefused(t, err)
			})
			t.Run("token of a user without the role refused", func(t *testing.T) {
				f := newTokenAuthzFixtureFor(t, tc)
				err := tc.call(NewUiService(f.deps), f.userToken("nobody-1", orgs, tc.capability), f)
				requireRefused(t, err)
			})
			if !tc.global {
				t.Run("token for another organization refused", func(t *testing.T) {
					f := newTokenAuthzFixtureFor(t, tc)
					err := tc.call(NewUiService(f.deps), f.userToken(admin, []string{"org-2"}, append([]string{tc.capability}, tc.tokenCapabilities...)...), f)
					requireRefused(t, err)
				})
			} else {
				t.Run("token for one organization refused", func(t *testing.T) {
					f := newTokenAuthzFixtureFor(t, tc)
					err := tc.call(NewUiService(f.deps), f.userToken(admin, []string{"org-1"}, tc.capability), f)
					requireRefused(t, err)
				})
			}
		})
	}
}

// TestListWorkers_ReadNeedsNoControl: a token with only workers:read sees the
// workers, and cannot change them.
func TestListWorkers_ReadNeedsNoControl(t *testing.T) {
	f := newTokenAuthzFixture(t)
	ui := NewUiService(f.deps)
	ctx := f.userToken("admin-1", []string{"org-1"}, tokencaps.WorkersRead)
	resp, err := ui.ListWorkers(ctx, csilapi.ListWorkersRequest{PoolId: &f.poolID})
	requireOK(t, err)
	if len(resp.Workers) != 1 {
		t.Fatalf("got %d workers, want 1", len(resp.Workers))
	}
	_, err = ui.DrainWorker(ctx, csilapi.DrainWorkerRequest{WorkerId: f.workerID})
	requireCode(t, err, "forbidden")
	_, err = ui.CreateEnrollmentToken(ctx, csilapi.CreateEnrollmentTokenRequest{PoolId: f.poolID})
	requireCode(t, err, "forbidden")
	_, err = ui.ListEnrollmentTokens(ctx, csilapi.ListEnrollmentTokensRequest{PoolId: f.poolID})
	requireCode(t, err, "forbidden")
}

// TestListEnrollmentTokens_TokenGetsMetadataOnly: a token caller gets the
// same metadata as a session, never a token value or hash.
func TestListEnrollmentTokens_TokenGetsMetadataOnly(t *testing.T) {
	f := newTokenAuthzFixture(t)
	resp, err := NewUiService(f.deps).ListEnrollmentTokens(f.userToken("admin-1", []string{"org-1"}, tokencaps.WorkersManage),
		csilapi.ListEnrollmentTokensRequest{PoolId: f.poolID})
	requireOK(t, err)
	if len(resp.Tokens) != 1 || resp.Tokens[0].TokenId != f.enrollmentID {
		t.Fatalf("tokens = %+v, want the one enrollment token", resp.Tokens)
	}
	// EnrollmentTokenSummary has no value or hash field: the CSIL type is
	// metadata only. This compile-time list fails when a field is added.
	_ = csilapi.EnrollmentTokenSummary{TokenId: "", PoolId: "", Name: nil, IsActive: false, LastUsedAt: nil,
		CreatedAt: "", DeactivatedAt: nil, ActiveWorkerCount: nil}
}

// TestRoleAssignment_TokenWithoutUserRecordsNoCreator: an instance token (no
// user) of a global admin capability can assign a role; the row then has no
// creating user.
func TestRoleAssignment_TokenWithoutUserRecordsNoCreator(t *testing.T) {
	f := newTokenAuthzFixture(t)
	f.st.apiTokens["instance"] = models.APIToken{TokenID: "instance-id", SubjectType: "instance_token", AllOrganizations: true,
		Capabilities: []string{tokencaps.OrganizationsManage}}
	org := "org-1"
	resp, err := NewUiService(f.deps).AssignRole(WithAuthToken(context.Background(), "instance"), csilapi.AssignRoleRequest{
		PrincipalType: models.PrincipalTypeUser, PrincipalId: "member-1", ScopeType: models.ScopeTypeOrg, ScopeId: &org, Role: models.RoleAdmin})
	requireOK(t, err)
	if resp.Assignment.CreatedBy != nil {
		t.Fatalf("created_by = %q, want none for a token with no user", *resp.Assignment.CreatedBy)
	}
}
