package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/oidcissuer"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/transportsecurity"
)

// oidcStore is the store surface of the OIDC routes.
type oidcStore interface {
	oidcissuer.KeyStore
	oidcissuer.FactsStore
	GetJobByID(ctx context.Context, jobID string) (*models.Job, error)
	GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
	GetProjectByOrgAndName(ctx context.Context, orgID, name string) (*models.Project, error)
	GetOIDCSettings(ctx context.Context, orgID, projectID string) (*models.OIDCSettings, error)
	SaveOIDCSettings(ctx context.Context, settings *models.OIDCSettings) error
}

// OIDCHandler serves the Reactorcide OIDC issuer for jobs (docs/oidc.md):
// the public discovery document and JWKS, the job token route, key
// administration, and the org and project settings.
type OIDCHandler struct {
	BaseHandler
	store    oidcStore
	appStore store.Store
	Issuer   *oidcissuer.Issuer
}

// NewOIDCHandler returns the handler. The issuer is off (nil) when the
// issuer URL is not set, when it breaks the transport rule, or when no
// master key is available.
func NewOIDCHandler(appStore store.Store, keys *secrets.MasterKeyManager) *OIDCHandler {
	value, _ := appStore.(oidcStore)
	h := &OIDCHandler{store: value, appStore: appStore}
	issuerURL := strings.TrimSpace(config.OIDCIssuerURL)
	if issuerURL == "" || value == nil {
		return h
	}
	if err := transportsecurity.ValidateURL(issuerURL, config.AllowInsecureTransport, "OIDC issuer"); err != nil {
		log.Printf("WARNING: OIDC issuer disabled: %v", err)
		return h
	}
	if keys == nil {
		log.Printf("WARNING: OIDC issuer disabled: no master key is available to protect the signing keys")
		return h
	}
	issuer := oidcissuer.New(issuerURL, config.OIDCSigningAlgorithm, value, keys)
	issuer.RotationPeriod = time.Duration(config.OIDCKeyRotationDays) * 24 * time.Hour
	h.Issuer = issuer
	return h
}

// IssuerPath returns the path part of the issuer URL ("" for the root).
func (h *OIDCHandler) IssuerPath() string {
	if h.Issuer == nil {
		return ""
	}
	parsed, err := url.Parse(h.Issuer.IssuerValue())
	if err != nil {
		return ""
	}
	return strings.TrimRight(parsed.Path, "/")
}

// Discovery serves ISSUER/.well-known/openid-configuration. It is public.
func (h *OIDCHandler) Discovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Issuer == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	h.respondWithJSON(w, http.StatusOK, h.Issuer.Discovery())
}

// JWKS serves ISSUER/.well-known/jwks.json. It is public.
func (h *OIDCHandler) JWKS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Issuer == nil {
		http.NotFound(w, r)
		return
	}
	set, err := h.Issuer.JWKS(r.Context())
	if err != nil {
		log.Printf("OIDC JWKS: %v", err)
		respondWP4Error(&h.BaseHandler, w, http.StatusServiceUnavailable, errors.New("the signing keys are not available"))
		return
	}
	// The cache time must stay well under the publish lead of a rotation.
	w.Header().Set("Cache-Control", "public, max-age=300")
	h.respondWithJSON(w, http.StatusOK, set)
}

