package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/owners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Workflow-scoped roles (plan B5): REST for the CLI and the admin token.
//
//	GET    /api/v1/organizations/{org}/workflow-roles?project=P[&workflow=W]
//	POST   /api/v1/organizations/{org}/workflow-roles
//	       {"project": P, "workflow": W, "group": G | "username": U | "principal_id": ID}
//	DELETE /api/v1/organizations/{org}/workflow-roles/{assignment_id}
//	GET    /api/v1/organizations/{org}/workflow-owners?project=P
//
// Every route needs organizations:manage for the organization (org admin
// for a user token), as for group and project role management.

var workflowSecurityIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type workflowRoleStore interface {
	GetOrganizationByName(context.Context, string) (*models.Organization, error)
	GetProjectByOrgAndName(ctx context.Context, orgID, name string) (*models.Project, error)
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
	GetUserByUsername(context.Context, string) (*models.User, error)
	ListGroupsByOrg(context.Context, string) ([]models.Group, error)
	GetPrincipal(ctx context.Context, principalID string) (*models.Principal, error)
	CreateRoleAssignment(ctx context.Context, assignment *models.RoleAssignment) error
	ListRoleAssignmentsByScope(ctx context.Context, scopeType string, scopeID *string) ([]models.RoleAssignment, error)
	GetRoleAssignmentByID(ctx context.Context, assignmentID string) (*models.RoleAssignment, error)
	DeleteRoleAssignment(ctx context.Context, assignmentID string) error
}

// WorkflowRoleHandler serves workflow-scoped role assignments.
type WorkflowRoleHandler struct {
	BaseHandler
	store    workflowRoleStore
	appStore store.Store
}

// NewWorkflowRoleHandler returns the handler.
func NewWorkflowRoleHandler(appStore store.Store) *WorkflowRoleHandler {
	value, _ := appStore.(workflowRoleStore)
	return &WorkflowRoleHandler{store: value, appStore: appStore}
}

type workflowRoleRequest struct {
	Project     string `json:"project"`
	Workflow    string `json:"workflow"`
	Group       string `json:"group,omitempty"`
	Username    string `json:"username,omitempty"`
	PrincipalID string `json:"principal_id,omitempty"`
}

type workflowRoleResponse struct {
	AssignmentID  string  `json:"assignment_id"`
	PrincipalType string  `json:"principal_type"`
	PrincipalID   string  `json:"principal_id"`
	PrincipalName string  `json:"principal_name,omitempty"`
	ProjectID     string  `json:"project_id"`
	Workflow      string  `json:"workflow"`
	Role          string  `json:"role"`
	CreatedAt     string  `json:"created_at"`
	CreatedBy     *string `json:"created_by,omitempty"`
}

type workflowOwnerResponse struct {
	Workflow string                  `json:"workflow"`
	Group    string                  `json:"group,omitempty"`
	Subjects []string                `json:"subjects,omitempty"`
	Contact  *models.WorkflowContact `json:"contact,omitempty"`
	Source   string                  `json:"source,omitempty"`
	CISHA    string                  `json:"ci_sha,omitempty"`
}

