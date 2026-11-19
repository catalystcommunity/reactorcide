package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// principalAdminStore is the store surface of the principal routes.
type principalAdminStore interface {
	principals.Store
	GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
	GetOrganizationByID(ctx context.Context, orgID string) (*models.Organization, error)
	ListPrincipals(ctx context.Context, orgID, kind string) ([]models.Principal, error)
	GetServicePrincipalByName(ctx context.Context, orgID, name string) (*models.Principal, error)
	CreateServicePrincipal(ctx context.Context, principal *models.Principal) error
	SetPrincipalStatus(ctx context.Context, principalID, status string) error
	LinkPrincipalIdentity(ctx context.Context, identity *models.PrincipalIdentity) error
	UnlinkPrincipalIdentity(ctx context.Context, identityType, value string) error
	GetPrincipalIdentity(ctx context.Context, identityType, value string) (*models.PrincipalIdentity, error)
	ListGroupsByOrg(ctx context.Context, orgID string) ([]models.Group, error)
	ListGroupsForPrincipal(ctx context.Context, principalID string) ([]models.Group, error)
	AddGroupMemberPrincipal(ctx context.Context, groupID, principalID string) error
	RemoveGroupMemberPrincipal(ctx context.Context, groupID, principalID string) error
	ListAPITokensForPrincipal(ctx context.Context, principalID string) ([]models.APIToken, error)
	GetAPITokenByID(ctx context.Context, tokenID string) (*models.APIToken, error)
	RevokeAPIToken(ctx context.Context, tokenID string) error
	GetProjectByOrgAndName(ctx context.Context, orgID, name string) (*models.Project, error)
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
	CreateRoleAssignment(ctx context.Context, assignment *models.RoleAssignment) error
	GetRoleAssignmentByID(ctx context.Context, assignmentID string) (*models.RoleAssignment, error)
	DeleteRoleAssignment(ctx context.Context, assignmentID string) error
	ListRoleAssignmentsForPrincipalID(ctx context.Context, principalID string, groupIDs []string) ([]models.RoleAssignment, error)
}

type principalRoleRequest struct {
	// Role is admin or member at the organization, or owner or member at a
	// project (Project set).
	Role    string `json:"role"`
	Project string `json:"project,omitempty"`
}

// PrincipalHandler serves principals to API tokens, for the CLI. An org
// admin (or the admin token) manages the service principals of the org,
// their groups and tokens, and the identities of the principals of the org
// (owner decision D24). The routes work with no logins.
type PrincipalHandler struct {
	BaseHandler
	store    principalAdminStore
	appStore store.Store
}

func NewPrincipalHandler(appStore store.Store) *PrincipalHandler {
	value, _ := appStore.(principalAdminStore)
	return &PrincipalHandler{store: value, appStore: appStore}
}

// PrincipalResponse is one principal with its identities, groups, and tokens.
type PrincipalResponse struct {
	PrincipalID  string                     `json:"principal_id"`
	Kind         string                     `json:"kind"`
	Organization string                     `json:"organization,omitempty"`
	Name         string                     `json:"name"`
	DisplayName  string                     `json:"display_name"`
	Status       string                     `json:"status"`
	Subjects     []string                   `json:"subjects,omitempty"`
	Identities   []models.PrincipalIdentity `json:"identities"`
	Groups       []string                   `json:"groups,omitempty"`
	Tokens       []PrincipalTokenResponse   `json:"tokens,omitempty"`
	CreatedAt    time.Time                  `json:"created_at"`
}

