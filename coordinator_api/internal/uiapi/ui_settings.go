package uiapi

import (
	"context"
	"encoding/json"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// requireGlobalAdmin is a small wrapper so every global-admin-only op in
// this file (settings, trusted identities, trusted domain patterns — the
// entirety of UI_AUTH_PLAN.md's matrix's last row) shares one authorization
// call site. An anonymous caller (no valid session at all) is rejected with
// "unauthorized" rather than "forbidden", matching every other
// identity-required op in this service (see Deps.requireUser) — "forbidden"
// is reserved for an authenticated-but-insufficiently-privileged caller.
func (s *UiService) requireGlobalAdmin(ctx context.Context) (authz.Identity, error) {
	id, _, err := s.deps.requireUser(ctx)
	if err != nil {
		return authz.Identity{}, err
	}
	if err := s.deps.Resolver.RequireGlobalAdmin(ctx, id); err != nil {
		return authz.Identity{}, mapPermissionErr(err)
	}
	return id, nil
}

func globalSettingToEntry(g *models.GlobalSetting) csilapi.GlobalSettingEntry {
	var v any
	_ = json.Unmarshal(g.Value, &v)
	return csilapi.GlobalSettingEntry{Key: g.Key, Value: v, UpdatedAt: formatTime(g.UpdatedAt)}
}

// GetGlobalSettings requires global admin.
func (s *UiService) GetGlobalSettings(ctx context.Context, req csilapi.GetGlobalSettingsRequest) (csilapi.GetGlobalSettingsResponse, error) {
	if _, err := s.requireGlobalAdmin(ctx); err != nil {
		return csilapi.GetGlobalSettingsResponse{}, err
	}

	settings, err := s.deps.Store.ListGlobalSettings(ctx)
	if err != nil {
		return csilapi.GetGlobalSettingsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.GlobalSettingEntry, len(settings))
	for i := range settings {
		out[i] = globalSettingToEntry(&settings[i])
	}
	return csilapi.GetGlobalSettingsResponse{
		NewProjectsPrivate: authz.NewProjectIsPrivate(ctx, s.deps.Store),
		Settings:           out,
	}, nil
}

// UpdateGlobalSettings requires global admin. new_projects_private, if
// given, is written under models.GlobalSettingNewProjectsPrivate; any other
// entries in settings are written verbatim under their own key (a generic
// escape hatch for future settings this service doesn't have a named field
// for yet).
func (s *UiService) UpdateGlobalSettings(ctx context.Context, req csilapi.UpdateGlobalSettingsRequest) (csilapi.UpdateGlobalSettingsResponse, error) {
	if _, err := s.requireGlobalAdmin(ctx); err != nil {
		return csilapi.UpdateGlobalSettingsResponse{}, err
	}

	if req.NewProjectsPrivate != nil {
		raw, _ := json.Marshal(*req.NewProjectsPrivate)
		if err := s.deps.Store.SetGlobalSetting(ctx, models.GlobalSettingNewProjectsPrivate, models.JSONValue(raw)); err != nil {
			return csilapi.UpdateGlobalSettingsResponse{}, NewServiceError("internal", "failed to update settings")
		}
	}
	for _, entry := range req.Settings {
		if err := requireNonEmpty("settings[].key", entry.Key, maxNameLength); err != nil {
			return csilapi.UpdateGlobalSettingsResponse{}, err
		}
		raw, err := json.Marshal(entry.Value)
		if err != nil {
			return csilapi.UpdateGlobalSettingsResponse{}, NewServiceError("invalid_argument", "settings[].value must be JSON-serializable")
		}
		if err := s.deps.Store.SetGlobalSetting(ctx, entry.Key, models.JSONValue(raw)); err != nil {
			return csilapi.UpdateGlobalSettingsResponse{}, NewServiceError("internal", "failed to update settings")
		}
	}

	settings, err := s.deps.Store.ListGlobalSettings(ctx)
	if err != nil {
		return csilapi.UpdateGlobalSettingsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.GlobalSettingEntry, len(settings))
	for i := range settings {
		out[i] = globalSettingToEntry(&settings[i])
	}
	return csilapi.UpdateGlobalSettingsResponse{
		NewProjectsPrivate: authz.NewProjectIsPrivate(ctx, s.deps.Store),
		Settings:           out,
	}, nil
}

func trustedIdentityToCsil(t *models.AuthTrustedIdentity) csilapi.TrustedIdentity {
	return csilapi.TrustedIdentity{Domain: t.Domain, Handle: t.Handle, Source: t.Source, CreatedAt: formatTime(t.CreatedAt)}
}

// ListTrustedIdentities requires global admin.
func (s *UiService) ListTrustedIdentities(ctx context.Context, req csilapi.ListTrustedIdentitiesRequest) (csilapi.ListTrustedIdentitiesResponse, error) {
	if _, err := s.requireGlobalAdmin(ctx); err != nil {
		return csilapi.ListTrustedIdentitiesResponse{}, err
	}
	rows, err := s.deps.Store.ListTrustedIdentities(ctx)
	if err != nil {
		return csilapi.ListTrustedIdentitiesResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.TrustedIdentity, len(rows))
	for i := range rows {
		out[i] = trustedIdentityToCsil(&rows[i])
	}
	return csilapi.ListTrustedIdentitiesResponse{Identities: out}, nil
}

// AddTrustedIdentity requires global admin. domain is required; an absent
// handle is a bare-domain wildcard (see models.AuthTrustedIdentity.Matches).
func (s *UiService) AddTrustedIdentity(ctx context.Context, req csilapi.AddTrustedIdentityRequest) (csilapi.AddTrustedIdentityResponse, error) {
	if _, err := s.requireGlobalAdmin(ctx); err != nil {
		return csilapi.AddTrustedIdentityResponse{}, err
	}
	if err := requireNonEmpty("domain", req.Domain, 255); err != nil {
		return csilapi.AddTrustedIdentityResponse{}, err
	}
	handle := derefOr(req.Handle, "")
	if err := optionalMaxLen("handle", handle, 255); err != nil {
		return csilapi.AddTrustedIdentityResponse{}, err
	}

	row := &models.AuthTrustedIdentity{Domain: req.Domain, Handle: handle, Source: models.TrustedIdentitySourceAdmin}
	if err := s.deps.Store.UpsertTrustedIdentity(ctx, row); err != nil {
		return csilapi.AddTrustedIdentityResponse{}, NewServiceError("internal", "failed to add trusted identity")
	}
	return csilapi.AddTrustedIdentityResponse{Identity: trustedIdentityToCsil(row)}, nil
}

// RemoveTrustedIdentity requires global admin.
func (s *UiService) RemoveTrustedIdentity(ctx context.Context, req csilapi.RemoveTrustedIdentityRequest) (csilapi.RemoveTrustedIdentityResponse, error) {
	if _, err := s.requireGlobalAdmin(ctx); err != nil {
		return csilapi.RemoveTrustedIdentityResponse{}, err
	}
	if err := requireNonEmpty("domain", req.Domain, 255); err != nil {
		return csilapi.RemoveTrustedIdentityResponse{}, err
	}
	handle := derefOr(req.Handle, "")

	if err := s.deps.Store.DeleteTrustedIdentity(ctx, req.Domain, handle); err != nil {
		return csilapi.RemoveTrustedIdentityResponse{}, mapStoreErr(err, "trusted identity not found")
	}
	return csilapi.RemoveTrustedIdentityResponse{Removed: true}, nil
}

func trustedDomainPatternToCsil(p *models.AuthTrustedDomainPattern) csilapi.TrustedDomainPattern {
	out := csilapi.TrustedDomainPattern{PatternId: p.PatternID, Pattern: p.Pattern, CreatedBy: p.CreatedBy, CreatedAt: formatTime(p.CreatedAt)}
	if p.Description != "" {
		d := p.Description
		out.Description = &d
	}
	return out
}

// ListTrustedDomainPatterns requires global admin.
func (s *UiService) ListTrustedDomainPatterns(ctx context.Context, req csilapi.ListTrustedDomainPatternsRequest) (csilapi.ListTrustedDomainPatternsResponse, error) {
	if _, err := s.requireGlobalAdmin(ctx); err != nil {
		return csilapi.ListTrustedDomainPatternsResponse{}, err
	}
	rows, err := s.deps.Store.ListTrustedDomainPatterns(ctx)
	if err != nil {
		return csilapi.ListTrustedDomainPatternsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.TrustedDomainPattern, len(rows))
	for i := range rows {
		out[i] = trustedDomainPatternToCsil(&rows[i])
	}
	return csilapi.ListTrustedDomainPatternsResponse{Patterns: out}, nil
}

// AddTrustedDomainPattern requires global admin. pattern is validated as a
// compilable RE2 pattern (auth.ValidateDomainPattern) before it is
// persisted — an admission-list regex that fails to compile would silently
// admit nobody (see auth.Admission.compiledPatterns, which skips
// non-compiling rows rather than failing every check).
func (s *UiService) AddTrustedDomainPattern(ctx context.Context, req csilapi.AddTrustedDomainPatternRequest) (csilapi.AddTrustedDomainPatternResponse, error) {
	id, err := s.requireGlobalAdmin(ctx)
	if err != nil {
		return csilapi.AddTrustedDomainPatternResponse{}, err
	}
	if verr := auth.ValidateDomainPattern(req.Pattern); verr != nil {
		return csilapi.AddTrustedDomainPatternResponse{}, NewServiceError("invalid_argument", verr.Error())
	}

	row := &models.AuthTrustedDomainPattern{Pattern: req.Pattern, Description: derefOr(req.Description, "")}
	if !id.Anonymous && id.UserID != "" {
		userID := id.UserID
		row.CreatedBy = &userID
	}
	if err := s.deps.Store.CreateTrustedDomainPattern(ctx, row); err != nil {
		return csilapi.AddTrustedDomainPatternResponse{}, NewServiceError("conflict", "this pattern already exists")
	}
	s.deps.Admission.Refresh()
	return csilapi.AddTrustedDomainPatternResponse{Pattern: trustedDomainPatternToCsil(row)}, nil
}

// RemoveTrustedDomainPattern requires global admin.
func (s *UiService) RemoveTrustedDomainPattern(ctx context.Context, req csilapi.RemoveTrustedDomainPatternRequest) (csilapi.RemoveTrustedDomainPatternResponse, error) {
	if _, err := s.requireGlobalAdmin(ctx); err != nil {
		return csilapi.RemoveTrustedDomainPatternResponse{}, err
	}
	if err := requireNonEmpty("pattern_id", req.PatternId, 64); err != nil {
		return csilapi.RemoveTrustedDomainPatternResponse{}, err
	}

	if err := s.deps.Store.DeleteTrustedDomainPattern(ctx, req.PatternId); err != nil {
		return csilapi.RemoveTrustedDomainPatternResponse{}, mapStoreErr(err, "trusted domain pattern not found")
	}
	s.deps.Admission.Refresh()
	return csilapi.RemoveTrustedDomainPatternResponse{Removed: true}, nil
}
