package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
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
	CreateCIApproval(context.Context, *models.CIApproval) error
	GetProjectByOrgAndName(context.Context, string, string) (*models.Project, error)
	ListGroupsForUser(context.Context, string) ([]models.Group, error)
}

type CISecurityHandler struct {
	BaseHandler
	store    ciSecurityStore
	appStore store.Store
}

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
	if h.store == nil || p == nil || !p.HasCapability(tokencaps.OrganizationsManage) {
		h.respondWithError(w, 403, store.ErrForbidden)
		return
	}
	if r.Method == http.MethodGet {
		if !principalAllowsGlobal(r.Context(), h.appStore, tokencaps.OrganizationsManage) {
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
	org, err := h.store.GetOrganizationByName(r.Context(), req.Organization)
	if err != nil || !principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.OrganizationsManage) {
		h.respondWithError(w, 403, store.ErrForbidden)
		return
	}
	user, err := h.store.GetUserByUsername(r.Context(), req.Username)
	if err != nil {
		h.respondWithError(w, 400, err)
		return
	}
	link := &models.VCSIdentityLink{Provider: req.Provider, ExternalSubject: req.ExternalSubject, UserID: user.UserID, VerifiedBy: "admin"}
	if err := h.store.CreateVCSIdentityLink(r.Context(), link); err != nil {
		h.respondWithError(w, 400, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "vcs_identity.create", "vcs_identity_link", link.LinkID, models.JSONB{"provider": link.Provider, "external_subject": link.ExternalSubject})
	h.respondWithJSON(w, 201, link)
}

type approvalRequest struct {
	Organization string `json:"organization"`
	Project      string `json:"project"`
	models.CIApproval
}

func (h *CISecurityHandler) Approve(w http.ResponseWriter, r *http.Request) {
	p := checkauth.GetPrincipalFromContext(r.Context())
	if h.store == nil || p == nil || !p.HasCapability(tokencaps.PoliciesManage) {
		h.respondWithError(w, 403, store.ErrForbidden)
		return
	}
	var req approvalRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		h.respondWithError(w, 400, store.ErrInvalidInput)
		return
	}
	org, err := h.store.GetOrganizationByName(r.Context(), req.Organization)
	if err != nil || !principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.PoliciesManage) {
		h.respondWithError(w, 403, store.ErrForbidden)
		return
	}
	project, err := h.store.GetProjectByOrgAndName(r.Context(), org.OrgID, req.Project)
	if err != nil {
		h.respondWithError(w, 400, err)
		return
	}
	req.ProjectID = project.ProjectID
	if p.UserID == "" || req.ApproverSubject == "" {
		h.respondWithError(w, 403, store.ErrForbidden)
		return
	}
	allowedSubject := false
	if req.ApproverSubject == "project_owner" {
		if roleStore, ok := h.appStore.(authz.RoleStore); ok {
			allowedSubject, _ = authz.NewResolver(roleStore).IsProjectOwner(r.Context(), authz.IdentityFromPrincipal(p, checkauth.GetUserFromContext(r.Context())), project.ProjectID)
		}
	} else if strings.HasPrefix(req.ApproverSubject, "reactorcide_group:") {
		groups, _ := h.store.ListGroupsForUser(r.Context(), p.UserID)
		name := strings.TrimPrefix(req.ApproverSubject, "reactorcide_group:")
		for _, group := range groups {
			if group.OrgID == org.OrgID && group.Name == name {
				allowedSubject = true
			}
		}
	}
	if !allowedSubject {
		h.respondWithError(w, 403, store.ErrForbidden)
		return
	}
	req.CIApproval.OrgID = org.OrgID
	if p.UserID != "" {
		req.ApproverUserID = &p.UserID
	}
	if err := h.store.CreateCIApproval(r.Context(), &req.CIApproval); err != nil {
		h.respondWithError(w, 400, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "ci_approval.create", "ci_approval", req.ApprovalID,
		models.JSONB{"project_id": req.ProjectID, "pr_number": req.PRNumber, "head_sha": req.HeadSHA,
			"base_sha": req.BaseSHA, "policy_revision": req.PolicyRevision, "workflow": req.WorkflowScope, "profile": req.ExecutionProfile})
	h.respondWithJSON(w, 201, req.CIApproval)
}
