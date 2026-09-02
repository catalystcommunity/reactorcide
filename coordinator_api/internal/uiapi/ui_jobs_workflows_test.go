package uiapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// These are the tests the whole list-jobs/get-job/list-workflows/get-workflow
// change exists for.
//
// Before these ops, the webapp served every one of these reads by calling the
// coordinator REST API with its own SERVICE token. The coordinator's visibility
// filter ran against that service identity, passed every row, and the webapp
// did not filter again -- so a logged-out browser could see private jobs. The
// negative cases below are what stops that regressing.

// visibilityFixture builds one public and one private project, each owning a
// job and a workflow, plus an unrelated user who is a member of neither.
type visibilityFixture struct {
	owner      models.User
	outsider   models.User
	publicJob  models.Job
	privateJob models.Job
	publicWF   models.WorkflowInstance
	privateWF  models.WorkflowInstance
}

func newVisibilityFixture(t *testing.T, st *fakeStore) visibilityFixture {
	t.Helper()
	owner := st.putUser(models.User{UserID: "owner-1"})
	outsider := st.putUser(models.User{UserID: "outsider-1"})

	publicProject := st.putProject(models.Project{
		OrgID: "org-1", UserID: &owner.UserID, Name: "public-project", IsPrivate: false,
	})
	privateProject := st.putProject(models.Project{
		OrgID: "org-1", UserID: &owner.UserID, Name: "private-project", IsPrivate: true,
	})

	publicJob := st.putJob(models.Job{
		JobID: "job-public", Name: "public build", Status: "completed",
		UserID: owner.UserID, OrgID: "org-1", ProjectID: &publicProject.ProjectID,
	})
	privateJob := st.putJob(models.Job{
		JobID: "job-private", Name: "private build", Status: "completed",
		UserID: owner.UserID, OrgID: "org-1", ProjectID: &privateProject.ProjectID,
	})
	publicWF := st.putWorkflow(models.WorkflowInstance{
		WorkflowID: "wf-public", Name: "public flow", Status: "success",
		UserID: owner.UserID, OrgID: "org-1", ProjectID: &publicProject.ProjectID,
	})
	privateWF := st.putWorkflow(models.WorkflowInstance{
		WorkflowID: "wf-private", Name: "private flow", Status: "success",
		UserID: owner.UserID, OrgID: "org-1", ProjectID: &privateProject.ProjectID,
	})

	return visibilityFixture{
		owner: owner, outsider: outsider,
		publicJob: publicJob, privateJob: privateJob,
		publicWF: publicWF, privateWF: privateWF,
	}
}

func jobIDs(jobs []csilapi.JobSummary) map[string]bool {
	out := map[string]bool{}
	for _, j := range jobs {
		out[j.JobId] = true
	}
	return out
}

func workflowIDs(workflows []csilapi.WorkflowSummaryDetail) map[string]bool {
	out := map[string]bool{}
	for _, w := range workflows {
		out[w.WorkflowId] = true
	}
	return out
}

func TestListJobsHidesPrivateJobsFromAnonymousCaller(t *testing.T) {
	deps, st := newTestDeps(t)
	fixture := newVisibilityFixture(t, st)
	ui := NewUiService(deps)

	// No auth token at all: the envelope carried no "auth" field.
	resp, err := ui.ListJobs(context.Background(), csilapi.ListJobsRequest{})
	requireOK(t, err)

	got := jobIDs(resp.Jobs)
	if !got[fixture.publicJob.JobID] {
		t.Error("anonymous caller should see the public job")
	}
	if got[fixture.privateJob.JobID] {
		t.Error("anonymous caller must NOT see the private job")
	}
	if resp.Total != 1 {
		t.Errorf("Total = %d, want 1 (the count must cover the visible set only)", resp.Total)
	}
}

