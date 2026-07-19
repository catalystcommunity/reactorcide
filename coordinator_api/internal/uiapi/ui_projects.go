package uiapi

import (
	"context"
	"sort"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// listAllLimit bounds the "list everything, filter client-side" queries this
// file uses (ListOrgs' project scan, ListProjects with no org filter) — a
// generous ceiling rather than real pagination, matching UI_AUTH_PLAN.md's
// "keep it simple" guidance for list-orgs. See ListOrgs' doc comment for the
// full rationale.
const listAllLimit = 10000

func projectToSummary(p *models.Project) csilapi.ProjectSummary {
	orgID := ""
	if p.UserID != nil {
		orgID = *p.UserID
	}
	return csilapi.ProjectSummary{
		ProjectId:   p.ProjectID,
		OrgId:       orgID,
		Name:        p.Name,
		Description: p.Description,
		RepoUrl:     p.RepoURL,
		IsPrivate:   p.IsPrivate,
		Enabled:     p.Enabled,
		CreatedAt:   formatTime(p.CreatedAt),
		UpdatedAt:   formatTime(p.UpdatedAt),
	}
}

func projectToDetail(p *models.Project) csilapi.ProjectDetail {
	orgID := ""
	if p.UserID != nil {
		orgID = *p.UserID
	}
	return csilapi.ProjectDetail{
		ProjectId:             p.ProjectID,
		OrgId:                 orgID,
		Name:                  p.Name,
		Description:           p.Description,
		RepoUrl:               p.RepoURL,
		IsPrivate:             p.IsPrivate,
		Enabled:               p.Enabled,
		TargetBranches:        append([]string{}, p.TargetBranches...),
		AllowedEventTypes:     append([]string{}, p.AllowedEventTypes...),
		DefaultCiSourceType:   string(p.DefaultCISourceType),
		DefaultCiSourceUrl:    p.DefaultCISourceURL,
		DefaultCiSourceRef:    p.DefaultCISourceRef,
		DefaultRunnerImage:    p.DefaultRunnerImage,
		DefaultJobCommand:     p.DefaultJobCommand,
		DefaultTimeoutSeconds: int64(p.DefaultTimeoutSeconds),
		DefaultQueueName:      p.DefaultQueueName,
		CreatedAt:             formatTime(p.CreatedAt),
		UpdatedAt:             formatTime(p.UpdatedAt),
	}
}

// ListOrgs lists every org (user acting as an org) the caller may see: the
// distinct owning orgs of every visibility-filtered project, plus every org
// the caller directly belongs to (their own org, and any org they hold a
// direct role_assignments row in). This is a deliberately simple
// approximation — there is no first-class orgs table this schema version
// (see UI_AUTH_PLAN.md's "no orgs table" note), so "every org" has no
// authoritative source short of "every users row", which would leak
// unrelated users' existence. Group-derived org membership is not included
// (only direct user role_assignments) to keep this to one query beyond the
// project scan.
func (s *UiService) ListOrgs(ctx context.Context, req csilapi.ListOrgsRequest) (csilapi.ListOrgsResponse, error) {
	id, user := s.deps.resolveIdentity(ctx)

	projects, err := s.deps.Store.ListProjects(ctx, listAllLimit, 0)
	if err != nil {
		return csilapi.ListOrgsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	visible, err := s.deps.Resolver.FilterVisibleProjects(ctx, id, projects)
	if err != nil {
		return csilapi.ListOrgsResponse{}, NewServiceError("internal", "an internal error occurred")
	}

	orgIDs := map[string]struct{}{}
	for i := range visible {
		if visible[i].UserID != nil && *visible[i].UserID != "" {
			orgIDs[*visible[i].UserID] = struct{}{}
		}
	}

	if user != nil {
		orgIDs[user.UserID] = struct{}{}
		assignments, err := s.deps.Store.ListRoleAssignmentsForPrincipal(ctx, user.UserID, nil)
		if err == nil {
			for _, a := range assignments {
				if a.ScopeType == models.ScopeTypeOrg && a.ScopeID != nil && *a.ScopeID != "" {
					orgIDs[*a.ScopeID] = struct{}{}
				}
			}
		}
	}

	orgs := make([]csilapi.OrgSummary, 0, len(orgIDs))
	for orgID := range orgIDs {
		owner, err := s.deps.Store.GetUserByID(ctx, orgID)
		if err != nil {
			continue
		}
		orgs = append(orgs, csilapi.OrgSummary{OrgId: owner.UserID, Name: owner.Username, IsPrivate: owner.IsPrivate})
	}
	sort.Slice(orgs, func(i, j int) bool { return orgs[i].Name < orgs[j].Name })

	return csilapi.ListOrgsResponse{Orgs: orgs}, nil
}

// ListProjects lists visibility-filtered projects, optionally scoped to one
// org.
func (s *UiService) ListProjects(ctx context.Context, req csilapi.ListProjectsRequest) (csilapi.ListProjectsResponse, error) {
	id, _ := s.deps.resolveIdentity(ctx)

	var (
		projects []models.Project
		err      error
	)
	if req.OrgId != nil && *req.OrgId != "" {
		projects, err = s.deps.Store.ListProjectsByOrg(ctx, *req.OrgId, listAllLimit, 0)
	} else {
		projects, err = s.deps.Store.ListProjects(ctx, listAllLimit, 0)
	}
	if err != nil {
		return csilapi.ListProjectsResponse{}, NewServiceError("internal", "an internal error occurred")
	}

	visible, err := s.deps.Resolver.FilterVisibleProjects(ctx, id, projects)
	if err != nil {
		return csilapi.ListProjectsResponse{}, NewServiceError("internal", "an internal error occurred")
	}

	out := make([]csilapi.ProjectSummary, len(visible))
	for i := range visible {
		out[i] = projectToSummary(&visible[i])
	}
	return csilapi.ListProjectsResponse{Projects: out}, nil
}

// GetProject fetches one project, applying visibility. A project that
// exists but is not visible to the caller reports not_found rather than
// forbidden, so an unauthorized caller can't distinguish "doesn't exist"
// from "exists but private".
func (s *UiService) GetProject(ctx context.Context, req csilapi.GetProjectRequest) (csilapi.GetProjectResponse, error) {
	if err := requireNonEmpty("project_id", req.ProjectId, 64); err != nil {
		return csilapi.GetProjectResponse{}, err
	}
	id, _ := s.deps.resolveIdentity(ctx)

	project, err := s.deps.Store.GetProjectByID(ctx, req.ProjectId)
	if err != nil {
		return csilapi.GetProjectResponse{}, mapStoreErr(err, "project not found")
	}
	visible, err := s.deps.Resolver.CanViewProject(ctx, id, project)
	if err != nil {
		return csilapi.GetProjectResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !visible {
		return csilapi.GetProjectResponse{}, NewServiceError("not_found", "project not found")
	}
	return csilapi.GetProjectResponse{Project: projectToDetail(project)}, nil
}

// CreateProject requires org admin (of org_id) or global admin. When the
// request omits is_private, the global new_projects_private setting decides
// the default (see authz.NewProjectIsPrivate).
func (s *UiService) CreateProject(ctx context.Context, req csilapi.CreateProjectRequest) (csilapi.CreateProjectResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.CreateProjectResponse{}, authErr
	}
	if err := requireNonEmpty("org_id", req.OrgId, 64); err != nil {
		return csilapi.CreateProjectResponse{}, err
	}
	if err := requireNonEmpty("name", req.Name, maxNameLength); err != nil {
		return csilapi.CreateProjectResponse{}, err
	}
	if err := requireNonEmpty("repo_url", req.RepoUrl, 2048); err != nil {
		return csilapi.CreateProjectResponse{}, err
	}

	if err := s.deps.Resolver.RequireOrgAdmin(ctx, id, req.OrgId); err != nil {
		return csilapi.CreateProjectResponse{}, mapPermissionErr(err)
	}

	isPrivate := authz.NewProjectIsPrivate(ctx, s.deps.Store)
	if req.IsPrivate != nil {
		isPrivate = *req.IsPrivate
	}

	orgID := req.OrgId
	project := &models.Project{
		UserID:      &orgID,
		Name:        req.Name,
		Description: derefOr(req.Description, ""),
		RepoURL:     req.RepoUrl,
		IsPrivate:   isPrivate,
		Enabled:     true,
	}
	if req.TargetBranches != nil {
		project.TargetBranches = req.TargetBranches
	}
	if req.AllowedEventTypes != nil {
		project.AllowedEventTypes = req.AllowedEventTypes
	}

	if err := s.deps.Store.CreateProject(ctx, project); err != nil {
		return csilapi.CreateProjectResponse{}, NewServiceError("internal", "failed to create project")
	}
	return csilapi.CreateProjectResponse{Project: projectToDetail(project)}, nil
}

// UpdateProject requires project-settings capability: project owner, org
// admin of the owning org, or global admin.
func (s *UiService) UpdateProject(ctx context.Context, req csilapi.UpdateProjectRequest) (csilapi.UpdateProjectResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.UpdateProjectResponse{}, authErr
	}
	if err := requireNonEmpty("project_id", req.ProjectId, 64); err != nil {
		return csilapi.UpdateProjectResponse{}, err
	}

	project, err := s.deps.Store.GetProjectByID(ctx, req.ProjectId)
	if err != nil {
		return csilapi.UpdateProjectResponse{}, mapStoreErr(err, "project not found")
	}
	if err := s.deps.Resolver.RequireProjectOwner(ctx, id, req.ProjectId); err != nil {
		return csilapi.UpdateProjectResponse{}, mapPermissionErr(err)
	}

	if req.Name != nil {
		if err := requireNonEmpty("name", *req.Name, maxNameLength); err != nil {
			return csilapi.UpdateProjectResponse{}, err
		}
		project.Name = *req.Name
	}
	if req.Description != nil {
		project.Description = *req.Description
	}
	if req.IsPrivate != nil {
		project.IsPrivate = *req.IsPrivate
	}
	if req.Enabled != nil {
		project.Enabled = *req.Enabled
	}
	if req.TargetBranches != nil {
		project.TargetBranches = req.TargetBranches
	}
	if req.AllowedEventTypes != nil {
		project.AllowedEventTypes = req.AllowedEventTypes
	}
	if req.DefaultRunnerImage != nil {
		project.DefaultRunnerImage = *req.DefaultRunnerImage
	}
	if req.DefaultJobCommand != nil {
		project.DefaultJobCommand = *req.DefaultJobCommand
	}
	if req.DefaultTimeoutSeconds != nil {
		if *req.DefaultTimeoutSeconds <= 0 {
			return csilapi.UpdateProjectResponse{}, NewServiceError("invalid_argument", "default_timeout_seconds must be positive")
		}
		project.DefaultTimeoutSeconds = int(*req.DefaultTimeoutSeconds)
	}
	if req.DefaultQueueName != nil {
		project.DefaultQueueName = *req.DefaultQueueName
	}

	if err := s.deps.Store.UpdateProject(ctx, project); err != nil {
		return csilapi.UpdateProjectResponse{}, NewServiceError("internal", "failed to update project")
	}
	return csilapi.UpdateProjectResponse{Project: projectToDetail(project)}, nil
}

// DeleteProject requires org admin (of the owning org) or global admin —
// stricter than UpdateProject: a plain project owner may not delete it.
func (s *UiService) DeleteProject(ctx context.Context, req csilapi.DeleteProjectRequest) (csilapi.DeleteProjectResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.DeleteProjectResponse{}, authErr
	}
	if err := requireNonEmpty("project_id", req.ProjectId, 64); err != nil {
		return csilapi.DeleteProjectResponse{}, err
	}

	project, err := s.deps.Store.GetProjectByID(ctx, req.ProjectId)
	if err != nil {
		return csilapi.DeleteProjectResponse{}, mapStoreErr(err, "project not found")
	}

	if project.UserID == nil {
		if err := s.deps.Resolver.RequireGlobalAdmin(ctx, id); err != nil {
			return csilapi.DeleteProjectResponse{}, mapPermissionErr(err)
		}
	} else if err := s.deps.Resolver.RequireOrgAdmin(ctx, id, *project.UserID); err != nil {
		return csilapi.DeleteProjectResponse{}, mapPermissionErr(err)
	}

	if err := s.deps.Store.DeleteProject(ctx, req.ProjectId); err != nil {
		return csilapi.DeleteProjectResponse{}, mapStoreErr(err, "project not found")
	}
	return csilapi.DeleteProjectResponse{Deleted: true}, nil
}
