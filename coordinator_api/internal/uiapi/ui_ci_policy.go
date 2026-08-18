package uiapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func ciPolicyToDetail(policy *models.CIPolicy) (csilapi.CiPolicyDetail, error) {
	document, err := json.Marshal(policy.Document)
	if err != nil {
		return csilapi.CiPolicyDetail{}, err
	}
	return csilapi.CiPolicyDetail{
		PolicyId: policy.PolicyID, ProjectId: policy.ProjectID,
		Revision: policy.Revision, Document: string(document),
		CreatedAt: formatTime(policy.CreatedAt), UpdatedAt: formatTime(policy.UpdatedAt),
		UpdatedBy: policy.UpdatedBy,
	}, nil
}

func (s *UiService) GetCiPolicy(ctx context.Context, req csilapi.GetCiPolicyRequest) (csilapi.GetCiPolicyResponse, error) {
	ctx, id, _, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.GetCiPolicyResponse{}, authErr
	}
	if err := requireNonEmpty("project_id", req.ProjectId, 64); err != nil {
		return csilapi.GetCiPolicyResponse{}, err
	}
	if err := s.deps.Resolver.RequireProjectPolicyManager(ctx, id, req.ProjectId); err != nil {
		return csilapi.GetCiPolicyResponse{}, mapPermissionErr(err)
	}
	policy, err := s.deps.Store.GetCIPolicyByProject(ctx, req.ProjectId)
	if err != nil {
		return csilapi.GetCiPolicyResponse{}, mapStoreErr(err, "CI policy not found")
	}
	detail, err := ciPolicyToDetail(policy)
	if err != nil {
		return csilapi.GetCiPolicyResponse{}, NewServiceError("internal", "failed to encode CI policy")
	}
	return csilapi.GetCiPolicyResponse{Policy: detail}, nil
}

func (s *UiService) PutCiPolicy(ctx context.Context, req csilapi.PutCiPolicyRequest) (csilapi.PutCiPolicyResponse, error) {
	ctx, id, user, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.PutCiPolicyResponse{}, authErr
	}
	if err := requireNonEmpty("project_id", req.ProjectId, 64); err != nil {
		return csilapi.PutCiPolicyResponse{}, err
	}
	if err := requireNonEmpty("document", req.Document, 1024*1024); err != nil {
		return csilapi.PutCiPolicyResponse{}, err
	}
	project, err := s.deps.Store.GetProjectByID(ctx, req.ProjectId)
	if err != nil {
		return csilapi.PutCiPolicyResponse{}, mapStoreErr(err, "project not found")
	}
	if err := s.deps.Resolver.RequireProjectPolicyManager(ctx, id, req.ProjectId); err != nil {
		return csilapi.PutCiPolicyResponse{}, mapPermissionErr(err)
	}

	parsed, err := cipolicy.ParseDocument([]byte(req.Document))
	if err != nil {
		return csilapi.PutCiPolicyResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	canonical, err := cipolicy.CanonicalDocument(parsed)
	if err != nil {
		return csilapi.PutCiPolicyResponse{}, NewServiceError("internal", "failed to encode CI policy")
	}
	var document models.JSONB
	if err := json.Unmarshal(canonical, &document); err != nil {
		return csilapi.PutCiPolicyResponse{}, NewServiceError("internal", "failed to encode CI policy")
	}

	current, getErr := s.deps.Store.GetCIPolicyByProject(ctx, req.ProjectId)
	if getErr != nil && !errors.Is(getErr, store.ErrNotFound) {
		return csilapi.PutCiPolicyResponse{}, NewServiceError("internal", "failed to read CI policy")
	}
	if req.ExpectedRevision != nil {
		if current == nil || current.Revision != *req.ExpectedRevision {
			return csilapi.PutCiPolicyResponse{}, NewServiceError("conflict", "CI policy revision changed")
		}
	}
	var updatedBy *string
	if user != nil {
		updatedBy = &user.UserID
	}
	policy := &models.CIPolicy{
		OrgID: project.OwnershipOrgID(), ProjectID: project.ProjectID,
		Document: document, Revision: parsed.Revision, UpdatedBy: updatedBy,
	}
	if err := s.deps.Store.UpsertCIPolicy(ctx, policy, req.ExpectedRevision); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return csilapi.PutCiPolicyResponse{}, NewServiceError("conflict", "CI policy revision changed")
		}
		return csilapi.PutCiPolicyResponse{}, NewServiceError("internal", "failed to store CI policy")
	}
	stored, err := s.deps.Store.GetCIPolicyByProject(ctx, req.ProjectId)
	if err != nil {
		return csilapi.PutCiPolicyResponse{}, NewServiceError("internal", "failed to read stored CI policy")
	}
	details := models.JSONB{"revision": stored.Revision}
	if current != nil {
		details["previous_revision"] = current.Revision
	}
	audit.RecordUser(ctx, s.deps.Store, user, project.OwnershipOrgID(), "ci_policy.updated", "project", project.ProjectID, details)
	detail, err := ciPolicyToDetail(stored)
	if err != nil {
		return csilapi.PutCiPolicyResponse{}, NewServiceError("internal", "failed to encode CI policy")
	}
	return csilapi.PutCiPolicyResponse{Policy: detail}, nil
}

func (s *UiService) DeleteCiPolicy(ctx context.Context, req csilapi.DeleteCiPolicyRequest) (csilapi.DeleteCiPolicyResponse, error) {
	ctx, id, user, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.DeleteCiPolicyResponse{}, authErr
	}
	if err := requireNonEmpty("project_id", req.ProjectId, 64); err != nil {
		return csilapi.DeleteCiPolicyResponse{}, err
	}
	project, err := s.deps.Store.GetProjectByID(ctx, req.ProjectId)
	if err != nil {
		return csilapi.DeleteCiPolicyResponse{}, mapStoreErr(err, "project not found")
	}
	if err := s.deps.Resolver.RequireProjectPolicyManager(ctx, id, req.ProjectId); err != nil {
		return csilapi.DeleteCiPolicyResponse{}, mapPermissionErr(err)
	}
	current, err := s.deps.Store.GetCIPolicyByProject(ctx, req.ProjectId)
	if err != nil {
		return csilapi.DeleteCiPolicyResponse{}, mapStoreErr(err, "CI policy not found")
	}
	if req.ExpectedRevision != nil && current.Revision != *req.ExpectedRevision {
		return csilapi.DeleteCiPolicyResponse{}, NewServiceError("conflict", "CI policy revision changed")
	}
	if err := s.deps.Store.DeleteCIPolicy(ctx, req.ProjectId, req.ExpectedRevision); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return csilapi.DeleteCiPolicyResponse{}, NewServiceError("conflict", "CI policy revision changed")
		}
		return csilapi.DeleteCiPolicyResponse{}, mapStoreErr(err, "CI policy not found")
	}
	audit.RecordUser(ctx, s.deps.Store, user, project.OwnershipOrgID(), "ci_policy.deleted", "project", project.ProjectID, models.JSONB{"revision": current.Revision})
	return csilapi.DeleteCiPolicyResponse{Deleted: true}, nil
}