func TestListJobsHidesPrivateJobsFromNonMember(t *testing.T) {
	deps, st := newTestDeps(t)
	fixture := newVisibilityFixture(t, st)
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, fixture.outsider.UserID)

	resp, err := ui.ListJobs(ctx, csilapi.ListJobsRequest{})
	requireOK(t, err)

	got := jobIDs(resp.Jobs)
	if !got[fixture.publicJob.JobID] {
		t.Error("a logged-in non-member should see the public job")
	}
	if got[fixture.privateJob.JobID] {
		t.Error("a logged-in non-member must NOT see the private job")
	}
}

func TestListJobsShowsPrivateJobsToOwner(t *testing.T) {
	deps, st := newTestDeps(t)
	fixture := newVisibilityFixture(t, st)
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, fixture.owner.UserID)

	resp, err := ui.ListJobs(ctx, csilapi.ListJobsRequest{})
	requireOK(t, err)

	got := jobIDs(resp.Jobs)
	if !got[fixture.publicJob.JobID] || !got[fixture.privateJob.JobID] {
		t.Errorf("the owner should see both jobs, saw %v", got)
	}
	if resp.Total != 2 {
		t.Errorf("Total = %d, want 2", resp.Total)
	}
}

// TestGetJobReportsNotFoundRatherThanForbidden pins the deliberate choice in
// GetJob: answering "forbidden" for a job that exists but is invisible confirms
// the id is real, which makes the op an existence oracle for private jobs.
func TestGetJobReportsNotFoundRatherThanForbidden(t *testing.T) {
	deps, st := newTestDeps(t)
	fixture := newVisibilityFixture(t, st)
	ui := NewUiService(deps)

	for name, ctx := range map[string]context.Context{
		"anonymous":  context.Background(),
		"non-member": mintSessionCtx(t, deps, fixture.outsider.UserID),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ui.GetJob(ctx, csilapi.GetJobRequest{JobId: fixture.privateJob.JobID})
			requireCode(t, err, "not_found")

			// The same caller must still be able to read the public job, or
			// this test would pass for the wrong reason.
			got, err := ui.GetJob(ctx, csilapi.GetJobRequest{JobId: fixture.publicJob.JobID})
			requireOK(t, err)
			if got.Job.JobId != fixture.publicJob.JobID {
				t.Errorf("JobId = %q, want %q", got.Job.JobId, fixture.publicJob.JobID)
			}
		})
	}
}

func TestListWorkflowsHidesPrivateWorkflows(t *testing.T) {
	deps, st := newTestDeps(t)
	fixture := newVisibilityFixture(t, st)
	ui := NewUiService(deps)

	for name, ctx := range map[string]context.Context{
		"anonymous":  context.Background(),
		"non-member": mintSessionCtx(t, deps, fixture.outsider.UserID),
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := ui.ListWorkflows(ctx, csilapi.ListWorkflowsRequest{})
			requireOK(t, err)

			got := workflowIDs(resp.Workflows)
			if !got[fixture.publicWF.WorkflowID] {
				t.Error("should see the public workflow")
			}
			if got[fixture.privateWF.WorkflowID] {
				t.Error("must NOT see the private workflow")
			}
		})
	}
}

func TestGetWorkflowReportsNotFoundForInvisibleWorkflow(t *testing.T) {
	deps, st := newTestDeps(t)
	fixture := newVisibilityFixture(t, st)
	ui := NewUiService(deps)

	_, err := ui.GetWorkflow(context.Background(), csilapi.GetWorkflowRequest{
		WorkflowId: fixture.privateWF.WorkflowID,
	})
	requireCode(t, err, "not_found")
}

