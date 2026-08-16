package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
)

// SecretsHandler handles secrets API endpoints
type SecretsHandler struct {
	BaseHandler
	store      store.Store
	keyManager *secrets.MasterKeyManager
}

// NewSecretsHandler creates a new SecretsHandler
func NewSecretsHandler(s store.Store, keyManager *secrets.MasterKeyManager) *SecretsHandler {
	return &SecretsHandler{
		store:      s,
		keyManager: keyManager,
	}
}

// SecretValueRequest represents a request to set a secret value
type SecretValueRequest struct {
	Value string `json:"value"`
}

// SecretValueResponse represents a response with a secret value
type SecretValueResponse struct {
	Value string `json:"value"`
}

// BatchGetRequest represents a batch get request
type BatchGetRequest struct {
	Refs       []secrets.SecretRef           `json:"refs"`
	ProjectID  string                        `json:"project_id,omitempty"`
	Selections []LocalContextSecretSelection `json:"selections,omitempty"`
}

// LocalContextSecretSelection binds a selected reference to the workflow job
// that will use it.
type LocalContextSecretSelection struct {
	Path    string `json:"path"`
	Key     string `json:"key"`
	JobName string `json:"job_name"`
}

// BatchGetResponse represents a batch get response
type BatchGetResponse struct {
	Secrets map[string]string `json:"secrets"`
}

