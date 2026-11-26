package uiapi

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func seedTrustFixture(t *testing.T) (*UiService, *fakeStore, *Deps, models.Project) {
	t.Helper()
	deps, st := newTestDeps(t)
	org := models.Organization{OrgID: "org-1", Name: "acme"}
	st.orgs[org.Name] = org
	project := st.putProject(models.Project{OrgID: "org-1", Name: "application"})
	return NewUiService(deps), st, deps, project
}

func TestTrustSettings_OrgAdminSetsOrganization(t *testing.T) {
	svc, st, deps, project := seedTrustFixture(t)
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	ctx := mintSessionCtx(t, deps, admin.UserID)

	_, err := svc.PutCiTrustSettings(ctx, csilapi.PutCiTrustSettingsRequest{Organization: "acme",
		Settings: csilapi.CiTrustSettings{RunGate: []string{models.RunGateForks}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetCiTrustSettings(ctx, csilapi.GetCiTrustSettingsRequest{Organization: "acme", ProjectId: &project.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if !got.CanLoosen || got.Settings.RunGate != nil || len(got.Effective.RunGate) != 1 || got.OrganizationEffective == nil {
		t.Fatalf("project view: %+v", got)
	}
	// An org admin can make a project less strict than the organization.
	if _, err := svc.PutCiTrustSettings(ctx, csilapi.PutCiTrustSettingsRequest{Organization: "acme", ProjectId: &project.ProjectID,
		Settings: csilapi.CiTrustSettings{RunGate: []string{}}}); err != nil {
		t.Fatalf("an org admin was refused: %v", err)
	}
}

func TestTrustSettings_ProjectOwnerCanOnlyTighten(t *testing.T) {
	svc, st, deps, project := seedTrustFixture(t)
	st.trustSettings = map[string]models.CITrustSettings{"org-1": {OrgID: "org-1", RunGate: []string{models.RunGateForks}}}
	owner := st.putUser(models.User{UserID: "owner-1"})
	seedProjectOwner(st, owner.UserID, project.ProjectID)
	ctx := mintSessionCtx(t, deps, owner.UserID)

	_, err := svc.PutCiTrustSettings(ctx, csilapi.PutCiTrustSettingsRequest{Organization: "acme", ProjectId: &project.ProjectID,
		Settings: csilapi.CiTrustSettings{RunGate: []string{models.RunGateForks, models.RunGateOutsideContributors}}})
	if err != nil {
		t.Fatalf("a stricter project setting was refused: %v", err)
	}
	_, err = svc.PutCiTrustSettings(ctx, csilapi.PutCiTrustSettingsRequest{Organization: "acme", ProjectId: &project.ProjectID,
		Settings: csilapi.CiTrustSettings{RunGate: []string{}}})
	requireCode(t, err, "forbidden")
	if !strings.Contains(err.Error(), "removes run gate condition forks") {
		t.Fatalf("the refusal must say why: %v", err)
	}
	_, err = svc.PutCiTrustSettings(ctx, csilapi.PutCiTrustSettingsRequest{Organization: "acme",
		Settings: csilapi.CiTrustSettings{RunGate: []string{}}})
	requireCode(t, err, "forbidden")
}

func TestTrustSettings_MemberCannotEdit(t *testing.T) {
	svc, st, deps, project := seedTrustFixture(t)
	member := st.putUser(models.User{UserID: "member-1"})
	seedProjectMember(st, member.UserID, project.ProjectID)
	ctx := mintSessionCtx(t, deps, member.UserID)
	_, err := svc.PutCiTrustSettings(ctx, csilapi.PutCiTrustSettingsRequest{Organization: "acme", ProjectId: &project.ProjectID,
		Settings: csilapi.CiTrustSettings{RunGate: []string{models.RunGateForks}}})
	requireCode(t, err, "forbidden")
}

func TestTrustSettings_InvalidValuesRefused(t *testing.T) {
	svc, st, deps, _ := seedTrustFixture(t)
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	ctx := mintSessionCtx(t, deps, admin.UserID)
	unknown := "no-such-profile"
	for name, settings := range map[string]csilapi.CiTrustSettings{
		"gate":    {RunGate: []string{"everyone"}},
		"profile": {OverrideProfile: &unknown},
		"review":  {RequiredReviews: []csilapi.RequiredReview{{Id: "x"}}},
	} {
		_, err := svc.PutCiTrustSettings(ctx, csilapi.PutCiTrustSettingsRequest{Organization: "acme", Settings: settings})
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestPrCommand_UsesTheApprovalPath(t *testing.T) {
	svc, st, deps, project := seedTrustFixture(t)
	user := st.putUser(models.User{UserID: "user-1"})
	seedProjectMember(st, user.UserID, project.ProjectID)
	ctx := mintSessionCtx(t, deps, user.UserID)
	label := "trust-anchors"
	_, err := svc.PrCommand(ctx, csilapi.PrCommandRequest{ProjectId: project.ProjectID, PrNumber: 4, Command: "accept", Label: &label})
	requireCode(t, err, "unavailable")

	executor := &fakeExecutor{}
	deps.Approvals = executor
	resp, err := svc.PrCommand(ctx, csilapi.PrCommandRequest{ProjectId: project.ProjectID, PrNumber: 4, Command: "accept", Label: &label})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("requests: %+v", executor.requests)
	}
	request := executor.requests[0]
	if request.UserID != user.UserID || request.Channel != models.CIApprovalChannelUI || request.Command.Label != label || request.PRNumber != 4 {
		t.Fatalf("request: %+v", request)
	}
	if resp.Effect == nil || *resp.Effect != "reviews" {
		t.Fatalf("response: %+v", resp)
	}
	executor.err = approvals.ErrNotAllowed
	_, err = svc.PrCommand(ctx, csilapi.PrCommandRequest{ProjectId: project.ProjectID, PrNumber: 4, Command: "approve-ci"})
	requireCode(t, err, "forbidden")

	_, err = svc.PrCommand(anonCtx(), csilapi.PrCommandRequest{ProjectId: project.ProjectID, PrNumber: 4, Command: "accept"})
	requireCode(t, err, "unauthorized")
}

func TestTrustSettings_PolicyTokenManagesOrganization(t *testing.T) {
	svc, st, _, _ := seedTrustFixture(t)
	st.apiTokens["policy-token"] = models.APIToken{TokenID: "token-1", SubjectType: "instance_token",
		OrganizationIDs: []string{"org-1"}, Capabilities: []string{tokencaps.PoliciesManage}}
	ctx := WithAuthToken(context.Background(), "policy-token")
	if _, err := svc.PutCiTrustSettings(ctx, csilapi.PutCiTrustSettingsRequest{Organization: "acme",
		Settings: csilapi.CiTrustSettings{RunGate: []string{models.RunGateForks}}}); err != nil {
		t.Fatalf("a policies:manage token was refused: %v", err)
	}
}

// The admin token and a wide service principal token give pull request
// commands as the instance administrator. The admin token needs no user in
// any auth mode.
func TestPrCommand_InstanceAdmin(t *testing.T) {
	svc, st, deps, project := seedTrustFixture(t)
	executor := &fakeExecutor{}
	deps.Approvals = executor
	st.apiTokens["admin-token"] = models.APIToken{TokenID: "token-admin", SubjectType: "instance_token",
		AllOrganizations: true, AllCapabilities: true}
	principalID := "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	st.apiTokens["wide-token"] = models.APIToken{TokenID: "token-wide", SubjectType: "instance_token",
		AllOrganizations: true, AllCapabilities: true, PrincipalID: &principalID}
	command := csilapi.PrCommandRequest{ProjectId: project.ProjectID, PrNumber: 4, Command: "approve-ci"}

	withAuthMode(t, "none")
	if _, err := svc.PrCommand(WithAuthToken(context.Background(), "admin-token"), command); err != nil {
		t.Fatalf("the admin token was refused on a no-login instance: %v", err)
	}
	if _, err := svc.PrCommand(WithAuthToken(context.Background(), "wide-token"), command); err != nil {
		t.Fatalf("a wide principal token was refused on a no-login instance: %v", err)
	}
	if len(executor.requests) != 2 {
		t.Fatalf("requests: %+v", executor.requests)
	}
	admin, wide := executor.requests[0], executor.requests[1]
	if !admin.InstanceAdmin || admin.TokenID != "token-admin" || admin.PrincipalID != "" || admin.UserID != "" {
		t.Fatalf("admin token request: %+v", admin)
	}
	if !wide.InstanceAdmin || wide.PrincipalID != principalID || wide.TokenID != "" {
		t.Fatalf("wide token request: %+v", wide)
	}

	// The login history guard refused mode none, and a login mode: the admin
	// token is still the instance administrator.
	config.SetNoLoginGuardPassed(false)
	if _, err := svc.PrCommand(WithAuthToken(context.Background(), "admin-token"), command); err != nil {
		t.Fatalf("the admin token was refused after the guard refused mode none: %v", err)
	}
	withAuthMode(t, "local-rp")
	if _, err := svc.PrCommand(WithAuthToken(context.Background(), "admin-token"), command); err != nil {
		t.Fatalf("the admin token was refused in a login mode: %v", err)
	}
	if len(executor.requests) != 4 || !executor.requests[3].InstanceAdmin || executor.requests[3].TokenID != "token-admin" {
		t.Fatalf("admin token requests: %+v", executor.requests)
	}
}

// A token needs workflows:control for a pull request command, as in REST.
func TestPrCommand_TokenNeedsWorkflowsControl(t *testing.T) {
	svc, st, deps, project := seedTrustFixture(t)
	executor := &fakeExecutor{}
	deps.Approvals = executor
	user := st.putUser(models.User{UserID: "user-1"})
	seedProjectMember(st, user.UserID, project.ProjectID)
	st.apiTokens["read-token"] = models.APIToken{TokenID: "token-read", SubjectType: "user_token", UserID: user.UserID,
		OrganizationIDs: []string{"org-1"}, Capabilities: []string{tokencaps.ProjectsRead}}
	st.apiTokens["control-token"] = models.APIToken{TokenID: "token-control", SubjectType: "user_token", UserID: user.UserID,
		OrganizationIDs: []string{"org-1"}, Capabilities: []string{tokencaps.ProjectsRead, tokencaps.WorkflowsControl}}
	command := csilapi.PrCommandRequest{ProjectId: project.ProjectID, PrNumber: 4, Command: "accept"}

	_, err := svc.PrCommand(WithAuthToken(context.Background(), "read-token"), command)
	requireCode(t, err, "forbidden")
	if _, err := svc.PrCommand(WithAuthToken(context.Background(), "control-token"), command); err != nil {
		t.Fatalf("a workflows:control token was refused: %v", err)
	}
	if len(executor.requests) != 1 || executor.requests[0].UserID != user.UserID || executor.requests[0].InstanceAdmin {
		t.Fatalf("requests: %+v", executor.requests)
	}
}