// PrincipalTokenResponse is one token of a principal. It never holds the
// token value.
type PrincipalTokenResponse struct {
	TokenID      string     `json:"token_id"`
	Name         string     `json:"name"`
	Capabilities []string   `json:"capabilities"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
	IsActive     bool       `json:"is_active"`
}

type createPrincipalRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
}

type principalIdentityRequest struct {
	IdentityType  string `json:"identity_type"`
	IdentityValue string `json:"identity_value"`
}

type principalGroupRequest struct {
	Group string `json:"group"`
}

type principalTokenRequest struct {
	Name         string     `json:"name"`
	Capabilities []string   `json:"capabilities,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

// ServeResolve handles GET /api/v1/principals/resolve?type=TYPE&value=VALUE.
// It needs global authority (the admin token or a global admin), because a
// human principal and its identities are not in one organization.
func (h *PrincipalHandler) ServeResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.store == nil || !principalAllowsGlobalAdmin(r.Context(), h.appStore, readOrManage(r)) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	h.resolve(w, r, "")
}

// ServePrincipals routes /api/v1/organizations/{org}/principals[/...]. rest
// is the path after "{org}/principals".
func (h *PrincipalHandler) ServePrincipals(w http.ResponseWriter, r *http.Request, orgName, rest string) {
	credential := checkauth.GetPrincipalFromContext(r.Context())
	// A read (GET) accepts organizations:read; a change needs
	// organizations:manage. Both need the organization admin role.
	if h.store == nil || credential == nil || !principalHasAny(credential, readOrManage(r)) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	org, err := h.store.GetOrganizationByName(r.Context(), orgName)
	if err != nil || !principalAllowsOrgAdmin(r.Context(), h.appStore, org.OrgID, readOrManage(r)) {
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
	case len(parts) == 0 && r.Method == http.MethodGet:
		h.list(w, r, org)
	case len(parts) == 0 && r.Method == http.MethodPost:
		h.create(w, r, org)
	case len(parts) == 1 && parts[0] == "resolve" && r.Method == http.MethodGet:
		h.resolve(w, r, org.OrgID)
	case len(parts) == 1 && r.Method == http.MethodGet:
		h.withPrincipal(w, r, org, parts[0], h.get)
	case len(parts) == 2 && (parts[1] == "disable" || parts[1] == "enable") && r.Method == http.MethodPost:
		status := models.PrincipalStatusDisabled
		if parts[1] == "enable" {
			status = models.PrincipalStatusActive
		}
		h.withPrincipal(w, r, org, parts[0], func(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal) {
			h.setStatus(w, r, org, p, status)
		})
	case len(parts) == 2 && parts[1] == "identities" && r.Method == http.MethodPost:
		h.withPrincipal(w, r, org, parts[0], h.linkIdentity)
	case len(parts) == 2 && parts[1] == "identities" && r.Method == http.MethodDelete:
		h.withPrincipal(w, r, org, parts[0], h.unlinkIdentity)
	case len(parts) == 2 && parts[1] == "groups" && r.Method == http.MethodPost:
		h.withPrincipal(w, r, org, parts[0], h.addGroup)
	case len(parts) == 3 && parts[1] == "groups" && r.Method == http.MethodDelete:
		group := parts[2]
		h.withPrincipal(w, r, org, parts[0], func(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal) {
			h.removeGroup(w, r, org, p, group)
		})
	case len(parts) == 2 && parts[1] == "roles" && r.Method == http.MethodGet:
		h.withPrincipal(w, r, org, parts[0], h.listRoles)
	case len(parts) == 2 && parts[1] == "roles" && r.Method == http.MethodPost:
		h.withPrincipal(w, r, org, parts[0], h.assignRole)
	case len(parts) == 3 && parts[1] == "roles" && r.Method == http.MethodDelete:
		assignmentID := parts[2]
		h.withPrincipal(w, r, org, parts[0], func(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal) {
			h.revokeRole(w, r, org, p, assignmentID)
		})
	case len(parts) == 2 && parts[1] == "tokens" && r.Method == http.MethodPost:
		h.withPrincipal(w, r, org, parts[0], h.issueToken)
	case len(parts) == 2 && parts[1] == "tokens" && r.Method == http.MethodGet:
		h.withPrincipal(w, r, org, parts[0], h.listTokens)
	case len(parts) == 3 && parts[1] == "tokens" && r.Method == http.MethodDelete:
		tokenID := parts[2]
		h.withPrincipal(w, r, org, parts[0], func(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal) {
			h.revokeToken(w, r, org, p, tokenID)
		})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *PrincipalHandler) list(w http.ResponseWriter, r *http.Request, org *models.Organization) {
	kind := r.URL.Query().Get("kind")
	rows, err := h.store.ListPrincipals(r.Context(), org.OrgID, kind)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]PrincipalResponse, 0, len(rows))
	for i := range rows {
		out = append(out, h.summary(r.Context(), org, &rows[i]))
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"principals": out})
}

