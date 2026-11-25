package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

type tokenAdministrationStore interface {
	store.Store
	GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
	GetOrganizationByID(ctx context.Context, orgID string) (*models.Organization, error)
	GetUserByUsername(ctx context.Context, username string) (*models.User, error)
	GetAPITokenByID(ctx context.Context, tokenID string) (*models.APIToken, error)
	ListAPITokens(ctx context.Context, filter store.APITokenListFilter) ([]models.APIToken, int64, error)
	RevokeAPIToken(ctx context.Context, tokenID string) error
}

type TokenHandler struct {
	BaseHandler
	store          store.Store
	administration tokenAdministrationStore
}

func NewTokenHandler(appStore store.Store) *TokenHandler {
	administration, _ := appStore.(tokenAdministrationStore)
	return &TokenHandler{store: appStore, administration: administration}
}

type CreateTokenRequest struct {
	Name          string     `json:"name"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	Organizations []string   `json:"organizations,omitempty"`
	Capabilities  []string   `json:"capabilities,omitempty"`
	AsUser        string     `json:"as_user,omitempty"`
}

type CreateTokenResponse struct {
	TokenResponse
	Token string `json:"token"`
}

type TokenResponse struct {
	TokenID       string     `json:"token_id"`
	Name          string     `json:"name"`
	SubjectType   string     `json:"subject_type"`
	Owner         string     `json:"owner,omitempty"`
	Organizations []string   `json:"organizations"`
	Capabilities  []string   `json:"capabilities"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	IsActive      bool       `json:"is_active"`
}

