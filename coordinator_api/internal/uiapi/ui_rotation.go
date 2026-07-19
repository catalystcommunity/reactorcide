package uiapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// webhookSecretPath/vcsCredentialPath are the generated secret-storage path
// convention for rotation-managed values (see UI_AUTH_PLAN.md task G's
// "add-webhook-secret/add-vcs-credential take a secret VALUE ... store the
// value via the org's DatabaseProvider under a generated path"). The row's
// "name" is the secret key within that path, so
// UNIQUE(project_id, provider, name) on the rotation tables also keeps
// secret storage collision-free.
func webhookSecretPath(projectID, provider string) string {
	return fmt.Sprintf("webhooks/%s/%s", projectID, provider)
}

func vcsCredentialPath(projectID, provider string) string {
	return fmt.Sprintf("vcs-credentials/%s/%s", projectID, provider)
}

// requireProjectManage loads project and requires ManageWebhookSecrets/
// ManageVCSCredentials-tier capability at it (org admin of the owning org,
// or global admin — see UI_AUTH_PLAN.md's matrix; a plain project owner may
// not manage credentials).
func (s *UiService) requireProjectManageCaps(ctx context.Context, id authz.Identity, projectID string) (*models.Project, authz.Caps, error) {
	project, err := s.deps.Store.GetProjectByID(ctx, projectID)
	if err != nil {
		return nil, authz.Caps{}, mapStoreErr(err, "project not found")
	}
	caps, err := s.deps.Resolver.Capabilities(ctx, id, authz.Scope{OrgID: project.UserID, ProjectID: &project.ProjectID})
	if err != nil {
		return nil, authz.Caps{}, NewServiceError("internal", "an internal error occurred")
	}
	return project, caps, nil
}

func webhookSecretToCsil(r *models.ProjectWebhookSecret) csilapi.WebhookSecretSummary {
	return csilapi.WebhookSecretSummary{
		Id:            r.ID,
		ProjectId:     r.ProjectID,
		Provider:      r.Provider,
		Name:          r.Name,
		IsActive:      r.IsActive,
		LastUsedAt:    formatTimePtr(r.LastUsedAt),
		CreatedAt:     formatTime(r.CreatedAt),
		DeactivatedAt: formatTimePtr(r.DeactivatedAt),
	}
}

func vcsCredentialToCsil(r *models.ProjectVCSCredential) csilapi.VcsCredentialSummary {
	return csilapi.VcsCredentialSummary{
		Id:            r.ID,
		ProjectId:     r.ProjectID,
		Provider:      r.Provider,
		Name:          r.Name,
		IsActive:      r.IsActive,
		LastUsedAt:    formatTimePtr(r.LastUsedAt),
		CreatedAt:     formatTime(r.CreatedAt),
		DeactivatedAt: formatTimePtr(r.DeactivatedAt),
	}
}

// ListWebhookSecrets requires manage-webhook-secrets capability (org
// admin/global admin of the project's owning org). Values are never
// returned — only rotation metadata.
func (s *UiService) ListWebhookSecrets(ctx context.Context, req csilapi.ListWebhookSecretsRequest) (csilapi.ListWebhookSecretsResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.ListWebhookSecretsResponse{}, authErr
	}
	if err := requireNonEmpty("project_id", req.ProjectId, 64); err != nil {
		return csilapi.ListWebhookSecretsResponse{}, err
	}
	_, caps, err := s.requireProjectManageCaps(ctx, id, req.ProjectId)
	if err != nil {
		return csilapi.ListWebhookSecretsResponse{}, err
	}
	if !caps.ManageWebhookSecrets {
		return csilapi.ListWebhookSecretsResponse{}, NewServiceError("forbidden", "you do not have permission to view this project's webhook secrets")
	}

	rows, err := s.deps.Store.ListProjectWebhookSecrets(ctx, req.ProjectId, nil)
	if err != nil {
		return csilapi.ListWebhookSecretsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.WebhookSecretSummary, len(rows))
	for i := range rows {
		out[i] = webhookSecretToCsil(&rows[i])
	}
	return csilapi.ListWebhookSecretsResponse{Secrets: out}, nil
}

