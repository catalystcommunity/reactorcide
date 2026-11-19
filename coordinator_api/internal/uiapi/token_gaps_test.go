package uiapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// The fake stores of these cases wrap the fixture store, so the roles,
// tokens, and organizations of the fixture apply.

func withAliasStore(f *tokenAuthzFixture) {
	aliases := newAliasFake(f.st)
	aliases.aliases["a-1"] = &models.ImageAlias{AliasID: "a-1", OrgID: "org-1", Name: "win", Kind: "container",
		OS: "linux", Arch: "amd64", ActiveRef: "registry.example/win@sha256:" + sha64("a"), ActiveDigest: "sha256:" + sha64("a")}
	f.deps.Store = aliases
}

func withScheduleStore(f *tokenAuthzFixture) {
	schedules := &scheduleFakeStore{fakeStore: f.st, schedules: map[string]models.CISchedule{}}
	schedules.schedules["sched-1"] = models.CISchedule{ScheduleID: "sched-1", OrgID: "org-1", ProjectID: f.projectID,
		Name: "nightly", Branch: "main", WorkflowRef: "ci", Cron: "0 3 * * *", Timezone: "UTC", Enabled: true}
	f.deps.Store = schedules
}

func withAlertStore(f *tokenAuthzFixture) {
	alerts := &alertFakeStore{fakeStore: f.st, rules: map[string]models.AlertRule{}}
	alerts.rules["rule-org"] = models.AlertRule{RuleID: "rule-org", OrgID: "org-1", Name: "org", Scope: "owner_group", OwnerGroup: "g", Condition: "pass_rate_below", Threshold: 0.5, WindowHours: 24}
	f.deps.Store = alerts
}

func withSettingsStore(f *tokenAuthzFixture) {
	f.deps.Store = &settingsFakeStore{fakeStore: f.st, infraRetries: map[string]int{}, egress: map[string]bool{},
		allowlists: map[string][]string{}, oidc: map[string]models.OIDCSettings{}}
}

func sha64(c string) string {
	out := ""
	for len(out) < 64 {
		out += c
	}
	return out
}