// BatchSetRequest represents a batch set request
type BatchSetRequest struct {
	Secrets []struct {
		Path  string `json:"path"`
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"secrets"`
}

// ListKeysResponse represents a list keys response
type ListKeysResponse struct {
	Keys []string `json:"keys"`
}

// ListPathsResponse represents a list paths response
type ListPathsResponse struct {
	Paths []string `json:"paths"`
}

// InitResponse represents an init response
type InitResponse struct {
	Status string `json:"status"`
	OrgID  string `json:"org_id"`
}

// getProvider creates a provider for the current user
func (h *SecretsHandler) getProvider(r *http.Request) (secrets.Provider, error) {
	user := checkauth.GetUserFromContext(r.Context())
	principal := checkauth.GetPrincipalFromContext(r.Context())
	if user == nil && principal == nil {
		return nil, errors.New("user not authenticated")
	}
	orgID, err := h.resolveSecretsOrg(r)
	if err != nil {
		return nil, err
	}
	if principal != nil {
		if !principal.HasCapability(tokencaps.SecretsManage) || !principal.HasOrganization(orgID) {
			return nil, store.ErrForbidden
		}
	} else {
		db := store.GetDBFromContext(r.Context())
		authorizer := secrets.NewOrgAuthorizer(db)
		if err := authorizer.CanAccessOrg(r.Context(), user.UserID, orgID); err != nil {
			return nil, err
		}
	}

	return h.getProviderForOrg(r, orgID)
}

func (h *SecretsHandler) getProviderForOrg(r *http.Request, orgID string) (secrets.Provider, error) {
	db := store.GetDBFromContext(r.Context())
	orgKey, err := h.keyManager.GetOrgEncryptionKey(db, orgID)
	if err != nil {
		return nil, err
	}

	return secrets.NewDatabaseProvider(db, orgID, orgKey)
}

func (h *SecretsHandler) resolveSecretsOrg(r *http.Request) (string, error) {
	if name := r.URL.Query().Get("org"); name != "" {
		if finder, ok := h.store.(interface {
			GetOrganizationByName(context.Context, string) (*models.Organization, error)
		}); ok {
			org, err := finder.GetOrganizationByName(r.Context(), name)
			if err != nil {
				return "", err
			}
			return org.OrgID, nil
		}
	}
	if p := checkauth.GetPrincipalFromContext(r.Context()); p != nil {
		if p.OwnerOrgID != "" {
			return p.OwnerOrgID, nil
		}
		if !p.AllOrganizations && len(p.OrganizationIDs) == 1 {
			return p.OrganizationIDs[0], nil
		}
	}
	if user := checkauth.GetUserFromContext(r.Context()); user != nil {
		return user.UserID, nil
	}
	return "", errors.New("an organization is required")
}

// GetSecret handles GET /api/v1/secrets/value?path=...&key=...
func (h *SecretsHandler) GetSecret(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	key := r.URL.Query().Get("key")

	if path == "" || key == "" {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_input",
			Message: "path and key query parameters required",
		})
		return
	}

	provider, err := h.getProvider(r)
	if err != nil {
		if errors.Is(err, secrets.ErrNotInitialized) {
			h.respondWithJSON(w, http.StatusPreconditionFailed, ErrorResponse{
				Error:   "not_initialized",
				Message: "secrets not initialized for this organization",
			})
			return
		}
		if secrets.IsAuthorizationError(err) || errors.Is(err, store.ErrForbidden) {
			h.respondWithJSON(w, http.StatusForbidden, ErrorResponse{
				Error:   "forbidden",
				Message: err.Error(),
			})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to get provider",
		})
		return
	}

	value, err := provider.Get(r.Context(), path, key)
	if err != nil {
		if errors.Is(err, secrets.ErrInvalidPath) || errors.Is(err, secrets.ErrInvalidKey) {
			h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
				Error:   "invalid_input",
				Message: err.Error(),
			})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to get secret",
		})
		return
	}

	if value == "" {
		h.respondWithJSON(w, http.StatusNotFound, ErrorResponse{
			Error:   "not_found",
			Message: "secret not found",
		})
		return
	}

	h.respondWithJSON(w, http.StatusOK, SecretValueResponse{Value: value})
}

// SetSecret handles PUT /api/v1/secrets/value?path=...&key=...
func (h *SecretsHandler) SetSecret(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	key := r.URL.Query().Get("key")

	if path == "" || key == "" {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_input",
			Message: "path and key query parameters required",
		})
		return
	}

	var req SecretValueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_input",
			Message: "invalid request body",
		})
		return
	}

	provider, err := h.getProvider(r)
	if err != nil {
		if errors.Is(err, secrets.ErrNotInitialized) {
			h.respondWithJSON(w, http.StatusPreconditionFailed, ErrorResponse{
				Error:   "not_initialized",
				Message: "secrets not initialized for this organization",
			})
			return
		}
		if secrets.IsAuthorizationError(err) || errors.Is(err, store.ErrForbidden) {
			h.respondWithJSON(w, http.StatusForbidden, ErrorResponse{
				Error:   "forbidden",
				Message: err.Error(),
			})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to get provider",
		})
		return
	}

	if err := provider.Set(r.Context(), path, key, req.Value); err != nil {
		if errors.Is(err, secrets.ErrInvalidPath) || errors.Is(err, secrets.ErrInvalidKey) {
			h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
				Error:   "invalid_input",
				Message: err.Error(),
			})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to set secret",
		})
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DeleteSecret handles DELETE /api/v1/secrets/value?path=...&key=...
func (h *SecretsHandler) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	key := r.URL.Query().Get("key")

	if path == "" || key == "" {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_input",
			Message: "path and key query parameters required",
		})
		return
	}

	provider, err := h.getProvider(r)
	if err != nil {
		if errors.Is(err, secrets.ErrNotInitialized) {
			h.respondWithJSON(w, http.StatusPreconditionFailed, ErrorResponse{
				Error:   "not_initialized",
				Message: "secrets not initialized for this organization",
			})
			return
		}
		if secrets.IsAuthorizationError(err) {
			h.respondWithJSON(w, http.StatusForbidden, ErrorResponse{
				Error:   "forbidden",
				Message: err.Error(),
			})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to get provider",
		})
		return
	}

	deleted, err := provider.Delete(r.Context(), path, key)
	if err != nil {
		if errors.Is(err, secrets.ErrInvalidPath) || errors.Is(err, secrets.ErrInvalidKey) {
			h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
				Error:   "invalid_input",
				Message: err.Error(),
			})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to delete secret",
		})
		return
	}

	if !deleted {
		h.respondWithJSON(w, http.StatusNotFound, ErrorResponse{
			Error:   "not_found",
			Message: "secret not found",
		})
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ListKeys handles GET /api/v1/secrets?path=...
func (h *SecretsHandler) ListKeys(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")

	if path == "" {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_input",
			Message: "path query parameter required",
		})
		return
	}

	provider, err := h.getProvider(r)
	if err != nil {
		if errors.Is(err, secrets.ErrNotInitialized) {
			h.respondWithJSON(w, http.StatusPreconditionFailed, ErrorResponse{
				Error:   "not_initialized",
				Message: "secrets not initialized for this organization",
			})
			return
		}
		if secrets.IsAuthorizationError(err) {
			h.respondWithJSON(w, http.StatusForbidden, ErrorResponse{
				Error:   "forbidden",
				Message: err.Error(),
			})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to get provider",
		})
		return
	}

	keys, err := provider.ListKeys(r.Context(), path)
	if err != nil {
		if errors.Is(err, secrets.ErrInvalidPath) {
			h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
				Error:   "invalid_input",
				Message: err.Error(),
			})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to list keys",
		})
		return
	}

	h.respondWithJSON(w, http.StatusOK, ListKeysResponse{Keys: keys})
}

// ListPaths handles GET /api/v1/secrets/paths
func (h *SecretsHandler) ListPaths(w http.ResponseWriter, r *http.Request) {
	provider, err := h.getProvider(r)
	if err != nil {
		if errors.Is(err, secrets.ErrNotInitialized) {
			h.respondWithJSON(w, http.StatusPreconditionFailed, ErrorResponse{
				Error:   "not_initialized",
				Message: "secrets not initialized for this organization",
			})
			return
		}
		if secrets.IsAuthorizationError(err) {
			h.respondWithJSON(w, http.StatusForbidden, ErrorResponse{
				Error:   "forbidden",
				Message: err.Error(),
			})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to get provider",
		})
		return
	}

	paths, err := provider.ListPaths(r.Context())
	if err != nil {
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to list paths",
		})
		return
	}

	h.respondWithJSON(w, http.StatusOK, ListPathsResponse{Paths: paths})
}

// BatchGet handles POST /api/v1/secrets/batch/get
func (h *SecretsHandler) BatchGet(w http.ResponseWriter, r *http.Request) {
	var req BatchGetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_input",
			Message: "invalid request body",
		})
		return
	}

	provider, project, err := h.batchGetProvider(r, &req)
	if err != nil {
		if errors.Is(err, secrets.ErrNotInitialized) {
			h.respondWithJSON(w, http.StatusPreconditionFailed, ErrorResponse{
				Error:   "not_initialized",
				Message: "secrets not initialized for this organization",
			})
			return
		}
		if secrets.IsAuthorizationError(err) || errors.Is(err, store.ErrForbidden) {
			h.respondWithJSON(w, http.StatusForbidden, ErrorResponse{
				Error:   "forbidden",
				Message: err.Error(),
			})
			return
		}
		if errors.Is(err, store.ErrInvalidInput) {
			h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: "invalid project-scoped secret selection"})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to get provider",
		})
		return
	}

	results, err := provider.GetMulti(r.Context(), req.Refs)
	if err != nil {
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to get secrets",
		})
		return
	}

	if project != nil {
		recordLocalContextSecretBatchRead(r.Context(), h.store, project, req.Refs)
	}
	h.respondWithJSON(w, http.StatusOK, BatchGetResponse{Secrets: results})
}

func recordLocalContextSecretBatchRead(ctx context.Context, value any, project *models.Project, refs []secrets.SecretRef) {
	references := make([]string, 0, len(refs))
	for _, ref := range refs {
		references = append(references, ref.Path+":"+ref.Key)
	}
	audit.Record(ctx, value, project.OrgID, "local_context.secret_batch_read", "project", project.ProjectID, models.JSONB{"references": references, "count": len(references)})
}

func (h *SecretsHandler) batchGetProvider(r *http.Request, req *BatchGetRequest) (secrets.Provider, *models.Project, error) {
	if req.ProjectID == "" {
		provider, err := h.getProvider(r)
		return provider, nil, err
	}
	project, err := h.store.GetProjectByID(r.Context(), req.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	roleStore, ok := h.store.(authz.RoleStore)
	if !ok {
		return nil, nil, store.ErrForbidden
	}
	allowed, err := canManageProjectSecrets(r.Context(), roleStore, project)
	if err != nil || !allowed {
		return nil, nil, store.ErrForbidden
	}
	if len(req.Selections) == 0 || len(req.Selections) != len(req.Refs) {
		return nil, nil, store.ErrInvalidInput
	}
	grants, ok := h.store.(worker.SecretGrantStore)
	if !ok {
		return nil, nil, store.ErrForbidden
	}
	for i, selection := range req.Selections {
		ref := req.Refs[i]
		if selection.Path != ref.Path || selection.Key != ref.Key || strings.TrimSpace(selection.JobName) == "" {
			return nil, nil, store.ErrInvalidInput
		}
		projectID := project.ProjectID
		job := &models.Job{OrgID: project.OrgID, UserID: project.OrgID, ProjectID: &projectID, Name: selection.JobName, ExecutionProfile: "standard", CIOrigin: "base"}
		if err := worker.AuthorizeSecretAccess(r.Context(), grants, job, ref.Path, ref.Key); err != nil {
			return nil, nil, store.ErrForbidden
		}
	}
	principal := checkauth.GetPrincipalFromContext(r.Context())
	if principal != nil && (!principal.HasCapability(tokencaps.ProjectsRead) || !principal.HasCapability(tokencaps.SecretsManage) || !principal.HasOrganization(project.OrgID)) {
		return nil, nil, store.ErrForbidden
	}
	provider, err := h.getProviderForOrg(r, project.OrgID)
	return provider, project, err
}

// canManageProjectSecrets restricts project-scoped secret downloads to a
// project owner, an administrator of the owning organization, or a scoped
// non-user token with secret-management capability. Secret grants restrict
// which selected values can be downloaded. They do not grant a project member
// permission to download a secret.
func canManageProjectSecrets(ctx context.Context, roleStore authz.RoleStore, project *models.Project) (bool, error) {
	principal := checkauth.GetPrincipalFromContext(ctx)
	user := checkauth.GetUserFromContext(ctx)
	if principal != nil {
		if !principal.HasCapability(tokencaps.ProjectsRead) || !principal.HasCapability(tokencaps.SecretsManage) || !principal.HasOrganization(project.OrgID) {
			return false, nil
		}
		if principal.CredentialType != "user_token" {
			return true, nil
		}
	}
	identity := authz.IdentityFromPrincipal(principal, user)
	return authz.NewResolver(roleStore).IsProjectOwner(ctx, identity, project.ProjectID)
}

// BatchSet handles POST /api/v1/secrets/batch/set
func (h *SecretsHandler) BatchSet(w http.ResponseWriter, r *http.Request) {
	var req BatchSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_input",
			Message: "invalid request body",
		})
		return
	}

	provider, err := h.getProvider(r)
	if err != nil {
		if errors.Is(err, secrets.ErrNotInitialized) {
			h.respondWithJSON(w, http.StatusPreconditionFailed, ErrorResponse{
				Error:   "not_initialized",
				Message: "secrets not initialized for this organization",
			})
			return
		}
		if secrets.IsAuthorizationError(err) {
			h.respondWithJSON(w, http.StatusForbidden, ErrorResponse{
				Error:   "forbidden",
				Message: err.Error(),
			})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to get provider",
		})
		return
	}

	for _, s := range req.Secrets {
		if err := provider.Set(r.Context(), s.Path, s.Key, s.Value); err != nil {
			if errors.Is(err, secrets.ErrInvalidPath) || errors.Is(err, secrets.ErrInvalidKey) {
				h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
					Error:   "invalid_input",
					Message: err.Error(),
				})
				return
			}
			h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
				Error:   "internal_error",
				Message: "failed to set secret",
			})
			return
		}
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// InitSecrets handles POST /api/v1/secrets/init
func (h *SecretsHandler) InitSecrets(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil && checkauth.GetPrincipalFromContext(r.Context()) == nil {
		h.respondWithJSON(w, http.StatusUnauthorized, ErrorResponse{
			Error:   "unauthorized",
			Message: "user not authenticated",
		})
		return
	}

	orgID, err := h.resolveSecretsOrg(r)
	if err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	if principal := checkauth.GetPrincipalFromContext(r.Context()); principal != nil && (!principal.HasCapability(tokencaps.SecretsManage) || !principal.HasOrganization(orgID)) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}

	db := store.GetDBFromContext(r.Context())

	// Check if already initialized
	var organization models.Organization
	if err := db.Select("secrets_initialized_at").
		Where("org_id = ?", orgID).
		First(&organization).Error; err != nil {
		h.respondWithJSON(w, http.StatusNotFound, ErrorResponse{
			Error:   "not_found",
			Message: "user not found",
		})
		return
	}

	if organization.SecretsInitializedAt != nil {
		h.respondWithJSON(w, http.StatusConflict, ErrorResponse{
			Error:   "already_exists",
			Message: "secrets already initialized",
		})
		return
	}

	// Initialize org secrets
	if err := h.keyManager.InitializeOrgSecrets(db, orgID); err != nil {
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to initialize secrets",
		})
		return
	}

	// Mark the organization as initialized.
	now := nowUTC()
	if err := db.Model(&models.Organization{}).
		Where("org_id = ?", orgID).
		Update("secrets_initialized_at", now).Error; err != nil {
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to update user",
		})
		return
	}

	h.respondWithJSON(w, http.StatusCreated, InitResponse{
		Status: "initialized",
		OrgID:  orgID,
	})
}

// nowUTC returns current time in UTC (can be mocked in tests)
var nowUTC = func() interface{} {
	return "now()"
}

// ---- Admin Endpoints ----

// MasterKeyRequest represents a request to create a master key
type MasterKeyRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// MasterKeyResponse represents a master key in responses
type MasterKeyResponse struct {
	KeyID       string `json:"key_id"`
	Name        string `json:"name"`
	IsActive    bool   `json:"is_active"`
	IsPrimary   bool   `json:"is_primary"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// MasterKeysListResponse represents a list of master keys
type MasterKeysListResponse struct {
	MasterKeys []MasterKeyResponse `json:"master_keys"`
}

// RotateResponse represents a rotation result
type RotateResponse struct {
	Status  string `json:"status"`
	KeyName string `json:"key_name"`
}

// CreateMasterKey handles POST /api/v1/admin/secrets/master-keys
// Creates a new master key entry in the database.
// The key must already exist in REACTORCIDE_MASTER_KEYS environment variable.
func (h *SecretsHandler) CreateMasterKey(w http.ResponseWriter, r *http.Request) {
	var req MasterKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_input",
			Message: "invalid request body",
		})
		return
	}

	if req.Name == "" {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_input",
			Message: "name is required",
		})
		return
	}

	mk, err := h.keyManager.RegisterMasterKey(store.GetDBFromContext(r.Context()), req.Name, req.Description)
	if err != nil {
		if err.Error() == "UNIQUE constraint violation" ||
			(err != nil && (err.Error() == "master key "+req.Name+" not found in REACTORCIDE_MASTER_KEYS" ||
				strings.Contains(err.Error(), "duplicate key value"))) {
			h.respondWithJSON(w, http.StatusConflict, ErrorResponse{
				Error:   "conflict",
				Message: err.Error(),
			})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: err.Error(),
		})
		return
	}

	h.respondWithJSON(w, http.StatusCreated, MasterKeyResponse{
		KeyID:       mk.KeyID,
		Name:        mk.Name,
		IsActive:    mk.IsActive,
		IsPrimary:   mk.IsPrimary,
		Description: mk.Description,
		CreatedAt:   mk.CreatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// ListMasterKeys handles GET /api/v1/admin/secrets/master-keys
// Lists all master keys in the database.
func (h *SecretsHandler) ListMasterKeys(w http.ResponseWriter, r *http.Request) {
	var masterKeys []models.MasterKey
	if err := store.GetDBFromContext(r.Context()).Order("created_at DESC").Find(&masterKeys).Error; err != nil {
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: "failed to list master keys",
		})
		return
	}

	response := MasterKeysListResponse{
		MasterKeys: make([]MasterKeyResponse, len(masterKeys)),
	}
	for i, mk := range masterKeys {
		response.MasterKeys[i] = MasterKeyResponse{
			KeyID:       mk.KeyID,
			Name:        mk.Name,
			IsActive:    mk.IsActive,
			IsPrimary:   mk.IsPrimary,
			Description: mk.Description,
			CreatedAt:   mk.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}

	h.respondWithJSON(w, http.StatusOK, response)
}

// RotateMasterKey handles POST /api/v1/admin/secrets/master-keys/{name}/rotate
// Encrypts all org keys with the specified master key.
func (h *SecretsHandler) RotateMasterKey(w http.ResponseWriter, r *http.Request) {
	keyName := GetIDFromContext(r, "key_name")
	if keyName == "" {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_input",
			Message: "key name is required",
		})
		return
	}

	if err := h.keyManager.RotateToKey(store.GetDBFromContext(r.Context()), keyName); err != nil {
		if strings.Contains(err.Error(), "not found") {
			h.respondWithJSON(w, http.StatusNotFound, ErrorResponse{
				Error:   "not_found",
				Message: err.Error(),
			})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: err.Error(),
		})
		return
	}

	h.respondWithJSON(w, http.StatusOK, RotateResponse{
		Status:  "rotated",
		KeyName: keyName,
	})
}