// AddWebhookSecret stores a new webhook signing secret value under a
// generated path and records only its reference in the rotation table.
func (s *UiService) AddWebhookSecret(ctx context.Context, req csilapi.AddWebhookSecretRequest) (csilapi.AddWebhookSecretResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.AddWebhookSecretResponse{}, authErr
	}
	if err := requireNonEmpty("project_id", req.ProjectId, 64); err != nil {
		return csilapi.AddWebhookSecretResponse{}, err
	}
	if err := requireNonEmpty("provider", req.Provider, 64); err != nil {
		return csilapi.AddWebhookSecretResponse{}, err
	}
	if err := requireNonEmpty("name", req.Name, maxNameLength); err != nil {
		return csilapi.AddWebhookSecretResponse{}, err
	}
	if strings.TrimSpace(req.Value) == "" {
		return csilapi.AddWebhookSecretResponse{}, NewServiceError("invalid_argument", "value must not be empty")
	}

	project, caps, err := s.requireProjectManageCaps(ctx, id, req.ProjectId)
	if err != nil {
		return csilapi.AddWebhookSecretResponse{}, err
	}
	if !caps.ManageWebhookSecrets {
		return csilapi.AddWebhookSecretResponse{}, NewServiceError("forbidden", "you do not have permission to manage this project's webhook secrets")
	}
	if project.UserID == nil {
		return csilapi.AddWebhookSecretResponse{}, NewServiceError("internal", "project has no owning org")
	}

	path := webhookSecretPath(req.ProjectId, req.Provider)
	provider, err := s.deps.SecretsProvider(ctx, *project.UserID)
	if err != nil {
		return csilapi.AddWebhookSecretResponse{}, err
	}
	if err := provider.Set(ctx, path, req.Name, req.Value); err != nil {
		return csilapi.AddWebhookSecretResponse{}, mapSecretsErr(err)
	}

	row := &models.ProjectWebhookSecret{
		ProjectID: req.ProjectId,
		Provider:  req.Provider,
		Name:      req.Name,
		SecretRef: path + ":" + req.Name,
		IsActive:  true,
	}
	if err := s.deps.Store.CreateProjectWebhookSecret(ctx, row); err != nil {
		return csilapi.AddWebhookSecretResponse{}, NewServiceError("conflict", "a webhook secret with this project/provider/name already exists")
	}
	return csilapi.AddWebhookSecretResponse{Secret: webhookSecretToCsil(row)}, nil
}

// DeactivateWebhookSecret marks a webhook secret inactive without deleting
// its stored value (still resolvable for audit/rollback until deleted).
func (s *UiService) DeactivateWebhookSecret(ctx context.Context, req csilapi.DeactivateWebhookSecretRequest) (csilapi.DeactivateWebhookSecretResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.DeactivateWebhookSecretResponse{}, authErr
	}
	if err := requireNonEmpty("id", req.Id, 64); err != nil {
		return csilapi.DeactivateWebhookSecretResponse{}, err
	}

	row, err := s.deps.Store.GetProjectWebhookSecretByID(ctx, req.Id)
	if err != nil {
		return csilapi.DeactivateWebhookSecretResponse{}, mapStoreErr(err, "webhook secret not found")
	}
	_, caps, err := s.requireProjectManageCaps(ctx, id, row.ProjectID)
	if err != nil {
		return csilapi.DeactivateWebhookSecretResponse{}, err
	}
	if !caps.ManageWebhookSecrets {
		return csilapi.DeactivateWebhookSecretResponse{}, NewServiceError("forbidden", "you do not have permission to manage this project's webhook secrets")
	}

	if err := s.deps.Store.DeactivateProjectWebhookSecret(ctx, req.Id); err != nil {
		return csilapi.DeactivateWebhookSecretResponse{}, mapStoreErr(err, "webhook secret not found")
	}
	row, err = s.deps.Store.GetProjectWebhookSecretByID(ctx, req.Id)
	if err != nil {
		return csilapi.DeactivateWebhookSecretResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	return csilapi.DeactivateWebhookSecretResponse{Secret: webhookSecretToCsil(row)}, nil
}

