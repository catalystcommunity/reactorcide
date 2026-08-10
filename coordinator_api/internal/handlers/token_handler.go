package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
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
	ListAPITokens(ctx context.Context) ([]models.APIToken, error)
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

type ListTokensResponse struct {
	Tokens []TokenResponse `json:"tokens"`
	Total  int             `json:"total"`
}

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

func (h *TokenHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	principal := checkauth.GetPrincipalFromContext(r.Context())
	if h.administration == nil || principal == nil || !principal.HasCapability(tokencaps.TokensManage) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	tokens, err := h.administration.ListAPITokens(r.Context())
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	responses := make([]TokenResponse, 0, len(tokens))
	for i := range tokens {
		if h.tokenVisibleToPrincipal(r, &tokens[i], principal) {
			responses = append(responses, h.tokenToResponse(r, &tokens[i]))
		}
	}
	h.respondWithJSON(w, http.StatusOK, ListTokensResponse{Tokens: responses, Total: len(responses)})
}

func (h *TokenHandler) tokenVisibleToPrincipal(r *http.Request, token *models.APIToken, principal *checkauth.Principal) bool {
	if principal.AllOrganizations {
		return principal.CredentialType != "user_token" || principalAllowsGlobal(r.Context(), h.store, tokencaps.TokensManage)
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
	organizations := []string{"*"}
	if !token.AllOrganizations {
		organizations = make([]string, 0, len(token.OrganizationIDs))
		for _, orgID := range token.OrganizationIDs {
			if organization, err := h.administration.GetOrganizationByID(r.Context(), orgID); err == nil {
				organizations = append(organizations, organization.Name)
			}
		}
	}
	capabilities := []string{tokencaps.All}
	if !token.AllCapabilities {
		capabilities = append([]string(nil), token.Capabilities...)
	}
	owner := ""
	if token.OwnerOrgID != nil {
		if org, err := h.administration.GetOrganizationByID(r.Context(), *token.OwnerOrgID); err == nil {
			owner = org.Name
		}
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
