package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// UiService implements csilapi.ReactorcideUi. Methods are split across
// ui_*.go files by resource family (projects, groups, roles, rotation,
// secrets, jobcontrol, settings) but all share this one receiver type and
// its embedded Deps.
type UiService struct {
	deps *Deps
}

// NewUiService constructs a UiService.
func NewUiService(deps *Deps) *UiService {
	return &UiService{deps: deps}
}

var _ csilapi.ReactorcideUi = (*UiService)(nil)

// GetCapabilities computes the caller's full capability set at the
// requested scope (see authz.Capabilities) and maps it to the generated
// response shape, plus the three named role-tier booleans
// (is_global_admin/is_org_admin/is_project_owner) the Caps struct itself
// doesn't carry.
func (s *UiService) GetCapabilities(ctx context.Context, req csilapi.GetCapabilitiesRequest) (csilapi.GetCapabilitiesResponse, error) {
	id, _ := s.deps.resolveIdentity(ctx)

	caps, err := s.deps.Resolver.Capabilities(ctx, id, authz.Scope{OrgID: req.OrgId, ProjectID: req.ProjectId})
	if err != nil {
		return csilapi.GetCapabilitiesResponse{}, NewServiceError("internal", "an internal error occurred")
	}

	isGlobalAdmin, err := s.deps.Resolver.IsGlobalAdmin(ctx, id)
	if err != nil {
		return csilapi.GetCapabilitiesResponse{}, NewServiceError("internal", "an internal error occurred")
	}

	orgID := req.OrgId
	if orgID == nil && req.ProjectId != nil {
		if project, err := s.deps.Store.GetProjectByID(ctx, *req.ProjectId); err == nil {
			orgID = project.UserID
		}
	}
	isOrgAdmin := false
	if orgID != nil {
		isOrgAdmin, err = s.deps.Resolver.IsOrgAdmin(ctx, id, *orgID)
		if err != nil {
			return csilapi.GetCapabilitiesResponse{}, NewServiceError("internal", "an internal error occurred")
		}
	}
	isProjectOwner := false
	if req.ProjectId != nil {
		isProjectOwner, err = s.deps.Resolver.IsProjectOwner(ctx, id, *req.ProjectId)
		if err != nil {
			return csilapi.GetCapabilitiesResponse{}, NewServiceError("internal", "an internal error occurred")
		}
	}

	return csilapi.GetCapabilitiesResponse{
		ViewPrivate:             caps.ViewPrivate,
		CancelJob:               caps.Cancel,
		KillJob:                 caps.Kill,
		RetryJob:                caps.Retry,
		CreateProject:           caps.CreateProject,
		DeleteProject:           caps.DeleteProject,
		ManageWebhookSecrets:    caps.ManageWebhookSecrets,
		ManageVcsCredentials:    caps.ManageVCSCredentials,
		ManageSecrets:           caps.ManageSecrets,
		ManageGroups:            caps.ManageGroupsRoles,
		ManageProjectSettings:   caps.ProjectSettings,
		ManageTrustedIdentities: caps.GlobalAdmin,
		ManageGlobalSettings:    caps.GlobalAdmin,
		IsGlobalAdmin:           isGlobalAdmin,
		IsOrgAdmin:              isOrgAdmin,
		IsProjectOwner:          isProjectOwner,
	}, nil
}
