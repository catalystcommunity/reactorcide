package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

type ciSecurityStore interface {
	GetOrganizationByName(context.Context, string) (*models.Organization, error)
	GetUserByUsername(context.Context, string) (*models.User, error)
	CreateVCSIdentityLink(context.Context, *models.VCSIdentityLink) error
	ListVCSIdentityLinks(context.Context) ([]models.VCSIdentityLink, error)
	DeleteVCSIdentityLink(context.Context, string) error
	GetProjectByOrgAndName(context.Context, string, string) (*models.Project, error)
	ListGroupsForUser(context.Context, string) ([]models.Group, error)
}

type CISecurityHandler struct {
	BaseHandler
	store    ciSecurityStore
	appStore store.Store
	// approvals is the one approval path (the webhook handler).
	approvals approvals.Executor
}

// SetApprovals wires the approval path.
func (h *CISecurityHandler) SetApprovals(executor approvals.Executor) { h.approvals = executor }

func NewCISecurityHandler(appStore store.Store) *CISecurityHandler {
	value, _ := appStore.(ciSecurityStore)
	return &CISecurityHandler{store: value, appStore: appStore}
}

type identityLinkRequest struct {
	Organization    string `json:"organization"`
	Provider        string `json:"provider"`
	ExternalSubject string `json:"external_subject"`
	Username        string `json:"username"`
}

func (h *CISecurityHandler) Identities(w http.ResponseWriter, r *http.Request, linkID string) {
	p := checkauth.GetPrincipalFromContext(r.Context())
	if h.store == nil || p == nil || !principalHasAny(p, readOrManage(r)) {
		h.respondWithError(w, 403, store.ErrForbidden)
		return
	}
	if r.Method == http.MethodGet {
		if !principalAllowsGlobalAdmin(r.Context(), h.appStore, readOrManage(r)) {
			h.respondWithError(w, 403, store.ErrForbidden)
			return
		}
		links, err := h.store.ListVCSIdentityLinks(r.Context())
		if err != nil {
			h.respondWithError(w, 500, err)
			return
		}
		h.respondWithJSON(w, 200, map[string]any{"links": links})
		return
	}
	if r.Method == http.MethodDelete {
		if !principalAllowsGlobal(r.Context(), h.appStore, tokencaps.OrganizationsManage) {
			h.respondWithError(w, 403, store.ErrForbidden)
			return
		}
		if err := h.store.DeleteVCSIdentityLink(r.Context(), linkID); err != nil {
			h.respondWithError(w, 400, err)
			return
		}
		audit.Record(r.Context(), h.appStore, "", "vcs_identity.delete", "vcs_identity_link", linkID, models.JSONB{})
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var req identityLinkRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		h.respondWithError(w, 400, store.ErrInvalidInput)
		return
	}
	// The link gives a user (a human principal) the VCS login. Its subjects
	// then apply in every organization, so only a global admin (or the
	// admin or instance token) may link it. An org admin links identities
	// only to service principals of the org (link-principal-identity).
	if !principalAllowsGlobal(r.Context(), h.appStore, tokencaps.OrganizationsManage) {
		h.respondWithError(w, 403, store.ErrForbidden)
		return
	}
	orgID := ""
	if strings.TrimSpace(req.Organization) != "" {
		org, err := h.store.GetOrganizationByName(r.Context(), req.Organization)
		if err != nil {
			h.respondWithError(w, 400, err)
			return
		}
		orgID = org.OrgID
	}
	user, err := h.store.GetUserByUsername(r.Context(), req.Username)
	if err != nil {
		h.respondWithError(w, 400, err)
		return
	}
	link := &models.VCSIdentityLink{Provider: req.Provider, ExternalSubject: req.ExternalSubject, UserID: user.UserID, VerifiedBy: "admin"}
	if err := h.store.CreateVCSIdentityLink(r.Context(), link); err != nil {
		if errors.Is(err, store.ErrConflict) {
			h.respondWithError(w, 409, err)
			return
		}
		h.respondWithError(w, 400, err)
		return
	}
	audit.Record(r.Context(), h.appStore, orgID, "vcs_identity.create", "vcs_identity_link", link.LinkID, models.JSONB{"provider": link.Provider, "external_subject": link.ExternalSubject})
	h.respondWithJSON(w, 201, link)
}

