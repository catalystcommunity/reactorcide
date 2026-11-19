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
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// refClassAdminStore is the store surface of the ref class routes.
type refClassAdminStore interface {
	store.RefClassStore
	GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
	ListProjects(ctx context.Context, limit, offset int) ([]models.Project, error)
}

// RefClassHandler serves ref class rules and explain (docs/ref-classes.md).
// The routes work with no logins (the admin token):
//
//	GET|PUT|DELETE /api/v1/organizations/{org}/ref-class-rules[/{name}]
//	POST           /api/v1/organizations/{org}/ref-classes/explain
//	GET|PUT|DELETE /api/v1/projects/{project}/ref-class-rules[/{name}]
//	POST           /api/v1/projects/{project}/ref-classes/explain
//
// Reading needs projects:read in the organization. An organization rule
// needs policies:manage and an org admin. A project rule needs
// policies:manage and a project owner (an org admin is an owner of every
// project of the organization).
type RefClassHandler struct {
	BaseHandler
	store    refClassAdminStore
	appStore store.Store
}

func NewRefClassHandler(appStore store.Store) *RefClassHandler {
	value, _ := appStore.(refClassAdminStore)
	return &RefClassHandler{store: value, appStore: appStore}
}

// RefClassRuleRequest is the body of PUT .../ref-class-rules/{name}.
type RefClassRuleRequest struct {
	Position        int      `json:"position"`
	Description     string   `json:"description,omitempty"`
	EventTypes      []string `json:"event_types,omitempty"`
	BranchGlob      string   `json:"branch_glob,omitempty"`
	TagGlob         string   `json:"tag_glob,omitempty"`
	BaseBranchGlob  string   `json:"base_branch_glob,omitempty"`
	IsPR            *bool    `json:"is_pr,omitempty"`
	IsFork          *bool    `json:"is_fork,omitempty"`
	IsDefaultBranch *bool    `json:"is_default_branch,omitempty"`
	Classes         []string `json:"classes"`
	// Enabled defaults to true.
	Enabled *bool `json:"enabled,omitempty"`
}

// RefClassRulesResponse lists the rules of one scope. Inherited holds the
// organization rules for a project listing.
type RefClassRulesResponse struct {
	Rules     []models.RefClassRule `json:"rules"`
	Inherited []models.RefClassRule `json:"inherited,omitempty"`
	Builtin   map[string][]string   `json:"builtin"`
}

// RefClassExplainRequest is the body of POST .../ref-classes/explain. With
// EventID, the coordinator uses the stored facts of that event, and the
// response also has the stored classes. Otherwise the facts come from the
// body.
type RefClassExplainRequest struct {
	EventID string               `json:"event_id,omitempty"`
	Facts   models.RefClassFacts `json:"facts"`
}

// RefClassExplainResponse is the explain output.
type RefClassExplainResponse = refclass.EventExplanation

// ServeOrganization routes /api/v1/organizations/{org}/ref-class-rules[/...]
// and /api/v1/organizations/{org}/ref-classes/explain. rest is the path
// after "{org}/".
func (h *RefClassHandler) ServeOrganization(w http.ResponseWriter, r *http.Request, orgName, rest string) {
	if h.store == nil {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("ref class rules are not available"))
		return
	}
	org, err := h.store.GetOrganizationByName(r.Context(), orgName)
	if err != nil {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	h.serve(w, r, org.OrgID, nil, rest)
}

// ServeProject routes /api/v1/projects/{project}/ref-class-rules[/...] and
// /api/v1/projects/{project}/ref-classes/explain. projectRef is a project
// ID, name, or repository URL.
func (h *RefClassHandler) ServeProject(w http.ResponseWriter, r *http.Request, projectRef, rest string) {
	if h.store == nil {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("ref class rules are not available"))
		return
	}
	project, err := h.resolveProject(r.Context(), projectRef)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	h.serve(w, r, project.OwnershipOrgID(), project, rest)
}

func (h *RefClassHandler) resolveProject(ctx context.Context, ref string) (*models.Project, error) {
	if project, err := h.store.GetProjectByID(ctx, ref); err == nil {
		return project, nil
	}
	projects, err := h.store.ListProjects(ctx, 10000, 0)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		if projects[i].Name == ref || projects[i].RepoURL == ref {
			return &projects[i], nil
		}
	}
	return nil, store.ErrNotFound
}

