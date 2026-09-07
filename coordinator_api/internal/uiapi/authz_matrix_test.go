package uiapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// TestCancelJob_PermissionMatrix drives; a plain member may never cancel;
// project owner/org admin/global admin may always cancel.
func TestCancelJob_PermissionMatrix(t *testing.T) {
	newJob := func(t *testing.T, st *fakeStore, orgID, projectID string) models.Job {
		t.Helper()
		return st.putJob(models.Job{UserID: orgID, ProjectID: &projectID, Status: "running", Name: "j"})
	}

	t.Run("anonymous in mode none may cancel", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeNone)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		job := newJob(t, st, "org-1", proj.ProjectID)

		ui := NewUiService(deps)
		resp, err := ui.CancelJob(anonCtx(), csilapi.CancelJobRequest{JobId: job.JobID})
		requireOK(t, err)
		if resp.Status != "cancelling" && resp.Status != "cancelled" {
			t.Errorf("Status = %q, want cancelling or cancelled", resp.Status)
		}
	})

	t.Run("anonymous in local-rp mode may not cancel", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		job := newJob(t, st, "org-1", proj.ProjectID)

		ui := NewUiService(deps)
		_, err := ui.CancelJob(anonCtx(), csilapi.CancelJobRequest{JobId: job.JobID})
		requireCode(t, err, "forbidden")
	})

	t.Run("plain member may not cancel", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		member := st.putUser(models.User{UserID: "member-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		job := newJob(t, st, "org-1", proj.ProjectID)
		seedProjectMember(st, member.UserID, proj.ProjectID)

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, member.UserID)
		_, err := ui.CancelJob(ctx, csilapi.CancelJobRequest{JobId: job.JobID})
		requireCode(t, err, "forbidden")
	})

	t.Run("project owner may cancel", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		owner := st.putUser(models.User{UserID: "owner-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		job := newJob(t, st, "org-1", proj.ProjectID)
		seedProjectOwner(st, owner.UserID, proj.ProjectID)

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, owner.UserID)
		_, err := ui.CancelJob(ctx, csilapi.CancelJobRequest{JobId: job.JobID})
		requireOK(t, err)
	})

	t.Run("org admin may cancel", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "admin-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		job := newJob(t, st, "org-1", proj.ProjectID)
		seedOrgAdmin(st, admin.UserID, "org-1")

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.CancelJob(ctx, csilapi.CancelJobRequest{JobId: job.JobID})
		requireOK(t, err)
	})

	t.Run("global admin may cancel", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "gadmin-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		job := newJob(t, st, "org-1", proj.ProjectID)
		seedGlobalAdmin(st, admin.UserID)

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.CancelJob(ctx, csilapi.CancelJobRequest{JobId: job.JobID})
		requireOK(t, err)
	})
}

// TestKillJob_PermissionMatrix drives the "kill job (force)" row: never
// available to anonymous callers (in ANY auth mode, including none — kill is
// a strictly stronger action than cancel), never to a plain member or even a
// project owner; only org admin/global admin.
func TestKillJob_PermissionMatrix(t *testing.T) {
	setup := func(t *testing.T) (*Deps, *fakeStore, models.Job) {
		t.Helper()
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		job := st.putJob(models.Job{UserID: "org-1", ProjectID: &proj.ProjectID, Status: "running", Name: "j"})
		return deps, st, job
	}

	t.Run("anonymous in mode none may not kill", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeNone)
		deps, _, job := setup(t)
		ui := NewUiService(deps)
		_, err := ui.KillJob(anonCtx(), csilapi.KillJobRequest{JobId: job.JobID})
		requireCode(t, err, "forbidden")
	})

	t.Run("project owner may not kill", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st, job := setup(t)
		owner := st.putUser(models.User{UserID: "owner-1"})
		seedProjectOwner(st, owner.UserID, *job.ProjectID)
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, owner.UserID)
		_, err := ui.KillJob(ctx, csilapi.KillJobRequest{JobId: job.JobID})
		requireCode(t, err, "forbidden")
	})

	t.Run("org admin may kill", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st, job := setup(t)
		admin := st.putUser(models.User{UserID: "admin-1"})
		seedOrgAdmin(st, admin.UserID, "org-1")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		resp, err := ui.KillJob(ctx, csilapi.KillJobRequest{JobId: job.JobID})
		requireOK(t, err)
		if resp.JobId != job.JobID {
			t.Errorf("JobId = %q, want %q", resp.JobId, job.JobID)
		}
	})

	t.Run("global admin may kill", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st, job := setup(t)
		admin := st.putUser(models.User{UserID: "gadmin-1"})
		seedGlobalAdmin(st, admin.UserID)
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.KillJob(ctx, csilapi.KillJobRequest{JobId: job.JobID})
		requireOK(t, err)
	})
}