// DecommissionMasterKey handles DELETE /api/v1/admin/secrets/master-keys/{name}
// Removes a master key after rotation is complete.
func (h *SecretsHandler) DecommissionMasterKey(w http.ResponseWriter, r *http.Request) {
	keyName := GetIDFromContext(r, "key_name")
	if keyName == "" {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_input",
			Message: "key name is required",
		})
		return
	}

	if err := h.keyManager.DecommissionKey(store.GetDBFromContext(r.Context()), keyName); err != nil {
		if errors.Is(err, secrets.ErrCannotDecommissionPrimary) {
			h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{
				Error:   "bad_request",
				Message: "cannot decommission the primary key",
			})
			return
		}
		if strings.Contains(err.Error(), "not found") {
			h.respondWithJSON(w, http.StatusNotFound, ErrorResponse{
				Error:   "not_found",
				Message: err.Error(),
			})
			return
		}
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: err.Error(),
		})
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{
		"status":   "decommissioned",
		"key_name": keyName,
	})
}

// SyncPrimary handles POST /api/v1/admin/secrets/sync-primary
// Syncs the primary key in database to match the environment configuration.
func (h *SecretsHandler) SyncPrimary(w http.ResponseWriter, r *http.Request) {
	if err := h.keyManager.SyncPrimaryFromEnv(store.GetDBFromContext(r.Context())); err != nil {
		h.respondWithJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: err.Error(),
		})
		return
	}

	primaryName, _ := h.keyManager.GetPrimaryKey()
	h.respondWithJSON(w, http.StatusOK, map[string]string{
		"status":  "synced",
		"primary": primaryName,
	})
}
