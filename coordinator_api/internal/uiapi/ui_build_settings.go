package uiapi

import (
	"context"
	"errors"
	"strings"

	"github.com/lib/pq"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/envdefaults"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagepolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// WP14: environment defaults, the image policy, and job links. The
// environment defaults and the image policy need an org admin (an org admin
// is an owner of each project of the organization). Job links follow the
// visibility of the job.

// envDefaultScope returns the scope and the scope reference of a request.
func envDefaultScope(workerClass, projectID *string) (string, string, error) {
	class, project := "", ""
	if workerClass != nil {
		class = *workerClass
	}
	if projectID != nil {
		project = *projectID
	}
	switch {
	case class != "" && project != "":
		return "", "", NewServiceError("invalid_argument", "set worker_class or project_id, not both")
	case class != "":
		return models.EnvDefaultScopeWorkerClass, class, nil
	case project != "":
		return models.EnvDefaultScopeProject, project, nil
	}
	return models.EnvDefaultScopeOrg, "", nil
}

func (s *UiService) envDefaultsStore() (store.EnvDefaultsStore, error) {
	value, ok := s.deps.Store.(store.EnvDefaultsStore)
	if !ok {
		return nil, NewServiceError("internal", "environment defaults are not available")
	}
	return value, nil
}

// envDefaultTokenCapabilities is the token capability of an environment
// default scope, as in the REST routes: read accepts the read or the write
// capability, a write needs the write capability.
func envDefaultTokenCapabilities(scope string, write bool) []string {
	read, manage := tokencaps.OrganizationsRead, tokencaps.OrganizationsManage
	switch scope {
	case models.EnvDefaultScopeWorkerClass:
		read, manage = tokencaps.WorkersRead, tokencaps.WorkersManage
	case models.EnvDefaultScopeProject:
		read, manage = tokencaps.ProjectsRead, tokencaps.ProjectsManage
	}
	if write {
		return []string{manage}
	}
	return []string{read, manage}
}

func envDefaultEntry(row models.EnvDefault) csilapi.EnvDefaultEntry {
	updated := formatTime(row.UpdatedAt)
	return csilapi.EnvDefaultEntry{Key: row.Key, Value: row.Value, UpdatedAt: &updated}
}

func (s *UiService) ListEnvDefaults(ctx context.Context, req csilapi.ListEnvDefaultsRequest) (csilapi.ListEnvDefaultsResponse, error) {
	scope, ref, err := envDefaultScope(req.WorkerClass, req.ProjectId)
	if err != nil {
		return csilapi.ListEnvDefaultsResponse{}, err
	}
	ctx, _, org, err := s.requireCIOrgAdmin(ctx, req.Organization, envDefaultTokenCapabilities(scope, false)...)
	if err != nil {
		return csilapi.ListEnvDefaultsResponse{}, err
	}
	defaults, err := s.envDefaultsStore()
	if err != nil {
		return csilapi.ListEnvDefaultsResponse{}, err
	}
	rows, err := defaults.ListEnvDefaults(ctx, org.OrgID, scope, ref)
	if err != nil {
		return csilapi.ListEnvDefaultsResponse{}, mapStoreErr(err, "scope not found")
	}
	entries := make([]csilapi.EnvDefaultEntry, len(rows))
	for i := range rows {
		entries[i] = envDefaultEntry(rows[i])
	}
	return csilapi.ListEnvDefaultsResponse{Scope: scope, Entries: entries}, nil
}

func (s *UiService) PutEnvDefault(ctx context.Context, req csilapi.PutEnvDefaultRequest) (csilapi.PutEnvDefaultResponse, error) {
	scope, ref, err := envDefaultScope(req.WorkerClass, req.ProjectId)
	if err != nil {
		return csilapi.PutEnvDefaultResponse{}, err
	}
	ctx, _, org, err := s.requireCIOrgAdmin(ctx, req.Organization, envDefaultTokenCapabilities(scope, true)...)
	if err != nil {
		return csilapi.PutEnvDefaultResponse{}, err
	}
	if err := envdefaults.Validate(req.Key, req.Value); err != nil {
		return csilapi.PutEnvDefaultResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	defaults, err := s.envDefaultsStore()
	if err != nil {
		return csilapi.PutEnvDefaultResponse{}, err
	}
	row, err := defaults.SetEnvDefault(ctx, org.OrgID, scope, ref, req.Key, req.Value)
	if err != nil {
		if errors.Is(err, store.ErrInvalidInput) {
			return csilapi.PutEnvDefaultResponse{}, NewServiceError("invalid_argument", err.Error())
		}
		return csilapi.PutEnvDefaultResponse{}, mapStoreErr(err, "scope not found")
	}
	s.recordAudit(ctx, org.OrgID, "env_default.set", "env_default", scope+":"+ref+":"+req.Key,
		models.JSONB{"scope": scope, "scope_ref": ref, "key": req.Key})
	return csilapi.PutEnvDefaultResponse{Entry: envDefaultEntry(*row)}, nil
}

func (s *UiService) DeleteEnvDefault(ctx context.Context, req csilapi.DeleteEnvDefaultRequest) (csilapi.DeleteEnvDefaultResponse, error) {
	scope, ref, err := envDefaultScope(req.WorkerClass, req.ProjectId)
	if err != nil {
		return csilapi.DeleteEnvDefaultResponse{}, err
	}
	ctx, _, org, err := s.requireCIOrgAdmin(ctx, req.Organization, envDefaultTokenCapabilities(scope, true)...)
	if err != nil {
		return csilapi.DeleteEnvDefaultResponse{}, err
	}
	defaults, err := s.envDefaultsStore()
	if err != nil {
		return csilapi.DeleteEnvDefaultResponse{}, err
	}
	if err := defaults.DeleteEnvDefault(ctx, org.OrgID, scope, ref, req.Key); err != nil {
		return csilapi.DeleteEnvDefaultResponse{}, mapStoreErr(err, "environment default not found")
	}
	s.recordAudit(ctx, org.OrgID, "env_default.delete", "env_default", scope+":"+ref+":"+req.Key,
		models.JSONB{"scope": scope, "scope_ref": ref, "key": req.Key})
	return csilapi.DeleteEnvDefaultResponse{Deleted: true}, nil
}

func imagePolicySummary(policy *models.ImagePolicy) csilapi.ImagePolicySummary {
	summary := csilapi.ImagePolicySummary{Enabled: policy.Enabled, AllowedPrefixes: append([]string{}, policy.AllowedPrefixes...),
		RequireDigest: policy.RequireDigest}
	if !policy.UpdatedAt.IsZero() {
		updated := formatTime(policy.UpdatedAt)
		summary.UpdatedAt = &updated
	}
	return summary
}

func (s *UiService) imagePolicyStore() (store.ImagePolicyStore, error) {
	value, ok := s.deps.Store.(store.ImagePolicyStore)
	if !ok {
		return nil, NewServiceError("internal", "image policies are not available")
	}
	return value, nil
}

func (s *UiService) GetImagePolicy(ctx context.Context, req csilapi.GetImagePolicyRequest) (csilapi.GetImagePolicyResponse, error) {
	ctx, _, org, err := s.requireCIOrgAdmin(ctx, req.Organization, tokencaps.OrganizationsRead, tokencaps.PoliciesManage)
	if err != nil {
		return csilapi.GetImagePolicyResponse{}, err
	}
	policies, err := s.imagePolicyStore()
	if err != nil {
		return csilapi.GetImagePolicyResponse{}, err
	}
	policy, err := policies.GetImagePolicy(ctx, org.OrgID)
	if err != nil {
		return csilapi.GetImagePolicyResponse{}, NewServiceError("internal", "could not read the image policy")
	}
	return csilapi.GetImagePolicyResponse{Policy: imagePolicySummary(policy)}, nil
}

func (s *UiService) PutImagePolicy(ctx context.Context, req csilapi.PutImagePolicyRequest) (csilapi.PutImagePolicyResponse, error) {
	ctx, _, org, err := s.requireCIOrgAdmin(ctx, req.Organization, tokencaps.PoliciesManage)
	if err != nil {
		return csilapi.PutImagePolicyResponse{}, err
	}
	policies, err := s.imagePolicyStore()
	if err != nil {
		return csilapi.PutImagePolicyResponse{}, err
	}
	prefixes := []string{}
	for _, prefix := range req.Policy.AllowedPrefixes {
		if trimmed := strings.TrimSpace(prefix); trimmed != "" {
			prefixes = append(prefixes, trimmed)
		}
	}
	policy := &models.ImagePolicy{OrgID: org.OrgID, Enabled: req.Policy.Enabled, AllowedPrefixes: pq.StringArray(prefixes), RequireDigest: req.Policy.RequireDigest}
	if err := imagepolicy.ValidatePolicy(policy); err != nil {
		return csilapi.PutImagePolicyResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	if err := policies.SetImagePolicy(ctx, policy); err != nil {
		return csilapi.PutImagePolicyResponse{}, NewServiceError("internal", "could not save the image policy")
	}
	s.recordAudit(ctx, org.OrgID, "image_policy.set", "image_policy", org.OrgID,
		models.JSONB{"enabled": policy.Enabled, "allowed_prefixes": prefixes, "require_digest": policy.RequireDigest})
	saved, err := policies.GetImagePolicy(ctx, org.OrgID)
	if err != nil {
		return csilapi.PutImagePolicyResponse{}, NewServiceError("internal", "could not read the image policy")
	}
	return csilapi.PutImagePolicyResponse{Policy: imagePolicySummary(saved)}, nil
}

func (s *UiService) ListJobLinks(ctx context.Context, req csilapi.ListJobLinksRequest) (csilapi.ListJobLinksResponse, error) {
	if err := requireNonEmpty("job_id", req.JobId, 64); err != nil {
		return csilapi.ListJobLinksResponse{}, err
	}
	identity, _ := s.deps.resolveIdentity(ctx)
	job, err := s.deps.Store.GetJobByID(ctx, req.JobId)
	if err != nil {
		return csilapi.ListJobLinksResponse{}, mapStoreErr(err, "job not found")
	}
	visible, err := s.deps.Resolver.CanViewJob(ctx, identity, job)
	if err != nil {
		return csilapi.ListJobLinksResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !visible {
		return csilapi.ListJobLinksResponse{}, NewServiceError("not_found", "job not found")
	}
	result := []csilapi.JobLinkSummary{}
	linkStore, ok := s.deps.Store.(store.JobLinkStore)
	if !ok {
		return csilapi.ListJobLinksResponse{Links: result}, nil
	}
	links, err := linkStore.ListJobLinks(ctx, job.JobID)
	if err != nil {
		return csilapi.ListJobLinksResponse{}, NewServiceError("internal", "could not read the job links")
	}
	for _, link := range links {
		result = append(result, csilapi.JobLinkSummary{Name: link.Name, Url: link.URL})
	}
	return csilapi.ListJobLinksResponse{Links: result}, nil
}
