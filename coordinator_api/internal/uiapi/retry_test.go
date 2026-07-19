package uiapi

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// TestRetryJob_PermissionMatrix drives UI_AUTH_PLAN.md's permission matrix
// "retry" row across every caller tier and both relevant auth modes: the
// same tier as cancel — anonymous may retry ONLY in mode none; a plain
// member may never retry; project owner/org admin/global admin may always
// retry.
func TestRetryJob_PermissionMatrix(t *testing.T) {
	newFailedJob := func(t *testing.T, st *fakeStore, orgID, projectID string) models.Job {
		t.Helper()
		return st.putJob(models.Job{UserID: orgID, ProjectID: &projectID, Status: "failed", Name: "j"})
	}

	t.Run("anonymous in mode none may retry", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeNone)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		job := newFailedJob(t, st, "org-1", proj.ProjectID)

		ui := NewUiService(deps)
		resp, err := ui.RetryJob(anonCtx(), csilapi.RetryJobRequest{JobId: job.JobID})
		requireOK(t, err)
		if resp.JobId == job.JobID {
			t.Errorf("JobId = %q, want a distinct new job id", resp.JobId)
		}
	})

	t.Run("anonymous in local-rp mode may not retry", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		job := newFailedJob(t, st, "org-1", proj.ProjectID)

		ui := NewUiService(deps)
		_, err := ui.RetryJob(anonCtx(), csilapi.RetryJobRequest{JobId: job.JobID})
		requireCode(t, err, "forbidden")
	})

	t.Run("plain member may not retry", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		member := st.putUser(models.User{UserID: "member-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		job := newFailedJob(t, st, "org-1", proj.ProjectID)
		seedProjectMember(st, member.UserID, proj.ProjectID)

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, member.UserID)
		_, err := ui.RetryJob(ctx, csilapi.RetryJobRequest{JobId: job.JobID})
		requireCode(t, err, "forbidden")
	})

	t.Run("project owner may retry", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		owner := st.putUser(models.User{UserID: "owner-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		job := newFailedJob(t, st, "org-1", proj.ProjectID)
		seedProjectOwner(st, owner.UserID, proj.ProjectID)

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, owner.UserID)
		_, err := ui.RetryJob(ctx, csilapi.RetryJobRequest{JobId: job.JobID})
		requireOK(t, err)
	})

	t.Run("org admin may retry", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "admin-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		job := newFailedJob(t, st, "org-1", proj.ProjectID)
		seedOrgAdmin(st, admin.UserID, "org-1")

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.RetryJob(ctx, csilapi.RetryJobRequest{JobId: job.JobID})
		requireOK(t, err)
	})

	t.Run("global admin may retry", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "gadmin-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		job := newFailedJob(t, st, "org-1", proj.ProjectID)
		seedGlobalAdmin(st, admin.UserID)

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.RetryJob(ctx, csilapi.RetryJobRequest{JobId: job.JobID})
		requireOK(t, err)
	})
}

// TestRetryJob_NotRetryable verifies a job whose status isn't failed/
// cancelled is refused with a "conflict" ServiceError, matching how
// jobcontrol.ErrNotCancellable is mapped for CancelJob.
func TestRetryJob_NotRetryable(t *testing.T) {
	withAuthMode(t, config.UIAuthModeNone)
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	job := st.putJob(models.Job{UserID: "org-1", Status: "running", Name: "j"})

	ui := NewUiService(deps)
	_, err := ui.RetryJob(anonCtx(), csilapi.RetryJobRequest{JobId: job.JobID})
	requireCode(t, err, "conflict")
}

// TestRetryJob_NotFound verifies an unknown job id maps to "not_found".
func TestRetryJob_NotFound(t *testing.T) {
	withAuthMode(t, config.UIAuthModeNone)
	deps, _ := newTestDeps(t)
	ui := NewUiService(deps)
	_, err := ui.RetryJob(anonCtx(), csilapi.RetryJobRequest{JobId: "does-not-exist"})
	requireCode(t, err, "not_found")
}

