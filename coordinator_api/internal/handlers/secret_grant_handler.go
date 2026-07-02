package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	pathmatch "path"
	"regexp"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func (h *ProjectHandler) ListGlobalSecretGrants(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	grantStore, ok := h.store.(projectSecretGrantStore)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("secret grant store not available"))
		return
	}
	ownerID, projectID, ok := h.secretGrantScopeFromQuery(w, r, user.UserID)
	if !ok {
		return
	}
	grants, err := grantStore.ListSecretGrants(r.Context(), ownerID, projectID)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, ListSecretGrantsResponse{Grants: grants, Total: len(grants)})
}

func (h *ProjectHandler) CreateGlobalSecretGrant(w http.ResponseWriter, r *http.Request) {
	h.createSecretGrantWithScope(w, r, false)
}

func (h *ProjectHandler) GetGlobalSecretGrant(w http.ResponseWriter, r *http.Request) {
	h.getSecretGrantWithScope(w, r, false)
}

func (h *ProjectHandler) UpdateGlobalSecretGrant(w http.ResponseWriter, r *http.Request) {
	h.updateSecretGrantWithScope(w, r, false)
}

func (h *ProjectHandler) DeleteGlobalSecretGrant(w http.ResponseWriter, r *http.Request) {
	h.deleteSecretGrantWithScope(w, r, false)
}

func (h *ProjectHandler) ApplySecretGrants(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	grantStore, ok := h.store.(projectSecretGrantStore)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("secret grant store not available"))
		return
	}
	var req SecretGrantApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	resp := SecretGrantApplyResponse{DryRun: req.DryRun}
	seenByScope := map[string]map[string]bool{}
	for _, item := range req.Grants {
		ownerID, projectID, ok := h.secretGrantScopeFromRequest(w, r, user.UserID, item)
		if !ok {
			return
		}
		scopeKey := secretGrantScopeKey(ownerID, projectID)
		if seenByScope[scopeKey] == nil {
			seenByScope[scopeKey] = map[string]bool{}
		}
		if item.Name == "" {
			h.respondWithError(w, http.StatusBadRequest, fmt.Errorf("secret grant name is required"))
			return
		}
		seenByScope[scopeKey][item.Name] = true

		existing, err := grantStore.GetSecretGrant(r.Context(), ownerID, projectID, item.Name)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		if strings.EqualFold(item.State, "absent") {
			if existing == nil {
				continue
			}
			resp.Deleted = append(resp.Deleted, *existing)
			if !req.DryRun {
				if err := grantStore.DeleteSecretGrant(r.Context(), ownerID, projectID, item.Name); err != nil {
					h.respondWithError(w, http.StatusInternalServerError, err)
					return
				}
			}
			continue
		}

		desired := &models.SecretGrant{UserID: ownerID, ProjectID: projectID}
		if existing != nil {
			*desired = *existing
		}
		if err := applySecretGrantRequest(desired, item); err != nil {
			h.respondWithError(w, http.StatusBadRequest, err)
			return
		}
		desired.UserID = ownerID
		desired.ProjectID = projectID

		if existing == nil {
			resp.Created = append(resp.Created, *desired)
			if !req.DryRun {
				if err := grantStore.CreateSecretGrant(r.Context(), desired); err != nil {
					h.respondWithError(w, http.StatusInternalServerError, err)
					return
				}
			}
			continue
		}
		if secretGrantEquivalent(*existing, *desired) {
			resp.Unchanged = append(resp.Unchanged, *existing)
			continue
		}
		resp.Updated = append(resp.Updated, *desired)
		if !req.DryRun {
			if err := grantStore.UpdateSecretGrant(r.Context(), desired); err != nil {
				h.respondWithError(w, http.StatusInternalServerError, err)
				return
			}
		}
	}

	if req.Prune {
		for scopeKey, desiredNames := range seenByScope {
			ownerID, projectID := parseSecretGrantScopeKey(scopeKey)
			current, err := grantStore.ListSecretGrants(r.Context(), ownerID, projectID)
			if err != nil {
				h.respondWithError(w, http.StatusInternalServerError, err)
				return
			}
			for _, grant := range current {
				if desiredNames[grant.Name] {
					continue
				}
				resp.Deleted = append(resp.Deleted, grant)
				if !req.DryRun {
					if err := grantStore.DeleteSecretGrant(r.Context(), ownerID, projectID, grant.Name); err != nil {
						h.respondWithError(w, http.StatusInternalServerError, err)
						return
					}
				}
			}
		}
	}

	h.respondWithJSON(w, http.StatusOK, resp)
}