func (h *PrincipalHandler) create(w http.ResponseWriter, r *http.Request, org *models.Organization) {
	var req createPrincipalRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !models.ValidServicePrincipalName(req.Name) {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	orgID := org.OrgID
	principal := &models.Principal{OrgID: &orgID, Name: req.Name, DisplayName: strings.TrimSpace(req.DisplayName)}
	if err := h.store.CreateServicePrincipal(r.Context(), principal); err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "principal.create", "principal", principal.PrincipalID,
		models.JSONB{"kind": principal.Kind, "name": principal.Name})
	h.respondWithJSON(w, http.StatusCreated, h.detail(r.Context(), org, principal))
}

func (h *PrincipalHandler) get(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal) {
	h.respondWithJSON(w, http.StatusOK, h.detail(r.Context(), org, p))
}

func (h *PrincipalHandler) resolve(w http.ResponseWriter, r *http.Request, orgID string) {
	identityType := strings.TrimSpace(r.URL.Query().Get("type"))
	value := strings.TrimSpace(r.URL.Query().Get("value"))
	resolver := principals.NewResolver(h.store, 0)
	var resolution *principals.Resolution
	var err error
	if identityType == "principal" {
		resolution, err = resolver.ResolveID(r.Context(), value)
	} else {
		resolution, err = resolver.Resolve(r.Context(), identityType, value)
	}
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if orgID != "" && !h.principalInOrg(r.Context(), orgID, &resolution.Principal) {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	var org *models.Organization
	if orgID != "" {
		org, _ = h.store.GetOrganizationByID(r.Context(), orgID)
	}
	response := h.detail(r.Context(), org, &resolution.Principal)
	h.respondWithJSON(w, http.StatusOK, map[string]any{"identity": resolution.Identity, "principal": response})
}

func (h *PrincipalHandler) setStatus(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal, status string) {
	if p.Kind != models.PrincipalKindService {
		// A human's state follows the users row (and LinkKeys admission).
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	if err := h.store.SetPrincipalStatus(r.Context(), p.PrincipalID, status); err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	p.Status = status
	audit.Record(r.Context(), h.appStore, org.OrgID, "principal."+map[string]string{models.PrincipalStatusActive: "enable", models.PrincipalStatusDisabled: "disable"}[status],
		"principal", p.PrincipalID, models.JSONB{"name": p.Name})
	h.respondWithJSON(w, http.StatusOK, h.detail(r.Context(), org, p))
}

func (h *PrincipalHandler) linkIdentity(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal) {
	var req principalIdentityRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	identity := &models.PrincipalIdentity{IdentityType: strings.TrimSpace(req.IdentityType), IdentityValue: req.IdentityValue,
		PrincipalID: p.PrincipalID, VerifiedBy: models.IdentityVerifiedByAdmin}
	if identity.IdentityType == models.IdentityTypeFederated {
		// Federated subjects come from a registered issuer (inbound
		// federation), not from a manual link.
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	// The subjects of a human apply in every organization, so an org admin
	// links identities only to service principals of the org. A human needs
	// global authority (a global admin or the admin token).
	if p.Kind != models.PrincipalKindService && !principalAllowsGlobal(r.Context(), h.appStore, tokencaps.OrganizationsManage) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	if err := h.store.LinkPrincipalIdentity(r.Context(), identity); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, store.ErrConflict) {
			code = http.StatusConflict
		}
		h.respondWithError(w, code, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "principal.identity.link", "principal", p.PrincipalID,
		models.JSONB{"identity_type": identity.IdentityType, "identity_value": identity.IdentityValue})
	h.respondWithJSON(w, http.StatusOK, h.detail(r.Context(), org, p))
}

func (h *PrincipalHandler) unlinkIdentity(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal) {
	identityType := strings.TrimSpace(r.URL.Query().Get("type"))
	value := models.NormalizeIdentityValue(identityType, r.URL.Query().Get("value"))
	existing, err := h.store.GetPrincipalIdentity(r.Context(), identityType, value)
	if err != nil || existing.PrincipalID != p.PrincipalID {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	if err := h.store.UnlinkPrincipalIdentity(r.Context(), identityType, value); err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "principal.identity.unlink", "principal", p.PrincipalID,
		models.JSONB{"identity_type": identityType, "identity_value": value})
	h.respondWithJSON(w, http.StatusOK, h.detail(r.Context(), org, p))
}

func (h *PrincipalHandler) findGroup(ctx context.Context, org *models.Organization, name string) (*models.Group, error) {
	groups, err := h.store.ListGroupsByOrg(ctx, org.OrgID)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].Name == name || groups[i].GroupID == name {
			return &groups[i], nil
		}
	}
	return nil, store.ErrNotFound
}