// TestCreateProject_PermissionMatrix drives the "create project" row: no
// caller tier below org admin may create a project, including an
// authenticated plain member of the target org.
func TestCreateProject_PermissionMatrix(t *testing.T) {
	req := func(orgID string) csilapi.CreateProjectRequest {
		return csilapi.CreateProjectRequest{OrgId: orgID, Name: "proj", RepoUrl: "github.com/x/y"}
	}

	t.Run("anonymous may not create", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		ui := NewUiService(deps)
		_, err := ui.CreateProject(anonCtx(), req("org-1"))
		requireCode(t, err, "unauthorized")
	})

	t.Run("member may not create", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		member := st.putUser(models.User{UserID: "member-1"})
		seedProjectMember(st, member.UserID, "unrelated-project")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, member.UserID)
		_, err := ui.CreateProject(ctx, req("org-1"))
		requireCode(t, err, "forbidden")
	})

	t.Run("org admin of the target org may create", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "admin-1"})
		seedOrgAdmin(st, admin.UserID, "org-1")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		resp, err := ui.CreateProject(ctx, req("org-1"))
		requireOK(t, err)
		if resp.Project.OrgId != "org-1" {
			t.Errorf("OrgId = %q, want org-1", resp.Project.OrgId)
		}
	})

	t.Run("org admin of a DIFFERENT org may not create in org-1", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		st.putUser(models.User{UserID: "org-2"})
		admin := st.putUser(models.User{UserID: "admin-2"})
		seedOrgAdmin(st, admin.UserID, "org-2")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.CreateProject(ctx, req("org-1"))
		requireCode(t, err, "forbidden")
	})

	t.Run("global admin may create in any org", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "gadmin-1"})
		seedGlobalAdmin(st, admin.UserID)
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.CreateProject(ctx, req("org-1"))
		requireOK(t, err)
	})
}

// TestSetSecret_PermissionMatrix drives the "set secrets (write-only)" row:
// org admin/global admin only.
func TestSetSecret_PermissionMatrix(t *testing.T) {
	req := csilapi.SetSecretRequest{OrgId: "org-1", Path: "svc/a", Key: "token", Value: "s3cr3t"}

	t.Run("anonymous may not set", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		ui := NewUiService(deps)
		_, err := ui.SetSecret(anonCtx(), req)
		requireCode(t, err, "unauthorized")
	})

	t.Run("project owner may not set", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		owner := st.putUser(models.User{UserID: "owner-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		seedProjectOwner(st, owner.UserID, proj.ProjectID)
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, owner.UserID)
		_, err := ui.SetSecret(ctx, req)
		requireCode(t, err, "forbidden")
	})

	t.Run("org admin may set", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "admin-1"})
		seedOrgAdmin(st, admin.UserID, "org-1")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		resp, err := ui.SetSecret(ctx, req)
		requireOK(t, err)
		if !resp.Ok {
			t.Errorf("Ok = false, want true")
		}
	})

	t.Run("global admin may set", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "gadmin-1"})
		seedGlobalAdmin(st, admin.UserID)
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.SetSecret(ctx, req)
		requireOK(t, err)
	})
}