// TestRetryWorkflow_PermissionMatrix mirrors TestRetryJob_PermissionMatrix
// for the workflow-level retry op.
func TestRetryWorkflow_PermissionMatrix(t *testing.T) {
	t.Run("anonymous in mode none may retry", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeNone)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		wf := st.putWorkflow(models.WorkflowInstance{UserID: "org-1", Name: "wf", Status: "failed"})

		ui := NewUiService(deps)
		resp, err := ui.RetryWorkflow(anonCtx(), csilapi.RetryWorkflowRequest{WorkflowInstanceId: wf.WorkflowID})
		requireOK(t, err)
		if resp.WorkflowInstanceId == wf.WorkflowID {
			t.Errorf("WorkflowInstanceId = %q, want a distinct new workflow id", resp.WorkflowInstanceId)
		}
	})

	t.Run("anonymous in local-rp mode may not retry", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		wf := st.putWorkflow(models.WorkflowInstance{UserID: "org-1", Name: "wf", Status: "failed"})

		ui := NewUiService(deps)
		_, err := ui.RetryWorkflow(anonCtx(), csilapi.RetryWorkflowRequest{WorkflowInstanceId: wf.WorkflowID})
		requireCode(t, err, "forbidden")
	})

	t.Run("plain member may not retry", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		member := st.putUser(models.User{UserID: "member-1"})
		wf := st.putWorkflow(models.WorkflowInstance{UserID: "org-1", Name: "wf", Status: "failed"})

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, member.UserID)
		_, err := ui.RetryWorkflow(ctx, csilapi.RetryWorkflowRequest{WorkflowInstanceId: wf.WorkflowID})
		requireCode(t, err, "forbidden")
	})

	t.Run("org admin may retry", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "admin-1"})
		wf := st.putWorkflow(models.WorkflowInstance{UserID: "org-1", Name: "wf", Status: "cancelled"})
		seedOrgAdmin(st, admin.UserID, "org-1")

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.RetryWorkflow(ctx, csilapi.RetryWorkflowRequest{WorkflowInstanceId: wf.WorkflowID})
		requireOK(t, err)
	})
}

// TestRetryWorkflow_NotRetryable verifies a running workflow is refused with
// "conflict".
func TestRetryWorkflow_NotRetryable(t *testing.T) {
	withAuthMode(t, config.UIAuthModeNone)
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	wf := st.putWorkflow(models.WorkflowInstance{UserID: "org-1", Name: "wf", Status: "running"})

	ui := NewUiService(deps)
	_, err := ui.RetryWorkflow(anonCtx(), csilapi.RetryWorkflowRequest{WorkflowInstanceId: wf.WorkflowID})
	requireCode(t, err, "conflict")
}

// TestRetryWorkflow_HappyPath verifies a fresh workflow instance is created,
// distinct from the old one, and the old instance is left untouched.
func TestRetryWorkflow_HappyPath(t *testing.T) {
	withAuthMode(t, config.UIAuthModeNone)
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	wf := st.putWorkflow(models.WorkflowInstance{UserID: "org-1", Name: "wf", Status: "failed"})

	ui := NewUiService(deps)
	resp, err := ui.RetryWorkflow(anonCtx(), csilapi.RetryWorkflowRequest{WorkflowInstanceId: wf.WorkflowID})
	requireOK(t, err)
	if resp.WorkflowInstanceId == "" || resp.WorkflowInstanceId == wf.WorkflowID {
		t.Fatalf("WorkflowInstanceId = %q, want a distinct new workflow id", resp.WorkflowInstanceId)
	}

	old, ok := st.workflows[wf.WorkflowID]
	if !ok || old.Status != "failed" {
		t.Errorf("expected the old workflow instance to remain untouched (status failed), got %+v", old)
	}
}