func (h *PrincipalHandler) addGroup(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal) {
	var req principalGroupRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	group, err := h.findGroup(r.Context(), org, strings.TrimSpace(req.Group))
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if err := h.store.AddGroupMemberPrincipal(r.Context(), group.GroupID, p.PrincipalID); err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "group.member.add", "group", group.GroupID,
		models.JSONB{"principal_id": p.PrincipalID})
	h.respondWithJSON(w, http.StatusOK, h.detail(r.Context(), org, p))
}

func (h *PrincipalHandler) removeGroup(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal, name string) {
	group, err := h.findGroup(r.Context(), org, name)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if err := h.store.RemoveGroupMemberPrincipal(r.Context(), group.GroupID, p.PrincipalID); err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "group.member.remove", "group", group.GroupID,
		models.JSONB{"principal_id": p.PrincipalID})
	w.WriteHeader(http.StatusNoContent)
}

// issueToken issues a token bound to a service principal. Its authority is
// its capabilities intersected with the roles and groups of the principal,
// the same as a user token. The caller cannot give capabilities it does not
// have.
func (h *PrincipalHandler) issueToken(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal) {
	credential := checkauth.GetPrincipalFromContext(r.Context())
	if credential == nil || !credential.HasCapability(tokencaps.TokensManage) ||
		!principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.TokensManage) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	if p.Kind != models.PrincipalKindService || p.OrgID == nil || *p.OrgID != org.OrgID {
		h.respondWithError(w, http.StatusBadRequest, errors.New("tokens are issued only for service principals of the organization"))
		return
	}
	if !p.IsActive() {
		h.respondWithError(w, http.StatusBadRequest, errors.New("the principal is disabled"))
		return
	}
	var req principalTokenRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = p.Name
	}
	allCapabilities := len(req.Capabilities) == 0
	if allCapabilities && !credential.AllCapabilities {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	requested, err := tokencaps.New(req.Capabilities...)
	if err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	if !credential.AllCapabilities && !requested.IsSubsetOf(credential.Capabilities) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	// A no-login instance gives a token with no capability list all
	// capabilities and all organizations. Only a global caller can issue it.
	noLoginAdmin := allCapabilities && config.NoLoginMode()
	if noLoginAdmin && !principalAllowsGlobal(r.Context(), h.appStore, tokencaps.OrganizationsManage) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	tokenString, err := generateSecureToken()
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	token := checkauth.NewPrincipalToken(tokenString, req.Name, req.ExpiresAt, org.OrgID, p.PrincipalID, requested, allCapabilities, noLoginAdmin)
	if err := h.appStore.CreateAPIToken(r.Context(), token); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "token.create", "api_token", token.TokenID,
		models.JSONB{"subject_type": token.SubjectType, "principal_id": p.PrincipalID, "all_capabilities": allCapabilities,
			"all_organizations": token.AllOrganizations, "capabilities": token.Capabilities, "intersect_roles": token.IntersectRoles})
	h.respondWithJSON(w, http.StatusCreated, map[string]any{"token": tokenString, "token_info": tokenResponseFor(token)})
}