// ListTokensResponse is one page of tokens. Total counts every token that
// matches the query, not only this page.
type ListTokensResponse struct {
	Tokens []TokenResponse `json:"tokens"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

// The page size of GET /api/v1/tokens when the request gives no limit, and
// the largest page size that a request can ask for.
const (
	defaultTokenPageSize = 100
	maxTokenPageSize     = 1000
)

func (h *TokenHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	principal := checkauth.GetPrincipalFromContext(r.Context())
	if h.administration == nil || principal == nil || !principal.HasCapability(tokencaps.TokensManage) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	var req CreateTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	organizationIDs := make([]string, 0, len(req.Organizations))
	allOrganizations := len(req.Organizations) == 0
	if allOrganizations && !principal.AllOrganizations {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	for _, name := range req.Organizations {
		organization, err := h.administration.GetOrganizationByName(r.Context(), name)
		if err != nil {
			h.respondWithError(w, http.StatusBadRequest, err)
			return
		}
		if !principal.HasOrganization(organization.OrgID) {
			h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		organizationIDs = append(organizationIDs, organization.OrgID)
	}

	allCapabilities := len(req.Capabilities) == 0
	if allCapabilities && !principal.AllCapabilities {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	requestedCapabilities, err := tokencaps.New(req.Capabilities...)
	if err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	if !principal.AllCapabilities && !requestedCapabilities.IsSubsetOf(principal.Capabilities) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	capabilitiesToCheck := requestedCapabilities.Slice()
	if allCapabilities {
		capabilitiesToCheck = tokencaps.Values()
	}
	if principal.CredentialType == "user_token" {
		for _, orgID := range organizationIDs {
			for _, capability := range capabilitiesToCheck {
				if !principalAllowsOrg(r.Context(), h.store, orgID, capability) {
					h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
					return
				}
			}
		}
	}

	subjectType := "service_token"
	var ownerOrgID *string
	var userID string
	if req.AsUser != "" {
		user, err := h.administration.GetUserByUsername(r.Context(), req.AsUser)
		if err != nil || !user.IsActive() {
			h.respondWithError(w, http.StatusBadRequest, errors.New("active delegated user not found"))
			return
		}
		userID = user.UserID
		subjectType = "user_token"
	} else if allOrganizations {
		if principal.CredentialType != "instance_token" || !principal.AllOrganizations || !principal.HasCapability(tokencaps.OrganizationsManage) {
			h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		subjectType = "instance_token"
	} else {
		ownerOrgID = &organizationIDs[0]
	}

	tokenString, err := generateSecureToken()
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	apiToken := &models.APIToken{UserID: userID, TokenHash: checkauth.HashAPIToken(tokenString), Name: req.Name,
		ExpiresAt: req.ExpiresAt, IsActive: true, SubjectType: subjectType, OwnerOrgID: ownerOrgID,
		AllOrganizations: allOrganizations, AllCapabilities: allCapabilities,
		OrganizationIDs: organizationIDs, Capabilities: requestedCapabilities.Slice()}
	if err := h.store.CreateAPIToken(r.Context(), apiToken); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	auditOrg := ""
	if apiToken.OwnerOrgID != nil {
		auditOrg = *apiToken.OwnerOrgID
	}
	audit.Record(r.Context(), h.store, auditOrg, "token.create", "api_token", apiToken.TokenID,
		models.JSONB{"subject_type": apiToken.SubjectType, "all_organizations": apiToken.AllOrganizations,
			"organizations": apiToken.OrganizationIDs, "all_capabilities": apiToken.AllCapabilities, "capabilities": apiToken.Capabilities})
	response := CreateTokenResponse{TokenResponse: h.tokenToResponse(r, apiToken), Token: tokenString}
	h.respondWithJSON(w, http.StatusCreated, response)
}

// ListTokens returns one page of the tokens that the caller can manage,
// newest first. Query parameters: limit (default 100, at most 1000), offset,
// and exclude_expired=true to leave out tokens whose expiry is in the past.
// Expired tokens are listed by default.
func (h *TokenHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	principal := checkauth.GetPrincipalFromContext(r.Context())
	if h.administration == nil || principal == nil || !principal.HasCapability(tokencaps.TokensManage) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	filter, err := parseTokenListQuery(r)
	if err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	if principal.AllOrganizations {
		if !h.globalTokensVisible(r, principal) {
			h.respondWithJSON(w, http.StatusOK, ListTokensResponse{Tokens: []TokenResponse{}, Limit: filter.Limit, Offset: filter.Offset})
			return
		}
		filter.AllOrganizations = true
	} else {
		filter.OrgIDs = h.manageableTokenOrgs(r, principal)
	}
	tokens, total, err := h.administration.ListAPITokens(r.Context(), filter)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	names := map[string]string{}
	responses := make([]TokenResponse, 0, len(tokens))
	for i := range tokens {
		responses = append(responses, h.tokenToResponseNamed(r, &tokens[i], names))
	}
	h.respondWithJSON(w, http.StatusOK, ListTokensResponse{Tokens: responses, Total: int(total), Limit: filter.Limit, Offset: filter.Offset})
}

// parseTokenListQuery reads limit, offset, and exclude_expired.
func parseTokenListQuery(r *http.Request) (store.APITokenListFilter, error) {
	filter := store.APITokenListFilter{Limit: defaultTokenPageSize}
	query := r.URL.Query()
	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxTokenPageSize {
			return filter, fmt.Errorf("limit must be 1 to %d", maxTokenPageSize)
		}
		filter.Limit = limit
	}
	if raw := query.Get("offset"); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return filter, fmt.Errorf("offset must be 0 or more")
		}
		filter.Offset = offset
	}
	if raw := query.Get("exclude_expired"); raw != "" {
		exclude, err := strconv.ParseBool(raw)
		if err != nil {
			return filter, fmt.Errorf("exclude_expired must be true or false")
		}
		filter.ExcludeExpired = exclude
	}
	return filter, nil
}

// globalTokensVisible reports whether a principal of all organizations can
// see every token.
func (h *TokenHandler) globalTokensVisible(r *http.Request, principal *checkauth.Principal) bool {
	return principal.CredentialType != "user_token" || principalAllowsGlobal(r.Context(), h.store, tokencaps.TokensManage)
}

// manageableTokenOrgs returns the organizations of the principal in which it
// can manage tokens. A token is visible when one of them owns it or is in its
// scope (the same rule as tokenVisibleToPrincipal).
func (h *TokenHandler) manageableTokenOrgs(r *http.Request, principal *checkauth.Principal) []string {
	candidates := append([]string(nil), principal.OrganizationIDs...)
	if principal.OwnerOrgID != "" {
		candidates = append(candidates, principal.OwnerOrgID)
	}
	seen := map[string]bool{}
	allowed := make([]string, 0, len(candidates))
	for _, orgID := range candidates {
		if orgID == "" || seen[orgID] {
			continue
		}
		seen[orgID] = true
		if principalAllowsOrg(r.Context(), h.store, orgID, tokencaps.TokensManage) {
			allowed = append(allowed, orgID)
		}
	}
	return allowed
}

func (h *TokenHandler) tokenVisibleToPrincipal(r *http.Request, token *models.APIToken, principal *checkauth.Principal) bool {
	if principal.AllOrganizations {
		return h.globalTokensVisible(r, principal)
	}
	if token.OwnerOrgID != nil && principalAllowsOrg(r.Context(), h.store, *token.OwnerOrgID, tokencaps.TokensManage) {
		return true
	}
	for _, orgID := range token.OrganizationIDs {
		if principalAllowsOrg(r.Context(), h.store, orgID, tokencaps.TokensManage) {
			return true
		}
	}
	return false
}

func (h *TokenHandler) DeleteToken(w http.ResponseWriter, r *http.Request) {
	principal := checkauth.GetPrincipalFromContext(r.Context())
	if h.administration == nil || principal == nil || !principal.HasCapability(tokencaps.TokensManage) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	token, err := h.administration.GetAPITokenByID(r.Context(), h.getID(r, "token_id"))
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if !h.tokenVisibleToPrincipal(r, token, principal) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	if err := h.administration.RevokeAPIToken(r.Context(), token.TokenID); err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	auditOrg := ""
	if token.OwnerOrgID != nil {
		auditOrg = *token.OwnerOrgID
	}
	audit.Record(r.Context(), h.store, auditOrg, "token.revoke", "api_token", token.TokenID, models.JSONB{"subject_type": token.SubjectType})
	w.WriteHeader(http.StatusNoContent)
}

func (h *TokenHandler) tokenToResponse(r *http.Request, token *models.APIToken) TokenResponse {
	return h.tokenToResponseNamed(r, token, nil)
}

// tokenToResponseNamed is tokenToResponse with a cache of organization names
// by id (nil for no cache), so a page of tokens looks up each organization
// once. An organization that cannot be found is left out, as before.
func (h *TokenHandler) tokenToResponseNamed(r *http.Request, token *models.APIToken, names map[string]string) TokenResponse {
	orgName := func(orgID string) (string, bool) {
		if name, ok := names[orgID]; ok {
			return name, name != ""
		}
		name := ""
		if organization, err := h.administration.GetOrganizationByID(r.Context(), orgID); err == nil {
			name = organization.Name
		}
		if names != nil {
			names[orgID] = name
		}
		return name, name != ""
	}
	organizations := []string{"*"}
	if !token.AllOrganizations {
		organizations = make([]string, 0, len(token.OrganizationIDs))
		for _, orgID := range token.OrganizationIDs {
			if name, ok := orgName(orgID); ok {
				organizations = append(organizations, name)
			}
		}
	}
	capabilities := []string{tokencaps.All}
	if !token.AllCapabilities {
		capabilities = append([]string(nil), token.Capabilities...)
	}
	owner := ""
	if token.OwnerOrgID != nil {
		owner, _ = orgName(*token.OwnerOrgID)
	}
	return TokenResponse{TokenID: token.TokenID, Name: token.Name, SubjectType: token.SubjectType, Owner: owner,
		Organizations: organizations, Capabilities: capabilities, CreatedAt: token.CreatedAt, UpdatedAt: token.UpdatedAt,
		ExpiresAt: token.ExpiresAt, LastUsedAt: token.LastUsedAt, IsActive: token.IsValid()}
}

func generateSecureToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
