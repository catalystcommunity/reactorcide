package authz

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Scope narrows a Capabilities computation to an org and/or a project.
// Leave both nil for the global scope (only GlobalAdmin-tier capabilities
// can ever be true there). Set ProjectID alone to have the project's owning
// org resolved automatically; set OrgID to skip that lookup, or to ask
// about org-level capabilities with no specific project in view.
type Scope struct {
	OrgID     *string
	ProjectID *string
}

// Caps is the full set of boolean capabilities a caller has at a Scope.
// Fields correspond 1:1 to the non-trivial rows of UI_AUTH_PLAN.md's
// permission matrix ("view public" is omitted — it is unconditionally true
// for every caller and isn't gated by anything in this struct).
type Caps struct {
	// ViewPrivate: view private orgs/projects/jobs/workflows/logs within
	// Scope. See CanViewProject/CanViewJob/etc for the actual per-resource
	// predicate — this field answers "does the caller have some private
	// visibility at this scope at all", useful for UI affordances.
	ViewPrivate bool
	// Cancel: graceful cancel of a job/workflow (cleanup hooks run).
	Cancel bool
	// Kill: forced/admin kill of a job (no cleanup guarantee).
	Kill bool
	// Retry: retry a failed/cancelled/timeout job, or a failed/cancelled
	// workflow (single job, a whole workflow as a fresh instance, or every
	// unsuccessful member job of a workflow in place). Same permission tier
	// as Cancel — see
	// jobcontrol.RetryJob/RetryWorkflow/RetryUnsuccessfulJobs and
	// UI_AUTH_PLAN.md's permission matrix, which lists retry alongside
	// cancel.
	Retry bool
	// CreateProject: create a new project in Scope's org.
	CreateProject bool
	// DeleteProject: delete Scope's project.
	DeleteProject bool
	// ManageWebhookSecrets: add/rotate/deactivate project webhook secrets.
	ManageWebhookSecrets bool
	// ManageVCSCredentials: add/rotate project VCS credentials.
	ManageVCSCredentials bool
	// ManageSecrets: set/delete secrets (write-only) and manage secret
	// grants.
	ManageSecrets bool
	// ManageGroupsRoles: manage groups and assign/revoke role assignments.
	ManageGroupsRoles bool
	// ProjectSettings: edit project settings (visibility, defaults).
	ProjectSettings bool
	// GlobalAdmin: the global-admin-only surface — trusted
	// identities/domain patterns, global settings.
	GlobalAdmin bool
}

// orgAdminCaps is what every org-admin-tier scope grants (matrix column
// "org admin", minus GlobalAdmin which only the true global-admin tier
// gets).
func orgAdminCaps() Caps {
	return Caps{
		ViewPrivate:          true,
		Cancel:               true,
		Kill:                 true,
		Retry:                true,
		CreateProject:        true,
		DeleteProject:        true,
		ManageWebhookSecrets: true,
		ManageVCSCredentials: true,
		ManageSecrets:        true,
		ManageGroupsRoles:    true,
		ProjectSettings:      true,
	}
}

// Capabilities computes id's full Caps at scope, per UI_AUTH_PLAN.md's
// permission matrix. mode is read from auth.CurrentMode() (Task C) to apply
// the anonymous-caller rows: in ModeNone, every caller is anonymous and may
// Cancel and Retry (trusted-LAN posture) but nothing else; in local-rp/rp
// mode, an anonymous (not-logged-in) caller gets an all-false Caps
// (view-public is implicit and unconditional, and is not represented in
// Caps).
func (r *Resolver) Capabilities(ctx context.Context, id Identity, scope Scope) (Caps, error) {
	if id.Anonymous || id.UserID == "" {
		if auth.CurrentMode() == auth.ModeNone {
			return Caps{Cancel: true, Retry: true}, nil
		}
		return Caps{}, nil
	}

	p, err := r.loadPrincipal(ctx, id.UserID)
	if err != nil {
		return Caps{}, err
	}

	if p.hasGlobalAdmin() {
		caps := orgAdminCaps()
		caps.GlobalAdmin = true
		return caps, nil
	}

	orgID := scope.OrgID
	if orgID == nil && scope.ProjectID != nil {
		project, err := r.store.GetProjectByID(ctx, *scope.ProjectID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return Caps{}, err
		}
		if project != nil {
			orgID = project.UserID
		}
	}

	if orgID != nil && (*orgID == id.UserID || p.hasOrgRole(*orgID, models.RoleAdmin)) {
		return orgAdminCaps(), nil
	}

	if scope.ProjectID != nil && p.hasProjectRole(*scope.ProjectID, models.RoleOwner) {
		return Caps{ViewPrivate: true, Cancel: true, Retry: true, ProjectSettings: true}, nil
	}

	viewPrivate := scope.ProjectID != nil && p.hasAnyProjectRole(*scope.ProjectID)
	return Caps{ViewPrivate: viewPrivate}, nil
}
