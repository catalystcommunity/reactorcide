package uiapi

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/oidcissuer"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// The job OIDC issuer (docs/oidc.md): the org and project settings, and the
// signing keys. The authorization is the one of the REST routes
// (handlers/oidc_handler.go): a role in the organization reads the
// settings, an org admin changes them, and only a global admin lists,
// rotates and retires keys.

type uiOIDCSettingsStore interface {
	GetOIDCSettings(ctx context.Context, orgID, projectID string) (*models.OIDCSettings, error)
	SaveOIDCSettings(ctx context.Context, settings *models.OIDCSettings) error
}

// effectiveOIDCEnabled is handlers.EffectiveOIDCEnabled: the project value
// unless it is inherit, else the organization value, else allow.
func effectiveOIDCEnabled(org, project *models.OIDCSettings) bool {
	if project != nil && project.Issuer != "" && project.Issuer != models.OIDCIssuerInherit {
		return project.Issuer == models.OIDCIssuerAllow
	}
	if org != nil && org.Issuer != "" && org.Issuer != models.OIDCIssuerInherit {
		return org.Issuer == models.OIDCIssuerAllow
	}
	return true
}

func audienceClaimsToWire(claims map[string]map[string]string) []csilapi.OidcAudienceClaims {
	audiences := make([]string, 0, len(claims))
	for audience := range claims {
		audiences = append(audiences, audience)
	}
	sort.Strings(audiences)
	out := make([]csilapi.OidcAudienceClaims, 0, len(audiences))
	for _, audience := range audiences {
		names := make([]string, 0, len(claims[audience]))
		for name := range claims[audience] {
			names = append(names, name)
		}
		sort.Strings(names)
		entry := csilapi.OidcAudienceClaims{Audience: audience, Claims: make([]csilapi.OidcClaim, 0, len(names))}
		for _, name := range names {
			entry.Claims = append(entry.Claims, csilapi.OidcClaim{Name: name, Value: claims[audience][name]})
		}
		out = append(out, entry)
	}
	return out
}

func audienceClaimsFromWire(in []csilapi.OidcAudienceClaims) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for _, entry := range in {
		audience := strings.TrimSpace(entry.Audience)
		if _, seen := out[audience]; seen {
			return nil, NewServiceError("invalid_argument", "an audience is given two times: "+audience)
		}
		claims := map[string]string{}
		for _, claim := range entry.Claims {
			name := strings.TrimSpace(claim.Name)
			if _, seen := claims[name]; seen {
				return nil, NewServiceError("invalid_argument", "a claim is given two times for audience "+audience+": "+name)
			}
			claims[name] = claim.Value
		}
		out[audience] = claims
	}
	return out, nil
}

// oidcSettingsScope resolves the caller, the organization and the optional
// project. manage selects the org admin check (else a role in the org).
func (s *UiService) oidcSettingsScope(ctx context.Context, organization string, projectID *string, manage bool) (uiOIDCSettingsStore, *models.Organization, *models.Project, bool, error) {
	ctx, id, _, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return nil, nil, nil, false, err
	}
	settings, ok := s.deps.Store.(uiOIDCSettingsStore)
	if !ok {
		return nil, nil, nil, false, NewServiceError("unavailable", "OIDC settings are not available")
	}
	org, err := s.orgByName(ctx, organization)
	if err != nil {
		return nil, nil, nil, false, err
	}
	// A token needs organizations:manage to change the settings and
	// organizations:read to read them, as in the REST routes.
	admin, _ := s.deps.Resolver.IsOrgAdmin(ctx, id, org.OrgID)
	admin = admin && requireTokenGrant(id, &org.OrgID, tokencaps.OrganizationsManage) == nil
	if manage && !admin {
		return nil, nil, nil, false, NewServiceError("forbidden", "only an org admin can change the OIDC settings")
	}
	if !manage {
		if err := s.requireOrgMember(ctx, id, org.OrgID, tokencaps.OrganizationsRead, tokencaps.OrganizationsManage); err != nil {
			return nil, nil, nil, false, err
		}
	}
	var project *models.Project
	if projectID != nil && strings.TrimSpace(*projectID) != "" {
		project, err = s.deps.Store.GetProjectByID(ctx, strings.TrimSpace(*projectID))
		if err != nil || project == nil || project.OwnershipOrgID() != org.OrgID {
			return nil, nil, nil, false, NewServiceError("not_found", "project not found")
		}
	}
	return settings, org, project, admin, nil
}