func (h *PrincipalHandler) listTokens(w http.ResponseWriter, r *http.Request, _ *models.Organization, p *models.Principal) {
	tokens, err := h.store.ListAPITokensForPrincipal(r.Context(), p.PrincipalID)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]PrincipalTokenResponse, 0, len(tokens))
	for i := range tokens {
		out = append(out, tokenResponseFor(&tokens[i]))
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

func (h *PrincipalHandler) revokeToken(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal, tokenID string) {
	credential := checkauth.GetPrincipalFromContext(r.Context())
	if credential == nil || !credential.HasCapability(tokencaps.TokensManage) ||
		!principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.TokensManage) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	token, err := h.store.GetAPITokenByID(r.Context(), tokenID)
	if err != nil || token.PrincipalID == nil || *token.PrincipalID != p.PrincipalID {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	if err := h.store.RevokeAPIToken(r.Context(), token.TokenID); err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "token.revoke", "api_token", token.TokenID,
		models.JSONB{"subject_type": token.SubjectType, "principal_id": p.PrincipalID})
	w.WriteHeader(http.StatusNoContent)
}

func tokenResponseFor(token *models.APIToken) PrincipalTokenResponse {
	capabilities := []string{tokencaps.All}
	if !token.AllCapabilities {
		capabilities = append([]string{}, token.Capabilities...)
	}
	return PrincipalTokenResponse{TokenID: token.TokenID, Name: token.Name, Capabilities: capabilities,
		CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt, LastUsedAt: token.LastUsedAt, IsActive: token.IsValid()}
}

// withPrincipal finds a principal of the org by service name or by UUID.
// A principal is in the org when it is a service principal of the org, or a
// human with a group membership or a role in the org.
func (h *PrincipalHandler) withPrincipal(w http.ResponseWriter, r *http.Request, org *models.Organization, ref string,
	next func(http.ResponseWriter, *http.Request, *models.Organization, *models.Principal)) {
	var principal *models.Principal
	if service, err := h.store.GetServicePrincipalByName(r.Context(), org.OrgID, ref); err == nil {
		principal = service
	} else if found, err := h.store.GetPrincipal(r.Context(), ref); err == nil && h.principalInOrg(r.Context(), org.OrgID, found) {
		principal = found
	}
	if principal == nil {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	next(w, r, org, principal)
}

func (h *PrincipalHandler) principalInOrg(ctx context.Context, orgID string, principal *models.Principal) bool {
	return principals.InOrg(ctx, h.store, orgID, principal)
}

func (h *PrincipalHandler) summary(ctx context.Context, org *models.Organization, principal *models.Principal) PrincipalResponse {
	response := PrincipalResponse{PrincipalID: principal.PrincipalID, Kind: principal.Kind, Name: principal.Name,
		DisplayName: principal.Label(), Status: principal.Status, CreatedAt: principal.CreatedAt,
		Identities: []models.PrincipalIdentity{}}
	if principal.OrgID != nil && org != nil && *principal.OrgID == org.OrgID {
		response.Organization = org.Name
	}
	if identities, err := h.store.ListPrincipalIdentities(ctx, principal.PrincipalID); err == nil {
		response.Identities = identities
	}
	return response
}

func (h *PrincipalHandler) detail(ctx context.Context, org *models.Organization, principal *models.Principal) PrincipalResponse {
	response := h.summary(ctx, org, principal)
	if groups, err := h.store.ListGroupsForPrincipal(ctx, principal.PrincipalID); err == nil {
		for _, group := range groups {
			if org == nil || group.OrgID == org.OrgID {
				response.Groups = append(response.Groups, group.Name)
			}
		}
	}
	if principal.Kind == models.PrincipalKindService {
		if tokens, err := h.store.ListAPITokensForPrincipal(ctx, principal.PrincipalID); err == nil {
			for i := range tokens {
				response.Tokens = append(response.Tokens, tokenResponseFor(&tokens[i]))
			}
		}
	}
	scope := principals.SubjectContext{}
	if org != nil {
		scope = principals.SubjectContext{OrgID: org.OrgID, OrgName: org.Name}
	}
	response.Subjects = principals.Subjects(&principals.Resolution{Principal: *principal, Identities: response.Identities}, scope)
	return response
}

// listRoles lists the direct role assignments of a principal (principal_type
// 'principal'). Roles through groups are on the groups.
func (h *PrincipalHandler) listRoles(w http.ResponseWriter, r *http.Request, _ *models.Organization, p *models.Principal) {
	assignments, err := h.store.ListRoleAssignmentsForPrincipalID(r.Context(), p.PrincipalID, nil)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"roles": assignments})
}

