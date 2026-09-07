package authz

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// organizationLookup is the OPTIONAL store surface visibility uses to read
// an organization's is_private flag. It is deliberately not part of RoleStore:
// every existing RoleStore implementation (and every test fake) keeps
// compiling, and a store that lacks it falls back to the legacy users-table
// flag (see visibilityBatch.orgIsPrivate). *postgres_store.PostgresDbStore
// satisfies it.
type organizationLookup interface {
	GetOrganizationByID(ctx context.Context, orgID string) (*models.Organization, error)
}

// visibilityBatch amortizes role-assignment and owning-org lookups across
// many CanView*/FilterVisible* checks made for the same caller in one
// request: the caller's principal (role assignments) is loaded once, and
// owning-org / owning-user / project lookups are cached as they're
// discovered. This is the "avoid N+1: batch-load owning orgs" behavior
// FilterVisibleProjects (and friends) need.
type visibilityBatch struct {
	resolver    *Resolver
	id          Identity
	principal   *principal // nil for anonymous/unresolvable identities
	globalAdmin bool

	// orgLookup is nil when the resolver's store cannot resolve organizations
	// by id; orgIsPrivate then reads only the legacy users row.
	orgLookup organizationLookup

	userCache    map[string]*models.User
	orgCache     map[string]*models.Organization
	projectCache map[string]*models.Project
}

func (r *Resolver) newVisibilityBatch(ctx context.Context, id Identity) (*visibilityBatch, error) {
	vb := &visibilityBatch{
		resolver:     r,
		id:           id,
		userCache:    make(map[string]*models.User),
		orgCache:     make(map[string]*models.Organization),
		projectCache: make(map[string]*models.Project),
	}
	if lookup, ok := r.store.(organizationLookup); ok {
		vb.orgLookup = lookup
	}
	if !id.Anonymous && id.UserID != "" {
		p, err := r.loadPrincipal(ctx, id.UserID)
		if err != nil {
			return nil, err
		}
		vb.principal = p
		vb.globalAdmin = p.hasGlobalAdmin()
	}
	return vb, nil
}

func (vb *visibilityBatch) getUser(ctx context.Context, userID string) (*models.User, error) {
	if userID == "" {
		return nil, nil
	}
	if u, ok := vb.userCache[userID]; ok {
		return u, nil
	}
	u, err := vb.resolver.store.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			vb.userCache[userID] = nil
			return nil, nil
		}
		return nil, err
	}
	vb.userCache[userID] = u
	return u, nil
}

// getOrganization resolves an organizations row by id through the optional
// organizationLookup, caching misses as nil. A nil result means "no such
// organization row" (or no lookup available), never an error.
func (vb *visibilityBatch) getOrganization(ctx context.Context, orgID string) (*models.Organization, error) {
	if orgID == "" || vb.orgLookup == nil {
		return nil, nil
	}
	if o, ok := vb.orgCache[orgID]; ok {
		return o, nil
	}
	o, err := vb.orgLookup.GetOrganizationByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			vb.orgCache[orgID] = nil
			return nil, nil
		}
		return nil, err
	}
	vb.orgCache[orgID] = o
	return o, nil
}

// orgIsPrivate answers "is the org identified by orgID private": the
// organizations row's is_private when one exists, else the legacy users
// row's is_private for the same id (orgs backfilled from users share the
// user's UUID, so login-provisioned users with no organizations row still
// resolve), else false. An empty orgID is never private.
func (vb *visibilityBatch) orgIsPrivate(ctx context.Context, orgID string) (bool, error) {
	if orgID == "" {
		return false, nil
	}
	org, err := vb.getOrganization(ctx, orgID)
	if err != nil {
		return false, err
	}
	if org != nil {
		return org.IsPrivate, nil
	}
	owner, err := vb.getUser(ctx, orgID)
	if err != nil {
		return false, err
	}
	if owner != nil {
		return owner.IsPrivate, nil
	}
	return false, nil
}