func (s *UiService) oidcSettingsView(ctx context.Context, settings uiOIDCSettingsStore, org *models.Organization, project *models.Project, current *models.OIDCSettings, canManage bool) csilapi.OidcSettingsResponse {
	out := csilapi.OidcSettingsResponse{Issuer: current.Issuer, AudienceClaims: audienceClaimsToWire(current.AudienceClaims),
		IssuerConfigured: s.deps.OIDCIssuer != nil, CanManage: canManage}
	if s.deps.OIDCIssuer != nil {
		out.IssuerUrl = optionalText(s.deps.OIDCIssuer.IssuerValue())
	}
	orgSettings, projectSettings := current, (*models.OIDCSettings)(nil)
	if project != nil {
		projectSettings = current
		orgSettings, _ = settings.GetOIDCSettings(ctx, org.OrgID, "")
	}
	out.EffectiveEnabled = s.deps.OIDCIssuer != nil && effectiveOIDCEnabled(orgSettings, projectSettings)
	return out
}

func loadOIDCSettings(ctx context.Context, settings uiOIDCSettingsStore, org *models.Organization, project *models.Project) (*models.OIDCSettings, error) {
	projectID := ""
	if project != nil {
		projectID = project.ProjectID
	}
	current, err := settings.GetOIDCSettings(ctx, org.OrgID, projectID)
	if err != nil {
		return nil, NewServiceError("internal", "could not read the OIDC settings")
	}
	if current == nil {
		current = &models.OIDCSettings{OrgID: org.OrgID, Issuer: models.OIDCIssuerAllow, AudienceClaims: map[string]map[string]string{}}
		if project != nil {
			id := project.ProjectID
			current.ProjectID = &id
			current.Issuer = models.OIDCIssuerInherit
		}
	}
	if current.AudienceClaims == nil {
		current.AudienceClaims = map[string]map[string]string{}
	}
	return current, nil
}

// GetOidcSettings returns the OIDC settings of an organization or project.
func (s *UiService) GetOidcSettings(ctx context.Context, req csilapi.GetOidcSettingsRequest) (csilapi.OidcSettingsResponse, error) {
	settings, org, project, admin, err := s.oidcSettingsScope(ctx, req.Organization, req.ProjectId, false)
	if err != nil {
		return csilapi.OidcSettingsResponse{}, err
	}
	current, err := loadOIDCSettings(ctx, settings, org, project)
	if err != nil {
		return csilapi.OidcSettingsResponse{}, err
	}
	return s.oidcSettingsView(ctx, settings, org, project, current, admin), nil
}

// PutOidcSettings changes the OIDC settings of an organization or project.
func (s *UiService) PutOidcSettings(ctx context.Context, req csilapi.PutOidcSettingsRequest) (csilapi.OidcSettingsResponse, error) {
	settings, org, project, _, err := s.oidcSettingsScope(ctx, req.Organization, req.ProjectId, true)
	if err != nil {
		return csilapi.OidcSettingsResponse{}, err
	}
	current, err := loadOIDCSettings(ctx, settings, org, project)
	if err != nil {
		return csilapi.OidcSettingsResponse{}, err
	}
	before := current.Issuer
	if req.Issuer != nil && *req.Issuer != "" {
		issuer := *req.Issuer
		valid := issuer == models.OIDCIssuerAllow || issuer == models.OIDCIssuerDeny || (project != nil && issuer == models.OIDCIssuerInherit)
		if !valid {
			return csilapi.OidcSettingsResponse{}, NewServiceError("invalid_argument", "issuer must be allow or deny (or inherit for a project)")
		}
		current.Issuer = issuer
	}
	if req.AudienceClaims != nil {
		claims, err := audienceClaimsFromWire(req.AudienceClaims)
		if err != nil {
			return csilapi.OidcSettingsResponse{}, err
		}
		if err := oidcissuer.ValidateAudienceClaims(claims); err != nil {
			return csilapi.OidcSettingsResponse{}, NewServiceError("invalid_argument", err.Error())
		}
		current.AudienceClaims = claims
	}
	if err := settings.SaveOIDCSettings(ctx, current); err != nil {
		return csilapi.OidcSettingsResponse{}, NewServiceError("internal", "could not save the OIDC settings")
	}
	projectID := ""
	if project != nil {
		projectID = project.ProjectID
	}
	audiences := make([]string, 0, len(current.AudienceClaims))
	for audience := range current.AudienceClaims {
		audiences = append(audiences, audience)
	}
	sort.Strings(audiences)
	s.recordAudit(ctx, org.OrgID, "oidc.settings.update", "oidc_settings", current.SettingID,
		models.JSONB{"project": projectID, "issuer": current.Issuer, "previous_issuer": before, "audiences": audiences})
	return s.oidcSettingsView(ctx, settings, org, project, current, true), nil
}