// DeleteWebhookSecret removes a webhook secret row and best-effort deletes
// its underlying stored value.
func (s *UiService) DeleteWebhookSecret(ctx context.Context, req csilapi.DeleteWebhookSecretRequest) (csilapi.DeleteWebhookSecretResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.DeleteWebhookSecretResponse{}, authErr
	}
	if err := requireNonEmpty("id", req.Id, 64); err != nil {
		return csilapi.DeleteWebhookSecretResponse{}, err
	}

	row, err := s.deps.Store.GetProjectWebhookSecretByID(ctx, req.Id)
	if err != nil {
		return csilapi.DeleteWebhookSecretResponse{}, mapStoreErr(err, "webhook secret not found")
	}
	project, caps, err := s.requireProjectManageCaps(ctx, id, row.ProjectID)
	if err != nil {
		return csilapi.DeleteWebhookSecretResponse{}, err
	}
	if !caps.ManageWebhookSecrets {
		return csilapi.DeleteWebhookSecretResponse{}, NewServiceError("forbidden", "you do not have permission to manage this project's webhook secrets")
	}

	if project.UserID != nil {
		if provider, err := s.deps.SecretsProvider(ctx, *project.UserID); err == nil {
			parts := strings.SplitN(row.SecretRef, ":", 2)
			if len(parts) == 2 {
				_, _ = provider.Delete(ctx, parts[0], parts[1])
			}
		}
	}

	if err := s.deps.Store.DeleteProjectWebhookSecret(ctx, req.Id); err != nil {
		return csilapi.DeleteWebhookSecretResponse{}, mapStoreErr(err, "webhook secret not found")
	}
	return csilapi.DeleteWebhookSecretResponse{Deleted: true}, nil
}

// ListVcsCredentials requires manage-vcs-credentials capability.
func (s *UiService) ListVcsCredentials(ctx context.Context, req csilapi.ListVcsCredentialsRequest) (csilapi.ListVcsCredentialsResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.ListVcsCredentialsResponse{}, authErr
	}
	if err := requireNonEmpty("project_id", req.ProjectId, 64); err != nil {
		return csilapi.ListVcsCredentialsResponse{}, err
	}
	_, caps, err := s.requireProjectManageCaps(ctx, id, req.ProjectId)
	if err != nil {
		return csilapi.ListVcsCredentialsResponse{}, err
	}
	if !caps.ManageVCSCredentials {
		return csilapi.ListVcsCredentialsResponse{}, NewServiceError("forbidden", "you do not have permission to view this project's vcs credentials")
	}

	rows, err := s.deps.Store.ListProjectVCSCredentials(ctx, req.ProjectId, nil)
	if err != nil {
		return csilapi.ListVcsCredentialsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.VcsCredentialSummary, len(rows))
	for i := range rows {
		out[i] = vcsCredentialToCsil(&rows[i])
	}
	return csilapi.ListVcsCredentialsResponse{Credentials: out}, nil
}

// AddVcsCredential stores a new VCS credential value under a generated path
// and records only its reference in the rotation table.
func (s *UiService) AddVcsCredential(ctx context.Context, req csilapi.AddVcsCredentialRequest) (csilapi.AddVcsCredentialResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.AddVcsCredentialResponse{}, authErr
	}
	if err := requireNonEmpty("project_id", req.ProjectId, 64); err != nil {
		return csilapi.AddVcsCredentialResponse{}, err
	}
	if err := requireNonEmpty("provider", req.Provider, 64); err != nil {
		return csilapi.AddVcsCredentialResponse{}, err
	}
	if err := requireNonEmpty("name", req.Name, maxNameLength); err != nil {
		return csilapi.AddVcsCredentialResponse{}, err
	}
	if strings.TrimSpace(req.Value) == "" {
		return csilapi.AddVcsCredentialResponse{}, NewServiceError("invalid_argument", "value must not be empty")
	}

	project, caps, err := s.requireProjectManageCaps(ctx, id, req.ProjectId)
	if err != nil {
		return csilapi.AddVcsCredentialResponse{}, err
	}
	if !caps.ManageVCSCredentials {
		return csilapi.AddVcsCredentialResponse{}, NewServiceError("forbidden", "you do not have permission to manage this project's vcs credentials")
	}
	if project.UserID == nil {
		return csilapi.AddVcsCredentialResponse{}, NewServiceError("internal", "project has no owning org")
	}

	path := vcsCredentialPath(req.ProjectId, req.Provider)
	provider, err := s.deps.SecretsProvider(ctx, *project.UserID)
	if err != nil {
		return csilapi.AddVcsCredentialResponse{}, err
	}
	if err := provider.Set(ctx, path, req.Name, req.Value); err != nil {
		return csilapi.AddVcsCredentialResponse{}, mapSecretsErr(err)
	}

	row := &models.ProjectVCSCredential{
		ProjectID: req.ProjectId,
		Provider:  req.Provider,
		Name:      req.Name,
		SecretRef: path + ":" + req.Name,
		IsActive:  true,
	}
	if err := s.deps.Store.CreateProjectVCSCredential(ctx, row); err != nil {
		return csilapi.AddVcsCredentialResponse{}, NewServiceError("conflict", "a vcs credential with this project/provider/name already exists")
	}
	return csilapi.AddVcsCredentialResponse{Credential: vcsCredentialToCsil(row)}, nil
}