func (vb *visibilityBatch) getProject(ctx context.Context, projectID string) (*models.Project, error) {
	if projectID == "" {
		return nil, nil
	}
	if p, ok := vb.projectCache[projectID]; ok {
		return p, nil
	}
	p, err := vb.resolver.store.GetProjectByID(ctx, projectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			vb.projectCache[projectID] = nil
			return nil, nil
		}
		return nil, err
	}
	vb.projectCache[projectID] = p
	return p, nil
}

// canViewPrivate is the shared "resource is private, can id see it anyway"
// check: the resource's own owner, project owners (direct or via group),
// project members (direct or via group, "if assigned" in the matrix), org
// admins of the owning org, and global admins can all see private resources;
// everyone else (including any anonymous caller) cannot.
func (vb *visibilityBatch) canViewPrivate(ctx context.Context, ownerUserID string, projectID *string) (bool, error) {
	if vb.id.Anonymous {
		return false, nil
	}
	if vb.id.UserID == "" {
		return vb.id.tokenAllows(ownerUserID, tokencaps.ProjectsRead) ||
			vb.id.tokenAllows(ownerUserID, tokencaps.JobsRead) ||
			vb.id.tokenAllows(ownerUserID, tokencaps.WorkflowsRead), nil
	}
	if ownerUserID != "" && ownerUserID == vb.id.UserID {
		return true, nil
	}
	if vb.globalAdmin {
		return true, nil
	}
	if vb.principal == nil {
		return false, nil
	}
	if ownerUserID != "" && vb.principal.hasOrgRole(ownerUserID, models.RoleAdmin) {
		return true, nil
	}
	if projectID != nil {
		if vb.principal.hasProjectRole(*projectID, models.RoleOwner) {
			return true, nil
		}
		if vb.principal.hasAnyProjectRole(*projectID) {
			return true, nil
		}
	}
	return false, nil
}

// canViewProject applies Project.IsEffectivelyPrivate (project.is_private OR
// the owning org's is_private) and, if private, canViewPrivate. The owning
// org is project.OwnershipOrgID() (org_id, falling back to the legacy
// user_id), so a project created through the REST API with a service token
// (user_id NULL, org_id set) is governed by its organization: that org's
// admins can see it when private, and that org's is_private cascades to it.
func (vb *visibilityBatch) canViewProject(ctx context.Context, project *models.Project) (bool, error) {
	ownerID := project.OwnershipOrgID()
	orgIsPrivate, err := vb.orgIsPrivate(ctx, ownerID)
	if err != nil {
		return false, err
	}
	if !project.IsEffectivelyPrivate(orgIsPrivate) {
		return true, nil
	}
	return vb.canViewPrivate(ctx, ownerID, &project.ProjectID)
}

// canViewOwned is the shared predicate for jobs/workflows: resources that
// belong to a project inherit that project's visibility in full (including
// project-member "if assigned" access); resources with no project (loose
// jobs, or a workflow with no project) are treated as belonging directly to
// their owning org (ownerOrgID, the resource's OwnershipOrgID()) and are
// visible if that org is not private, else to the owner/org-admins/
// global-admins only).
func (vb *visibilityBatch) canViewOwned(ctx context.Context, ownerOrgID string, projectID *string) (bool, error) {
	if projectID != nil && *projectID != "" {
		project, err := vb.getProject(ctx, *projectID)
		if err != nil {
			return false, err
		}
		if project != nil {
			return vb.canViewProject(ctx, project)
		}
		// Project referenced but no longer resolvable (deleted): fall through
		// to the org-only treatment below rather than fail open.
	}
	orgIsPrivate, err := vb.orgIsPrivate(ctx, ownerOrgID)
	if err != nil {
		return false, err
	}
	if !orgIsPrivate {
		return true, nil
	}
	return vb.canViewPrivate(ctx, ownerOrgID, nil)
}

// CanViewProject reports whether id may view project: public projects
// (project.IsPrivate false and the owning org not private) are visible to
// everyone; private projects are visible to assigned project members/owners,
// org admins of the owning org, and global admins.
func (r *Resolver) CanViewProject(ctx context.Context, id Identity, project *models.Project) (bool, error) {
	vb, err := r.newVisibilityBatch(ctx, id)
	if err != nil {
		return false, err
	}
	return vb.canViewProject(ctx, project)
}