// OIDCTokenRequest is the body of a job token request.
type OIDCTokenRequest struct {
	Audience   string `json:"audience"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

// OIDCTokenResponse is a signed job token.
type OIDCTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Audience  string    `json:"audience"`
	Subject   string    `json:"subject"`
	ID        string    `json:"jti"`
}

// ErrOIDCDisabledForScope is a request from a job of an org or project that
// turned the issuer off.
var ErrOIDCDisabledForScope = errors.New("the OIDC issuer is disabled for this organization or project")

// EffectiveOIDCEnabled returns the effective issuer setting: the project
// value unless it is inherit, else the organization value, else allow.
func EffectiveOIDCEnabled(org, project *models.OIDCSettings) bool {
	if project != nil && project.Issuer != "" && project.Issuer != models.OIDCIssuerInherit {
		return project.Issuer == models.OIDCIssuerAllow
	}
	if org != nil && org.Issuer != "" && org.Issuer != models.OIDCIssuerInherit {
		return org.Issuer == models.OIDCIssuerAllow
	}
	return true
}

// Token handles POST /api/v1/oidc/token. Only a job token can call it. The
// claims come from the coordinator records of the bound job.
func (h *OIDCHandler) Token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Issuer == nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusNotFound, oidcissuer.ErrDisabled)
		return
	}
	ctx := r.Context()
	credential := checkauth.GetPrincipalFromContext(ctx)
	if credential == nil || credential.CredentialType != "job_token" || credential.BoundJobID == "" {
		respondWP4Error(&h.BaseHandler, w, http.StatusForbidden, errors.New("only a job token can request an OIDC token"))
		return
	}
	var req OIDCTokenRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil || !oidcissuer.ValidAudience(req.Audience) {
		respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, errors.New("audience is required: 1 to 512 characters, no spaces, not \"*\""))
		return
	}
	ttl := oidcissuer.DefaultTokenTTL
	if req.TTLSeconds != 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
		if ttl < oidcissuer.MinTokenTTL || ttl > oidcissuer.MaxTokenTTL {
			respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, errors.New("ttl_seconds must be 60 to 3600"))
			return
		}
	}
	job, err := h.store.GetJobByID(ctx, credential.BoundJobID)
	if err != nil || job == nil || !credential.HasOrganization(job.OwnershipOrgID()) {
		respondWP4Error(&h.BaseHandler, w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	if job.Status != "running" && job.Status != "scheduling" {
		respondWP4Error(&h.BaseHandler, w, http.StatusForbidden, errors.New("the job is not running"))
		return
	}
	orgSettings, err := h.store.GetOIDCSettings(ctx, job.OwnershipOrgID(), "")
	if err != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusInternalServerError, err)
		return
	}
	var projectSettings *models.OIDCSettings
	if job.ProjectID != nil {
		if projectSettings, err = h.store.GetOIDCSettings(ctx, job.OwnershipOrgID(), *job.ProjectID); err != nil {
			respondWP4Error(&h.BaseHandler, w, http.StatusInternalServerError, err)
			return
		}
	}
	if !EffectiveOIDCEnabled(orgSettings, projectSettings) {
		respondWP4Error(&h.BaseHandler, w, http.StatusForbidden, ErrOIDCDisabledForScope)
		return
	}
	facts, err := oidcissuer.LoadJobFacts(ctx, h.store, job)
	if err != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)
	// A token never outlives the job: the job start plus its timeout.
	if job.TimeoutSeconds > 0 {
		start := now
		if job.StartedAt != nil {
			start = job.StartedAt.UTC()
		}
		deadline := start.Add(time.Duration(job.TimeoutSeconds) * time.Second)
		if !deadline.After(now) {
			respondWP4Error(&h.BaseHandler, w, http.StatusForbidden, errors.New("the job is past its timeout"))
			return
		}
		if deadline.Before(expiresAt) {
			expiresAt = deadline
		}
	}
	var orgClaims, projectClaims map[string]map[string]string
	if orgSettings != nil {
		orgClaims = orgSettings.AudienceClaims
	}
	if projectSettings != nil {
		projectClaims = projectSettings.AudienceClaims
	}
	claims := oidcissuer.Claims(facts, oidcissuer.ExtraClaimsFor(req.Audience, orgClaims, projectClaims))
	token, err := h.Issuer.Sign(ctx, req.Audience, claims, expiresAt)
	if err != nil {
		log.Printf("OIDC sign: %v", err)
		respondWP4Error(&h.BaseHandler, w, http.StatusServiceUnavailable, errors.New("the OIDC token could not be signed"))
		return
	}
	subject, _ := claims["sub"].(string)
	audit.Record(ctx, h.appStore, job.OwnershipOrgID(), "oidc.token", "job", job.JobID,
		models.JSONB{"audience": req.Audience, "subject": subject, "jti": token.ID, "kid": token.KeyID,
			"expires_at": expiresAt.Format(time.RFC3339), "ci_origin": facts.CIOrigin,
			"execution_profile": facts.ExecutionProfile, "ref_classes": facts.RefClasses})
	h.respondWithJSON(w, http.StatusOK, OIDCTokenResponse{Token: token.Value, ExpiresAt: expiresAt,
		Audience: req.Audience, Subject: subject, ID: token.ID})
}

// OIDCKeyResponse is a signing key record without key material.
type OIDCKeyResponse struct {
	KID         string     `json:"kid"`
	Algorithm   string     `json:"algorithm"`
	CreatedAt   time.Time  `json:"created_at"`
	ActivatesAt time.Time  `json:"activates_at"`
	RetiresAt   *time.Time `json:"retires_at,omitempty"`
	State       string     `json:"state"`
}

func keyState(key models.OIDCSigningKey, signing string, now time.Time) string {
	switch {
	case key.RetiresAt != nil && !key.RetiresAt.After(now):
		return "retired"
	case key.KID == signing:
		return "signing"
	case key.ActivatesAt.After(now):
		return "pending"
	}
	return "published"
}

// ServeKeys routes /api/v1/oidc/keys[/rotate | /{kid}/retire]. It needs
// global authority with secrets:manage (the admin token).
func (h *OIDCHandler) ServeKeys(w http.ResponseWriter, r *http.Request, rest string) {
	if h.Issuer == nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusNotFound, oidcissuer.ErrDisabled)
		return
	}
	// A read of the keys (GET) also accepts organizations:read or
	// organizations:manage of a global admin; a change needs secrets:manage.
	allowed := principalAllowsGlobal(r.Context(), h.appStore, tokencaps.SecretsManage)
	if !allowed && r.Method == http.MethodGet {
		allowed = principalAllowsGlobalAdmin(r.Context(), h.appStore, readOrManage(r))
	}
	if !allowed {
		respondWP4Error(&h.BaseHandler, w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	parts := []string{}
	for _, part := range strings.Split(strings.Trim(rest, "/"), "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	ctx := r.Context()
	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		h.listKeys(w, r)
	case len(parts) == 1 && parts[0] == "rotate" && r.Method == http.MethodPost:
		var req struct {
			Immediate bool `json:"immediate"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req)
		key, err := h.Issuer.Rotate(ctx, req.Immediate)
		if err != nil {
			respondWP4Error(&h.BaseHandler, w, http.StatusInternalServerError, err)
			return
		}
		audit.Record(ctx, h.appStore, "", "oidc.key.rotate", "oidc_key", "", models.JSONB{"kid": key.KID,
			"activates_at": key.ActivatesAt.Format(time.RFC3339), "immediate": req.Immediate})
		h.listKeys(w, r)
	case len(parts) == 2 && parts[1] == "retire" && r.Method == http.MethodPost:
		if err := h.Issuer.Retire(ctx, parts[0], time.Now()); err != nil {
			respondWP4Error(&h.BaseHandler, w, statusForStoreError(err), err)
			return
		}
		audit.Record(ctx, h.appStore, "", "oidc.key.retire", "oidc_key", "", models.JSONB{"kid": parts[0]})
		h.listKeys(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *OIDCHandler) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.Issuer.Keys(r.Context())
	if err != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UTC()
	signing := ""
	var newest time.Time
	for _, key := range keys {
		if !key.ActivatesAt.After(now) && (key.RetiresAt == nil || key.RetiresAt.After(now)) && key.ActivatesAt.After(newest) {
			signing, newest = key.KID, key.ActivatesAt
		}
	}
	out := make([]OIDCKeyResponse, 0, len(keys))
	for _, key := range keys {
		out = append(out, OIDCKeyResponse{KID: key.KID, Algorithm: key.Algorithm, CreatedAt: key.CreatedAt,
			ActivatesAt: key.ActivatesAt, RetiresAt: key.RetiresAt, State: keyState(key, signing, now)})
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"issuer": h.Issuer.IssuerValue(), "keys": out})
}