// assignRole gives a principal a role in the organization or in one of its
// projects. It never gives a global role.
func (h *PrincipalHandler) assignRole(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal) {
	var req principalRoleRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	assignment := &models.RoleAssignment{PrincipalType: models.PrincipalTypePrincipal, PrincipalID: p.PrincipalID, Role: req.Role}
	if strings.TrimSpace(req.Project) == "" {
		if req.Role != models.RoleAdmin && req.Role != models.RoleMember {
			h.respondWithError(w, http.StatusBadRequest, errors.New("an organization role is admin or member"))
			return
		}
		orgID := org.OrgID
		assignment.ScopeType, assignment.ScopeID = models.ScopeTypeOrg, &orgID
	} else {
		if req.Role != models.RoleOwner && req.Role != models.RoleMember {
			h.respondWithError(w, http.StatusBadRequest, errors.New("a project role is owner or member"))
			return
		}
		project, err := h.store.GetProjectByOrgAndName(r.Context(), org.OrgID, strings.TrimSpace(req.Project))
		if err != nil {
			h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
			return
		}
		projectID := project.ProjectID
		assignment.ScopeType, assignment.ScopeID = models.ScopeTypeProject, &projectID
	}
	if actor := principals.ActorOf(r.Context()); !actor.IsZero() {
		assignment.CreatedBy = actor.Ptr()
	}
	if err := h.store.CreateRoleAssignment(r.Context(), assignment); err != nil {
		h.respondWithError(w, http.StatusConflict, store.ErrAlreadyExists)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "role_assignment.create", "role_assignment", assignment.AssignmentID,
		models.JSONB{"principal_type": assignment.PrincipalType, "principal_id": p.PrincipalID, "scope_type": assignment.ScopeType,
			"scope_id": assignment.ScopeID, "role": assignment.Role})
	h.respondWithJSON(w, http.StatusCreated, assignment)
}

func (h *PrincipalHandler) revokeRole(w http.ResponseWriter, r *http.Request, org *models.Organization, p *models.Principal, assignmentID string) {
	assignment, err := h.store.GetRoleAssignmentByID(r.Context(), assignmentID)
	if err != nil || assignment.PrincipalType != models.PrincipalTypePrincipal || assignment.PrincipalID != p.PrincipalID ||
		assignment.ScopeType == models.ScopeTypeGlobal {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	if assignment.ScopeType == models.ScopeTypeOrg && (assignment.ScopeID == nil || *assignment.ScopeID != org.OrgID) {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	if assignment.ScopeType == models.ScopeTypeProject {
		project, projectErr := h.store.GetProjectByID(r.Context(), valueOrEmptyString(assignment.ScopeID))
		if projectErr != nil || project.OwnershipOrgID() != org.OrgID {
			h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
			return
		}
	}
	if err := h.store.DeleteRoleAssignment(r.Context(), assignment.AssignmentID); err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "role_assignment.delete", "role_assignment", assignment.AssignmentID,
		models.JSONB{"principal_id": p.PrincipalID})
	w.WriteHeader(http.StatusNoContent)
}

func valueOrEmptyString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