// TestAddTrustedDomainPattern_PermissionMatrix drives the last matrix row
// ("trusted users/domain-regexes, global settings"): global admin only — even
// an org admin (of any org) may not manage the admission list.
func TestAddTrustedDomainPattern_PermissionMatrix(t *testing.T) {
	req := csilapi.AddTrustedDomainPatternRequest{Pattern: `^.*\.example\.com$`}

	t.Run("anonymous may not add", func(t *testing.T) {
		deps, _ := newTestDeps(t)
		ui := NewUiService(deps)
		_, err := ui.AddTrustedDomainPattern(anonCtx(), req)
		requireCode(t, err, "unauthorized")
	})

	t.Run("org admin may not add", func(t *testing.T) {
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		admin := st.putUser(models.User{UserID: "admin-1"})
		seedOrgAdmin(st, admin.UserID, "org-1")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.AddTrustedDomainPattern(ctx, req)
		requireCode(t, err, "forbidden")
	})

	t.Run("global admin may add", func(t *testing.T) {
		deps, st := newTestDeps(t)
		admin := st.putUser(models.User{UserID: "gadmin-1"})
		seedGlobalAdmin(st, admin.UserID)
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		resp, err := ui.AddTrustedDomainPattern(ctx, req)
		requireOK(t, err)
		if resp.Pattern.Pattern != req.Pattern {
			t.Errorf("Pattern = %q, want %q", resp.Pattern.Pattern, req.Pattern)
		}
	})

	t.Run("invalid regex is rejected even for global admin", func(t *testing.T) {
		deps, st := newTestDeps(t)
		admin := st.putUser(models.User{UserID: "gadmin-1"})
		seedGlobalAdmin(st, admin.UserID)
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.AddTrustedDomainPattern(ctx, csilapi.AddTrustedDomainPatternRequest{Pattern: "(unclosed"})
		requireCode(t, err, "invalid_argument")
	})
}

// TestUnauthorizedSession reports how an invalid/expired/unknown session
// token is handled: it is treated exactly like an anonymous caller by
// resolveIdentity (never a transport error), and ops that require a session
// then reject it with "unauthorized" via requireUser.
func TestUnauthorizedSession(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	ui := NewUiService(deps)

	ctx := WithAuthToken(anonCtx(), "totally-not-a-real-session-token")
	_, err := ui.CreateProject(ctx, csilapi.CreateProjectRequest{OrgId: "org-1", Name: "p", RepoUrl: "r"})
	requireCode(t, err, "unauthorized")

	// GetCapabilities never requires a session (anonymous callers get a real,
	// if empty/limited, capability set) — an invalid token there degrades to
	// anonymous rather than erroring.
	as := NewUiService(deps)
	capsResp, err := as.GetCapabilities(ctx, csilapi.GetCapabilitiesRequest{})
	requireOK(t, err)
	if capsResp.IsGlobalAdmin {
		t.Errorf("IsGlobalAdmin = true for an invalid session token, want false")
	}
}

// seedJobRaw stores a job in the fake exactly as given, bypassing putJob's
// uuid-bindability assertion on jobs.user_id. The real column is
// `type:uuid;default:null`: a job created through a service token has no user
// and is stored with a NULL user_id, which reads back into Go as "". putJob
// refuses "" because it treats every seeded value as a query BINDING, but this
// row is real and is exactly the shape that broke org-admin job control.
func seedJobRaw(st *fakeStore, job models.Job) models.Job {
	st.mu.Lock()
	defer st.mu.Unlock()
	if job.JobID == "" {
		job.JobID = st.genID("job")
	}
	st.jobs[job.JobID] = job
	return job
}

// newServiceTokenJobFixture seeds an org ("org-1"), a project the org owns
// (projects.org_id = org-1) and a job in that project that has NO user_id and
// NO org_id of its own -- the coordinator must resolve the owning org from the
// project. Before jobControlScope, ui_jobcontrol.go passed `OrgID: &job.UserID`
// (a non-nil pointer to ""), which suppressed authz.Capabilities' project->org
// resolution and denied the org's admins.
func newServiceTokenJobFixture(t *testing.T, st *fakeStore, status string) (models.Project, models.Job) {
	t.Helper()
	st.putUser(models.User{UserID: "org-1"})
	proj := st.putProject(models.Project{OrgID: "org-1", UserID: strPtr("org-1"), Name: "svc-project"})
	job := seedJobRaw(st, models.Job{UserID: "", OrgID: "", ProjectID: &proj.ProjectID, Status: status, Name: "svc-job"})
	return proj, job
}