type approvalRequest struct {
	Organization string `json:"organization"`
	Project      string `json:"project"`
	PRNumber     int    `json:"pr_number"`
	// Command is approve-ci, override-ci, accept, revoke, retry, or rerun.
	Command string `json:"command"`
	Label   string `json:"label,omitempty"`
	// HeadSHA, when set, must be the current head of the pull request.
	HeadSHA string `json:"head_sha,omitempty"`
	// Shifts are the optional resource and timeout changes of retry or
	// rerun (see internal/runshifts). Other commands refuse them.
	Shifts map[string]any `json:"shifts,omitempty"`
}

// Approve runs a pull request command for the token's user through the one
// approval path. The command's own subject rules decide what it may do.
func (h *CISecurityHandler) Approve(w http.ResponseWriter, r *http.Request) {
	p := checkauth.GetPrincipalFromContext(r.Context())
	// A user token acts as its user; a service principal token acts as its
	// service principal. The command's own subject rules decide what either
	// may do. On a no-login instance only, the admin token and a wide
	// service principal token act as the instance administrator.
	instanceAdmin := config.NoLoginMode() && p.IsWideInstanceToken()
	if h.store == nil || h.approvals == nil || p == nil ||
		(p.UserID == "" && !p.IsServicePrincipalToken() && !instanceAdmin) ||
		!p.HasCapability(tokencaps.WorkflowsControl) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	var req approvalRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.PRNumber <= 0 || strings.TrimSpace(req.Command) == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	org, err := h.store.GetOrganizationByName(r.Context(), req.Organization)
	if err != nil || !principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.WorkflowsControl) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	project, err := h.store.GetProjectByOrgAndName(r.Context(), org.OrgID, req.Project)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	command := approvals.Command{Name: strings.TrimSpace(req.Command), Label: strings.TrimSpace(req.Label)}
	if len(req.Shifts) > 0 {
		if command.Name != approvals.CommandRetry && command.Name != approvals.CommandRerun {
			h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_shift", Message: "only retry and rerun take shifts"})
			return
		}
		shifts, err := runshifts.ParseMap(req.Shifts)
		if err != nil {
			respondShiftError(&h.BaseHandler, w, err)
			return
		}
		command.Shifts = shifts
	}
	result, err := h.approvals.Execute(r.Context(), approvals.Request{Project: project, PRNumber: req.PRNumber,
		HeadSHA: strings.TrimSpace(req.HeadSHA), Command: command,
		Channel: models.CIApprovalChannelAPI, UserID: p.UserID, PrincipalID: approvalPrincipalOf(p, instanceAdmin),
		InstanceAdmin: instanceAdmin, TokenID: instanceAdminTokenOf(p, instanceAdmin)})
	switch {
	case errors.Is(err, approvals.ErrNotAllowed):
		h.respondWithError(w, http.StatusForbidden, err)
		return
	case errors.Is(err, approvals.ErrHeadChanged):
		h.respondWithError(w, http.StatusConflict, err)
		return
	case err != nil:
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, result)
}

// approvalPrincipalOf is the principal that gives a command: the service
// principal of a service principal token, or the principal of a wide token
// of the instance administrator.
func approvalPrincipalOf(p *checkauth.Principal, instanceAdmin bool) string {
	if instanceAdmin {
		return p.PrincipalID
	}
	return servicePrincipalOf(p)
}

// instanceAdminTokenOf is the token of the instance administrator when the
// token has no principal (the admin token).
func instanceAdminTokenOf(p *checkauth.Principal, instanceAdmin bool) string {
	if instanceAdmin && p.PrincipalID == "" {
		return p.CredentialID
	}
	return ""
}