func (h *RefClassHandler) serve(w http.ResponseWriter, r *http.Request, orgID string, project *models.Project, rest string) {
	var projectID *string
	if project != nil {
		projectID = &project.ProjectID
	}
	parts := []string{}
	for _, part := range strings.Split(strings.Trim(rest, "/"), "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if !principalAllowsOrg(r.Context(), h.appStore, orgID, tokencaps.ProjectsRead) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	switch {
	case len(parts) == 2 && parts[0] == "ref-classes" && parts[1] == "explain" && r.Method == http.MethodPost:
		h.explain(w, r, orgID, projectID)
	case len(parts) == 1 && parts[0] == "ref-class-rules" && r.Method == http.MethodGet:
		h.list(w, r, orgID, projectID)
	case len(parts) == 2 && parts[0] == "ref-class-rules" && r.Method == http.MethodGet:
		rule, err := h.store.GetRefClassRule(r.Context(), orgID, projectID, parts[1])
		if err != nil {
			h.respondWithError(w, refClassErrorStatus(err), err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, rule)
	case len(parts) == 2 && parts[0] == "ref-class-rules" && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		if !h.canManage(r.Context(), orgID, project) {
			h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		if r.Method == http.MethodPut {
			h.set(w, r, orgID, projectID, parts[1])
		} else {
			h.delete(w, r, orgID, projectID, parts[1])
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// canManage: an org rule needs an org admin, a project rule a project owner.
func (h *RefClassHandler) canManage(ctx context.Context, orgID string, project *models.Project) bool {
	if project == nil {
		return principalAllowsOrg(ctx, h.appStore, orgID, tokencaps.PoliciesManage)
	}
	return principalAllowsProjectOwner(ctx, h.appStore, project, tokencaps.PoliciesManage)
}

// principalAllowsProjectOwner reports whether the caller may manage a
// project setting: a token with the capability for the organization, and,
// when roles apply, a project owner (or an org or global admin).
func principalAllowsProjectOwner(ctx context.Context, appStore store.Store, project *models.Project, capability string) bool {
	principal := checkauth.GetPrincipalFromContext(ctx)
	orgID := project.OwnershipOrgID()
	if principal == nil || !principal.HasOrganization(orgID) || !principal.HasCapability(capability) {
		return false
	}
	if !principal.RolesApply() {
		return true
	}
	roleStore, ok := appStore.(authz.RoleStore)
	if !ok {
		return false
	}
	user := checkauth.GetUserFromContext(ctx)
	if !principal.IsServicePrincipalToken() && (user == nil || !user.IsActive()) {
		return false
	}
	owner, err := authz.NewResolver(roleStore).IsProjectOwner(ctx, authz.IdentityFromPrincipal(principal, user), project.ProjectID)
	return err == nil && owner
}

func (h *RefClassHandler) list(w http.ResponseWriter, r *http.Request, orgID string, projectID *string) {
	rules, err := h.store.ListRefClassRules(r.Context(), orgID, projectID)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	response := RefClassRulesResponse{Rules: emptyRules(rules), Builtin: refclass.BuiltinRuleNames()}
	if projectID != nil {
		inherited, err := h.store.ListRefClassRules(r.Context(), orgID, nil)
		if err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		response.Inherited = emptyRules(inherited)
	}
	h.respondWithJSON(w, http.StatusOK, response)
}

func emptyRules(rules []models.RefClassRule) []models.RefClassRule {
	if rules == nil {
		return []models.RefClassRule{}
	}
	return rules
}

// RefClassRuleFromRequest builds a rule from a request body.
func RefClassRuleFromRequest(orgID string, projectID *string, name string, req RefClassRuleRequest) models.RefClassRule {
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	eventTypes := req.EventTypes
	if eventTypes == nil {
		eventTypes = []string{}
	}
	return models.RefClassRule{
		OrgID: orgID, ProjectID: projectID, Name: strings.TrimSpace(name), Position: req.Position,
		Description: req.Description, EventTypes: eventTypes,
		BranchGlob: strings.TrimSpace(req.BranchGlob), TagGlob: strings.TrimSpace(req.TagGlob), BaseBranchGlob: strings.TrimSpace(req.BaseBranchGlob),
		IsPR: req.IsPR, IsFork: req.IsFork, IsDefaultBranch: req.IsDefaultBranch,
		Classes: refclass.Normalize(req.Classes), Enabled: enabled,
	}
}

func (h *RefClassHandler) set(w http.ResponseWriter, r *http.Request, orgID string, projectID *string, name string) {
	var req RefClassRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	rule := RefClassRuleFromRequest(orgID, projectID, name, req)
	if err := refclass.ValidateRule(rule); err != nil {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: err.Error()})
		return
	}
	if err := h.store.UpsertRefClassRule(r.Context(), &rule); err != nil {
		h.respondWithError(w, refClassErrorStatus(err), err)
		return
	}
	audit.Record(r.Context(), h.appStore, orgID, "ref_class_rule.set", "ref_class_rule", rule.RuleID, models.JSONB{
		"name": rule.Name, "project_id": projectID, "classes": []string(rule.Classes), "enabled": rule.Enabled,
	})
	h.respondWithJSON(w, http.StatusOK, rule)
}

func (h *RefClassHandler) delete(w http.ResponseWriter, r *http.Request, orgID string, projectID *string, name string) {
	rule, err := h.store.GetRefClassRule(r.Context(), orgID, projectID, name)
	if err != nil {
		h.respondWithError(w, refClassErrorStatus(err), err)
		return
	}
	if err := h.store.DeleteRefClassRule(r.Context(), orgID, projectID, name); err != nil {
		h.respondWithError(w, refClassErrorStatus(err), err)
		return
	}
	audit.Record(r.Context(), h.appStore, orgID, "ref_class_rule.delete", "ref_class_rule", rule.RuleID, models.JSONB{
		"name": rule.Name, "project_id": projectID,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *RefClassHandler) explain(w http.ResponseWriter, r *http.Request, orgID string, projectID *string) {
	var req RefClassExplainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	response, err := ExplainRefClasses(r.Context(), h.appStore, orgID, projectID, req)
	if err != nil {
		h.respondWithError(w, refClassErrorStatus(err), err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, response)
}

// ExplainRefClasses computes the explain output for a request. An event ID
// must name an event of the organization (and of the project, when given).
func ExplainRefClasses(ctx context.Context, st interface{}, orgID string, projectID *string, req RefClassExplainRequest) (*RefClassExplainResponse, error) {
	return refclass.ExplainRequest(ctx, st, orgID, projectID, req.EventID, req.Facts)
}

func refClassErrorStatus(err error) int {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrInvalidInput):
		return http.StatusBadRequest
	case errors.Is(err, store.ErrAlreadyExists):
		return http.StatusConflict
	case errors.Is(err, store.ErrForbidden):
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}
