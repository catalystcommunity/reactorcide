package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/lib/pq"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/envdefaults"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagepolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
)

// buildSettingsRoutes serves the WP14 REST API (plan items C1, C3, C4).
// Every route works with the admin API token.
//
//	GET               /api/v1/organizations/ORG/env-defaults
//	PUT|DELETE        /api/v1/organizations/ORG/env-defaults/KEY
//	GET               /api/v1/organizations/ORG/worker-classes/CLASS/env-defaults
//	PUT|DELETE        /api/v1/organizations/ORG/worker-classes/CLASS/env-defaults/KEY
//	GET               /api/v1/projects/PROJECT/env-defaults
//	PUT|DELETE        /api/v1/projects/PROJECT/env-defaults/KEY
//	GET|PUT           /api/v1/organizations/ORG/image-policy
//	GET               /api/v1/jobs/JOB/links
type buildSettingsRoutes struct {
	BaseHandler
	store store.Store
}

func newBuildSettingsRoutes(appStore store.Store) *buildSettingsRoutes {
	return &buildSettingsRoutes{store: appStore}
}

// serveOrg handles ORG/env-defaults[/KEY], ORG/worker-classes/CLASS/env-defaults[/KEY]
// and ORG/image-policy. It returns false for other paths.
func (s *buildSettingsRoutes) serveOrg(w http.ResponseWriter, r *http.Request, path string,
	transactionMiddleware, authMiddleware func(http.Handler) http.Handler) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	var handler http.HandlerFunc
	switch {
	case len(parts) == 2 && parts[1] == "image-policy":
		handler = func(w http.ResponseWriter, r *http.Request) { s.serveImagePolicy(w, r, parts[0]) }
	case (len(parts) == 2 || len(parts) == 3) && parts[1] == "env-defaults":
		key := ""
		if len(parts) == 3 {
			key = parts[2]
		}
		handler = func(w http.ResponseWriter, r *http.Request) {
			s.serveEnvDefaults(w, r, parts[0], models.EnvDefaultScopeOrg, "", key)
		}
	case (len(parts) == 4 || len(parts) == 5) && parts[1] == "worker-classes" && parts[3] == "env-defaults":
		key := ""
		if len(parts) == 5 {
			key = parts[4]
		}
		handler = func(w http.ResponseWriter, r *http.Request) {
			s.serveEnvDefaults(w, r, parts[0], models.EnvDefaultScopeWorkerClass, parts[2], key)
		}
	default:
		return false
	}
	transactionMiddleware(authMiddleware(handler)).ServeHTTP(w, r)
	return true
}

// serveProject handles PROJECT/env-defaults[/KEY].
func (s *buildSettingsRoutes) serveProject(w http.ResponseWriter, r *http.Request, parts []string,
	transactionMiddleware, authMiddleware func(http.Handler) http.Handler) bool {
	if (len(parts) != 2 && len(parts) != 3) || parts[1] != "env-defaults" {
		return false
	}
	key := ""
	if len(parts) == 3 {
		key = parts[2]
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		project, err := s.store.GetProjectByID(r.Context(), parts[0])
		if err != nil || project == nil {
			s.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
			return
		}
		s.serveEnvDefaultsForOrg(w, r, project.OrgID, models.EnvDefaultScopeProject, project.ProjectID, key)
	})
	transactionMiddleware(authMiddleware(handler)).ServeHTTP(w, r)
	return true
}

type envDefaultsResponse struct {
	Scope       string              `json:"scope"`
	WorkerClass string              `json:"worker_class,omitempty"`
	ProjectID   string              `json:"project_id,omitempty"`
	EnvDefaults []models.EnvDefault `json:"env_defaults"`
}

type envDefaultRequest struct {
	Value string `json:"value"`
}

