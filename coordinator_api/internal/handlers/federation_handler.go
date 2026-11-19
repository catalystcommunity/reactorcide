package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/clientaddr"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/federation"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/ratelimit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// federationAdminStore is the store surface of the federation routes.
type federationAdminStore interface {
	federation.Store
	GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
	GetServicePrincipalByName(ctx context.Context, orgID, name string) (*models.Principal, error)
	CreateFederationIssuer(ctx context.Context, issuer *models.FederationIssuer) error
	UpdateFederationIssuer(ctx context.Context, issuer *models.FederationIssuer) error
	ListFederationIssuers(ctx context.Context, orgID string) ([]models.FederationIssuer, error)
	GetFederationIssuer(ctx context.Context, orgID, nameOrID string) (*models.FederationIssuer, error)
	DeleteFederationIssuer(ctx context.Context, orgID, issuerID string) error
	CreateFederationMapping(ctx context.Context, mapping *models.FederationMapping) error
	UpdateFederationMapping(ctx context.Context, mapping *models.FederationMapping) error
	GetFederationMapping(ctx context.Context, orgID, issuerID, nameOrID string) (*models.FederationMapping, error)
	DeleteFederationMapping(ctx context.Context, orgID, mappingID string) error
}

var (
	federationJWKSOnce  sync.Once
	federationJWKSCache *federation.JWKSCache
)

// sharedJWKSCache is the one JWKS cache of the process. Every exchange uses
// it, so a JWKS is fetched once per TTL and not once per request.
func sharedJWKSCache() *federation.JWKSCache {
	federationJWKSOnce.Do(func() {
		federationJWKSCache = federation.NewJWKSCache(config.AllowInsecureTransport)
	})
	return federationJWKSCache
}

// FederationHandler serves the federation REST routes: issuer and mapping
// administration for org admins (owner decision D24), and the public token
// exchange.
type FederationHandler struct {
	BaseHandler
	store    federationAdminStore
	appStore store.Store
	// Exchanger is replaceable in tests.
	Exchanger *federation.Exchanger
	// SourceLimiter limits token exchange requests per source address, and
	// IssuerLimiter limits issued tokens per verified issuer. nil: no limit.
	SourceLimiter *ratelimit.Limiter
	IssuerLimiter *ratelimit.Limiter
	// TrustedProxies are the proxies whose X-Forwarded-For the source limit
	// uses (REACTORCIDE_TRUSTED_PROXY_CIDRS). Empty: the TCP peer only.
	TrustedProxies clientaddr.Trusted
}

func NewFederationHandler(appStore store.Store) *FederationHandler {
	value, _ := appStore.(federationAdminStore)
	h := &FederationHandler{store: value, appStore: appStore,
		SourceLimiter: ratelimit.New(config.FederationRateLimitPerMinute, config.FederationRateLimitBurst),
		IssuerLimiter: ratelimit.New(config.FederationIssuerRateLimitPerMinute, config.FederationIssuerRateLimitBurst)}
	if trusted, err := clientaddr.Parse(config.TrustedProxyCIDRs); err != nil {
		// Fail safe: an invalid list trusts no proxy, so the source stays the
		// TCP peer.
		logging.Log.WithError(err).Error("REACTORCIDE_TRUSTED_PROXY_CIDRS is not valid; the federation source limit uses the TCP peer")
	} else {
		h.TrustedProxies = trusted
	}
	if value != nil {
		h.Exchanger = &federation.Exchanger{Store: value, Verifier: federation.NewVerifier(sharedJWKSCache()),
			AllowIssuer: h.allowIssuer}
	}
	return h
}

// allowIssuer applies the issuer limit (key: organization and issuer).
func (h *FederationHandler) allowIssuer(issuer *models.FederationIssuer) (bool, time.Duration) {
	if h.IssuerLimiter == nil || issuer == nil {
		return true, 0
	}
	return h.IssuerLimiter.Allow(issuer.OrgID + "/" + issuer.IssuerID)
}

// exchangeSource is the source address of an exchange request: the TCP peer
// address without the port. Only when the peer is a trusted proxy
// (REACTORCIDE_TRUSTED_PROXY_CIDRS) is it the right-most X-Forwarded-For
// address that is not a trusted proxy. Otherwise forwarded headers are not
// used, because a caller can set them.
func (h *FederationHandler) exchangeSource(r *http.Request) string {
	return h.TrustedProxies.Source(r)
}