// TestGetWorkflowReturnsNodesInline covers the DAG view's data source. Node
// dependencies have existed on the row since workflows landed and were exposed
// by no API at all before this op.
func TestGetWorkflowReturnsNodesInline(t *testing.T) {
	deps, st := newTestDeps(t)
	fixture := newVisibilityFixture(t, st)
	st.putWorkflowNodes(fixture.publicWF.WorkflowID, []models.WorkflowNode{
		{NodeID: "n-build", WorkflowID: fixture.publicWF.WorkflowID, Name: "build",
			DisplayName: "build", Status: "success"},
		{NodeID: "n-deploy", WorkflowID: fixture.publicWF.WorkflowID, Name: "deploy",
			DisplayName: "deploy", Status: "pending",
			DependsOn: []string{"build"}, Condition: "all_success"},
	})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, fixture.owner.UserID)

	resp, err := ui.GetWorkflow(ctx, csilapi.GetWorkflowRequest{WorkflowId: fixture.publicWF.WorkflowID})
	requireOK(t, err)

	if len(resp.Nodes) != 2 {
		t.Fatalf("Nodes = %d, want 2", len(resp.Nodes))
	}
	byName := map[string]csilapi.WorkflowNodeSummary{}
	for _, n := range resp.Nodes {
		byName[n.Name] = n
	}
	deploy, ok := byName["deploy"]
	if !ok {
		t.Fatal("deploy node missing")
	}
	if len(deploy.DependsOn) != 1 || deploy.DependsOn[0] != "build" {
		t.Errorf("deploy.DependsOn = %v, want [build]", deploy.DependsOn)
	}
	// A root node must carry an empty array, not a null: depends_on is
	// `[* text]` on the wire, and a null would decode as a missing field.
	build := byName["build"]
	if build.DependsOn == nil {
		t.Error("build.DependsOn is nil; a root node must encode as an empty array")
	}
}

// TestListJobsPaginationCountsTheVisibleSet is the pagination property that a
// "fetch a page, then filter it" implementation gets wrong: the page can come
// back short, and the total reports the filtered page length rather than a real
// count. See postgres_store/visibility_operations.go.
func TestListJobsPaginationCountsTheVisibleSet(t *testing.T) {
	deps, st := newTestDeps(t)
	owner := st.putUser(models.User{UserID: "owner-1"})
	publicProject := st.putProject(models.Project{
		OrgID: "org-1", UserID: &owner.UserID, Name: "public", IsPrivate: false,
	})
	privateProject := st.putProject(models.Project{
		OrgID: "org-1", UserID: &owner.UserID, Name: "private", IsPrivate: true,
	})
	// Interleave private jobs among public ones so a naive implementation
	// would return a short first page.
	for i, projectID := range []string{
		publicProject.ProjectID, privateProject.ProjectID,
		publicProject.ProjectID, privateProject.ProjectID,
		publicProject.ProjectID,
	} {
		pid := projectID
		st.putJob(models.Job{
			JobID: string(rune('a'+i)) + "-job", Status: "completed",
			UserID: owner.UserID, OrgID: "org-1", ProjectID: &pid,
		})
	}
	ui := NewUiService(deps)

	limit := int64(2)
	resp, err := ui.ListJobs(context.Background(), csilapi.ListJobsRequest{Limit: &limit})
	requireOK(t, err)

	if resp.Total != 3 {
		t.Errorf("Total = %d, want 3 (the three public jobs)", resp.Total)
	}
	if len(resp.Jobs) != 2 {
		t.Errorf("page size = %d, want a full page of 2", len(resp.Jobs))
	}
}

// TestDescribeFormMetadataServesEventTypesWithoutASession covers the form
// hinting requirement: the vocabulary must be reachable, and it must not carry
// deployment data.
func TestDescribeFormMetadataServesEventTypesWithoutASession(t *testing.T) {
	deps, _ := newTestDeps(t)
	ui := NewUiService(deps)

	resp, err := ui.DescribeFormMetadata(context.Background(), csilapi.DescribeFormMetadataRequest{})
	requireOK(t, err)

	if len(resp.EventTypes) == 0 {
		t.Fatal("EventTypes is empty")
	}
	for _, choice := range resp.EventTypes {
		if choice.Value == "" || choice.Label == "" || choice.Description == "" {
			t.Errorf("choice %+v: every field feeds a control or its tooltip and must be set", choice)
		}
		if choice.Value == "ping" {
			t.Error("ping is a webhook liveness check and never starts CI; it must not be offered")
		}
	}
	if len(resp.CheckoutModes) == 0 || len(resp.NodeConditions) == 0 || len(resp.JobStatuses) == 0 {
		t.Error("every static vocabulary should be served")
	}
}
