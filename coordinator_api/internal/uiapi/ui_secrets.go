package uiapi

import (
	"context"
	"regexp"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// requireManageSecrets requires org admin/global admin (manage-secrets
// capability) at orgID. Every secrets op (write-only: set/delete/list-paths,
// plus secret grants) is gated the same way — UI_AUTH_PLAN.md's matrix has
// no separate "view" tier for these.
func (s *UiService) requireManageSecrets(ctx context.Context, id authz.Identity, orgID string) error {
	caps, err := s.deps.Resolver.Capabilities(ctx, id, authz.Scope{OrgID: &orgID})
	if err != nil {
		return NewServiceError("internal", "an internal error occurred")
	}
	if !caps.ManageSecrets {
		return NewServiceError("forbidden", "you do not have permission to manage secrets for this org")
	}
	return nil
}

// SetSecret writes a secret value (write-only: this op never returns a
// value, including on subsequent reads through this service — there is no
// get-secret op).
func (s *UiService) SetSecret(ctx context.Context, req csilapi.SetSecretRequest) (csilapi.SetSecretResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.SetSecretResponse{}, authErr
	}
	if err := requireNonEmpty("org_id", req.OrgId, 64); err != nil {
		return csilapi.SetSecretResponse{}, err
	}
	if strings.TrimSpace(req.Value) == "" {
		return csilapi.SetSecretResponse{}, NewServiceError("invalid_argument", "value must not be empty")
	}
	if err := s.requireManageSecrets(ctx, id, req.OrgId); err != nil {
		return csilapi.SetSecretResponse{}, err
	}

	provider, err := s.deps.SecretsProvider(ctx, req.OrgId)
	if err != nil {
		return csilapi.SetSecretResponse{}, err
	}
	if err := provider.Set(ctx, req.Path, req.Key, req.Value); err != nil {
		return csilapi.SetSecretResponse{}, mapSecretsErr(err)
	}
	return csilapi.SetSecretResponse{Ok: true}, nil
}

// DeleteSecret deletes a secret value.
func (s *UiService) DeleteSecret(ctx context.Context, req csilapi.DeleteSecretRequest) (csilapi.DeleteSecretResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.DeleteSecretResponse{}, authErr
	}
	if err := requireNonEmpty("org_id", req.OrgId, 64); err != nil {
		return csilapi.DeleteSecretResponse{}, err
	}
	if err := s.requireManageSecrets(ctx, id, req.OrgId); err != nil {
		return csilapi.DeleteSecretResponse{}, err
	}

	provider, err := s.deps.SecretsProvider(ctx, req.OrgId)
	if err != nil {
		return csilapi.DeleteSecretResponse{}, err
	}
	deleted, err := provider.Delete(ctx, req.Path, req.Key)
	if err != nil {
		return csilapi.DeleteSecretResponse{}, mapSecretsErr(err)
	}
	return csilapi.DeleteSecretResponse{Deleted: deleted}, nil
}

// ListSecretPaths lists paths (and the keys under each) — never values.
func (s *UiService) ListSecretPaths(ctx context.Context, req csilapi.ListSecretPathsRequest) (csilapi.ListSecretPathsResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.ListSecretPathsResponse{}, authErr
	}
	if err := requireNonEmpty("org_id", req.OrgId, 64); err != nil {
		return csilapi.ListSecretPathsResponse{}, err
	}
	if err := s.requireManageSecrets(ctx, id, req.OrgId); err != nil {
		return csilapi.ListSecretPathsResponse{}, err
	}

	provider, err := s.deps.SecretsProvider(ctx, req.OrgId)
	if err != nil {
		return csilapi.ListSecretPathsResponse{}, err
	}
	paths, err := provider.ListPaths(ctx)
	if err != nil {
		return csilapi.ListSecretPathsResponse{}, mapSecretsErr(err)
	}

	prefix := ""
	if req.PathPrefix != nil {
		prefix = *req.PathPrefix
	}
	out := make([]csilapi.SecretPathEntry, 0, len(paths))
	for _, path := range paths {
		if prefix != "" && !strings.HasPrefix(path, prefix) {
			continue
		}
		keys, err := provider.ListKeys(ctx, path)
		if err != nil {
			return csilapi.ListSecretPathsResponse{}, mapSecretsErr(err)
		}
		out = append(out, csilapi.SecretPathEntry{Path: path, Keys: keys})
	}
	return csilapi.ListSecretPathsResponse{Paths: out}, nil
}