// oidcKeyState is handlers.keyState.
func oidcKeyState(key models.OIDCSigningKey, signing string, now time.Time) string {
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

func (s *UiService) oidcKeys(ctx context.Context) (csilapi.ListOidcKeysResponse, error) {
	issuer := s.deps.OIDCIssuer
	if issuer == nil {
		return csilapi.ListOidcKeysResponse{IssuerConfigured: false, Keys: []csilapi.OidcSigningKey{}}, nil
	}
	keys, err := issuer.Keys(ctx)
	if err != nil {
		return csilapi.ListOidcKeysResponse{}, NewServiceError("internal", "could not read the OIDC signing keys")
	}
	now := time.Now().UTC()
	signing := ""
	var newest time.Time
	for _, key := range keys {
		if !key.ActivatesAt.After(now) && (key.RetiresAt == nil || key.RetiresAt.After(now)) && key.ActivatesAt.After(newest) {
			signing, newest = key.KID, key.ActivatesAt
		}
	}
	out := csilapi.ListOidcKeysResponse{IssuerConfigured: true, Issuer: optionalText(issuer.IssuerValue()), Keys: make([]csilapi.OidcSigningKey, 0, len(keys))}
	for _, key := range keys {
		out.Keys = append(out.Keys, csilapi.OidcSigningKey{Kid: key.KID, Algorithm: key.Algorithm, CreatedAt: formatTime(key.CreatedAt),
			ActivatesAt: formatTime(key.ActivatesAt), RetiresAt: formatTimePtr(key.RetiresAt), State: oidcKeyState(key, signing, now)})
	}
	return out, nil
}

// ListOidcKeys lists the signing keys of the issuer (a global admin).
func (s *UiService) ListOidcKeys(ctx context.Context, req csilapi.ListOidcKeysRequest) (csilapi.ListOidcKeysResponse, error) {
	if _, err := s.requireGlobalAdminRead(ctx); err != nil {
		return csilapi.ListOidcKeysResponse{}, err
	}
	return s.oidcKeys(ctx)
}

// RotateOidcKey makes a new signing key (a global admin).
func (s *UiService) RotateOidcKey(ctx context.Context, req csilapi.RotateOidcKeyRequest) (csilapi.ListOidcKeysResponse, error) {
	if _, err := s.requireGlobalAdmin(ctx); err != nil {
		return csilapi.ListOidcKeysResponse{}, err
	}
	if s.deps.OIDCIssuer == nil {
		return csilapi.ListOidcKeysResponse{}, NewServiceError("unavailable", oidcissuer.ErrDisabled.Error())
	}
	immediate := req.Immediate != nil && *req.Immediate
	key, err := s.deps.OIDCIssuer.Rotate(ctx, immediate)
	if err != nil {
		return csilapi.ListOidcKeysResponse{}, NewServiceError("internal", "could not rotate the OIDC signing key")
	}
	s.recordAudit(ctx, "", "oidc.key.rotate", "oidc_key", "", models.JSONB{"kid": key.KID,
		"activates_at": key.ActivatesAt.Format(time.RFC3339), "immediate": immediate})
	return s.oidcKeys(ctx)
}

// RetireOidcKey retires a signing key now (a global admin).
func (s *UiService) RetireOidcKey(ctx context.Context, req csilapi.RetireOidcKeyRequest) (csilapi.ListOidcKeysResponse, error) {
	if _, err := s.requireGlobalAdmin(ctx); err != nil {
		return csilapi.ListOidcKeysResponse{}, err
	}
	if s.deps.OIDCIssuer == nil {
		return csilapi.ListOidcKeysResponse{}, NewServiceError("unavailable", oidcissuer.ErrDisabled.Error())
	}
	if err := requireNonEmpty("kid", req.Kid, 200); err != nil {
		return csilapi.ListOidcKeysResponse{}, err
	}
	if err := s.deps.OIDCIssuer.Retire(ctx, req.Kid, time.Now()); err != nil {
		return csilapi.ListOidcKeysResponse{}, mapStoreErr(err, "key not found")
	}
	s.recordAudit(ctx, "", "oidc.key.retire", "oidc_key", "", models.JSONB{"kid": req.Kid})
	return s.oidcKeys(ctx)
}
