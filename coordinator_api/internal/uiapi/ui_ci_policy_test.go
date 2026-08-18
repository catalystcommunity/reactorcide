package uiapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

const testCIPolicy = `version: 1
defaults:
  ci_source: base
  profile: standard
head_ci:
  - id: tests
    actors:
      any: [repository_write]
    workflows: [tests]
    paths: [.reactorcide/**]
    events: [pull_request_opened, pull_request_updated]
    head_repository: same
    use:
      ci_source: head
      profile: pr-untrusted
      workers: default
`

func TestCIPolicyLifecycle(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	project := st.putProject(models.Project{OrgID: "org-1", UserID: &admin.UserID, Name: "application"})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	put, err := ui.PutCiPolicy(ctx, csilapi.PutCiPolicyRequest{ProjectId: project.ProjectID, Document: testCIPolicy})
	requireOK(t, err)
	if put.Policy.Revision == "" || put.Policy.ProjectId != project.ProjectID {
		t.Fatalf("policy = %+v", put.Policy)
	}

	got, err := ui.GetCiPolicy(ctx, csilapi.GetCiPolicyRequest{ProjectId: project.ProjectID})
	requireOK(t, err)
	if got.Policy.Revision != put.Policy.Revision {
		t.Fatalf("revision = %q, want %q", got.Policy.Revision, put.Policy.Revision)
	}

	wrong := "wrong-revision"
	_, err = ui.PutCiPolicy(ctx, csilapi.PutCiPolicyRequest{ProjectId: project.ProjectID, Document: testCIPolicy, ExpectedRevision: &wrong})
	requireCode(t, err, "conflict")

	deleted, err := ui.DeleteCiPolicy(ctx, csilapi.DeleteCiPolicyRequest{ProjectId: project.ProjectID, ExpectedRevision: &put.Policy.Revision})
	requireOK(t, err)
	if !deleted.Deleted {
		t.Fatal("Deleted = false")
	}
	_, err = ui.GetCiPolicy(ctx, csilapi.GetCiPolicyRequest{ProjectId: project.ProjectID})
	requireCode(t, err, "not_found")
}

func TestCIPolicyAllowsScopedPolicyManagementToken(t *testing.T) {
	deps, st := newTestDeps(t)
	project := st.putProject(models.Project{OrgID: "org-1", Name: "application"})
	st.apiTokens["policy-token"] = models.APIToken{
		TokenID: "token-1", SubjectType: "instance_token", OrganizationIDs: []string{"org-1"},
		Capabilities: []string{tokencaps.PoliciesManage},
	}
	ui := NewUiService(deps)
	ctx := WithAuthToken(context.Background(), "policy-token")

	put, err := ui.PutCiPolicy(ctx, csilapi.PutCiPolicyRequest{ProjectId: project.ProjectID, Document: testCIPolicy})
	requireOK(t, err)
	if put.Policy.Revision == "" {
		t.Fatal("policy revision is empty")
	}
}

func TestCIPolicyRejectsTokenWithoutPolicyCapability(t *testing.T) {
	deps, st := newTestDeps(t)
	project := st.putProject(models.Project{OrgID: "org-1", Name: "application"})
	st.apiTokens["project-token"] = models.APIToken{
		TokenID: "token-1", SubjectType: "instance_token", OrganizationIDs: []string{"org-1"},
		Capabilities: []string{tokencaps.ProjectsManage},
	}
	ui := NewUiService(deps)
	ctx := WithAuthToken(context.Background(), "project-token")

	_, err := ui.PutCiPolicy(ctx, csilapi.PutCiPolicyRequest{ProjectId: project.ProjectID, Document: testCIPolicy})
	requireCode(t, err, "forbidden")
}

func TestCIPolicyRejectsOldRepositoryMaintainerField(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	project := st.putProject(models.Project{OrgID: "org-1", UserID: &admin.UserID, Name: "application"})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	invalid := testCIPolicy + "policy_maintainers: {any: [project_owner]}\n"
	_, err := ui.PutCiPolicy(ctx, csilapi.PutCiPolicyRequest{ProjectId: project.ProjectID, Document: invalid})
	requireCode(t, err, "invalid_argument")
}