func secretGrantToCsil(g *models.SecretGrant) csilapi.SecretGrant {
	out := csilapi.SecretGrant{
		GrantId:           g.GrantID,
		OrgId:             g.UserID,
		ProjectId:         g.ProjectID,
		Name:              g.Name,
		SecretPathMatch:   g.SecretPathMatch,
		SecretPathPattern: g.SecretPathPattern,
		JobNameMatch:      g.JobNameMatch,
		CreatedAt:         formatTime(g.CreatedAt),
		UpdatedAt:         formatTime(g.UpdatedAt),
	}
	if g.JobNamePattern != "" {
		p := g.JobNamePattern
		out.JobNamePattern = &p
	}
	if g.Description != "" {
		d := g.Description
		out.Description = &d
	}
	return out
}

func validSecretPathMatch(s string) bool {
	switch s {
	case models.SecretGrantMatchAny, models.SecretGrantMatchExact, models.SecretGrantMatchPrefix,
		models.SecretGrantMatchGlob, models.SecretGrantMatchRegex:
		return true
	default:
		return false
	}
}

// ListSecretGrants lists every secret grant (global and project-scoped)
// under an org.
func (s *UiService) ListSecretGrants(ctx context.Context, req csilapi.ListSecretGrantsRequest) (csilapi.ListSecretGrantsResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.ListSecretGrantsResponse{}, authErr
	}
	if err := requireNonEmpty("org_id", req.OrgId, 64); err != nil {
		return csilapi.ListSecretGrantsResponse{}, err
	}
	if err := s.requireManageSecrets(ctx, id, req.OrgId); err != nil {
		return csilapi.ListSecretGrantsResponse{}, err
	}

	grants, err := s.deps.Store.ListSecretGrantsByOrg(ctx, req.OrgId)
	if err != nil {
		return csilapi.ListSecretGrantsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.SecretGrant, len(grants))
	for i := range grants {
		out[i] = secretGrantToCsil(&grants[i])
	}
	return csilapi.ListSecretGrantsResponse{Grants: out}, nil
}

// CreateSecretGrant creates a new secret grant.
func (s *UiService) CreateSecretGrant(ctx context.Context, req csilapi.CreateSecretGrantRequest) (csilapi.CreateSecretGrantResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.CreateSecretGrantResponse{}, authErr
	}
	if err := requireNonEmpty("org_id", req.OrgId, 64); err != nil {
		return csilapi.CreateSecretGrantResponse{}, err
	}
	if err := requireNonEmpty("name", req.Name, maxNameLength); err != nil {
		return csilapi.CreateSecretGrantResponse{}, err
	}
	if !validSecretPathMatch(req.SecretPathMatch) {
		return csilapi.CreateSecretGrantResponse{}, NewServiceError("invalid_argument", "secret_path_match must be one of any, exact, prefix, glob, regex")
	}
	if req.SecretPathMatch != models.SecretGrantMatchAny {
		if err := requireNonEmpty("secret_path_pattern", req.SecretPathPattern, 1024); err != nil {
			return csilapi.CreateSecretGrantResponse{}, err
		}
	}
	if req.SecretPathMatch == models.SecretGrantMatchRegex {
		if _, err := regexp.Compile(req.SecretPathPattern); err != nil {
			return csilapi.CreateSecretGrantResponse{}, NewServiceError("invalid_argument", "secret_path_pattern is not a valid regular expression")
		}
	}
	jobNameMatch := derefOr(req.JobNameMatch, models.SecretGrantMatchAny)
	if !validSecretPathMatch(jobNameMatch) {
		return csilapi.CreateSecretGrantResponse{}, NewServiceError("invalid_argument", "job_name_match must be one of any, exact, prefix, glob, regex")
	}
	if jobNameMatch == models.SecretGrantMatchRegex {
		if _, err := regexp.Compile(derefOr(req.JobNamePattern, "")); err != nil {
			return csilapi.CreateSecretGrantResponse{}, NewServiceError("invalid_argument", "job_name_pattern is not a valid regular expression")
		}
	}

	if err := s.requireManageSecrets(ctx, id, req.OrgId); err != nil {
		return csilapi.CreateSecretGrantResponse{}, err
	}
	if req.ProjectId != nil {
		if _, err := s.deps.Store.GetProjectByID(ctx, *req.ProjectId); err != nil {
			return csilapi.CreateSecretGrantResponse{}, NewServiceError("invalid_argument", "project_id does not refer to a known project")
		}
	}

	grant := &models.SecretGrant{
		UserID:            req.OrgId,
		ProjectID:         req.ProjectId,
		Name:              req.Name,
		SecretPathMatch:   req.SecretPathMatch,
		SecretPathPattern: req.SecretPathPattern,
		JobNameMatch:      jobNameMatch,
		JobNamePattern:    derefOr(req.JobNamePattern, ""),
		Description:       derefOr(req.Description, ""),
	}
	if err := s.deps.Store.CreateSecretGrant(ctx, grant); err != nil {
		return csilapi.CreateSecretGrantResponse{}, NewServiceError("internal", "failed to create secret grant")
	}
	return csilapi.CreateSecretGrantResponse{Grant: secretGrantToCsil(grant)}, nil
}