// TestRetryUnsuccessfulJobs_PermissionMatrix mirrors the other two retry ops'
// permission matrix, plus the skip-successful-jobs happy path the feature
// spec calls out.
func TestRetryUnsuccessfulJobs_PermissionMatrix(t *testing.T) {
	t.Run("anonymous in local-rp mode may not retry", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		wf := st.putWorkflow(models.WorkflowInstance{UserID: "org-1", Name: "wf", Status: "failed"})

		ui := NewUiService(deps)
		_, err := ui.RetryUnsuccessfulJobs(anonCtx(), csilapi.RetryUnsuccessfulJobsRequest{WorkflowInstanceId: wf.WorkflowID})
		requireCode(t, err, "forbidden")
	})

	t.Run("plain member may not retry", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		member := st.putUser(models.User{UserID: "member-1"})
		wf := st.putWorkflow(models.WorkflowInstance{UserID: "org-1", Name: "wf", Status: "failed"})

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, member.UserID)
		_, err := ui.RetryUnsuccessfulJobs(ctx, csilapi.RetryUnsuccessfulJobsRequest{WorkflowInstanceId: wf.WorkflowID})
		requireCode(t, err, "forbidden")
	})
}

// TestRetryUnsuccessfulJobs_SkipsSuccessfulJobs verifies the bulk retry op
// retries every failed/cancelled member job in place, skips a completed
// node's job, and reports accurate retried/skipped counts.
func TestRetryUnsuccessfulJobs_SkipsSuccessfulJobs(t *testing.T) {
	withAuthMode(t, config.UIAuthModeNone)
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	wf := st.putWorkflow(models.WorkflowInstance{UserID: "org-1", Name: "wf", Status: "failed"})

	okJob := st.putJob(models.Job{UserID: "org-1", Status: "completed", Name: "lint"})
	failedJob := st.putJob(models.Job{UserID: "org-1", Status: "failed", Name: "build", WorkflowID: &wf.WorkflowID})
	cancelledJob := st.putJob(models.Job{UserID: "org-1", Status: "cancelled", Name: "test", WorkflowID: &wf.WorkflowID})

	st.nodes[wf.WorkflowID] = []models.WorkflowNode{
		{NodeID: "node-ok", WorkflowID: wf.WorkflowID, Name: "lint", Status: "completed", JobID: strPtr(okJob.JobID)},
		{NodeID: "node-failed", WorkflowID: wf.WorkflowID, Name: "build", Status: "failed", JobID: strPtr(failedJob.JobID)},
		{NodeID: "node-cancelled", WorkflowID: wf.WorkflowID, Name: "test", Status: "cancelled", JobID: strPtr(cancelledJob.JobID)},
		{NodeID: "node-no-job", WorkflowID: wf.WorkflowID, Name: "never-ran", Status: "pending"},
	}

	ui := NewUiService(deps)
	resp, err := ui.RetryUnsuccessfulJobs(anonCtx(), csilapi.RetryUnsuccessfulJobsRequest{WorkflowInstanceId: wf.WorkflowID})
	requireOK(t, err)

	if resp.RetriedCount != 2 {
		t.Errorf("RetriedCount = %d, want 2 (build, test)", resp.RetriedCount)
	}
	if len(resp.JobIds) != 2 {
		t.Errorf("JobIds = %+v, want 2 entries", resp.JobIds)
	}
	for _, id := range resp.JobIds {
		if id == failedJob.JobID || id == cancelledJob.JobID || id == okJob.JobID {
			t.Errorf("JobIds contains an original job id %q, want only new retried job ids", id)
		}
	}
	// node-ok's completed job is skipped (not retryable); node-no-job has no
	// job at all and doesn't count toward either total.
	if resp.SkippedCount != 1 {
		t.Errorf("SkippedCount = %d, want 1 (node-ok's completed job)", resp.SkippedCount)
	}
}

// TestRetryUnsuccessfulJobs_NotFound verifies an unknown workflow id maps to
// "not_found".
func TestRetryUnsuccessfulJobs_NotFound(t *testing.T) {
	withAuthMode(t, config.UIAuthModeNone)
	deps, _ := newTestDeps(t)
	ui := NewUiService(deps)
	_, err := ui.RetryUnsuccessfulJobs(anonCtx(), csilapi.RetryUnsuccessfulJobsRequest{WorkflowInstanceId: "does-not-exist"})
	requireCode(t, err, "not_found")
}