// tokenGapCases are the operations that accepted a token before but checked
// only the role of its owner. A token must never exceed its own
// capabilities.
func tokenGapCases() []tokenAuthzCase {
	org := "acme"
	orgID := "org-1"
	return []tokenAuthzCase{
		// Image aliases: organizations:read (or policies:manage) to read,
		// policies:manage to change.
		{name: "list-image-aliases", capability: tokencaps.OrganizationsRead, wrap: withAliasStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.ListImageAliases(ctx, csilapi.ListImageAliasesRequest{Organization: org})
				return err
			}},
		{name: "get-image-alias", capability: tokencaps.OrganizationsRead, wrap: withAliasStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.GetImageAlias(ctx, csilapi.GetImageAliasRequest{Organization: org, Name: "win"})
				return err
			}},
		{name: "get-image-rollout-status", capability: tokencaps.OrganizationsRead, wrap: withAliasStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.GetImageRolloutStatus(ctx, csilapi.GetImageRolloutStatusRequest{Organization: org})
				return err
			}},
		{name: "set-image-alias", capability: tokencaps.PoliciesManage, wrap: withAliasStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				osName, arch := "linux", "amd64"
				_, err := ui.SetImageAlias(ctx, csilapi.SetImageAliasRequest{Organization: org, Name: "lin",
					Ref: "registry.example/lin@sha256:" + sha64("b"), Os: &osName, Arch: &arch})
				return err
			}},
		{name: "delete-image-alias", capability: tokencaps.PoliciesManage, wrap: withAliasStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.DeleteImageAlias(ctx, csilapi.DeleteImageAliasRequest{Organization: org, Name: "win"})
				return err
			}},
		// Schedules: projects:read to read, projects:manage to change.
		{name: "list-schedules", capability: tokencaps.ProjectsRead, wrap: withScheduleStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.ListSchedules(ctx, csilapi.ListSchedulesRequest{ProjectId: f.projectID})
				return err
			}},
		{name: "list-schedule-fires", capability: tokencaps.ProjectsRead, wrap: withScheduleStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.ListScheduleFires(ctx, csilapi.ListScheduleFiresRequest{ScheduleId: "sched-1"})
				return err
			}},
		{name: "create-schedule", capability: tokencaps.ProjectsManage, wrap: withScheduleStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.CreateSchedule(ctx, csilapi.CreateScheduleRequest{ProjectId: f.projectID, Name: "weekly",
					Branch: "main", WorkflowRef: "ci", Cron: "0 4 * * 1"})
				return err
			}},
		{name: "update-schedule", capability: tokencaps.ProjectsManage, wrap: withScheduleStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.UpdateSchedule(ctx, csilapi.UpdateScheduleRequest{ScheduleId: "sched-1", Enabled: boolPtr(false)})
				return err
			}},
		{name: "delete-schedule", capability: tokencaps.ProjectsManage, wrap: withScheduleStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.DeleteSchedule(ctx, csilapi.DeleteScheduleRequest{ScheduleId: "sched-1"})
				return err
			}},
		// Principals: organizations:manage, as in REST; tokens also
		// tokens:manage.
		{name: "list-principals", capability: tokencaps.OrganizationsRead,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.ListPrincipals(ctx, csilapi.ListPrincipalsRequest{OrgId: orgID})
				return err
			}},
		{name: "create-service-principal", capability: tokencaps.OrganizationsManage,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.CreateServicePrincipal(ctx, csilapi.CreateServicePrincipalRequest{OrgId: orgID, Name: "deployer"})
				return err
			}},
		{name: "resolve-principal", capability: tokencaps.OrganizationsRead,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.ResolvePrincipal(ctx, csilapi.ResolvePrincipalRequest{OrgId: orgID, IdentityType: "principal", IdentityValue: "x"})
				return err
			}},
		{name: "issue-principal-token", capability: tokencaps.TokensManage, tokenCapabilities: []string{tokencaps.OrganizationsManage, tokencaps.TokensManage}, withoutCapabilities: []string{tokencaps.OrganizationsManage},
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.IssuePrincipalToken(ctx, csilapi.IssuePrincipalTokenRequest{OrgId: orgID, PrincipalId: "p-1", Name: "t"})
				return err
			}},
		{name: "revoke-principal-token", capability: tokencaps.TokensManage, tokenCapabilities: []string{tokencaps.OrganizationsManage, tokencaps.TokensManage}, withoutCapabilities: []string{tokencaps.OrganizationsManage},
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.RevokePrincipalToken(ctx, csilapi.RevokePrincipalTokenRequest{OrgId: orgID, PrincipalId: "p-1", TokenId: "t-1"})
				return err
			}},
		// Group VCS links: organizations:manage, as in the REST group routes.
		{name: "list-group-vcs-links", capability: tokencaps.OrganizationsRead,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.ListGroupVcsLinks(ctx, csilapi.ListGroupVcsLinksRequest{GroupId: f.groupID})
				return err
			}},
		{name: "link-group-vcs-team", capability: tokencaps.OrganizationsManage,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.LinkGroupVcsTeam(ctx, csilapi.LinkGroupVcsTeamRequest{GroupId: f.groupID, Team: "acme/devs"})
				return err
			}},
		{name: "sync-group-vcs-teams", capability: tokencaps.OrganizationsManage,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.SyncGroupVcsTeams(ctx, csilapi.SyncGroupVcsTeamsRequest{GroupId: f.groupID})
				return err
			}},
		{name: "list-group-member-details", capability: tokencaps.OrganizationsRead,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.ListGroupMemberDetails(ctx, csilapi.ListGroupMemberDetailsRequest{GroupId: f.groupID})
				return err
			}},
		// OIDC settings: organizations:read to read, organizations:manage
		// to change, as in REST.
		{name: "get-oidc-settings", capability: tokencaps.OrganizationsRead, wrap: withSettingsStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.GetOidcSettings(ctx, csilapi.GetOidcSettingsRequest{Organization: org})
				return err
			}},
		{name: "put-oidc-settings", capability: tokencaps.OrganizationsManage, wrap: withSettingsStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.PutOidcSettings(ctx, csilapi.PutOidcSettingsRequest{Organization: org})
				return err
			}},
		// Alert rules of the organization: organizations:manage, as in REST.
		{name: "create-alert-rule", capability: tokencaps.OrganizationsManage, wrap: withAlertStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				cond := "pass_rate_below"
				threshold := 0.5
				group := "g"
				_, err := ui.CreateAlertRule(ctx, csilapi.CreateAlertRuleRequest{OrgId: orgID, Name: "new", Scope: "owner_group", OwnerGroup: &group,
					Rule: csilapi.AlertRuleInput{Condition: &cond, Threshold: &threshold}})
				return err
			}},
		{name: "update-alert-rule", capability: tokencaps.OrganizationsManage, wrap: withAlertStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.UpdateAlertRule(ctx, csilapi.UpdateAlertRuleRequest{OrgId: orgID, RuleId: "rule-org",
					Rule: csilapi.AlertRuleInput{Enabled: boolPtr(false)}})
				return err
			}},
		{name: "delete-alert-rule", capability: tokencaps.OrganizationsManage, wrap: withAlertStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.DeleteAlertRule(ctx, csilapi.DeleteAlertRuleRequest{OrgId: orgID, RuleId: "rule-org"})
				return err
			}},
		// Global admin reads: organizations:read with all organizations.
		{name: "list-trusted-domain-patterns", global: true, capability: tokencaps.OrganizationsRead,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.ListTrustedDomainPatterns(ctx, csilapi.ListTrustedDomainPatternsRequest{})
				return err
			}},
		{name: "list-oidc-keys", global: true, capability: tokencaps.OrganizationsRead,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.ListOidcKeys(ctx, csilapi.ListOidcKeysRequest{})
				return err
			}},
		{name: "list-github-apps", global: true, capability: tokencaps.OrganizationsRead,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.ListGithubApps(ctx, csilapi.ListGitHubAppsRequest{})
				return err
			}},
		{name: "get-vcs-owner-allowlist", global: true, capability: tokencaps.OrganizationsRead, wrap: withSettingsStore,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.GetVcsOwnerAllowlist(ctx, csilapi.GetVcsOwnerAllowlistRequest{Organization: org})
				return err
			}},
		// Required set org settings: projects:read (or policies:manage) to
		// read, as in REST.
		{name: "get-required-set-org-settings", capability: tokencaps.ProjectsRead,
			call: func(ui *UiService, ctx context.Context, f *tokenAuthzFixture) error {
				_, err := ui.GetRequiredSetOrgSettings(ctx, csilapi.GetRequiredSetOrgSettingsRequest{Organization: org})
				return err
			}},
	}
}