// ServeOrganization routes the paths after /api/v1/organizations/{org}/.
func (h *WorkflowRoleHandler) ServeOrganization(w http.ResponseWriter, r *http.Request, orgName, rest string) {
	principal := checkauth.GetPrincipalFromContext(r.Context())
	// A read (GET) accepts organizations:read; a change needs
	// organizations:manage. Both need the organization admin role.
	if h.store == nil || principal == nil || !principalHasAny(principal, readOrManage(r)) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	org, err := h.store.GetOrganizationByName(r.Context(), orgName)
	if err != nil || org == nil || !principalAllowsOrgAdmin(r.Context(), h.appStore, org.OrgID, readOrManage(r)) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	parts := []string{}
	for _, part := range strings.Split(strings.Trim(rest, "/"), "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	switch {
	case len(parts) == 1 && parts[0] == "workflow-owners" && r.Method == http.MethodGet:
		h.listOwners(w, r, org)
	case len(parts) == 1 && parts[0] == "workflow-roles" && r.Method == http.MethodGet:
		h.list(w, r, org)
	case len(parts) == 1 && parts[0] == "workflow-roles" && r.Method == http.MethodPost:
		h.assign(w, r, org)
	case len(parts) == 2 && parts[0] == "workflow-roles" && r.Method == http.MethodDelete:
		h.revoke(w, r, org, parts[1])
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *WorkflowRoleHandler) project(r *http.Request, org *models.Organization, name string) (*models.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("project is required")
	}
	project, err := h.store.GetProjectByOrgAndName(r.Context(), org.OrgID, name)
	if err != nil || project == nil {
		return nil, store.ErrNotFound
	}
	return project, nil
}

func groupNameOf(a *models.RoleAssignment, groups []models.Group) string {
	if a.PrincipalType == models.PrincipalTypeGroup {
		for _, g := range groups {
			if g.GroupID == a.PrincipalID {
				return g.Name
			}
		}
	}
	return ""
}

func workflowRoleToResponse(a *models.RoleAssignment, name string) workflowRoleResponse {
	return workflowRoleResponse{AssignmentID: a.AssignmentID, PrincipalType: a.PrincipalType, PrincipalID: a.PrincipalID,
		PrincipalName: name, ProjectID: valueOrEmptyString(a.ScopeID), Workflow: valueOrEmptyString(a.ScopeWorkflow),
		Role: a.Role, CreatedAt: a.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"), CreatedBy: a.CreatedBy}
}

func (h *WorkflowRoleHandler) list(w http.ResponseWriter, r *http.Request, org *models.Organization) {
	project, err := h.project(r, org, r.URL.Query().Get("project"))
	if err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	workflow := strings.TrimSpace(r.URL.Query().Get("workflow"))
	projectID := project.ProjectID
	assignments, err := h.store.ListRoleAssignmentsByScope(r.Context(), models.ScopeTypeWorkflow, &projectID)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	groups, _ := h.store.ListGroupsByOrg(r.Context(), org.OrgID)
	out := []workflowRoleResponse{}
	for i := range assignments {
		if workflow != "" && valueOrEmptyString(assignments[i].ScopeWorkflow) != workflow {
			continue
		}
		out = append(out, workflowRoleToResponse(&assignments[i], groupNameOf(&assignments[i], groups)))
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"roles": out})
}