func (h *ProjectHandler) createSecretGrantWithScope(w http.ResponseWriter, r *http.Request, projectRoute bool) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	grantStore, ok := h.store.(projectSecretGrantStore)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("secret grant store not available"))
		return
	}
	var req SecretGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	if !projectRoute {
		if req.ProjectID == "" {
			req.ProjectID = r.URL.Query().Get("project_id")
		}
		if req.Project == "" {
			req.Project = r.URL.Query().Get("project")
		}
	}
	var ownerID string
	var projectID *string
	var scopeOK bool
	if projectRoute {
		ownerID, projectID, scopeOK = h.secretGrantScope(w, r, user.UserID, projectRoute)
	} else {
		ownerID, projectID, scopeOK = h.secretGrantScopeFromRequest(w, r, user.UserID, req)
	}
	if !scopeOK {
		return
	}
	grant := &models.SecretGrant{UserID: ownerID, ProjectID: projectID}
	if err := applySecretGrantRequest(grant, req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	if err := grantStore.CreateSecretGrant(r.Context(), grant); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	h.respondWithJSON(w, http.StatusCreated, grant)
}

func (h *ProjectHandler) getSecretGrantWithScope(w http.ResponseWriter, r *http.Request, projectRoute bool) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	grantStore, ok := h.store.(projectSecretGrantStore)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("secret grant store not available"))
		return
	}
	ownerID, projectID, ok := h.secretGrantScope(w, r, user.UserID, projectRoute)
	if !ok {
		return
	}
	ref := h.getID(r, "grant_id")
	if ref == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	grant, err := grantStore.GetSecretGrant(r.Context(), ownerID, projectID, ref)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, grant)
}

func (h *ProjectHandler) updateSecretGrantWithScope(w http.ResponseWriter, r *http.Request, projectRoute bool) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	grantStore, ok := h.store.(projectSecretGrantStore)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("secret grant store not available"))
		return
	}
	ownerID, projectID, ok := h.secretGrantScope(w, r, user.UserID, projectRoute)
	if !ok {
		return
	}
	ref := h.getID(r, "grant_id")
	if ref == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	var req SecretGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	grant, err := grantStore.GetSecretGrant(r.Context(), ownerID, projectID, ref)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if err := applySecretGrantRequest(grant, req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	if err := grantStore.UpdateSecretGrant(r.Context(), grant); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, grant)
}

func (h *ProjectHandler) deleteSecretGrantWithScope(w http.ResponseWriter, r *http.Request, projectRoute bool) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	grantStore, ok := h.store.(projectSecretGrantStore)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("secret grant store not available"))
		return
	}
	ownerID, projectID, ok := h.secretGrantScope(w, r, user.UserID, projectRoute)
	if !ok {
		return
	}
	ref := h.getID(r, "grant_id")
	if ref == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	if err := grantStore.DeleteSecretGrant(r.Context(), ownerID, projectID, ref); err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) secretGrantScope(w http.ResponseWriter, r *http.Request, fallbackUserID string, projectRoute bool) (string, *string, bool) {
	if !projectRoute {
		return h.secretGrantScopeFromQuery(w, r, fallbackUserID)
	}
	project, ownerID, ok := h.projectAndOwner(w, r, fallbackUserID)
	if !ok {
		return "", nil, false
	}
	return ownerID, &project.ProjectID, true
}

func (h *ProjectHandler) secretGrantScopeFromQuery(w http.ResponseWriter, r *http.Request, fallbackUserID string) (string, *string, bool) {
	req := SecretGrantRequest{
		ProjectID: r.URL.Query().Get("project_id"),
		Project:   r.URL.Query().Get("project"),
	}
	return h.secretGrantScopeFromRequest(w, r, fallbackUserID, req)
}

func (h *ProjectHandler) secretGrantScopeFromRequest(w http.ResponseWriter, r *http.Request, fallbackUserID string, req SecretGrantRequest) (string, *string, bool) {
	projectRef := strings.TrimSpace(req.ProjectID)
	if projectRef == "" {
		projectRef = strings.TrimSpace(req.Project)
	}
	if projectRef == "" {
		return fallbackUserID, nil, true
	}
	project, err := h.resolveSecretGrantProject(r, projectRef)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return "", nil, false
	}
	ownerID := fallbackUserID
	if project.UserID != nil && *project.UserID != "" {
		ownerID = *project.UserID
	}
	return ownerID, &project.ProjectID, true
}

