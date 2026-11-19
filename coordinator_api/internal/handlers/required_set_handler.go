package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// RequiredSetHandler serves the required set of a project
// (docs/required-set.md). The routes work with no logins (the admin token):
//
//	GET  /api/v1/projects/{project}/required-set
//	POST /api/v1/projects/{project}/required-set/enable
//	POST /api/v1/projects/{project}/required-set/disable
//	PUT  /api/v1/projects/{project}/required-set/approvers
//	POST /api/v1/projects/{project}/required-set/proposals
//	POST /api/v1/projects/{project}/required-set/proposals/{id}/{approve|reject|withdraw}
//	POST /api/v1/projects/{project}/required-set/quarantine
//	POST /api/v1/projects/{project}/required-set/unquarantine
//	GET|PUT /api/v1/organizations/{org}/required-set-settings
//
// The authorization is in requiredset.Service, the same for the UI.
type RequiredSetHandler struct {
	BaseHandler
	appStore store.Store
}

func NewRequiredSetHandler(appStore store.Store) *RequiredSetHandler {
	return &RequiredSetHandler{appStore: appStore}
}

type requiredSetOrgStore interface {
	GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
	ListProjects(ctx context.Context, limit, offset int) ([]models.Project, error)
}

func (h *RequiredSetHandler) service() *requiredset.Service {
	return &requiredset.Service{Store: h.appStore, Record: func(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB) {
		audit.Record(ctx, h.appStore, orgID, action, subjectType, subjectID, details)
	}}
}

// RequiredSetCaller is the caller of a REST request.
func RequiredSetCaller(ctx context.Context, value any) requiredset.Caller {
	principal := checkauth.GetPrincipalFromContext(ctx)
	user := checkauth.GetUserFromContext(ctx)
	identity := authz.IdentityFromPrincipal(principal, user)
	actor := requestActor(ctx, value)
	if actor.IsZero() {
		actor = principals.FromCredential(principal, user)
	}
	return requiredset.Caller{Identity: identity, Actor: actor}
}

// RequiredSetErrorStatus maps a service error to an HTTP status.
func RequiredSetErrorStatus(err error) int {
	switch requiredset.ErrorCode(err) {
	case "permission_denied":
		return http.StatusForbidden
	case "invalid_argument":
		return http.StatusBadRequest
	case "not_found":
		return http.StatusNotFound
	case "conflict", "failed_precondition":
		return http.StatusConflict
	case "unavailable":
		return http.StatusNotImplemented
	}
	return http.StatusInternalServerError
}

func (h *RequiredSetHandler) fail(w http.ResponseWriter, err error) {
	code := requiredset.ErrorCode(err)
	h.respondWithJSON(w, RequiredSetErrorStatus(err), ErrorResponse{Error: code, Message: err.Error()})
}