// DeactivateVcsCredential marks a VCS credential inactive without deleting
// its stored value.
func (s *UiService) DeactivateVcsCredential(ctx context.Context, req csilapi.DeactivateVcsCredentialRequest) (csilapi.DeactivateVcsCredentialResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.DeactivateVcsCredentialResponse{}, authErr
	}
	if err := requireNonEmpty("id", req.Id, 64); err != nil {
		return csilapi.DeactivateVcsCredentialResponse{}, err
	}

	row, err := s.deps.Store.GetProjectVCSCredentialByID(ctx, req.Id)
	if err != nil {
		return csilapi.DeactivateVcsCredentialResponse{}, mapStoreErr(err, "vcs credential not found")
	}
	_, caps, err := s.requireProjectManageCaps(ctx, id, row.ProjectID)
	if err != nil {
		return csilapi.DeactivateVcsCredentialResponse{}, err
	}
	if !caps.ManageVCSCredentials {
		return csilapi.DeactivateVcsCredentialResponse{}, NewServiceError("forbidden", "you do not have permission to manage this project's vcs credentials")
	}

	if err := s.deps.Store.DeactivateProjectVCSCredential(ctx, req.Id); err != nil {
		return csilapi.DeactivateVcsCredentialResponse{}, mapStoreErr(err, "vcs credential not found")
	}
	row, err = s.deps.Store.GetProjectVCSCredentialByID(ctx, req.Id)
	if err != nil {
		return csilapi.DeactivateVcsCredentialResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	return csilapi.DeactivateVcsCredentialResponse{Credential: vcsCredentialToCsil(row)}, nil
}

// DeleteVcsCredential removes a VCS credential row and best-effort deletes
// its underlying stored value, mirroring DeleteWebhookSecret.
func (s *UiService) DeleteVcsCredential(ctx context.Context, req csilapi.DeleteVcsCredentialRequest) (csilapi.DeleteVcsCredentialResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.DeleteVcsCredentialResponse{}, authErr
	}
	if err := requireNonEmpty("id", req.Id, 64); err != nil {
		return csilapi.DeleteVcsCredentialResponse{}, err
	}

	row, err := s.deps.Store.GetProjectVCSCredentialByID(ctx, req.Id)
	if err != nil {
		return csilapi.DeleteVcsCredentialResponse{}, mapStoreErr(err, "vcs credential not found")
	}
	project, caps, err := s.requireProjectManageCaps(ctx, id, row.ProjectID)
	if err != nil {
		return csilapi.DeleteVcsCredentialResponse{}, err
	}
	if !caps.ManageVCSCredentials {
		return csilapi.DeleteVcsCredentialResponse{}, NewServiceError("forbidden", "you do not have permission to manage this project's vcs credentials")
	}

	if project.UserID != nil {
		if provider, err := s.deps.SecretsProvider(ctx, *project.UserID); err == nil {
			parts := strings.SplitN(row.SecretRef, ":", 2)
			if len(parts) == 2 {
				_, _ = provider.Delete(ctx, parts[0], parts[1])
			}
		}
	}

	if err := s.deps.Store.DeleteProjectVCSCredential(ctx, req.Id); err != nil {
		return csilapi.DeleteVcsCredentialResponse{}, mapStoreErr(err, "vcs credential not found")
	}
	return csilapi.DeleteVcsCredentialResponse{Deleted: true}, nil
}
