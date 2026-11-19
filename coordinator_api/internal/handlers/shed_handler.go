package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// shedRoutes serves the shed level and defaults profile REST API (plan items
// A5 and D4). Every route works with the admin API token.
//
//	GET|PUT /api/v1/shed                                 global shed level
//	GET|PUT /api/v1/organizations/NAME/shed              organization shed level and actions
//	GET|PUT /api/v1/organizations/NAME/defaults-profile  defaults profile
type shedRoutes struct {
	BaseHandler
	store store.Store
}

func newShedRoutes(appStore store.Store) *shedRoutes {
	return &shedRoutes{store: appStore}
}

func (s *shedRoutes) register(mux *http.ServeMux, transactionMiddleware, authMiddleware func(http.Handler) http.Handler) {
	mux.HandleFunc("/api/v1/shed", func(w http.ResponseWriter, r *http.Request) {
		transactionMiddleware(authMiddleware(http.HandlerFunc(s.serveGlobal))).ServeHTTP(w, r)
	})
}

// serveOrg handles NAME/shed and NAME/defaults-profile. It returns false for
// other paths.
func (s *shedRoutes) serveOrg(w http.ResponseWriter, r *http.Request, path string,
	transactionMiddleware, authMiddleware func(http.Handler) http.Handler) bool {
	parts := strings.Split(path, "/")
	if len(parts) != 2 || (parts[1] != "shed" && parts[1] != "defaults-profile") {
		return false
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if parts[1] == "shed" {
			s.serveOrgShed(w, r, parts[0])
			return
		}
		s.serveDefaultsProfile(w, r, parts[0])
	})
	transactionMiddleware(authMiddleware(handler)).ServeHTTP(w, r)
	return true
}

// shedRequest is the PUT body of a shed route. Level empty keeps the level.
type shedRequest struct {
	Level string `json:"level"`
	// Actions replaces the actions of each level (organization only).
	Actions *models.ShedActionsConfig `json:"actions,omitempty"`
	// ResetActions returns the organization to the built-in actions.
	ResetActions bool `json:"reset_actions,omitempty"`
	// Reason is a text for the audit log.
	Reason string `json:"reason,omitempty"`
}

func (s *shedRoutes) shedStore(w http.ResponseWriter) (store.DraftShedStore, bool) {
	shed, ok := s.store.(store.DraftShedStore)
	if !ok {
		s.respondWithError(w, http.StatusNotImplemented, errors.New("shed levels are not supported by this store"))
	}
	return shed, ok
}