func (h *RequiredSetHandler) resolveProject(ctx context.Context, ref string) (*models.Project, error) {
	projects, ok := h.appStore.(requiredSetOrgStore)
	if !ok {
		return nil, store.ErrNotFound
	}
	if project, err := projects.GetProjectByID(ctx, ref); err == nil {
		return project, nil
	}
	list, err := projects.ListProjects(ctx, 10000, 0)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == ref || list[i].RepoURL == ref {
			return &list[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// RequiredSetEntryResponse is one row of the listing.
type RequiredSetEntryResponse struct {
	WorkflowSecurityID string     `json:"workflow_security_id"`
	Name               string     `json:"name,omitempty"`
	BranchPattern      string     `json:"branch_pattern"`
	State              string     `json:"state"`
	InSet              bool       `json:"in_set"`
	Source             string     `json:"source,omitempty"`
	AddedBy            *string    `json:"added_by,omitempty"`
	AddedAt            *time.Time `json:"added_at,omitempty"`
	Reason             string     `json:"reason,omitempty"`
	QuarantinedUntil   *time.Time `json:"quarantined_until,omitempty"`
	QuarantineReason   string     `json:"quarantine_reason,omitempty"`
	QuarantinedBy      *string    `json:"quarantined_by,omitempty"`
	LastSeen           *time.Time `json:"last_seen,omitempty"`
}

// RequiredSetResponse is the body of GET .../required-set.
type RequiredSetResponse struct {
	ProjectID         string                           `json:"project_id"`
	Enabled           bool                             `json:"enabled"`
	EnabledAt         *time.Time                       `json:"enabled_at,omitempty"`
	EnabledBy         *string                          `json:"enabled_by,omitempty"`
	Workflows         []RequiredSetEntryResponse       `json:"workflows"`
	Proposals         []models.RequiredSetProposal     `json:"proposals"`
	Approvers         []string                         `json:"approvers"`
	ApproversSource   string                           `json:"approvers_source"`
	ApproverRules     []models.RequiredSetApproverRule `json:"approver_rules"`
	ApproversInherit  bool                             `json:"approvers_inherit"`
	RulesInherit      bool                             `json:"approver_rules_inherit"`
	MaxQuarantineDays int                              `json:"max_quarantine_days"`
	Permissions       requiredSetPermissions           `json:"permissions"`
}

type requiredSetPermissions struct {
	Propose            bool `json:"propose"`
	Manage             bool `json:"manage"`
	ApproveAllBranches bool `json:"approve_all_branches"`
}

// RequiredSetViewResponse converts a service view.
func RequiredSetViewResponse(projectID string, view *requiredset.View) RequiredSetResponse {
	response := RequiredSetResponse{ProjectID: projectID, Enabled: view.Enabled, EnabledAt: view.EnabledAt, EnabledBy: view.EnabledBy,
		Workflows: []RequiredSetEntryResponse{}, Proposals: view.Proposals, Approvers: view.Approvers,
		ApproversSource: view.ApproversSource, ApproverRules: view.ApproverRules, ApproversInherit: view.ApproversInherit,
		RulesInherit: view.RulesInherit, MaxQuarantineDays: view.MaxQuarantineDays,
		Permissions: requiredSetPermissions{Propose: view.Permissions.Propose, Manage: view.Permissions.Manage,
			ApproveAllBranches: view.Permissions.ApproveAllBranches}}
	if response.Approvers == nil {
		response.Approvers = []string{}
	}
	if response.Proposals == nil {
		response.Proposals = []models.RequiredSetProposal{}
	}
	for _, row := range view.Rows {
		entry := RequiredSetEntryResponse{WorkflowSecurityID: row.WorkflowSecurityID, Name: row.Name,
			BranchPattern: row.BranchPattern, State: row.State, LastSeen: row.LastSeen}
		if row.Entry != nil {
			added := row.Entry.AddedAt
			entry.InSet, entry.Source, entry.AddedBy, entry.AddedAt, entry.Reason = true, row.Entry.Source, row.Entry.AddedBy, &added, row.Entry.Reason
			if row.State == requiredset.StateQuarantined {
				entry.QuarantinedUntil, entry.QuarantineReason, entry.QuarantinedBy = row.Entry.QuarantinedUntil, row.Entry.QuarantineReason, row.Entry.QuarantinedBy
			}
		}
		response.Workflows = append(response.Workflows, entry)
	}
	return response
}

// RequiredSetProposeRequest is the body of POST .../required-set/proposals.
type RequiredSetProposeRequest struct {
	Action        string `json:"action"`
	Workflow      string `json:"workflow"`
	BranchPattern string `json:"branch_pattern,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Approve       bool   `json:"approve,omitempty"`
}

// RequiredSetDecisionRequest is the body of POST .../proposals/{id}/{decision}.
type RequiredSetDecisionRequest struct {
	Reason string `json:"reason,omitempty"`
}

// RequiredSetQuarantineRequest is the body of POST .../required-set/quarantine.
// Until (RFC 3339) or Duration ("72h", "7d") sets the expiry.
type RequiredSetQuarantineRequest struct {
	Workflow      string `json:"workflow"`
	BranchPattern string `json:"branch_pattern,omitempty"`
	Until         string `json:"until,omitempty"`
	Duration      string `json:"duration,omitempty"`
	Reason        string `json:"reason"`
}

// RequiredSetApproversRequest is the body of PUT .../required-set/approvers.
// null inherits the organization value.
type RequiredSetApproversRequest struct {
	Approvers     *[]string                         `json:"approvers"`
	ApproverRules *[]models.RequiredSetApproverRule `json:"approver_rules"`
}

// RequiredSetOrgSettingsRequest is the body of PUT
// /organizations/{org}/required-set-settings.
type RequiredSetOrgSettingsRequest struct {
	Approvers         []string                         `json:"approvers"`
	ApproverRules     []models.RequiredSetApproverRule `json:"approver_rules"`
	MaxQuarantineDays int                              `json:"max_quarantine_days"`
}

func decodeRequiredSetBody(r *http.Request, value any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	return json.NewDecoder(r.Body).Decode(value)
}

// ServeProject routes /api/v1/projects/{project}/required-set[/...]. rest is
// the path after "required-set".
func (h *RequiredSetHandler) ServeProject(w http.ResponseWriter, r *http.Request, projectRef, rest string) {
	ctx := r.Context()
	project, err := h.resolveProject(ctx, projectRef)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	caller := RequiredSetCaller(ctx, h.appStore)
	service := h.service()
	parts := []string{}
	for _, part := range strings.Split(strings.Trim(rest, "/"), "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	respondView := func(status int) {
		view, err := service.Get(ctx, caller, project)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, status, RequiredSetViewResponse(project.ProjectID, view))
	}
	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		respondView(http.StatusOK)
	case len(parts) == 1 && (parts[0] == "enable" || parts[0] == "disable") && r.Method == http.MethodPost:
		seeded, err := service.SetEnabled(ctx, caller, project, parts[0] == "enable")
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, map[string]any{"enabled": parts[0] == "enable", "seeded": seeded})
	case len(parts) == 1 && parts[0] == "approvers" && r.Method == http.MethodPut:
		var req RequiredSetApproversRequest
		if err := decodeRequiredSetBody(r, &req); err != nil {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		var approvers []string
		if req.Approvers != nil {
			approvers = append([]string{}, (*req.Approvers)...)
		}
		if err := service.SetProjectApprovers(ctx, caller, project, approvers, req.ApproverRules); err != nil {
			h.fail(w, err)
			return
		}
		respondView(http.StatusOK)
	case len(parts) == 1 && parts[0] == "proposals" && r.Method == http.MethodGet:
		view, err := service.Get(ctx, caller, project)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, map[string]any{"proposals": RequiredSetViewResponse(project.ProjectID, view).Proposals})
	case len(parts) == 1 && parts[0] == "proposals" && r.Method == http.MethodPost:
		var req RequiredSetProposeRequest
		if err := decodeRequiredSetBody(r, &req); err != nil {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		if req.Action == "" {
			req.Action = models.RequiredSetActionPromote
		}
		proposal, err := service.Propose(ctx, caller, project, requiredset.ProposeRequest{Action: req.Action,
			WorkflowSecurityID: req.Workflow, BranchPattern: req.BranchPattern, Reason: req.Reason, Approve: req.Approve})
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusCreated, proposal)
	case len(parts) == 3 && parts[0] == "proposals" && r.Method == http.MethodPost:
		state := map[string]string{"approve": models.RequiredSetProposalApproved, "reject": models.RequiredSetProposalRejected,
			"withdraw": models.RequiredSetProposalWithdrawn}[parts[2]]
		if state == "" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		var req RequiredSetDecisionRequest
		if err := decodeRequiredSetBody(r, &req); err != nil {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		proposal, err := service.Decide(ctx, caller, project, parts[1], state, req.Reason)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, proposal)
	case len(parts) == 1 && parts[0] == "quarantine" && r.Method == http.MethodPost:
		var req RequiredSetQuarantineRequest
		if err := decodeRequiredSetBody(r, &req); err != nil {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		until, err := requiredset.ParseQuarantineExpiry(req.Until, req.Duration, time.Now().UTC())
		if err != nil {
			h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_argument", Message: err.Error()})
			return
		}
		entry, err := service.Quarantine(ctx, caller, project, requiredset.QuarantineRequest{WorkflowSecurityID: req.Workflow,
			BranchPattern: req.BranchPattern, Until: until, Reason: req.Reason})
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, entry)
	case len(parts) == 1 && parts[0] == "unquarantine" && r.Method == http.MethodPost:
		var req RequiredSetQuarantineRequest
		if err := decodeRequiredSetBody(r, &req); err != nil {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		if err := service.Unquarantine(ctx, caller, project, req.Workflow, req.BranchPattern); err != nil {
			h.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// RequiredSetOrgSettingsResponse is the body of GET
// /organizations/{org}/required-set-settings.
type RequiredSetOrgSettingsResponse struct {
	Approvers         []string                         `json:"approvers"`
	ApproverRules     []models.RequiredSetApproverRule `json:"approver_rules"`
	MaxQuarantineDays int                              `json:"max_quarantine_days"`
}

// ServeOrganization routes /api/v1/organizations/{org}/required-set-settings.
func (h *RequiredSetHandler) ServeOrganization(w http.ResponseWriter, r *http.Request, orgName string) {
	ctx := r.Context()
	orgs, ok := h.appStore.(requiredSetOrgStore)
	sets, okSets := h.appStore.(store.RequiredSetStore)
	if !ok || !okSets {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("the required set is not available"))
		return
	}
	org, err := orgs.GetOrganizationByName(ctx, orgName)
	if err != nil {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !principalAllowsOrg(ctx, h.appStore, org.OrgID, tokencaps.ProjectsRead) {
			h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		settings, err := sets.GetOrgRequiredSetSettings(ctx, org.OrgID)
		if err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, RequiredSetOrgSettingsResponse{Approvers: append([]string{}, settings.Approvers...),
			ApproverRules: settings.ApproverRules, MaxQuarantineDays: settings.MaxQuarantineDays})
	case http.MethodPut:
		var req RequiredSetOrgSettingsRequest
		if err := decodeRequiredSetBody(r, &req); err != nil {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		caller := RequiredSetCaller(ctx, h.appStore)
		if err := h.service().SetOrgSettings(ctx, caller, org.OrgID, req.Approvers, req.ApproverRules, req.MaxQuarantineDays); err != nil {
			h.fail(w, err)
			return
		}
		settings, err := sets.GetOrgRequiredSetSettings(ctx, org.OrgID)
		if err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, RequiredSetOrgSettingsResponse{Approvers: append([]string{}, settings.Approvers...),
			ApproverRules: settings.ApproverRules, MaxQuarantineDays: settings.MaxQuarantineDays})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