// FederationIssuerRequest is the body of an issuer create or update. On an
// update, an omitted field keeps its value.
type FederationIssuerRequest struct {
	Name             string   `json:"name"`
	IssuerURL        string   `json:"issuer_url"`
	JWKSURL          *string  `json:"jwks_url,omitempty"`
	Audiences        []string `json:"audiences,omitempty"`
	Algorithms       []string `json:"algorithms,omitempty"`
	ClockSkewSeconds *int     `json:"clock_skew_seconds,omitempty"`
	Status           string   `json:"status,omitempty"`
}

// FederationMappingRequest is the body of a mapping create or update. On an
// update, an omitted field keeps its value.
type FederationMappingRequest struct {
	Name string `json:"name"`
	// Principal is a service principal of the org: its name or UUID.
	Principal       string                       `json:"principal,omitempty"`
	Conditions      []models.FederationCondition `json:"conditions,omitempty"`
	Capabilities    []string                     `json:"capabilities,omitempty"`
	TokenTTLSeconds int                          `json:"token_ttl_seconds,omitempty"`
	Status          string                       `json:"status,omitempty"`
}

// FederationMappingResponse is a mapping with its principal name.
type FederationMappingResponse struct {
	models.FederationMapping
	Issuer        string `json:"issuer"`
	PrincipalName string `json:"principal_name"`
}