// OIDCSettingsRequest is the body of a settings update. An omitted field
// keeps its value.
type OIDCSettingsRequest struct {
	Issuer         string                        `json:"issuer,omitempty"`
	AudienceClaims *map[string]map[string]string `json:"audience_claims,omitempty"`
}

// OIDCSettingsResponse is the setting of one scope and the effective value.
type OIDCSettingsResponse struct {
	Organization     string                       `json:"organization"`
	Project          string                       `json:"project,omitempty"`
	Issuer           string                       `json:"issuer"`
	AudienceClaims   map[string]map[string]string `json:"audience_claims"`
	EffectiveEnabled bool                         `json:"effective_enabled"`
	IssuerURL        string                       `json:"issuer_url,omitempty"`
}

// ServeSettings handles /api/v1/organizations/{org}/oidc-settings[/projects/{project}].
// GET needs organizations:read; PUT needs organizations:manage (an org admin).
func (h *OIDCHandler) ServeSettings(w http.ResponseWriter, r *http.Request, orgName, rest string) {
	ctx := r.Context()
	if h.store == nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusServiceUnavailable, errors.New("settings are not available"))
		return
	}
	capability := tokencaps.OrganizationsRead
	if r.Method == http.MethodPut {
		capability = tokencaps.OrganizationsManage
	} else if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	org, err := h.store.GetOrganizationByName(ctx, orgName)
	if err != nil || !principalAllowsOrg(ctx, h.appStore, org.OrgID, capability) {
		respondWP4Error(&h.BaseHandler, w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	var project *models.Project
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 2 && parts[0] == "projects" && parts[1] != "" {
		project, err = h.store.GetProjectByOrgAndName(ctx, org.OrgID, parts[1])
		if err != nil || project == nil {
			respondWP4Error(&h.BaseHandler, w, http.StatusNotFound, store.ErrNotFound)
			return
		}
	} else if strings.Trim(rest, "/") != "" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	projectID := ""
	if project != nil {
		projectID = project.ProjectID
	}
	current, err := h.store.GetOIDCSettings(ctx, org.OrgID, projectID)
	if err != nil {
		respondWP4Error(&h.BaseHandler, w, http.StatusInternalServerError, err)
		return
	}
	if current == nil {
		current = &models.OIDCSettings{OrgID: org.OrgID, Issuer: models.OIDCIssuerAllow, AudienceClaims: map[string]map[string]string{}}
		if project != nil {
			current.ProjectID = &project.ProjectID
			current.Issuer = models.OIDCIssuerInherit
		}
	}
	if r.Method == http.MethodPut {
		var req OIDCSettingsRequest
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req) != nil {
			respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		before := current.Issuer
		if req.Issuer != "" {
			valid := req.Issuer == models.OIDCIssuerAllow || req.Issuer == models.OIDCIssuerDeny ||
				(project != nil && req.Issuer == models.OIDCIssuerInherit)
			if !valid {
				respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, errors.New("issuer must be allow or deny (or inherit for a project)"))
				return
			}
			current.Issuer = req.Issuer
		}
		if req.AudienceClaims != nil {
			if err := oidcissuer.ValidateAudienceClaims(*req.AudienceClaims); err != nil {
				respondWP4Error(&h.BaseHandler, w, http.StatusBadRequest, err)
				return
			}
			current.AudienceClaims = *req.AudienceClaims
		}
		if err := h.store.SaveOIDCSettings(ctx, current); err != nil {
			respondWP4Error(&h.BaseHandler, w, http.StatusInternalServerError, err)
			return
		}
		audiences := make([]string, 0, len(current.AudienceClaims))
		for audience := range current.AudienceClaims {
			audiences = append(audiences, audience)
		}
		audit.Record(ctx, h.appStore, org.OrgID, "oidc.settings.update", "oidc_settings", current.SettingID,
			models.JSONB{"project": projectID, "issuer": current.Issuer, "previous_issuer": before, "audiences": audiences})
	}
	response := OIDCSettingsResponse{Organization: org.Name, Issuer: current.Issuer, AudienceClaims: current.AudienceClaims}
	if response.AudienceClaims == nil {
		response.AudienceClaims = map[string]map[string]string{}
	}
	if h.Issuer != nil {
		response.IssuerURL = h.Issuer.IssuerValue()
	}
	orgSettings := current
	var projectSettings *models.OIDCSettings
	if project != nil {
		response.Project = project.Name
		projectSettings = current
		orgSettings, _ = h.store.GetOIDCSettings(ctx, org.OrgID, "")
	}
	response.EffectiveEnabled = h.Issuer != nil && EffectiveOIDCEnabled(orgSettings, projectSettings)
	h.respondWithJSON(w, http.StatusOK, response)
}
