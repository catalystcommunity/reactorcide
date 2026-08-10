package uiapi

import (
	"context"
	"errors"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/lib/pq"
)

type uiCISecurityStore interface {
	GetOrganizationByName(context.Context, string) (*models.Organization, error)
	CreateExecutionProfile(context.Context, *models.ExecutionProfile) error
	GetExecutionProfile(context.Context, string, string) (*models.ExecutionProfile, error)
	ListExecutionProfiles(context.Context, string) ([]models.ExecutionProfile, error)
	UpdateExecutionProfile(context.Context, *models.ExecutionProfile) error
	DeleteExecutionProfile(context.Context, string, string) error
	CreateWorkerClass(context.Context, *models.WorkerClass) error
	GetWorkerClass(context.Context, string, string) (*models.WorkerClass, error)
	ListWorkerClasses(context.Context, string) ([]models.WorkerClass, error)
	UpdateWorkerClass(context.Context, *models.WorkerClass) error
	DeleteWorkerClass(context.Context, string) error
	GrantWorkerClassPool(context.Context, string, string) error
	RevokeWorkerClassPool(context.Context, string, string) error
	ListPoolsForWorkerClass(context.Context, string) ([]models.WorkerPool, error)
	GetWorkerPoolByID(context.Context, string) (*models.WorkerPool, error)
	GetProjectByOrgAndName(context.Context, string, string) (*models.Project, error)
	CreateCIApproval(context.Context, *models.CIApproval) error
	ListGroupsForUser(context.Context, string) ([]models.Group, error)
}

func (s *UiService) ciSecurityStore() (uiCISecurityStore, error) {
	value, ok := s.deps.Store.(uiCISecurityStore)
	if !ok {
		return nil, NewServiceError("internal", "CI security management is not available")
	}
	return value, nil
}

func (s *UiService) requireCIOrgAdmin(ctx context.Context, organization string) (uiCISecurityStore, *models.Organization, error) {
	id, _, err := s.deps.requireUser(ctx)
	if err != nil {
		return nil, nil, err
	}
	value, err := s.ciSecurityStore()
	if err != nil {
		return nil, nil, err
	}
	org, err := value.GetOrganizationByName(ctx, organization)
	if err != nil {
		return nil, nil, mapStoreErr(err, "organization not found")
	}
	if err := s.deps.Resolver.RequireOrgAdmin(ctx, id, org.OrgID); err != nil {
		return nil, nil, mapPermissionErr(err)
	}
	return value, org, nil
}

func profileSummary(profile *models.ExecutionProfile) csilapi.ExecutionProfileSummary {
	var ceiling *int64
	if profile.TimeoutCeilingSeconds != nil {
		value := int64(*profile.TimeoutCeilingSeconds)
		ceiling = &value
	}
	return csilapi.ExecutionProfileSummary{Name: profile.Name, DenySecrets: profile.DenySecrets,
		SecretPathAllowlist: append([]string{}, profile.SecretPathAllowlist...), RuntimeCapabilities: append([]string{}, profile.RuntimeCapabilities...),
		MayRunAsRoot: profile.MayRunAsRoot, AllowedWorkerClasses: append([]string{}, profile.AllowedWorkerClasses...),
		TimeoutCeilingSeconds: ceiling, ResourceCeilings: map[string]any(profile.ResourceCeilings),
		CacheNamespace: profile.CacheNamespace, ArtifactNamespace: profile.ArtifactNamespace, TrustedCacheWrites: profile.TrustedCacheWrites}
}

