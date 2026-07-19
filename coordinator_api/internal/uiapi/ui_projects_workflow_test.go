package uiapi

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func TestProjects_HappyPath(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "org-1", Username: "org-one"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	created, err := ui.CreateProject(ctx, csilapi.CreateProjectRequest{
		OrgId: "org-1", Name: "proj-a", RepoUrl: "github.com/org/repo",
	})
	requireOK(t, err)
	if created.Project.IsPrivate {
		t.Fatalf("IsPrivate = true, want false (public-by-default when unspecified)")
	}

	got, err := ui.GetProject(ctx, csilapi.GetProjectRequest{ProjectId: created.Project.ProjectId})
	requireOK(t, err)
	if got.Project.Name != "proj-a" {
		t.Fatalf("Name = %q, want proj-a", got.Project.Name)
	}

	updated, err := ui.UpdateProject(ctx, csilapi.UpdateProjectRequest{
		ProjectId: created.Project.ProjectId, IsPrivate: boolPtr(true),
	})
	requireOK(t, err)
	if !updated.Project.IsPrivate {
		t.Fatalf("IsPrivate = false after update, want true")
	}

	// A private project is invisible to an anonymous caller...
	_, err = ui.GetProject(anonCtx(), csilapi.GetProjectRequest{ProjectId: created.Project.ProjectId})
	requireCode(t, err, "not_found")

	// ...and to an unrelated authenticated user...
	other := st.putUser(models.User{UserID: "other-1"})
	otherCtx := mintSessionCtx(t, deps, other.UserID)
	_, err = ui.GetProject(otherCtx, csilapi.GetProjectRequest{ProjectId: created.Project.ProjectId})
	requireCode(t, err, "not_found")

	// ...but visible to the owning org admin.
	_, err = ui.GetProject(ctx, csilapi.GetProjectRequest{ProjectId: created.Project.ProjectId})
	requireOK(t, err)

	deleted, err := ui.DeleteProject(ctx, csilapi.DeleteProjectRequest{ProjectId: created.Project.ProjectId})
	requireOK(t, err)
	if !deleted.Deleted {
		t.Fatalf("Deleted = false")
	}
}

func TestListProjects_VisibilityFiltering(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	st.putProject(models.Project{UserID: strPtr("org-1"), Name: "public-proj", IsPrivate: false})
	st.putProject(models.Project{UserID: strPtr("org-1"), Name: "private-proj", IsPrivate: true})
	ui := NewUiService(deps)

	listed, err := ui.ListProjects(anonCtx(), csilapi.ListProjectsRequest{})
	requireOK(t, err)
	if len(listed.Projects) != 1 || listed.Projects[0].Name != "public-proj" {
		t.Fatalf("Projects = %+v, want only public-proj visible to anonymous", listed.Projects)
	}
}

func TestListOrgs_IncludesOwnOrgAndVisibleProjectOwners(t *testing.T) {
	deps, st := newTestDeps(t)
	user := st.putUser(models.User{UserID: "org-1", Username: "org-one"})
	st.putUser(models.User{UserID: "org-2", Username: "org-two"})
	st.putProject(models.Project{UserID: strPtr("org-2"), Name: "public-proj", IsPrivate: false})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, user.UserID)

	listed, err := ui.ListOrgs(ctx, csilapi.ListOrgsRequest{})
	requireOK(t, err)
	names := map[string]bool{}
	for _, o := range listed.Orgs {
		names[o.Name] = true
	}
	if !names["org-one"] {
		t.Errorf("Orgs = %+v, want to include the caller's own org", listed.Orgs)
	}
	if !names["org-two"] {
		t.Errorf("Orgs = %+v, want to include the owner of a visible project", listed.Orgs)
	}
}

func TestCancelWorkflow_HappyPath(t *testing.T) {
	withAuthMode(t, config.UIAuthModeLocalRP)
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "org-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	wf := st.putWorkflow(models.WorkflowInstance{UserID: "org-1", Name: "wf", Status: "running"})
	st.nodes[wf.WorkflowID] = []models.WorkflowNode{
		{NodeID: "node-1", WorkflowID: wf.WorkflowID, Name: "n1", Status: "pending"},
	}

	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	resp, err := ui.CancelWorkflow(ctx, csilapi.CancelWorkflowRequest{WorkflowInstanceId: wf.WorkflowID})
	requireOK(t, err)
	if resp.WorkflowInstanceId != wf.WorkflowID {
		t.Fatalf("WorkflowInstanceId = %q, want %q", resp.WorkflowInstanceId, wf.WorkflowID)
	}
	if resp.Status != "cancelled" {
		t.Fatalf("Status = %q, want cancelled (only pending node, resolves synchronously)", resp.Status)
	}
}

func TestCancelWorkflow_Forbidden(t *testing.T) {
	withAuthMode(t, config.UIAuthModeLocalRP)
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	member := st.putUser(models.User{UserID: "member-1"})
	wf := st.putWorkflow(models.WorkflowInstance{UserID: "org-1", Name: "wf", Status: "running"})

	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, member.UserID)
	_, err := ui.CancelWorkflow(ctx, csilapi.CancelWorkflowRequest{WorkflowInstanceId: wf.WorkflowID})
	requireCode(t, err, "forbidden")
}