// FederationTokenRequest is the body of a token exchange.
type FederationTokenRequest struct {
	Organization string `json:"organization"`
	// Token is the external JWT (the subject token).
	Token string `json:"token"`
	// Principal optionally names the service principal (name or UUID).
	Principal    string   `json:"principal,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// FederationTokenResponse is the result of a token exchange.
type FederationTokenResponse struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int64     `json:"expires_in"`
	ExpiresAt    time.Time `json:"expires_at"`
	TokenID      string    `json:"token_id"`
	PrincipalID  string    `json:"principal_id"`
	Principal    string    `json:"principal"`
	Capabilities []string  `json:"capabilities"`
}

// ServeFederation routes /api/v1/organizations/{org}/federation/... rest is
// the path after "federation".
func (h *FederationHandler) ServeFederation(w http.ResponseWriter, r *http.Request, orgName, rest string) {
	credential := checkauth.GetPrincipalFromContext(r.Context())
	if h.store == nil || credential == nil || !credential.HasCapability(tokencaps.OrganizationsManage) {
		respondWP4Error(&h.BaseHandler, w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	org, err := h.store.GetOrganizationByName(r.Context(), orgName)
	if err != nil || !principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.OrganizationsManage) {
		respondWP4Error(&h.BaseHandler, w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	parts := []string{}
	for _, part := range strings.Split(strings.Trim(rest, "/"), "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 || parts[0] != "issuers" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	parts = parts[1:]
	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		h.listIssuers(w, r, org)
	case len(parts) == 0 && r.Method == http.MethodPost:
		h.createIssuer(w, r, org)
	case len(parts) == 1 && r.Method == http.MethodGet:
		h.withIssuer(w, r, org, parts[0], func(issuer *models.FederationIssuer) { h.respondWithJSON(w, http.StatusOK, issuer) })
	case len(parts) == 1 && r.Method == http.MethodPut:
		h.withIssuer(w, r, org, parts[0], func(issuer *models.FederationIssuer) { h.updateIssuer(w, r, org, issuer) })
	case len(parts) == 1 && r.Method == http.MethodDelete:
		h.withIssuer(w, r, org, parts[0], func(issuer *models.FederationIssuer) { h.deleteIssuer(w, r, org, issuer) })
	case len(parts) == 2 && parts[1] == "mappings" && r.Method == http.MethodGet:
		h.withIssuer(w, r, org, parts[0], func(issuer *models.FederationIssuer) { h.listMappings(w, r, org, issuer) })
	case len(parts) == 2 && parts[1] == "mappings" && r.Method == http.MethodPost:
		h.withIssuer(w, r, org, parts[0], func(issuer *models.FederationIssuer) { h.createMapping(w, r, org, issuer) })
	case len(parts) == 3 && parts[1] == "mappings":
		h.withIssuer(w, r, org, parts[0], func(issuer *models.FederationIssuer) {
			mapping, err := h.store.GetFederationMapping(r.Context(), org.OrgID, issuer.IssuerID, parts[2])
			if err != nil {
				respondWP4Error(&h.BaseHandler, w, http.StatusNotFound, store.ErrNotFound)
				return
			}
			switch r.Method {
			case http.MethodGet:
				h.respondWithJSON(w, http.StatusOK, h.mappingResponse(r.Context(), org, issuer, mapping))
			case http.MethodPut:
				h.updateMapping(w, r, org, issuer, mapping)
			case http.MethodDelete:
				h.deleteMapping(w, r, org, issuer, mapping)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *FederationHandler) withIssuer(w http.ResponseWriter, r *http.Request, org *models.Organization, name string, fn func(*models.FederationIssuer)) {
	issuer, err := h.store.GetFederationIssuer(r.Context(), org.OrgID, name)
	if err != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	fn(issuer)
}

func (h *FederationHandler) listIssuers(w http.ResponseWriter, r *http.Request, org *models.Organization) {
	issuers, err := h.store.ListFederationIssuers(r.Context(), org.OrgID)
	if err != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusInternalServerError, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"issuers": issuers})
}

func (h *FederationHandler) createIssuer(w http.ResponseWriter, r *http.Request, org *models.Organization) {
	var req FederationIssuerRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	issuer := &models.FederationIssuer{OrgID: org.OrgID, Name: req.Name, IssuerURL: req.IssuerURL,
		Audiences: req.Audiences, Algorithms: req.Algorithms, ClockSkewSeconds: federation.DefaultClockSkew, Status: req.Status}
	applyIssuerRequest(issuer, &req)
	if err := federation.NormalizeIssuer(issuer, config.AllowInsecureTransport); err != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, err)
		return
	}
	if err := h.store.CreateFederationIssuer(r.Context(), issuer); err != nil {
		respondWP4Error(&h.BaseHandler, w, statusForStoreError(err), err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "federation.issuer.create", "federation_issuer", issuer.IssuerID,
		models.JSONB{"name": issuer.Name, "issuer_url": issuer.IssuerURL, "audiences": []string(issuer.Audiences),
			"algorithms": []string(issuer.Algorithms)})
	h.respondWithJSON(w, http.StatusCreated, issuer)
}

// applyIssuerRequest copies the set fields of req to issuer.
func applyIssuerRequest(issuer *models.FederationIssuer, req *FederationIssuerRequest) {
	if req.IssuerURL != "" {
		issuer.IssuerURL = req.IssuerURL
	}
	if req.JWKSURL != nil {
		issuer.JWKSURL = *req.JWKSURL
	}
	if req.Audiences != nil {
		issuer.Audiences = req.Audiences
	}
	if req.Algorithms != nil {
		issuer.Algorithms = req.Algorithms
	}
	if req.ClockSkewSeconds != nil {
		issuer.ClockSkewSeconds = *req.ClockSkewSeconds
	}
	if req.Status != "" {
		issuer.Status = req.Status
	}
}

func (h *FederationHandler) updateIssuer(w http.ResponseWriter, r *http.Request, org *models.Organization, issuer *models.FederationIssuer) {
	var req FederationIssuerRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	before := *issuer
	applyIssuerRequest(issuer, &req)
	if err := federation.NormalizeIssuer(issuer, config.AllowInsecureTransport); err != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, err)
		return
	}
	if err := h.store.UpdateFederationIssuer(r.Context(), issuer); err != nil {
		respondWP4Error(&h.BaseHandler, w, statusForStoreError(err), err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "federation.issuer.update", "federation_issuer", issuer.IssuerID,
		models.JSONB{"name": issuer.Name, "issuer_url": issuer.IssuerURL, "status": issuer.Status,
			"previous_issuer_url": before.IssuerURL, "previous_status": before.Status})
	h.respondWithJSON(w, http.StatusOK, issuer)
}

func (h *FederationHandler) deleteIssuer(w http.ResponseWriter, r *http.Request, org *models.Organization, issuer *models.FederationIssuer) {
	if err := h.store.DeleteFederationIssuer(r.Context(), org.OrgID, issuer.IssuerID); err != nil {
		respondWP4Error(&h.BaseHandler, w, statusForStoreError(err), err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "federation.issuer.delete", "federation_issuer", issuer.IssuerID,
		models.JSONB{"name": issuer.Name, "issuer_url": issuer.IssuerURL})
	w.WriteHeader(http.StatusNoContent)
}

func (h *FederationHandler) listMappings(w http.ResponseWriter, r *http.Request, org *models.Organization, issuer *models.FederationIssuer) {
	mappings, err := h.store.ListFederationMappings(r.Context(), org.OrgID, issuer.IssuerID)
	if err != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusInternalServerError, err)
		return
	}
	out := make([]FederationMappingResponse, 0, len(mappings))
	for i := range mappings {
		out = append(out, h.mappingResponse(r.Context(), org, issuer, &mappings[i]))
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"mappings": out})
}

func (h *FederationHandler) mappingResponse(ctx context.Context, _ *models.Organization, issuer *models.FederationIssuer, mapping *models.FederationMapping) FederationMappingResponse {
	out := FederationMappingResponse{FederationMapping: *mapping, Issuer: issuer.Name}
	if principal, err := h.store.GetPrincipal(ctx, mapping.PrincipalID); err == nil && principal != nil {
		out.PrincipalName = principal.Name
	}
	return out
}

// resolveServicePrincipal returns an active-or-disabled service principal of
// the org by name or UUID.
func resolveServicePrincipal(ctx context.Context, st federationAdminStore, org *models.Organization, ref string) (*models.Principal, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("principal is required")
	}
	principal, err := st.GetServicePrincipalByName(ctx, org.OrgID, ref)
	if err != nil {
		principal, err = st.GetPrincipal(ctx, ref)
	}
	if err != nil || principal == nil || principal.Kind != models.PrincipalKindService || principal.OrgID == nil ||
		*principal.OrgID != org.OrgID || principal.PrincipalID == "" {
		return nil, errors.New("principal must be a service principal of the organization")
	}
	return principal, nil
}

func (h *FederationHandler) mappingLimit(r *http.Request, org *models.Organization) (federation.CapabilityLimit, bool) {
	credential := checkauth.GetPrincipalFromContext(r.Context())
	// A mapping issues tokens, so the caller also needs tokens:manage.
	if credential == nil || !credential.HasCapability(tokencaps.TokensManage) ||
		!principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.TokensManage) {
		return federation.CapabilityLimit{}, false
	}
	return federation.CapabilityLimit{All: credential.AllCapabilities, Capabilities: credential.Capabilities}, true
}

func (h *FederationHandler) createMapping(w http.ResponseWriter, r *http.Request, org *models.Organization, issuer *models.FederationIssuer) {
	limit, ok := h.mappingLimit(r, org)
	if !ok {
		respondWP4Error(&h.BaseHandler, w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	var req FederationMappingRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	principal, err := resolveServicePrincipal(r.Context(), h.store, org, req.Principal)
	if err != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, err)
		return
	}
	mapping := &models.FederationMapping{IssuerID: issuer.IssuerID, OrgID: org.OrgID, Name: req.Name,
		PrincipalID: principal.PrincipalID, Conditions: req.Conditions, Capabilities: req.Capabilities,
		TokenTTLSeconds: req.TokenTTLSeconds, Status: req.Status}
	if err := federation.NormalizeMapping(mapping, limit); err != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, err)
		return
	}
	if err := h.store.CreateFederationMapping(r.Context(), mapping); err != nil {
		respondWP4Error(&h.BaseHandler, w, statusForStoreError(err), err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "federation.mapping.create", "federation_mapping", mapping.MappingID,
		mappingAuditDetails(issuer, mapping))
	h.respondWithJSON(w, http.StatusCreated, h.mappingResponse(r.Context(), org, issuer, mapping))
}

func mappingAuditDetails(issuer *models.FederationIssuer, mapping *models.FederationMapping) models.JSONB {
	conditions := make([]any, 0, len(mapping.Conditions))
	for _, c := range mapping.Conditions {
		conditions = append(conditions, map[string]any{"claim": c.Claim, "op": c.Op, "value": c.Value})
	}
	return models.JSONB{"issuer": issuer.Name, "name": mapping.Name, "principal_id": mapping.PrincipalID,
		"conditions": conditions, "capabilities": []string(mapping.Capabilities),
		"token_ttl_seconds": mapping.TokenTTLSeconds, "status": mapping.Status}
}

func (h *FederationHandler) updateMapping(w http.ResponseWriter, r *http.Request, org *models.Organization, issuer *models.FederationIssuer, mapping *models.FederationMapping) {
	limit, ok := h.mappingLimit(r, org)
	if !ok {
		respondWP4Error(&h.BaseHandler, w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	var req FederationMappingRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	if req.Principal != "" {
		principal, err := resolveServicePrincipal(r.Context(), h.store, org, req.Principal)
		if err != nil {
			respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, err)
			return
		}
		mapping.PrincipalID = principal.PrincipalID
	}
	if req.Conditions != nil {
		mapping.Conditions = req.Conditions
	}
	if req.Capabilities != nil {
		mapping.Capabilities = req.Capabilities
	}
	if req.TokenTTLSeconds != 0 {
		mapping.TokenTTLSeconds = req.TokenTTLSeconds
	}
	if req.Status != "" {
		mapping.Status = req.Status
	}
	if err := federation.NormalizeMapping(mapping, limit); err != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, err)
		return
	}
	if err := h.store.UpdateFederationMapping(r.Context(), mapping); err != nil {
		respondWP4Error(&h.BaseHandler, w, statusForStoreError(err), err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "federation.mapping.update", "federation_mapping", mapping.MappingID,
		mappingAuditDetails(issuer, mapping))
	h.respondWithJSON(w, http.StatusOK, h.mappingResponse(r.Context(), org, issuer, mapping))
}

func (h *FederationHandler) deleteMapping(w http.ResponseWriter, r *http.Request, org *models.Organization, issuer *models.FederationIssuer, mapping *models.FederationMapping) {
	if _, ok := h.mappingLimit(r, org); !ok {
		respondWP4Error(&h.BaseHandler, w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	if err := h.store.DeleteFederationMapping(r.Context(), org.OrgID, mapping.MappingID); err != nil {
		respondWP4Error(&h.BaseHandler, w, statusForStoreError(err), err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "federation.mapping.delete", "federation_mapping", mapping.MappingID,
		models.JSONB{"issuer": issuer.Name, "name": mapping.Name, "principal_id": mapping.PrincipalID})
	w.WriteHeader(http.StatusNoContent)
}

func statusForStoreError(err error) int {
	switch {
	case errors.Is(err, store.ErrAlreadyExists), errors.Is(err, store.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrInvalidInput):
		return http.StatusBadRequest
	case errors.Is(err, store.ErrForbidden):
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}

// maxExchangeBody limits the exchange request body. A JWT is small.
const maxExchangeBody = 64 << 10

// Exchange handles POST /api/v1/federation/token. The caller presents an
// external OIDC token; no Reactorcide credential is necessary. The route
// runs without the request transaction, so that the audit record of a
// refused exchange is kept.
func (h *FederationHandler) Exchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Overload protection (plan item D4): the exchange is public, and each
	// request can verify a signature and fetch a JWKS.
	if h.SourceLimiter != nil {
		if ok, wait := h.SourceLimiter.Allow(h.exchangeSource(r)); !ok {
			metrics.RecordRateLimited("federation")
			writeTooManyRequests(w, wait, "Too many token exchange requests from this address; retry after the Retry-After interval")
			return
		}
	}
	if h.store == nil || h.Exchanger == nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusServiceUnavailable, errors.New("federation is not available"))
		return
	}
	var req FederationTokenRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchangeBody)).Decode(&req) != nil ||
		strings.TrimSpace(req.Organization) == "" || strings.TrimSpace(req.Token) == "" {
		respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, errors.New("organization and token are required"))
		return
	}
	ctx := r.Context()
	// A refusal for an unknown organization has the same answer as a refusal
	// for a token, so that the route does not list organizations.
	denied := func(orgID string, refusal error, issuerURL, issuerID, subject string) {
		details := models.JSONB{"reason": refusal.Error()}
		if issuerURL != "" {
			details["issuer_url"] = issuerURL
		}
		if issuerID != "" {
			details["issuer_id"] = issuerID
		}
		if subject != "" {
			details["subject"] = subject
		}
		audit.Record(context.WithoutCancel(ctx), h.appStore, orgID, "federation.exchange.denied", "federation_issuer", issuerID, details)
		h.respondWithJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized", "message": refusal.Error()})
	}
	org, err := h.store.GetOrganizationByName(ctx, strings.TrimSpace(req.Organization))
	if err != nil || org == nil {
		denied("", federation.ErrUnknownIssuer, "", "", "")
		return
	}
	principalID := ""
	if req.Principal != "" {
		principal, err := resolveServicePrincipal(ctx, h.store, org, req.Principal)
		if err != nil {
			denied(org.OrgID, federation.ErrPrincipal, "", "", "")
			return
		}
		principalID = principal.PrincipalID
	}
	result, err := h.Exchanger.Exchange(ctx, federation.ExchangeRequest{OrgID: org.OrgID, Token: req.Token,
		PrincipalID: principalID, Capabilities: req.Capabilities})
	if err != nil {
		var refusal *federation.ExchangeError
		if errors.As(err, &refusal) && errors.Is(refusal.Reason, federation.ErrRateLimited) {
			metrics.RecordRateLimited("federation_issuer")
			writeTooManyRequests(w, refusal.RetryAfter, refusal.Reason.Error())
			return
		}
		if errors.As(err, &refusal) {
			denied(org.OrgID, refusal.Reason, refusal.Issuer, refusal.IssuerID, refusal.Subject)
			return
		}
		respondWP4Error(&h.BaseHandler, w, http.StatusInternalServerError, errors.New("the token exchange failed"))
		return
	}
	// The actor of the audit record is the principal that the exchange
	// authenticated.
	auditCtx := checkauth.SetPrincipalContext(ctx, &checkauth.Principal{CredentialID: result.TokenInfo.TokenID,
		CredentialType: result.TokenInfo.SubjectType, PrincipalID: result.Principal.PrincipalID})
	audit.Record(auditCtx, h.appStore, org.OrgID, "federation.exchange", "principal", result.Principal.PrincipalID,
		models.JSONB{"issuer": result.Issuer.Name, "issuer_url": result.Issuer.IssuerURL, "subject": result.Claims.Subject,
			"jti": result.Claims.ID, "mapping": result.Mapping.Name, "token_id": result.TokenInfo.TokenID,
			"capabilities": result.Capabilities, "expires_at": result.ExpiresAt.Format(time.RFC3339)})
	h.respondWithJSON(w, http.StatusOK, FederationTokenResponse{
		AccessToken: result.Token, TokenType: "Bearer", ExpiresIn: int64(time.Until(result.ExpiresAt).Seconds()),
		ExpiresAt: result.ExpiresAt, TokenID: result.TokenInfo.TokenID, PrincipalID: result.Principal.PrincipalID,
		Principal: result.Principal.Name, Capabilities: result.Capabilities,
	})
}

// respondWP4Error answers with code and the message of err. A store error
// keeps the standard mapping of BaseHandler. Other errors are validation or
// refusal messages that the caller must see (BaseHandler shows them as 500).
// A 500 never shows its message.
func respondWP4Error(h *BaseHandler, w http.ResponseWriter, code int, err error) {
	for _, sentinel := range []error{store.ErrNotFound, store.ErrInvalidInput, store.ErrAlreadyExists, store.ErrConflict,
		store.ErrForbidden, store.ErrUnauthorized, store.ErrServiceUnavailable} {
		if errors.Is(err, sentinel) {
			h.respondWithError(w, code, err)
			return
		}
	}
	if code >= http.StatusInternalServerError && code != http.StatusServiceUnavailable {
		h.respondWithError(w, code, err)
		return
	}
	kind := strings.ToLower(strings.ReplaceAll(http.StatusText(code), " ", "_"))
	h.respondWithJSON(w, code, ErrorResponse{Error: kind, Message: err.Error()})
}