func profileFromSummary(orgID string, value csilapi.ExecutionProfileSummary) *models.ExecutionProfile {
	profile := &models.ExecutionProfile{OrgID: orgID, Name: value.Name, DenySecrets: value.DenySecrets,
		SecretPathAllowlist: pq.StringArray(value.SecretPathAllowlist), RuntimeCapabilities: pq.StringArray(value.RuntimeCapabilities),
		MayRunAsRoot: value.MayRunAsRoot, AllowedWorkerClasses: pq.StringArray(value.AllowedWorkerClasses),
		CacheNamespace: value.CacheNamespace, ArtifactNamespace: value.ArtifactNamespace, TrustedCacheWrites: value.TrustedCacheWrites}
	if value.TimeoutCeilingSeconds != nil {
		ceiling := int(*value.TimeoutCeilingSeconds)
		profile.TimeoutCeilingSeconds = &ceiling
	}
	if ceilings, ok := value.ResourceCeilings.(map[string]any); ok {
		profile.ResourceCeilings = models.JSONB(ceilings)
	}
	return profile
}

func (s *UiService) ListExecutionProfiles(ctx context.Context, req csilapi.ListExecutionProfilesRequest) (csilapi.ListExecutionProfilesResponse, error) {
	value, org, err := s.requireCIOrgAdmin(ctx, req.Organization)
	if err != nil {
		return csilapi.ListExecutionProfilesResponse{}, err
	}
	rows, err := value.ListExecutionProfiles(ctx, org.OrgID)
	if err != nil {
		return csilapi.ListExecutionProfilesResponse{}, NewServiceError("internal", "could not list execution profiles")
	}
	result := make([]csilapi.ExecutionProfileSummary, len(rows))
	for i := range rows {
		result[i] = profileSummary(&rows[i])
	}
	return csilapi.ListExecutionProfilesResponse{Profiles: result}, nil
}