func (h *WorkflowRoleHandler) assign(w http.ResponseWriter, r *http.Request, org *models.Organization) {
	var req workflowRoleRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	project, err := h.project(r, org, req.Project)
	if err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	workflow := strings.TrimSpace(req.Workflow)
	if !workflowSecurityIDPattern.MatchString(workflow) {
		h.respondWithError(w, http.StatusBadRequest, errors.New("workflow must be a workflow security ID (the YAML id)"))
		return
	}
	set := 0
	for _, value := range []string{req.Group, req.Username, req.PrincipalID} {
		if strings.TrimSpace(value) != "" {
			set++
		}
	}
	if set != 1 {
		h.respondWithError(w, http.StatusBadRequest, errors.New("give exactly one of group, username, principal_id"))
		return
	}
	projectID := project.ProjectID
	assignment := &models.RoleAssignment{ScopeType: models.ScopeTypeWorkflow, ScopeID: &projectID, ScopeWorkflow: &workflow, Role: models.RoleOwner}
	name := ""
	switch {
	case strings.TrimSpace(req.Group) != "":
		groups, listErr := h.store.ListGroupsByOrg(r.Context(), org.OrgID)
		if listErr != nil {
			h.respondWithError(w, http.StatusInternalServerError, listErr)
			return
		}
		for _, g := range groups {
			if g.Name == strings.TrimSpace(req.Group) {
				assignment.PrincipalType, assignment.PrincipalID, name = models.PrincipalTypeGroup, g.GroupID, g.Name
			}
		}
		if assignment.PrincipalID == "" {
			h.respondWithError(w, http.StatusNotFound, errors.New("group not found in the organization"))
			return
		}
	case strings.TrimSpace(req.Username) != "":
		user, userErr := h.store.GetUserByUsername(r.Context(), strings.TrimSpace(req.Username))
		if userErr != nil || user == nil {
			h.respondWithError(w, http.StatusNotFound, errors.New("user not found"))
			return
		}
		assignment.PrincipalType, assignment.PrincipalID, name = models.PrincipalTypeUser, user.UserID, user.Username
	default:
		principal, principalErr := h.store.GetPrincipal(r.Context(), strings.TrimSpace(req.PrincipalID))
		if principalErr != nil || principal == nil || principal.PrincipalID == models.SystemPrincipalID ||
			(principal.OrgID != nil && *principal.OrgID != org.OrgID) {
			h.respondWithError(w, http.StatusNotFound, errors.New("principal not found"))
			return
		}
		assignment.PrincipalType, assignment.PrincipalID = models.PrincipalTypePrincipal, principal.PrincipalID
	}
	if actor := principals.ActorOf(r.Context()); !actor.IsZero() {
		assignment.CreatedBy = actor.Ptr()
	}
	if err := h.store.CreateRoleAssignment(r.Context(), assignment); err != nil {
		h.respondWithError(w, http.StatusConflict, store.ErrAlreadyExists)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "role_assignment.create", "role_assignment", assignment.AssignmentID,
		models.JSONB{"principal_type": assignment.PrincipalType, "principal_id": assignment.PrincipalID, "scope_type": assignment.ScopeType,
			"scope_id": assignment.ScopeID, "scope_workflow": workflow, "role": assignment.Role})
	h.respondWithJSON(w, http.StatusCreated, workflowRoleToResponse(assignment, name))
}

func (h *WorkflowRoleHandler) revoke(w http.ResponseWriter, r *http.Request, org *models.Organization, assignmentID string) {
	assignment, err := h.store.GetRoleAssignmentByID(r.Context(), assignmentID)
	if err != nil || assignment.ScopeType != models.ScopeTypeWorkflow {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	project, err := h.store.GetProjectByID(r.Context(), valueOrEmptyString(assignment.ScopeID))
	if err != nil || project == nil || project.OwnershipOrgID() != org.OrgID {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	if err := h.store.DeleteRoleAssignment(r.Context(), assignment.AssignmentID); err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "role_assignment.delete", "role_assignment", assignment.AssignmentID,
		models.JSONB{"principal_type": assignment.PrincipalType, "principal_id": assignment.PrincipalID, "scope_type": assignment.ScopeType,
			"scope_id": assignment.ScopeID, "scope_workflow": valueOrEmptyString(assignment.ScopeWorkflow), "role": assignment.Role})
	w.WriteHeader(http.StatusNoContent)
}

// listOwners lists the latest trusted owner snapshot of every workflow of a
// project (owners.ForProject).
func (h *WorkflowRoleHandler) listOwners(w http.ResponseWriter, r *http.Request, org *models.Organization) {
	project, err := h.project(r, org, r.URL.Query().Get("project"))
	if err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	snapshots, err := owners.ForProject(r.Context(), h.appStore, project.ProjectID)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	out := []workflowOwnerResponse{}
	for workflow, snapshot := range snapshots {
		out = append(out, workflowOwnerResponse{Workflow: workflow, Group: snapshot.GroupName(), Subjects: snapshot.Subjects,
			Contact: snapshot.Contact, Source: snapshot.Source, CISHA: snapshot.CISHA})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Workflow < out[j].Workflow })
	h.respondWithJSON(w, http.StatusOK, map[string]any{"owners": out})
}