// newPrivateProjectJobFixture seeds a PRIVATE project owned by "org-1" with a
// job in it, and no role assignments -- so nobody but the org's admins and
// global admins can view it.
func newPrivateProjectJobFixture(t *testing.T, st *fakeStore, status string) (models.Project, models.Job) {
	t.Helper()
	st.putUser(models.User{UserID: "org-1"})
	proj := st.putProject(models.Project{OrgID: "org-1", UserID: strPtr("org-1"), Name: "private-project", IsPrivate: true})
	job := st.putJob(models.Job{UserID: "org-1", OrgID: "org-1", ProjectID: &proj.ProjectID, Status: status, Name: "private-job"})
	return proj, job
}

// TestJobControl_OrgAdminOfProjectOrg_ServiceTokenJob is the regression test
// for the bug UI_MANAGEMENT_GAPS_PLAN.md ground-truth item 4 describes: an
// org admin of the PROJECT's owning org must be able to cancel, kill and
// retry a job whose own user_id is empty (created by a service token).
func TestJobControl_OrgAdminOfProjectOrg_ServiceTokenJob(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		_, job := newServiceTokenJobFixture(t, st, "running")
		admin := st.putUser(models.User{UserID: "admin-1"})
		seedOrgAdmin(st, admin.UserID, "org-1")

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		resp, err := ui.CancelJob(ctx, csilapi.CancelJobRequest{JobId: job.JobID})
		requireOK(t, err)
		if resp.JobId != job.JobID {
			t.Errorf("JobId = %q, want %q", resp.JobId, job.JobID)
		}
	})

	t.Run("kill", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		_, job := newServiceTokenJobFixture(t, st, "running")
		admin := st.putUser(models.User{UserID: "admin-1"})
		seedOrgAdmin(st, admin.UserID, "org-1")

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.KillJob(ctx, csilapi.KillJobRequest{JobId: job.JobID})
		requireOK(t, err)
	})

	t.Run("retry", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		_, job := newServiceTokenJobFixture(t, st, "failed")
		admin := st.putUser(models.User{UserID: "admin-1"})
		seedOrgAdmin(st, admin.UserID, "org-1")

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		resp, err := ui.RetryJob(ctx, csilapi.RetryJobRequest{JobId: job.JobID})
		requireOK(t, err)
		if resp.JobId == job.JobID {
			t.Errorf("JobId = %q, want a distinct new job id", resp.JobId)
		}
	})

	// The fix must not widen the tier: an admin of an UNRELATED org gets
	// nothing from a project that org-1 owns.
	t.Run("admin of a different org may not cancel", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		_, job := newServiceTokenJobFixture(t, st, "running")
		st.putUser(models.User{UserID: "org-2"})
		admin := st.putUser(models.User{UserID: "admin-2"})
		seedOrgAdmin(st, admin.UserID, "org-2")

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.CancelJob(ctx, csilapi.CancelJobRequest{JobId: job.JobID})
		requireCode(t, err, "forbidden")
	})

	// Kill stays org-admin tier: a project owner of the same project is still
	// refused even though the scope is now resolved through the project.
	t.Run("project owner may still not kill", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		proj, job := newServiceTokenJobFixture(t, st, "running")
		owner := st.putUser(models.User{UserID: "owner-1"})
		seedProjectOwner(st, owner.UserID, proj.ProjectID)

		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, owner.UserID)
		_, err := ui.KillJob(ctx, csilapi.KillJobRequest{JobId: job.JobID})
		requireCode(t, err, "forbidden")
		// ...while the same owner may cancel, which pins that the two tiers
		// still differ after the shared Capabilities call replaced
		// RequireOrgAdmin in KillJob.
		_, err = ui.CancelJob(ctx, csilapi.CancelJobRequest{JobId: job.JobID})
		requireOK(t, err)
	})
}