// FilterVisibleProjects returns the subset of projects visible to id,
// preserving order. Batches the caller's role-assignment lookup and
// owning-org lookups (see visibilityBatch) so this is O(1) principal loads +
// O(distinct owning orgs) org/user loads rather than O(len(projects)) of either.
func (r *Resolver) FilterVisibleProjects(ctx context.Context, id Identity, projects []models.Project) ([]models.Project, error) {
	vb, err := r.newVisibilityBatch(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]models.Project, 0, len(projects))
	for i := range projects {
		ok, err := vb.canViewProject(ctx, &projects[i])
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, projects[i])
		}
	}
	return out, nil
}

// CanViewJob reports whether id may view job, via job.ProjectID's project
// visibility, or (for a project-less job) the owning org's visibility.
func (r *Resolver) CanViewJob(ctx context.Context, id Identity, job *models.Job) (bool, error) {
	vb, err := r.newVisibilityBatch(ctx, id)
	if err != nil {
		return false, err
	}
	return vb.canViewOwned(ctx, job.OwnershipOrgID(), job.ProjectID)
}

// FilterVisibleJobs returns the subset of jobs visible to id, preserving
// order. See FilterVisibleProjects for the batching rationale.
func (r *Resolver) FilterVisibleJobs(ctx context.Context, id Identity, jobs []models.Job) ([]models.Job, error) {
	vb, err := r.newVisibilityBatch(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]models.Job, 0, len(jobs))
	for i := range jobs {
		ok, err := vb.canViewOwned(ctx, jobs[i].OwnershipOrgID(), jobs[i].ProjectID)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, jobs[i])
		}
	}
	return out, nil
}

// CanViewWorkflowInstance reports whether id may view wf, via wf.ProjectID's
// project visibility, or (for a project-less workflow) the owning org's
// visibility.
func (r *Resolver) CanViewWorkflowInstance(ctx context.Context, id Identity, wf *models.WorkflowInstance) (bool, error) {
	vb, err := r.newVisibilityBatch(ctx, id)
	if err != nil {
		return false, err
	}
	return vb.canViewOwned(ctx, wf.OwnershipOrgID(), wf.ProjectID)
}

// CanViewWorkflowSummary is CanViewWorkflowInstance's counterpart for the
// denormalized models.WorkflowSummary read model (workflow_handler.go's
// ListWorkflows/GetWorkflow).
func (r *Resolver) CanViewWorkflowSummary(ctx context.Context, id Identity, summary *models.WorkflowSummary) (bool, error) {
	vb, err := r.newVisibilityBatch(ctx, id)
	if err != nil {
		return false, err
	}
	return vb.canViewOwned(ctx, summary.OwnershipOrgID(), summary.ProjectID)
}

// FilterVisibleWorkflowSummaries returns the subset of summaries visible to
// id, preserving order. See FilterVisibleProjects for the batching rationale.
func (r *Resolver) FilterVisibleWorkflowSummaries(ctx context.Context, id Identity, summaries []models.WorkflowSummary) ([]models.WorkflowSummary, error) {
	vb, err := r.newVisibilityBatch(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]models.WorkflowSummary, 0, len(summaries))
	for i := range summaries {
		ok, err := vb.canViewOwned(ctx, summaries[i].OwnershipOrgID(), summaries[i].ProjectID)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, summaries[i])
		}
	}
	return out, nil
}

// SettingsStore is the narrow store surface NewProjectIsPrivate needs.
type SettingsStore interface {
	GetGlobalSetting(ctx context.Context, key string) (*models.GlobalSetting, error)
}

// NewProjectIsPrivate reads the global_settings "new_projects_private" key
// (see models.GlobalSettingNewProjectsPrivate), returning false when the
// setting is absent, unreadable, or not a JSON boolean.
func NewProjectIsPrivate(ctx context.Context, s SettingsStore) bool {
	setting, err := s.GetGlobalSetting(ctx, models.GlobalSettingNewProjectsPrivate)
	if err != nil || setting == nil {
		return false
	}
	var v bool
	if err := json.Unmarshal(setting.Value, &v); err != nil {
		return false
	}
	return v
}
