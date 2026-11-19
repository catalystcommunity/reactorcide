package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func (s *UiService) recordAudit(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB) {
	if checkauth.GetPrincipalFromContext(ctx) == nil {
		// An op that did not keep the context of requireManagementIdentity:
		// resolve the token again, so the audit names the token credential.
		if withPrincipal, _, _, err := s.deps.requireManagementIdentity(ctx); err == nil {
			ctx = withPrincipal
		}
	}
	_, user := s.deps.resolveIdentity(ctx)
	audit.RecordUser(ctx, s.deps.Store, user, orgID, action, subjectType, subjectID, details)
}

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

	scope := authz.Scope{OrgID: req.OrgId, ProjectID: req.ProjectId}
	if req.WorkflowId != nil && *req.WorkflowId != "" {
		// WP7: one workflow. Its owners can cancel and retry it. A workflow
		// the caller cannot view reports "not found".
		wig, ok := s.deps.Store.(workflowInstanceGetter)
		if !ok {
			return csilapi.GetCapabilitiesResponse{}, NewServiceError("internal", "workflows are not supported by this server's store configuration")
		}
		wf, err := wig.GetWorkflowInstance(ctx, *req.WorkflowId)
		if err != nil {
			return csilapi.GetCapabilitiesResponse{}, mapStoreErr(err, "workflow not found")
		}
		if err := s.requireWorkflowVisible(ctx, id, wf); err != nil {
			return csilapi.GetCapabilitiesResponse{}, err
		}
		scope = workflowScope(wf)
		req.ProjectId, req.OrgId = scope.ProjectID, scope.OrgID
	}
	caps, err := s.deps.Resolver.Capabilities(ctx, id, scope)
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
			// The owning org is projects.org_id (OwnershipOrgID), not the
			// legacy user_id column, which is nil/empty for REST-created
			// projects and is a different UUID from the org for the rest.
			owningOrg := project.OwnershipOrgID()
			orgID = &owningOrg
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

	// The role flags of a token are limited by the token, like caps: the
	// admin capability of the scope must be in the token.
	if id.Token != nil {
		isGlobalAdmin = isGlobalAdmin && requireTokenGrant(id, nil, tokencaps.OrganizationsManage) == nil
		isOrgAdmin = isOrgAdmin && requireTokenGrant(id, orgID, tokencaps.OrganizationsManage) == nil
		isProjectOwner = isProjectOwner && orgID != nil && requireTokenGrant(id, orgID, tokencaps.ProjectsManage) == nil
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
		ManageWorkers:           caps.ManageWorkers,
		ManageProjectSettings:   caps.ProjectSettings,
		ManageTrustedIdentities: caps.GlobalAdmin,
		ManageGlobalSettings:    caps.GlobalAdmin,
		IsGlobalAdmin:           isGlobalAdmin,
		IsOrgAdmin:              isOrgAdmin,
		IsProjectOwner:          isProjectOwner,
		ManageWorkflow:          optionalBool(caps.ManageWorkflow),
	}, nil
}