// TestSubscriptionScopeTokenGrant: outbound event subscriptions need
// organizations:manage (org scope) or projects:manage (project scope) in the
// token, as in the REST routes. The operations need the outbound event
// manager, so this checks the scope check that each of them calls.
func TestSubscriptionScopeTokenGrant(t *testing.T) {
	f := newTokenAuthzFixture(t)
	ui := NewUiService(f.deps)
	ctxFor := func(userID string, orgs []string, capabilities ...string) authzIdentityCtx {
		c, id, _, err := f.deps.requireManagementIdentity(f.userToken(userID, orgs, capabilities...))
		requireOK(t, err)
		return authzIdentityCtx{c, id}
	}
	session, id, _, err := f.deps.requireManagementIdentity(mintSessionCtx(t, f.deps, "admin-1"))
	requireOK(t, err)
	requireOK(t, ui.authorizeSubscriptionScope(session, id, "org-1", nil))
	requireOK(t, ui.authorizeSubscriptionScope(session, id, "org-1", &f.projectID))

	org := ctxFor("admin-1", []string{"org-1"}, tokencaps.OrganizationsManage)
	requireOK(t, ui.authorizeSubscriptionScope(org.ctx, org.id, "org-1", nil))
	requireRefused(t, ui.authorizeSubscriptionScope(org.ctx, org.id, "org-1", &f.projectID))
	project := ctxFor("admin-1", []string{"org-1"}, tokencaps.ProjectsManage)
	requireOK(t, ui.authorizeSubscriptionScope(project.ctx, project.id, "org-1", &f.projectID))
	requireRefused(t, ui.authorizeSubscriptionScope(project.ctx, project.id, "org-1", nil))
	none := ctxFor("admin-1", []string{"org-1"}, tokencaps.JobsRead)
	requireRefused(t, ui.authorizeSubscriptionScope(none.ctx, none.id, "org-1", nil))
	other := ctxFor("admin-1", []string{"org-2"}, tokencaps.OrganizationsManage)
	requireRefused(t, ui.authorizeSubscriptionScope(other.ctx, other.id, "org-1", nil))
	nobody := ctxFor("nobody-1", []string{"org-1"}, tokencaps.OrganizationsManage)
	requireRefused(t, ui.authorizeSubscriptionScope(nobody.ctx, nobody.id, "org-1", nil))
}

