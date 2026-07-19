package uiapi

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// TestCancelJob_PermissionMatrix drives UI_AUTH_PLAN.md's permission matrix
// "cancel job/workflow" row across every caller tier and both relevant auth
// modes: anonymous may cancel ONLY in mode none; a plain member may never
// cancel; project owner/org admin/global admin may always cancel.
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
// available to anonymous callers (in ANY auth mode, including none — kill
// is a strictly stronger action than cancel), never to a plain member or
// even a project owner; only org admin/global admin.
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
// ("trusted users/domain-regexes, global settings"): global admin only —
// even an org admin (of any org) may not manage the admission list.
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

	// GetCapabilities never requires a session (anonymous callers get a
	// real, if empty/limited, capability set) — an invalid token there
	// degrades to anonymous rather than erroring.
	as := NewUiService(deps)
	capsResp, err := as.GetCapabilities(ctx, csilapi.GetCapabilitiesRequest{})
	requireOK(t, err)
	if capsResp.IsGlobalAdmin {
		t.Errorf("IsGlobalAdmin = true for an invalid session token, want false")
	}
}