// UpdateSecretGrant updates an existing secret grant.
func (s *UiService) UpdateSecretGrant(ctx context.Context, req csilapi.UpdateSecretGrantRequest) (csilapi.UpdateSecretGrantResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.UpdateSecretGrantResponse{}, authErr
	}
	if err := requireNonEmpty("grant_id", req.GrantId, 64); err != nil {
		return csilapi.UpdateSecretGrantResponse{}, err
	}

	grant, err := s.deps.Store.GetSecretGrantByID(ctx, req.GrantId)
	if err != nil {
		return csilapi.UpdateSecretGrantResponse{}, mapStoreErr(err, "secret grant not found")
	}
	if err := s.requireManageSecrets(ctx, id, grant.UserID); err != nil {
		return csilapi.UpdateSecretGrantResponse{}, err
	}

	if req.Name != nil {
		if err := requireNonEmpty("name", *req.Name, maxNameLength); err != nil {
			return csilapi.UpdateSecretGrantResponse{}, err
		}
		grant.Name = *req.Name
	}
	if req.SecretPathMatch != nil {
		if !validSecretPathMatch(*req.SecretPathMatch) {
			return csilapi.UpdateSecretGrantResponse{}, NewServiceError("invalid_argument", "secret_path_match must be one of any, exact, prefix, glob, regex")
		}
		grant.SecretPathMatch = *req.SecretPathMatch
	}
	if req.SecretPathPattern != nil {
		grant.SecretPathPattern = *req.SecretPathPattern
	}
	if grant.SecretPathMatch == models.SecretGrantMatchRegex {
		if _, err := regexp.Compile(grant.SecretPathPattern); err != nil {
			return csilapi.UpdateSecretGrantResponse{}, NewServiceError("invalid_argument", "secret_path_pattern is not a valid regular expression")
		}
	}
	if req.JobNameMatch != nil {
		if !validSecretPathMatch(*req.JobNameMatch) {
			return csilapi.UpdateSecretGrantResponse{}, NewServiceError("invalid_argument", "job_name_match must be one of any, exact, prefix, glob, regex")
		}
		grant.JobNameMatch = *req.JobNameMatch
	}
	if req.JobNamePattern != nil {
		grant.JobNamePattern = *req.JobNamePattern
	}
	if req.Description != nil {
		grant.Description = *req.Description
	}

	if err := s.deps.Store.UpdateSecretGrant(ctx, grant); err != nil {
		return csilapi.UpdateSecretGrantResponse{}, NewServiceError("internal", "failed to update secret grant")
	}
	return csilapi.UpdateSecretGrantResponse{Grant: secretGrantToCsil(grant)}, nil
}

// DeleteSecretGrant deletes a secret grant.
func (s *UiService) DeleteSecretGrant(ctx context.Context, req csilapi.DeleteSecretGrantRequest) (csilapi.DeleteSecretGrantResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.DeleteSecretGrantResponse{}, authErr
	}
	if err := requireNonEmpty("grant_id", req.GrantId, 64); err != nil {
		return csilapi.DeleteSecretGrantResponse{}, err
	}

	grant, err := s.deps.Store.GetSecretGrantByID(ctx, req.GrantId)
	if err != nil {
		return csilapi.DeleteSecretGrantResponse{}, mapStoreErr(err, "secret grant not found")
	}
	if err := s.requireManageSecrets(ctx, id, grant.UserID); err != nil {
		return csilapi.DeleteSecretGrantResponse{}, err
	}

	if err := s.deps.Store.DeleteSecretGrant(ctx, grant.UserID, grant.ProjectID, grant.GrantID); err != nil {
		return csilapi.DeleteSecretGrantResponse{}, mapStoreErr(err, "secret grant not found")
	}
	return csilapi.DeleteSecretGrantResponse{Deleted: true}, nil
}