type authzIdentityCtx struct {
	ctx context.Context
	id  authz.Identity
}

// TestRefClassAndPriorityScopeTokenGrant: ref class rules and priority
// lanes need projects:read (or policies:manage) to read and policies:manage
// to change, as in the REST routes.
func TestRefClassAndPriorityScopeTokenGrant(t *testing.T) {
	f := newTokenAuthzFixture(t)
	f.deps.Store = &scopeFakeStore{fakeStore: f.st}
	ui := NewUiService(f.deps)
	check := func(ctx context.Context, write bool) error {
		if _, _, _, _, _, err := ui.refClassScope(ctx, "acme", nil, write); err != nil {
			return err
		}
		_, _, _, _, _, err := ui.priorityScope(ctx, "acme", nil, write)
		return err
	}
	if _, ok := f.deps.Store.(uiRefClassStore); !ok {
		t.Skip("the fake store has no ref class store")
	}
	if _, ok := f.deps.Store.(uiPriorityStore); !ok {
		t.Skip("the fake store has no priority store")
	}
	requireOK(t, check(mintSessionCtx(t, f.deps, "admin-1"), true))
	requireOK(t, check(f.userToken("admin-1", []string{"org-1"}, tokencaps.ProjectsRead), false))
	requireRefused(t, check(f.userToken("admin-1", []string{"org-1"}, tokencaps.ProjectsRead), true))
	requireOK(t, check(f.userToken("admin-1", []string{"org-1"}, tokencaps.PoliciesManage), true))
	requireRefused(t, check(f.userToken("admin-1", []string{"org-1"}, tokencaps.JobsRead), false))
	requireRefused(t, check(f.userToken("admin-1", []string{"org-2"}, tokencaps.PoliciesManage), false))
	requireRefused(t, check(f.userToken("nobody-1", []string{"org-1"}, tokencaps.PoliciesManage), true))
}

// TestTokenListsStayInTokenScope: a token sees jobs and workflows only with
// the read capability, and only in its organizations.
func TestTokenListsStayInTokenScope(t *testing.T) {
	f := newTokenAuthzFixture(t)
	requireOK(t, f.st.CreateOrganization(context.Background(), &models.Organization{OrgID: "org-p", Name: "private", IsPrivate: true}))
	seedOrgAdmin(f.st, "admin-1", "org-p")
	f.st.putJob(models.Job{UserID: "org-p", OrgID: "org-p", Name: "secret-job", Status: "completed"})
	ui := NewUiService(f.deps)

	names := func(ctx context.Context) []string {
		resp, err := ui.ListJobs(ctx, csilapi.ListJobsRequest{})
		requireOK(t, err)
		out := []string{}
		for _, job := range resp.Jobs {
			out = append(out, job.Name)
		}
		return out
	}
	has := func(list []string, name string) bool {
		for _, n := range list {
			if n == name {
				return true
			}
		}
		return false
	}
	if !has(names(mintSessionCtx(t, f.deps, "admin-1")), "secret-job") {
		t.Fatal("the admin session must see the private job")
	}
	if !has(names(f.userToken("admin-1", []string{"org-p"}, tokencaps.JobsRead)), "secret-job") {
		t.Fatal("a jobs:read token for the org must see the private job")
	}
	if has(names(f.userToken("admin-1", []string{"org-p"}, tokencaps.WorkersRead)), "secret-job") {
		t.Fatal("a token without jobs:read must not see the private job")
	}
	if has(names(f.userToken("admin-1", []string{"org-1"}, tokencaps.JobsRead)), "secret-job") {
		t.Fatal("a token for another org must not see the private job")
	}
}