// envDefaultCapabilities returns the read and the write capability of a
// scope.
func envDefaultCapabilities(scope string) (string, string) {
	switch scope {
	case models.EnvDefaultScopeWorkerClass:
		return tokencaps.WorkersRead, tokencaps.WorkersManage
	case models.EnvDefaultScopeProject:
		return tokencaps.ProjectsRead, tokencaps.ProjectsManage
	}
	return tokencaps.OrganizationsRead, tokencaps.OrganizationsManage
}

func (s *buildSettingsRoutes) serveEnvDefaults(w http.ResponseWriter, r *http.Request, orgName, scope, scopeRef, key string) {
	org, ok := s.organization(w, r, orgName)
	if !ok {
		return
	}
	s.serveEnvDefaultsForOrg(w, r, org.OrgID, scope, scopeRef, key)
}

func (s *buildSettingsRoutes) serveEnvDefaultsForOrg(w http.ResponseWriter, r *http.Request, orgID, scope, scopeRef, key string) {
	defaults, ok := s.store.(store.EnvDefaultsStore)
	if !ok {
		s.respondWithError(w, http.StatusNotImplemented, errors.New("environment defaults are not supported by this store"))
		return
	}
	readCap, writeCap := envDefaultCapabilities(scope)
	switch {
	case r.Method == http.MethodGet && key == "":
		if !principalAllowsOrg(r.Context(), s.store, orgID, readCap) {
			s.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		rows, err := defaults.ListEnvDefaults(r.Context(), orgID, scope, scopeRef)
		if err != nil {
			s.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		response := envDefaultsResponse{Scope: scope, EnvDefaults: rows}
		if rows == nil {
			response.EnvDefaults = []models.EnvDefault{}
		}
		if scope == models.EnvDefaultScopeWorkerClass {
			response.WorkerClass = scopeRef
		}
		if scope == models.EnvDefaultScopeProject {
			response.ProjectID = scopeRef
		}
		s.respondWithJSON(w, http.StatusOK, response)
	case (r.Method == http.MethodPut || r.Method == http.MethodPatch) && key != "":
		if !principalAllowsOrg(r.Context(), s.store, orgID, writeCap) {
			s.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		var req envDefaultRequest
		if err := decodeStrict(r, &req); err != nil {
			s.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: "the body must be {\"value\": \"...\"}"})
			return
		}
		if err := envdefaults.Validate(key, req.Value); err != nil {
			s.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: err.Error()})
			return
		}
		row, err := defaults.SetEnvDefault(r.Context(), orgID, scope, scopeRef, key, req.Value)
		if err != nil {
			if errors.Is(err, store.ErrInvalidInput) {
				s.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: err.Error()})
				return
			}
			s.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		audit.Record(r.Context(), s.store, orgID, "env_default.set", "env_default", scope+":"+scopeRef+":"+key,
			models.JSONB{"scope": scope, "scope_ref": scopeRef, "key": key})
		s.respondWithJSON(w, http.StatusOK, row)
	case r.Method == http.MethodDelete && key != "":
		if !principalAllowsOrg(r.Context(), s.store, orgID, writeCap) {
			s.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		if err := defaults.DeleteEnvDefault(r.Context(), orgID, scope, scopeRef, key); err != nil {
			s.respondWithError(w, http.StatusNotFound, err)
			return
		}
		audit.Record(r.Context(), s.store, orgID, "env_default.delete", "env_default", scope+":"+scopeRef+":"+key,
			models.JSONB{"scope": scope, "scope_ref": scopeRef, "key": key})
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

type imagePolicyRequest struct {
	Enabled         bool     `json:"enabled"`
	AllowedPrefixes []string `json:"allowed_prefixes"`
	RequireDigest   bool     `json:"require_digest"`
}

func (s *buildSettingsRoutes) serveImagePolicy(w http.ResponseWriter, r *http.Request, orgName string) {
	policies, ok := s.store.(store.ImagePolicyStore)
	if !ok {
		s.respondWithError(w, http.StatusNotImplemented, errors.New("image policies are not supported by this store"))
		return
	}
	org, ok := s.organization(w, r, orgName)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !principalAllowsOrg(r.Context(), s.store, org.OrgID, tokencaps.OrganizationsRead) {
			s.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		policy, err := policies.GetImagePolicy(r.Context(), org.OrgID)
		if err != nil {
			s.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		s.respondWithJSON(w, http.StatusOK, policy)
	case http.MethodPut, http.MethodPatch:
		if !principalAllowsOrg(r.Context(), s.store, org.OrgID, tokencaps.PoliciesManage) {
			s.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		var req imagePolicyRequest
		if err := decodeStrict(r, &req); err != nil {
			s.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input",
				Message: "the body must be {\"enabled\": bool, \"allowed_prefixes\": [...], \"require_digest\": bool}"})
			return
		}
		policy := &models.ImagePolicy{OrgID: org.OrgID, Enabled: req.Enabled, AllowedPrefixes: pq.StringArray(trimAll(req.AllowedPrefixes)), RequireDigest: req.RequireDigest}
		if err := imagepolicy.ValidatePolicy(policy); err != nil {
			s.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: err.Error()})
			return
		}
		if err := policies.SetImagePolicy(r.Context(), policy); err != nil {
			s.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		audit.Record(r.Context(), s.store, org.OrgID, "image_policy.set", "image_policy", org.OrgID,
			models.JSONB{"enabled": policy.Enabled, "allowed_prefixes": []string(policy.AllowedPrefixes), "require_digest": policy.RequireDigest})
		saved, err := policies.GetImagePolicy(r.Context(), org.OrgID)
		if err != nil {
			s.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		s.respondWithJSON(w, http.StatusOK, saved)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *buildSettingsRoutes) organization(w http.ResponseWriter, r *http.Request, name string) (*models.Organization, bool) {
	orgs, ok := s.store.(organizationStore)
	if !ok {
		s.respondWithError(w, http.StatusNotImplemented, errors.New("organizations are not supported by this store"))
		return nil, false
	}
	org, err := orgs.GetOrganizationByName(r.Context(), name)
	if err != nil {
		s.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return nil, false
	}
	return org, true
}

func trimAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

// imagePolicyRefusal returns the decision reason when the organization image
// policy refuses the image of a job that is being submitted, or "".
func imagePolicyRefusal(ctx context.Context, appStore store.Store, job *models.Job) string {
	policies, ok := appStore.(store.ImagePolicyStore)
	if !ok || job.OwnershipOrgID() == "" {
		return ""
	}
	policy, err := policies.GetImagePolicy(ctx, job.OwnershipOrgID())
	if err != nil {
		// The claim checks the policy again and fails closed.
		return ""
	}
	image := worker.DefaultRunnerImage
	if job.ContainerImage != nil && *job.ContainerImage != "" {
		image = *job.ContainerImage
	} else if job.RunnerImage != "" {
		image = job.RunnerImage
	}
	if err := imagepolicy.Check(policy, image); err != nil {
		return err.Error()
	}
	return ""
}

type jobLinksResponse struct {
	JobID string           `json:"job_id"`
	Links []models.JobLink `json:"links"`
}

// GetJobLinks serves GET /api/v1/jobs/JOB/links.
func (h *JobHandler) GetJobLinks(w http.ResponseWriter, r *http.Request) {
	jobID := h.getID(r, "job_id")
	if jobID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	job, err := h.store.GetJobByID(r.Context(), jobID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil && checkauth.GetPrincipalFromContext(r.Context()) == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	if !h.canSubjectAccessJob(r.Context(), user, job, tokencaps.JobsRead) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	links := []models.JobLink{}
	if linkStore, ok := h.store.(store.JobLinkStore); ok {
		if links, err = linkStore.ListJobLinks(r.Context(), job.JobID); err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
	}
	h.respondWithJSON(w, http.StatusOK, jobLinksResponse{JobID: job.JobID, Links: links})
}