func decodeStrict(r *http.Request, value any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func (s *shedRoutes) serveGlobal(w http.ResponseWriter, r *http.Request) {
	shed, ok := s.shedStore(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		principal := checkauth.GetPrincipalFromContext(r.Context())
		if principal == nil || !principal.HasCapability(tokencaps.OrganizationsRead) {
			s.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		state, err := shed.GetShedState(r.Context(), "")
		if err != nil {
			s.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		s.respondWithJSON(w, http.StatusOK, state)
	case http.MethodPut, http.MethodPatch:
		if !principalAllowsGlobal(r.Context(), s.store, tokencaps.OrganizationsManage) {
			s.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		var req shedRequest
		if err := decodeStrict(r, &req); err != nil || !models.IsValidShedLevel(req.Level) || req.Actions != nil || req.ResetActions {
			s.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input",
				Message: "the body must be {\"level\": \"normal|degraded|critical\", \"reason\": ...}; actions are an organization setting"})
			return
		}
		before, _ := shed.GetShedState(r.Context(), "")
		if err := shed.SetGlobalShedLevel(r.Context(), req.Level); err != nil {
			s.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		audit.Record(r.Context(), s.store, "", "shed.set_global", "global_setting", models.GlobalSettingShedLevel,
			models.JSONB{"from": before.GlobalLevel, "to": req.Level, "reason": req.Reason})
		state, err := shed.GetShedState(r.Context(), "")
		if err != nil {
			s.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		s.respondWithJSON(w, http.StatusOK, state)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *shedRoutes) organization(w http.ResponseWriter, r *http.Request, name string) (*models.Organization, bool) {
	orgs, ok := s.store.(organizationStore)
	if !ok {
		s.respondWithError(w, http.StatusNotImplemented, errors.New("organizations are not supported by this store"))
		return nil, false
	}
	org, err := orgs.GetOrganizationByName(r.Context(), name)
	if err != nil {
		s.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return nil, false
	}
	return org, true
}

func (s *shedRoutes) serveOrgShed(w http.ResponseWriter, r *http.Request, name string) {
	shed, ok := s.shedStore(w)
	if !ok {
		return
	}
	org, ok := s.organization(w, r, name)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !principalAllowsOrg(r.Context(), s.store, org.OrgID, tokencaps.OrganizationsRead) {
			s.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		state, err := shed.GetShedState(r.Context(), org.OrgID)
		if err != nil {
			s.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		s.respondWithJSON(w, http.StatusOK, state)
	case http.MethodPut, http.MethodPatch:
		if !principalAllowsOrg(r.Context(), s.store, org.OrgID, tokencaps.OrganizationsManage) {
			s.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		var req shedRequest
		if err := decodeStrict(r, &req); err != nil || (req.Level != "" && !models.IsValidShedLevel(req.Level)) ||
			(req.Level == "" && req.Actions == nil && !req.ResetActions) || (req.Actions != nil && req.ResetActions) {
			s.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input",
				Message: "the body must set level (normal, degraded, critical), actions, or reset_actions"})
			return
		}
		state, err := setOrganizationShed(r.Context(), s.store, shed, org, req)
		if err != nil {
			s.respondWithError(w, http.StatusBadRequest, err)
			return
		}
		s.respondWithJSON(w, http.StatusOK, state)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// setOrganizationShed applies a shed request to an organization and records
// an audit event with the old and the new values.
func setOrganizationShed(ctx context.Context, appStore any, shed store.DraftShedStore, org *models.Organization, req shedRequest) (models.ShedState, error) {
	before, err := shed.GetShedState(ctx, org.OrgID)
	if err != nil {
		return models.ShedState{}, err
	}
	if err := shed.SetOrganizationShed(ctx, org.OrgID, req.Level, req.Actions, req.ResetActions); err != nil {
		return models.ShedState{}, err
	}
	after, err := shed.GetShedState(ctx, org.OrgID)
	if err != nil {
		return models.ShedState{}, err
	}
	details := models.JSONB{"from": before.OrgLevel, "to": after.OrgLevel, "level_in_force": after.Level, "reason": req.Reason}
	if req.Actions != nil || req.ResetActions {
		details["actions"] = after.Config
	}
	audit.Record(ctx, appStore, org.OrgID, "shed.set_org", "organization", org.Name, details)
	return after, nil
}

type defaultsProfileRequest struct {
	Profile string `json:"profile"`
}

type defaultsProfileResponse struct {
	Organization string `json:"organization"`
	Profile      string `json:"profile"`
	// Applied lists the settings that the profile wrote.
	RunGate       []string `json:"run_gate"`
	DynamicJobs   string   `json:"dynamic_jobs"`
	BuiltInPolicy bool     `json:"builtin_policy"`
	// CacheWriteClasses is the cache_write_classes of the "standard"
	// execution profile that the profile writes.
	CacheWriteClasses []string `json:"cache_write_classes"`
}

func defaultsProfileView(org *models.Organization, profile string) defaultsProfileResponse {
	settings := models.SettingsForDefaultsProfile(profile)
	return defaultsProfileResponse{Organization: org.Name, Profile: profile, RunGate: settings.RunGate,
		DynamicJobs: settings.DynamicJobs, BuiltInPolicy: settings.BuiltInPolicy, CacheWriteClasses: settings.CacheWriteClasses}
}

func (s *shedRoutes) serveDefaultsProfile(w http.ResponseWriter, r *http.Request, name string) {
	org, ok := s.organization(w, r, name)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !principalAllowsOrg(r.Context(), s.store, org.OrgID, tokencaps.OrganizationsRead) {
			s.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		s.respondWithJSON(w, http.StatusOK, defaultsProfileView(org, org.DefaultsProfile))
	case http.MethodPut, http.MethodPost:
		// The profile writes the run gate and dynamic_jobs: the caller must
		// manage the organization and its CI trust settings.
		if !principalAllowsOrg(r.Context(), s.store, org.OrgID, tokencaps.OrganizationsManage) ||
			!principalAllowsOrg(r.Context(), s.store, org.OrgID, tokencaps.PoliciesManage) {
			s.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		var req defaultsProfileRequest
		if err := decodeStrict(r, &req); err != nil || !models.IsValidDefaultsProfile(req.Profile) {
			s.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: "the body must be {\"profile\": \"secure\"} or {\"profile\": \"legacy\"}"})
			return
		}
		profiles, ok := s.store.(store.DefaultsProfileStore)
		if !ok {
			s.respondWithError(w, http.StatusNotImplemented, errors.New("defaults profiles are not supported by this store"))
			return
		}
		updatedBy := "api"
		if principal := checkauth.GetPrincipalFromContext(r.Context()); principal != nil {
			updatedBy = "token:" + principal.CredentialID
		}
		if err := profiles.ApplyOrganizationDefaultsProfile(r.Context(), org.OrgID, req.Profile, updatedBy); err != nil {
			s.respondWithError(w, http.StatusBadRequest, err)
			return
		}
		view := defaultsProfileView(org, req.Profile)
		audit.Record(r.Context(), s.store, org.OrgID, "organization.defaults_profile", "organization", org.Name,
			models.JSONB{"from": org.DefaultsProfile, "to": req.Profile, "run_gate": view.RunGate,
				"dynamic_jobs": view.DynamicJobs, "builtin_policy": view.BuiltInPolicy})
		s.respondWithJSON(w, http.StatusOK, view)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
