package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

type profileStore interface {
	GetOrganizationByName(context.Context, string) (*models.Organization, error)
	CreateExecutionProfile(context.Context, *models.ExecutionProfile) error
	GetExecutionProfile(context.Context, string, string) (*models.ExecutionProfile, error)
	ListExecutionProfiles(context.Context, string) ([]models.ExecutionProfile, error)
	UpdateExecutionProfile(context.Context, *models.ExecutionProfile) error
	DeleteExecutionProfile(context.Context, string, string) error
}

type ProfileHandler struct {
	BaseHandler
	store    profileStore
	appStore store.Store
}

func NewProfileHandler(appStore store.Store) *ProfileHandler {
	ps, _ := appStore.(profileStore)
	return &ProfileHandler{store: ps, appStore: appStore}
}

func (h *ProfileHandler) authorize(r *http.Request, org *models.Organization) bool {
	return principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.PoliciesManage)
}

func (h *ProfileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, orgName, profileName string) {
	if h.store == nil {
		h.respondWithError(w, http.StatusServiceUnavailable, store.ErrServiceUnavailable)
		return
	}
	org, err := h.store.GetOrganizationByName(r.Context(), orgName)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if !h.authorize(r, org) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if profileName == "" {
			items, err := h.store.ListExecutionProfiles(r.Context(), org.OrgID)
			if err != nil {
				h.respondWithError(w, 500, err)
				return
			}
			h.respondWithJSON(w, http.StatusOK, map[string]any{"profiles": items})
			return
		}
		profile, err := h.store.GetExecutionProfile(r.Context(), org.OrgID, profileName)
		if err != nil {
			h.respondWithError(w, 404, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, profile)
	case http.MethodPost:
		if profileName != "" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var profile models.ExecutionProfile
		if json.NewDecoder(r.Body).Decode(&profile) != nil {
			h.respondWithError(w, 400, store.ErrInvalidInput)
			return
		}
		profile.OrgID = org.OrgID
		profile.ProfileID = ""
		profile.CreatedAt = time.Time{}
		profile.UpdatedAt = time.Time{}
		profile.Name = strings.TrimSpace(profile.Name)
		if err := h.store.CreateExecutionProfile(r.Context(), &profile); err != nil {
			h.respondWithError(w, 400, err)
			return
		}
		audit.Record(r.Context(), h.appStore, org.OrgID, "execution_profile.create", "execution_profile", profile.ProfileID, models.JSONB{"name": profile.Name})
		h.respondWithJSON(w, http.StatusCreated, profile)
	case http.MethodPut, http.MethodPatch:
		profile, err := h.store.GetExecutionProfile(r.Context(), org.OrgID, profileName)
		if err != nil {
			h.respondWithError(w, 404, err)
			return
		}
		profileID := profile.ProfileID
		createdAt := profile.CreatedAt
		if json.NewDecoder(r.Body).Decode(profile) != nil {
			h.respondWithError(w, 400, store.ErrInvalidInput)
			return
		}
		profile.OrgID = org.OrgID
		profile.Name = profileName
		profile.ProfileID = profileID
		profile.CreatedAt = createdAt
		if err := h.store.UpdateExecutionProfile(r.Context(), profile); err != nil {
			h.respondWithError(w, 400, err)
			return
		}
		audit.Record(r.Context(), h.appStore, org.OrgID, "execution_profile.update", "execution_profile", profile.ProfileID, models.JSONB{"name": profile.Name})
		h.respondWithJSON(w, http.StatusOK, profile)
	case http.MethodDelete:
		if err := h.store.DeleteExecutionProfile(r.Context(), org.OrgID, profileName); err != nil {
			h.respondWithError(w, 400, err)
			return
		}
		audit.Record(r.Context(), h.appStore, org.OrgID, "execution_profile.delete", "execution_profile", profileName, models.JSONB{})
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