func (h *ProjectHandler) resolveSecretGrantProject(r *http.Request, ref string) (*models.Project, error) {
	if project, err := h.store.GetProjectByID(r.Context(), ref); err == nil {
		return project, nil
	}
	projects, err := h.store.ListProjects(r.Context(), 10000, 0)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		if project.ProjectID == ref || project.Name == ref || project.RepoURL == ref {
			return &project, nil
		}
	}
	return nil, store.ErrNotFound
}

func applySecretGrantRequest(grant *models.SecretGrant, req SecretGrantRequest) error {
	if req.Name != "" {
		grant.Name = req.Name
	}
	if grant.Name == "" {
		return fmt.Errorf("secret grant name is required")
	}

	secretPattern := strings.TrimSpace(req.SecretPathPattern)
	secretMatch := strings.TrimSpace(req.SecretPathMatch)
	if secretPattern == "" && req.SecretPathPrefix != "" {
		secretPattern = strings.TrimSpace(req.SecretPathPrefix)
		if secretMatch == "" {
			secretMatch = models.SecretGrantMatchPrefix
		}
	}
	if secretPattern != "" {
		grant.SecretPathPattern = secretPattern
	}
	if secretMatch != "" {
		grant.SecretPathMatch = secretMatch
	}
	if grant.SecretPathMatch == "" {
		grant.SecretPathMatch = models.SecretGrantMatchPrefix
	}
	if grant.SecretPathPattern == "" {
		return fmt.Errorf("secret path pattern is required")
	}
	if !validSecretGrantMatch(grant.SecretPathMatch, false) {
		return fmt.Errorf("invalid secret_path_match %q", grant.SecretPathMatch)
	}
	if err := validateSecretGrantPattern(grant.SecretPathMatch, grant.SecretPathPattern); err != nil {
		return err
	}

	jobPattern := strings.TrimSpace(req.JobNamePattern)
	jobMatch := strings.TrimSpace(req.JobNameMatch)
	if jobPattern == "" && req.JobName != "" {
		jobPattern = strings.TrimSpace(req.JobName)
		if jobMatch == "" {
			jobMatch = models.SecretGrantMatchExact
		}
	}
	if jobPattern != "" {
		grant.JobNamePattern = jobPattern
	}
	if jobMatch != "" {
		grant.JobNameMatch = jobMatch
	}
	if grant.JobNameMatch == "" {
		grant.JobNameMatch = models.SecretGrantMatchAny
	}
	if grant.JobNameMatch == models.SecretGrantMatchAny {
		grant.JobNamePattern = ""
	} else if grant.JobNamePattern == "" {
		return fmt.Errorf("job name pattern is required when job_name_match is %q", grant.JobNameMatch)
	}
	if !validSecretGrantMatch(grant.JobNameMatch, true) {
		return fmt.Errorf("invalid job_name_match %q", grant.JobNameMatch)
	}
	if err := validateSecretGrantPattern(grant.JobNameMatch, grant.JobNamePattern); err != nil {
		return err
	}
	if req.Description != "" {
		grant.Description = req.Description
	}
	return nil
}

func validSecretGrantMatch(match string, allowAny bool) bool {
	switch match {
	case models.SecretGrantMatchExact, models.SecretGrantMatchPrefix, models.SecretGrantMatchGlob, models.SecretGrantMatchRegex:
		return true
	case models.SecretGrantMatchAny:
		return allowAny
	default:
		return false
	}
}

func validateSecretGrantPattern(match, pattern string) error {
	switch match {
	case models.SecretGrantMatchGlob:
		if _, err := pathmatch.Match(pattern, ""); err != nil {
			return fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
		}
	case models.SecretGrantMatchRegex:
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid regex pattern %q: %w", pattern, err)
		}
	}
	return nil
}

func secretGrantEquivalent(a, b models.SecretGrant) bool {
	return a.Name == b.Name &&
		a.UserID == b.UserID &&
		stringPtrEqual(a.ProjectID, b.ProjectID) &&
		a.SecretPathMatch == b.SecretPathMatch &&
		a.SecretPathPattern == b.SecretPathPattern &&
		a.JobNameMatch == b.JobNameMatch &&
		a.JobNamePattern == b.JobNamePattern &&
		a.Description == b.Description
}

func stringPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func secretGrantScopeKey(ownerID string, projectID *string) string {
	if projectID == nil {
		return ownerID + "|"
	}
	return ownerID + "|" + *projectID
}

func parseSecretGrantScopeKey(key string) (string, *string) {
	ownerID, projectID, found := strings.Cut(key, "|")
	if !found || projectID == "" {
		return ownerID, nil
	}
	return ownerID, &projectID
}
