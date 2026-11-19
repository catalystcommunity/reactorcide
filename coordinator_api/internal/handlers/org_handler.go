package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

type organizationStore interface {
	CreateOrganization(ctx context.Context, organization *models.Organization) error
	GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
	ListOrganizations(ctx context.Context, limit, offset int) ([]models.Organization, error)
	UpdateOrganization(ctx context.Context, organization *models.Organization) error
	DeleteOrganization(ctx context.Context, orgID, replacementOrgID string) error
	SetDefaultOrganization(ctx context.Context, orgID string) error
	GetDefaultOrganization(ctx context.Context) (*models.Organization, error)
}

type OrganizationHandler struct {
	BaseHandler
	store    organizationStore
	appStore store.Store
}

func NewOrganizationHandler(appStore store.Store) *OrganizationHandler {
	organizationStore, _ := appStore.(organizationStore)
	return &OrganizationHandler{store: organizationStore, appStore: appStore}
}

type organizationRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	IsPrivate   bool   `json:"is_private"`
	Status      string `json:"status"`
	// InfraRetries sets how many automatic retries an infra.platform
	// failure gets (0-5). Omitted keeps the stored value.
	InfraRetries *int `json:"infra_retries,omitempty"`
	// EgressControl turns egress control on or off for the organization
	// (docs/egress-proxy.md). Omitted keeps the stored value.
	EgressControl *bool `json:"egress_control,omitempty"`
	// Event settings. Omitted keeps the stored value.
	DuplicateEvents *string `json:"duplicate_events,omitempty"`
	DynamicJobs     *string `json:"dynamic_jobs,omitempty"`
	DraftPRs        *string `json:"draft_prs,omitempty"`
	// DefaultsProfile selects the defaults of a new organization: "secure"
	// (the default) or "legacy". Create only; use PUT
	// /api/v1/organizations/NAME/defaults-profile to change it later.
	DefaultsProfile string `json:"defaults_profile,omitempty"`
}

type organizationDeleteRequest struct {
	Replacement string `json:"replacement"`
	Confirm     bool   `json:"confirm"`
}

type organizationResponse struct {
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	IsPrivate     bool   `json:"is_private"`
	Status        string `json:"status"`
	IsDefault     bool   `json:"is_default"`
	InfraRetries  *int   `json:"infra_retries,omitempty"`
	EgressControl bool   `json:"egress_control"`
	// Event, defaults and shed settings (WP9).
	DuplicateEvents string `json:"duplicate_events,omitempty"`
	DynamicJobs     string `json:"dynamic_jobs,omitempty"`
	DraftPRs        string `json:"draft_prs,omitempty"`
	DefaultsProfile string `json:"defaults_profile,omitempty"`
	ShedLevel       string `json:"shed_level,omitempty"`
}

func (h *OrganizationHandler) require(r *http.Request, capability string, orgID string) bool {
	if orgID == "" {
		return principalAllowsGlobal(r.Context(), h.appStore, capability)
	}
	return principalAllowsOrg(r.Context(), h.appStore, orgID, capability)
}

func (h *OrganizationHandler) Create(w http.ResponseWriter, r *http.Request) {
	if h.store == nil || !h.require(r, tokencaps.OrganizationsManage, "") {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	var req organizationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	if req.DefaultsProfile != "" && !models.IsValidDefaultsProfile(req.DefaultsProfile) {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	organization := &models.Organization{Name: req.Name, DisplayName: req.DisplayName, IsPrivate: req.IsPrivate, Status: req.Status,
		DefaultsProfile: req.DefaultsProfile}
	if err := h.store.CreateOrganization(r.Context(), organization); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrAlreadyExists) {
			status = http.StatusConflict
		}
		h.respondWithError(w, status, err)
		return
	}
	audit.Record(r.Context(), h.store, organization.OrgID, "organization.create", "organization", organization.Name,
		models.JSONB{"status": organization.Status, "defaults_profile": organization.DefaultsProfile})
	h.respondWithJSON(w, http.StatusCreated, h.response(r, organization))
}