// TestGetCapabilitiesRoleFlagsFollowTheToken: the role flags of a token are
// limited by the token, like the capabilities.
func TestGetCapabilitiesRoleFlagsFollowTheToken(t *testing.T) {
	f := newTokenAuthzFixture(t)
	ui := NewUiService(f.deps)
	org := "org-1"
	resp, err := ui.GetCapabilities(f.userToken("admin-1", []string{"org-1"}, tokencaps.JobsRead), csilapi.GetCapabilitiesRequest{OrgId: &org})
	requireOK(t, err)
	if resp.IsOrgAdmin || resp.ManageWorkers {
		t.Fatalf("a jobs:read token is not an org admin: %+v", resp)
	}
	resp, err = ui.GetCapabilities(f.userToken("admin-1", []string{"org-1"}, tokencaps.OrganizationsManage), csilapi.GetCapabilitiesRequest{OrgId: &org})
	requireOK(t, err)
	if !resp.IsOrgAdmin {
		t.Fatal("an organizations:manage token of the admin is an org admin")
	}
	resp, err = ui.GetCapabilities(f.userToken("gadmin-1", []string{"org-1"}, tokencaps.OrganizationsManage), csilapi.GetCapabilitiesRequest{})
	requireOK(t, err)
	if resp.IsGlobalAdmin {
		t.Fatal("a token for one org is not a global admin")
	}
}

// scopeFakeStore gives the ref class and priority scope checks their store
// type. The checks call only the organization and project lookups.
type scopeFakeStore struct {
	*fakeStore
	store.RefClassStore
	store.PriorityStore
}

func (s *scopeFakeStore) GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error) {
	return s.fakeStore.GetOrganizationByName(ctx, name)
}

func (s *scopeFakeStore) GetProjectByID(ctx context.Context, projectID string) (*models.Project, error) {
	return s.fakeStore.GetProjectByID(ctx, projectID)
}

// TestReadCapabilityCannotWrite: organizations:read gives the reads of
// groups, roles, principals, and global settings, never a change.
func TestReadCapabilityCannotWrite(t *testing.T) {
	f := newTokenAuthzFixture(t)
	ui := NewUiService(f.deps)
	org := f.userToken("admin-1", []string{"org-1"}, tokencaps.OrganizationsRead)
	_, err := ui.ListGroups(org, csilapi.ListGroupsRequest{OrgId: "org-1"})
	requireOK(t, err)
	_, err = ui.CreateGroup(org, csilapi.CreateGroupRequest{OrgId: "org-1", Name: "new"})
	requireRefused(t, err)
	orgID := "org-1"
	_, err = ui.AssignRole(org, csilapi.AssignRoleRequest{PrincipalType: models.PrincipalTypeUser, PrincipalId: "member-1",
		ScopeType: models.ScopeTypeOrg, ScopeId: &orgID, Role: models.RoleAdmin})
	requireRefused(t, err)
	global := f.userToken("gadmin-1", nil, tokencaps.OrganizationsRead)
	_, err = ui.ListTrustedIdentities(global, csilapi.ListTrustedIdentitiesRequest{})
	requireOK(t, err)
	_, err = ui.AddTrustedIdentity(global, csilapi.AddTrustedIdentityRequest{Domain: "example.com"})
	requireRefused(t, err)
}
