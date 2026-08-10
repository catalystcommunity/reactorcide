package authz

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// Scope narrows a Capabilities computation to an org and/or a project. Leave
// both nil for the global scope (only GlobalAdmin-tier capabilities can ever
// be true there). Set ProjectID alone to have the project's owning org
// resolved automatically; set OrgID to skip that lookup, or to ask about
// org-level capabilities with no specific project in view.
type Scope struct {
	OrgID     *string
	ProjectID *string
}

// Caps is the full set of boolean capabilities a caller has at a Scope.
// Fields correspond 1:1 to the non-trivial rows of).
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
	// as Cancel — see jobcontrol.RetryJob/RetryWorkflow/RetryUnsuccessfulJobs.
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
	// ManageWorkers: manage worker pools, workers, enrollment tokens, and
	// queues — create/rename/delete queues, quarantine/disable/drain a
	// worker, pool + enrollment-token CRUD. Same org-admin/global-admin tier
	// as ManageSecrets/ ManageGroupsRoles; there is no separate "view"
	// capability for this surface.
	ManageWorkers bool
	// ProjectSettings: edit project settings (visibility, defaults).
	ProjectSettings bool
	// GlobalAdmin: the global-admin-only surface — trusted identities/domain
	// patterns, global settings.
	GlobalAdmin bool
}

// orgAdminCaps is what every org-admin-tier scope grants (matrix column "org
// admin", minus GlobalAdmin which only the true global-admin tier gets).
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
		ManageWorkers:        true,
		ProjectSettings:      true,
	}
}

// Capabilities computes id's full Caps at scope. mode is read from
// auth.CurrentMode to apply the anonymous-caller rows. In ModeNone,
// every caller is anonymous and may Cancel and Retry (trusted-LAN posture)
// but nothing else; in local-rp/rp mode, an anonymous (not-logged-in) caller
// gets an all-false Caps (view-public is implicit and unconditional, and is
// not represented in Caps).
func (r *Resolver) Capabilities(ctx context.Context, id Identity, scope Scope) (Caps, error) {
	if id.Anonymous {
		if auth.CurrentMode() == auth.ModeNone {
			return Caps{Cancel: true, Retry: true}, nil
		}
		return Caps{}, nil
	}

	var orgID *string = scope.OrgID
	if orgID == nil && scope.ProjectID != nil {
		project, err := r.store.GetProjectByID(ctx, *scope.ProjectID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return Caps{}, err
		}
		if project != nil {
			resolvedOrgID := project.OwnershipOrgID()
			orgID = &resolvedOrgID
		}
	}

	if id.UserID == "" {
		return capsFromToken(id, orgID), nil
	}

	p, err := r.loadPrincipal(ctx, id.UserID)
	if err != nil {
		return Caps{}, err
	}

	if p.hasGlobalAdmin() {
		caps := orgAdminCaps()
		caps.GlobalAdmin = true
		if id.isToken() {
			caps = intersectCapsWithToken(caps, id)
		}
		return caps, nil
	}

	if orgID != nil && (*orgID == id.UserID || p.hasOrgRole(*orgID, models.RoleAdmin)) {
		caps := orgAdminCaps()
		if id.isToken() {
			caps = intersectCapsWithToken(caps, id)
		}
		return caps, nil
	}

	if scope.ProjectID != nil && p.hasProjectRole(*scope.ProjectID, models.RoleOwner) {
		caps := Caps{ViewPrivate: true, Cancel: true, Retry: true, ProjectSettings: true}
		if id.isToken() {
			caps = intersectCapsWithToken(caps, id)
		}
		return caps, nil
	}

	viewPrivate := scope.ProjectID != nil && p.hasAnyProjectRole(*scope.ProjectID)
	caps := Caps{ViewPrivate: viewPrivate}
	if id.isToken() {
		caps = intersectCapsWithToken(caps, id)
	}
	return caps, nil
}

func capsFromToken(id Identity, orgID *string) Caps {
	if id.Token == nil || (orgID != nil && !id.Token.HasOrganization(*orgID)) {
		return Caps{}
	}
	if orgID == nil && !id.Token.AllOrganizations {
		return Caps{}
	}
	return capsForCapabilitySet(tokenCapabilities(id))
}

func intersectCapsWithToken(caps Caps, id Identity) Caps {
	tokenCaps := capsForCapabilitySet(tokenCapabilities(id))
	return Caps{
		ViewPrivate: caps.ViewPrivate && tokenCaps.ViewPrivate,
		Cancel:      caps.Cancel && tokenCaps.Cancel, Kill: caps.Kill && tokenCaps.Kill,
		Retry:                caps.Retry && tokenCaps.Retry,
		CreateProject:        caps.CreateProject && tokenCaps.CreateProject,
		DeleteProject:        caps.DeleteProject && tokenCaps.DeleteProject,
		ManageWebhookSecrets: caps.ManageWebhookSecrets && tokenCaps.ManageWebhookSecrets,
		ManageVCSCredentials: caps.ManageVCSCredentials && tokenCaps.ManageVCSCredentials,
		ManageSecrets:        caps.ManageSecrets && tokenCaps.ManageSecrets,
		ManageGroupsRoles:    caps.ManageGroupsRoles && tokenCaps.ManageGroupsRoles,
		ManageWorkers:        caps.ManageWorkers && tokenCaps.ManageWorkers,
		ProjectSettings:      caps.ProjectSettings && tokenCaps.ProjectSettings,
		GlobalAdmin:          caps.GlobalAdmin && tokenCaps.GlobalAdmin,
	}
}

func capsForCapabilitySet(set tokencaps.Set) Caps {
	return Caps{
		ViewPrivate: set.Has(tokencaps.OrganizationsRead) || set.Has(tokencaps.ProjectsRead) || set.Has(tokencaps.JobsRead) || set.Has(tokencaps.WorkflowsRead) || set.Has(tokencaps.LogsRead),
		Cancel:      set.Has(tokencaps.JobsCancel) || set.Has(tokencaps.WorkflowsControl),
		Kill:        set.Has(tokencaps.JobsCancel), Retry: set.Has(tokencaps.JobsRetry) || set.Has(tokencaps.WorkflowsControl),
		CreateProject: set.Has(tokencaps.ProjectsCreate), DeleteProject: set.Has(tokencaps.ProjectsManage),
		ManageWebhookSecrets: set.Has(tokencaps.SecretsManage), ManageVCSCredentials: set.Has(tokencaps.SecretsManage),
		ManageSecrets: set.Has(tokencaps.SecretsManage), ManageGroupsRoles: set.Has(tokencaps.OrganizationsManage),
		ManageWorkers: set.Has(tokencaps.WorkersManage), ProjectSettings: set.Has(tokencaps.ProjectsManage),
		GlobalAdmin: set.Has(tokencaps.OrganizationsManage),
	}
}