func (h *OrganizationHandler) List(w http.ResponseWriter, r *http.Request) {
	principal := checkauth.GetPrincipalFromContext(r.Context())
	if h.store == nil || principal == nil || !principal.HasCapability(tokencaps.OrganizationsRead) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	organizations, err := h.store.ListOrganizations(r.Context(), 0, 0)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	items := make([]organizationResponse, 0, len(organizations))
	for i := range organizations {
		if principalAllowsOrg(r.Context(), h.appStore, organizations[i].OrgID, tokencaps.OrganizationsRead) {
			items = append(items, h.response(r, &organizations[i]))
		}
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"organizations": items, "total": len(items)})
}

func (h *OrganizationHandler) Get(w http.ResponseWriter, r *http.Request) {
	organization, err := h.store.GetOrganizationByName(r.Context(), h.getID(r, "organization_name"))
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if !h.require(r, tokencaps.OrganizationsRead, organization.OrgID) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	h.respondWithJSON(w, http.StatusOK, h.response(r, organization))
}

func (h *OrganizationHandler) Update(w http.ResponseWriter, r *http.Request) {
	organization, err := h.store.GetOrganizationByName(r.Context(), h.getID(r, "organization_name"))
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if !h.require(r, tokencaps.OrganizationsManage, organization.OrgID) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	var req organizationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	if req.Name != "" && req.Name != organization.Name {
		h.respondWithError(w, http.StatusBadRequest, errors.New("organization name is immutable"))
		return
	}
	organization.DisplayName = req.DisplayName
	organization.IsPrivate = req.IsPrivate
	if req.Status != "" {
		organization.Status = req.Status
	}
	if req.InfraRetries != nil && !models.IsValidInfraRetries(*req.InfraRetries) {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	eventSettings, err := applyOrganizationEventSettings(organization, req)
	if err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	if err := h.store.UpdateOrganization(r.Context(), organization); err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	details := models.JSONB{"status": organization.Status}
	if req.InfraRetries != nil {
		settings, ok := h.store.(store.InfraRetryStore)
		if !ok {
			h.respondWithError(w, http.StatusNotImplemented, errors.New("infra_retries is not supported by this store"))
			return
		}
		if err := settings.SetOrganizationInfraRetries(r.Context(), organization.OrgID, *req.InfraRetries); err != nil {
			h.respondWithError(w, http.StatusBadRequest, err)
			return
		}
		value := *req.InfraRetries
		organization.InfraRetries = &value
		details["infra_retries"] = value
	}
	if req.EgressControl != nil {
		settings, ok := h.store.(store.EgressControlStore)
		if !ok {
			h.respondWithError(w, http.StatusNotImplemented, errors.New("egress_control is not supported by this store"))
			return
		}
		if err := settings.SetOrganizationEgressControl(r.Context(), organization.OrgID, *req.EgressControl); err != nil {
			h.respondWithError(w, http.StatusBadRequest, err)
			return
		}
		organization.EgressControl = *req.EgressControl
		details["egress_control"] = *req.EgressControl
	}
	if req.DraftPRs != nil {
		settings, ok := h.store.(store.DraftShedStore)
		if !ok {
			h.respondWithError(w, http.StatusNotImplemented, errors.New("draft_prs is not supported by this store"))
			return
		}
		if err := settings.SetOrganizationDraftPRs(r.Context(), organization.OrgID, *req.DraftPRs); err != nil {
			h.respondWithError(w, http.StatusBadRequest, err)
			return
		}
		organization.DraftPRs = *req.DraftPRs
	}
	if eventSettings != nil {
		audit.Record(r.Context(), h.store, organization.OrgID, "organization.event_settings", "organization", organization.Name, eventSettings)
	}
	audit.Record(r.Context(), h.store, organization.OrgID, "organization.update", "organization", organization.Name, details)
	h.respondWithJSON(w, http.StatusOK, h.response(r, organization))
}

func (h *OrganizationHandler) SetDefault(w http.ResponseWriter, r *http.Request) {
	organization, err := h.store.GetOrganizationByName(r.Context(), h.getID(r, "organization_name"))
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if !h.require(r, tokencaps.OrganizationsManage, "") {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	if err := h.store.SetDefaultOrganization(r.Context(), organization.OrgID); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	audit.Record(r.Context(), h.store, organization.OrgID, "organization.set_default", "organization", organization.Name, models.JSONB{})
	h.respondWithJSON(w, http.StatusOK, h.response(r, organization))
}

func (h *OrganizationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	organization, err := h.store.GetOrganizationByName(r.Context(), h.getID(r, "organization_name"))
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}

	var req organizationDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Replacement == "" || !req.Confirm {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	replacement, err := h.store.GetOrganizationByName(r.Context(), req.Replacement)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if !h.require(r, tokencaps.OrganizationsManage, organization.OrgID) ||
		!h.require(r, tokencaps.OrganizationsManage, replacement.OrgID) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	if err := h.store.DeleteOrganization(r.Context(), organization.OrgID, replacement.OrgID); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	audit.Record(r.Context(), h.store, replacement.OrgID, "organization.delete", "organization", organization.Name,
		models.JSONB{"replacement": replacement.Name})
	w.WriteHeader(http.StatusNoContent)
}

func (h *OrganizationHandler) response(r *http.Request, organization *models.Organization) organizationResponse {
	response := organizationResponse{Name: organization.Name, DisplayName: organization.DisplayName, IsPrivate: organization.IsPrivate, Status: organization.Status, InfraRetries: organization.InfraRetries, EgressControl: organization.EgressControl,
		DuplicateEvents: organization.DuplicateEvents, DynamicJobs: organization.DynamicJobs, DraftPRs: organization.DraftPRs,
		DefaultsProfile: organization.DefaultsProfile, ShedLevel: organization.ShedLevel}
	if current, err := h.store.GetDefaultOrganization(r.Context()); err == nil {
		response.IsDefault = current.OrgID == organization.OrgID
	}
	return response
}

// applyOrganizationEventSettings validates the event settings of an update
// request and sets them on organization. It returns the audit details of what
// changed (nil when nothing did). draft_prs is validated here and written by
// the caller after the update.
func applyOrganizationEventSettings(organization *models.Organization, req organizationRequest) (models.JSONB, error) {
	changed := models.JSONB{}
	if req.DuplicateEvents != nil {
		if *req.DuplicateEvents != models.DuplicateEventsRun && *req.DuplicateEvents != models.DuplicateEventsSkip {
			return nil, errors.New("duplicate_events must be run or skip")
		}
		if organization.DuplicateEvents != *req.DuplicateEvents {
			changed["duplicate_events"] = models.JSONB{"from": organization.DuplicateEvents, "to": *req.DuplicateEvents}
		}
		organization.DuplicateEvents = *req.DuplicateEvents
	}
	if req.DynamicJobs != nil {
		if !models.IsValidDynamicJobsOrgSetting(*req.DynamicJobs) {
			return nil, errors.New("dynamic_jobs must be allow or deny")
		}
		if organization.DynamicJobs != *req.DynamicJobs {
			changed["dynamic_jobs"] = models.JSONB{"from": organization.DynamicJobs, "to": *req.DynamicJobs}
		}
		organization.DynamicJobs = *req.DynamicJobs
	}
	if req.DraftPRs != nil {
		if !models.IsValidDraftPRsOrgSetting(*req.DraftPRs) {
			return nil, errors.New("draft_prs must be run, skip or skip_when_shedding")
		}
		if organization.DraftPRs != *req.DraftPRs {
			changed["draft_prs"] = models.JSONB{"from": organization.DraftPRs, "to": *req.DraftPRs}
		}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	return changed, nil
}