// TestJobControl_InvisibleJobReportsNotFound pins ground-truth item 5: job
// control checks visibility BEFORE capability, and an invisible job answers
// "not_found" (never "forbidden") so the op is not an existence oracle. The
// anonymous mode-none caller is the sharpest case: that caller holds Cancel
// and Retry by design, and before this check could cancel a job in a private
// project it could not even list.
func TestJobControl_InvisibleJobReportsNotFound(t *testing.T) {
	ops := map[string]func(ui *UiService, ctx context.Context, jobID string) error{
		"cancel": func(ui *UiService, ctx context.Context, jobID string) error {
			_, err := ui.CancelJob(ctx, csilapi.CancelJobRequest{JobId: jobID})
			return err
		},
		"kill": func(ui *UiService, ctx context.Context, jobID string) error {
			_, err := ui.KillJob(ctx, csilapi.KillJobRequest{JobId: jobID})
			return err
		},
		"retry": func(ui *UiService, ctx context.Context, jobID string) error {
			_, err := ui.RetryJob(ctx, csilapi.RetryJobRequest{JobId: jobID})
			return err
		},
	}

	for name, op := range ops {
		t.Run("anonymous in mode none/"+name, func(t *testing.T) {
			withAuthMode(t, config.UIAuthModeNone)
			deps, st := newTestDeps(t)
			_, job := newPrivateProjectJobFixture(t, st, "running")
			if name == "retry" {
				_, job = newPrivateProjectJobFixture(t, st, "failed")
			}
			ui := NewUiService(deps)
			requireCode(t, op(ui, anonCtx(), job.JobID), "not_found")
			// The job must be untouched: not_found is answered before any
			// state change.
			stored, err := st.GetJobByID(context.Background(), job.JobID)
			requireOK(t, err)
			if stored.Status != job.Status {
				t.Errorf("job status = %q after a refused %s, want %q", stored.Status, name, job.Status)
			}
		})

		t.Run("logged-in non-member/"+name, func(t *testing.T) {
			withAuthMode(t, config.UIAuthModeLocalRP)
			deps, st := newTestDeps(t)
			_, job := newPrivateProjectJobFixture(t, st, "running")
			outsider := st.putUser(models.User{UserID: "outsider-1"})
			ui := NewUiService(deps)
			ctx := mintSessionCtx(t, deps, outsider.UserID)
			requireCode(t, op(ui, ctx, job.JobID), "not_found")
		})
	}

	// The private project's org admin can see it, so the same ops go through
	// to the capability check and succeed -- or this test would pass for the
	// wrong reason.
	t.Run("org admin still controls the private job", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		_, job := newPrivateProjectJobFixture(t, st, "running")
		admin := st.putUser(models.User{UserID: "admin-1"})
		seedOrgAdmin(st, admin.UserID, "org-1")
		ui := NewUiService(deps)
		ctx := mintSessionCtx(t, deps, admin.UserID)
		_, err := ui.CancelJob(ctx, csilapi.CancelJobRequest{JobId: job.JobID})
		requireOK(t, err)
	})
}

// TestWorkflowControl_InvisibleWorkflowReportsNotFound is the workflow-op
// counterpart of TestJobControl_InvisibleJobReportsNotFound.
func TestWorkflowControl_InvisibleWorkflowReportsNotFound(t *testing.T) {
	newPrivateWorkflow := func(t *testing.T, st *fakeStore, status string) models.WorkflowInstance {
		t.Helper()
		proj, _ := newPrivateProjectJobFixture(t, st, "completed")
		return st.putWorkflow(models.WorkflowInstance{UserID: "org-1", OrgID: "org-1", ProjectID: &proj.ProjectID, Name: "wf", Status: status})
	}
	ops := map[string]func(ui *UiService, ctx context.Context, wfID string) error{
		"cancel-workflow": func(ui *UiService, ctx context.Context, wfID string) error {
			_, err := ui.CancelWorkflow(ctx, csilapi.CancelWorkflowRequest{WorkflowInstanceId: wfID})
			return err
		},
		"retry-workflow": func(ui *UiService, ctx context.Context, wfID string) error {
			_, err := ui.RetryWorkflow(ctx, csilapi.RetryWorkflowRequest{WorkflowInstanceId: wfID})
			return err
		},
		"retry-unsuccessful-jobs": func(ui *UiService, ctx context.Context, wfID string) error {
			_, err := ui.RetryUnsuccessfulJobs(ctx, csilapi.RetryUnsuccessfulJobsRequest{WorkflowInstanceId: wfID})
			return err
		},
	}
	for name, op := range ops {
		t.Run("anonymous in mode none/"+name, func(t *testing.T) {
			withAuthMode(t, config.UIAuthModeNone)
			deps, st := newTestDeps(t)
			status := "running"
			if name != "cancel-workflow" {
				status = "failed"
			}
			wf := newPrivateWorkflow(t, st, status)
			ui := NewUiService(deps)
			requireCode(t, op(ui, anonCtx(), wf.WorkflowID), "not_found")
		})
	}
}