func (s *UiService) PutExecutionProfile(ctx context.Context, req csilapi.PutExecutionProfileRequest) (csilapi.PutExecutionProfileResponse, error) {
	value, org, err := s.requireCIOrgAdmin(ctx, req.Organization)
	if err != nil {
		return csilapi.PutExecutionProfileResponse{}, err
	}
	profile := profileFromSummary(org.OrgID, req.Profile)
	existing, getErr := value.GetExecutionProfile(ctx, org.OrgID, profile.Name)
	if getErr == nil {
		profile.ProfileID, profile.CreatedAt = existing.ProfileID, existing.CreatedAt
		err = value.UpdateExecutionProfile(ctx, profile)
	} else if errors.Is(getErr, store.ErrNotFound) {
		err = value.CreateExecutionProfile(ctx, profile)
	} else {
		err = getErr
	}
	if err != nil {
		return csilapi.PutExecutionProfileResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	action := "execution_profile.create"
	if getErr == nil {
		action = "execution_profile.update"
	}
	s.recordAudit(ctx, org.OrgID, action, "execution_profile", profile.ProfileID, models.JSONB{"name": profile.Name})
	return csilapi.PutExecutionProfileResponse{Profile: profileSummary(profile)}, nil
}

func (s *UiService) DeleteExecutionProfile(ctx context.Context, req csilapi.DeleteExecutionProfileRequest) (csilapi.DeleteExecutionProfileResponse, error) {
	value, org, err := s.requireCIOrgAdmin(ctx, req.Organization)
	if err != nil {
		return csilapi.DeleteExecutionProfileResponse{}, err
	}
	if err := value.DeleteExecutionProfile(ctx, org.OrgID, req.Name); err != nil {
		return csilapi.DeleteExecutionProfileResponse{}, mapStoreErr(err, "execution profile could not be deleted")
	}
	s.recordAudit(ctx, org.OrgID, "execution_profile.delete", "execution_profile", req.Name, models.JSONB{})
	return csilapi.DeleteExecutionProfileResponse{Deleted: true}, nil
}

func classSummary(value *models.WorkerClass, poolIDs []string) csilapi.WorkerClassSummary {
	return csilapi.WorkerClassSummary{Name: value.Name, Protected: value.Protected, PoolIds: poolIDs}
}

func (s *UiService) ListWorkerClasses(ctx context.Context, req csilapi.ListWorkerClassesRequest) (csilapi.ListWorkerClassesResponse, error) {
	value, org, err := s.requireCIOrgAdmin(ctx, req.Organization)
	if err != nil {
		return csilapi.ListWorkerClassesResponse{}, err
	}
	rows, err := value.ListWorkerClasses(ctx, org.OrgID)
	if err != nil {
		return csilapi.ListWorkerClassesResponse{}, NewServiceError("internal", "could not list worker classes")
	}
	result := make([]csilapi.WorkerClassSummary, len(rows))
	for i := range rows {
		pools, err := value.ListPoolsForWorkerClass(ctx, rows[i].ClassID)
		if err != nil {
			return csilapi.ListWorkerClassesResponse{}, NewServiceError("internal", "could not list worker class pools")
		}
		poolIDs := make([]string, len(pools))
		for j := range pools {
			poolIDs[j] = pools[j].PoolID
		}
		result[i] = classSummary(&rows[i], poolIDs)
	}
	return csilapi.ListWorkerClassesResponse{WorkerClasses: result}, nil
}

func (s *UiService) PutWorkerClass(ctx context.Context, req csilapi.PutWorkerClassRequest) (csilapi.PutWorkerClassResponse, error) {
	value, org, err := s.requireCIOrgAdmin(ctx, req.Organization)
	if err != nil {
		return csilapi.PutWorkerClassResponse{}, err
	}
	class, getErr := value.GetWorkerClass(ctx, org.OrgID, req.WorkerClass.Name)
	if getErr == nil {
		class.Protected = req.WorkerClass.Protected
		err = value.UpdateWorkerClass(ctx, class)
	} else if errors.Is(getErr, store.ErrNotFound) {
		class = &models.WorkerClass{OrgID: org.OrgID, Name: req.WorkerClass.Name, Protected: req.WorkerClass.Protected}
		err = value.CreateWorkerClass(ctx, class)
	} else {
		err = getErr
	}
	if err != nil {
		return csilapi.PutWorkerClassResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	action := "worker_class.create"
	if getErr == nil {
		action = "worker_class.update"
	}
	s.recordAudit(ctx, org.OrgID, action, "worker_class", class.ClassID, models.JSONB{"name": class.Name, "protected": class.Protected})
	return csilapi.PutWorkerClassResponse{WorkerClass: classSummary(class, nil)}, nil
}

func (s *UiService) DeleteWorkerClass(ctx context.Context, req csilapi.DeleteWorkerClassRequest) (csilapi.DeleteWorkerClassResponse, error) {
	value, org, err := s.requireCIOrgAdmin(ctx, req.Organization)
	if err != nil {
		return csilapi.DeleteWorkerClassResponse{}, err
	}
	class, err := value.GetWorkerClass(ctx, org.OrgID, req.Name)
	if err != nil {
		return csilapi.DeleteWorkerClassResponse{}, mapStoreErr(err, "worker class not found")
	}
	if err := value.DeleteWorkerClass(ctx, class.ClassID); err != nil {
		return csilapi.DeleteWorkerClassResponse{}, mapStoreErr(err, "worker class could not be deleted")
	}
	s.recordAudit(ctx, org.OrgID, "worker_class.delete", "worker_class", class.ClassID, models.JSONB{"name": class.Name})
	return csilapi.DeleteWorkerClassResponse{Deleted: true}, nil
}

func (s *UiService) SetWorkerClassPool(ctx context.Context, req csilapi.SetWorkerClassPoolRequest) (csilapi.SetWorkerClassPoolResponse, error) {
	value, org, err := s.requireCIOrgAdmin(ctx, req.Organization)
	if err != nil {
		return csilapi.SetWorkerClassPoolResponse{}, err
	}
	class, err := value.GetWorkerClass(ctx, org.OrgID, req.WorkerClass)
	if err != nil {
		return csilapi.SetWorkerClassPoolResponse{}, mapStoreErr(err, "worker class not found")
	}
	pool, err := value.GetWorkerPoolByID(ctx, req.PoolId)
	if err != nil {
		return csilapi.SetWorkerClassPoolResponse{}, mapStoreErr(err, "worker pool not found")
	}
	if pool.OrgID == nil {
		if _, err := s.requireGlobalAdmin(ctx); err != nil {
			return csilapi.SetWorkerClassPoolResponse{}, err
		}
	}
	if req.Granted {
		err = value.GrantWorkerClassPool(ctx, class.ClassID, pool.PoolID)
	} else {
		err = value.RevokeWorkerClassPool(ctx, class.ClassID, pool.PoolID)
	}
	if err != nil {
		return csilapi.SetWorkerClassPoolResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	action := "worker_class.pool_grant"
	if !req.Granted {
		action = "worker_class.pool_revoke"
	}
	s.recordAudit(ctx, org.OrgID, action, "worker_class", class.ClassID, models.JSONB{"pool_id": pool.PoolID})
	return csilapi.SetWorkerClassPoolResponse{Ok: true}, nil
}

func (s *UiService) CreateCiApproval(ctx context.Context, req csilapi.CreateCiApprovalRequest) (csilapi.CreateCiApprovalResponse, error) {
	id, user, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.CreateCiApprovalResponse{}, authErr
	}
	value, org, err := s.requireCIOrgAdmin(ctx, req.Organization)
	if err != nil {
		return csilapi.CreateCiApprovalResponse{}, err
	}
	project, err := value.GetProjectByOrgAndName(ctx, org.OrgID, req.Project)
	if err != nil {
		return csilapi.CreateCiApprovalResponse{}, mapStoreErr(err, "project not found")
	}
	allowed := false
	if req.ApproverSubject == "project_owner" {
		allowed, _ = s.deps.Resolver.IsProjectOwner(ctx, id, project.ProjectID)
	} else {
		for _, group := range mustGroups(value.ListGroupsForUser(ctx, user.UserID)) {
			if group.OrgID == org.OrgID && req.ApproverSubject == "reactorcide_group:"+group.Name {
				allowed = true
			}
		}
	}
	if !allowed {
		return csilapi.CreateCiApprovalResponse{}, NewServiceError("forbidden", "the caller does not hold the approver subject")
	}
	approval := &models.CIApproval{OrgID: org.OrgID, ProjectID: project.ProjectID, PRNumber: int(req.PrNumber),
		HeadRepository: req.HeadRepository, HeadSHA: req.HeadSha, BaseSHA: req.BaseSha, PolicyRevision: req.PolicyRevision,
		WorkflowScope: req.WorkflowScope, ExecutionProfile: req.ExecutionProfile, ApproverUserID: &user.UserID,
		ApproverSubject: req.ApproverSubject}
	if req.ExpiresAt != nil {
		expires, parseErr := time.Parse(time.RFC3339, *req.ExpiresAt)
		if parseErr != nil {
			return csilapi.CreateCiApprovalResponse{}, NewServiceError("invalid_argument", "expires_at must use RFC3339")
		}
		approval.ExpiresAt = &expires
	}
	if err := value.CreateCIApproval(ctx, approval); err != nil {
		return csilapi.CreateCiApprovalResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	s.recordAudit(ctx, org.OrgID, "ci_approval.create", "ci_approval", approval.ApprovalID,
		models.JSONB{"project_id": project.ProjectID, "pr_number": approval.PRNumber, "head_sha": approval.HeadSHA,
			"base_sha": approval.BaseSHA, "policy_revision": approval.PolicyRevision, "workflow": approval.WorkflowScope,
			"profile": approval.ExecutionProfile, "approver_subject": approval.ApproverSubject})
	return csilapi.CreateCiApprovalResponse{ApprovalId: approval.ApprovalID}, nil
}

func mustGroups(groups []models.Group, _ error) []models.Group { return groups }
