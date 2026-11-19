package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/lease"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/reporting"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/lib/pq"
)

// ReportingHandler serves report modes, the agreement report, and path
// acceptances (docs/reporting-and-ownership.md). The routes work with no
// logins (the admin token):
//
//	GET|PUT /api/v1/projects/{project}/reporting
//	GET     /api/v1/projects/{project}/agreement?since=7d&limit=500
//	GET     /api/v1/projects/{project}/path-acceptances?state=accepted
//	POST    /api/v1/projects/{project}/path-acceptances
//	POST    /api/v1/projects/{project}/path-acceptances/{id}/{accept|reject|revoke}
//	GET|PUT /api/v1/organizations/{org}/reporting-settings
//
// The authorization is in reporting.Service, the same for the UI.
type ReportingHandler struct {
	BaseHandler
	appStore store.Store
	projects *RequiredSetHandler
}

// NewReportingHandler returns the handler.
func NewReportingHandler(appStore store.Store) *ReportingHandler {
	return &ReportingHandler{appStore: appStore, projects: NewRequiredSetHandler(appStore)}
}

func (h *ReportingHandler) service() *reporting.Service {
	return &reporting.Service{Store: h.appStore, Record: func(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB) {
		audit.Record(ctx, h.appStore, orgID, action, subjectType, subjectID, details)
	}}
}

func (h *ReportingHandler) fail(w http.ResponseWriter, err error) {
	h.respondWithJSON(w, RequiredSetErrorStatus(err), ErrorResponse{Error: reporting.ErrorCode(err), Message: err.Error()})
}

func decodeReportingBody(r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}

// ReportingRoutes reports whether a project route belongs to this handler.
func ReportingRoutes(parts []string) bool {
	if len(parts) < 2 {
		return false
	}
	switch parts[1] {
	case "reporting", "agreement", "path-acceptances":
		return true
	}
	return false
}

// ParseSince reads a time filter: an RFC 3339 time, or a duration back from
// now ("72h", "7d"). Empty gives nil.
func ParseSince(value string, now time.Time) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return &parsed, nil
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		count, err := strconv.Atoi(days)
		if err != nil || count < 0 || count > 3650 {
			return nil, errors.New("since must be an RFC 3339 time or a duration")
		}
		since := now.Add(-time.Duration(count) * 24 * time.Hour)
		return &since, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		return nil, errors.New("since must be an RFC 3339 time or a duration")
	}
	since := now.Add(-duration)
	return &since, nil
}

// ServeProject serves the project routes. parts[0] is the project
// reference; parts[1] is reporting, agreement, or path-acceptances.
func (h *ReportingHandler) ServeProject(w http.ResponseWriter, r *http.Request, parts []string) {
	ctx := r.Context()
	project, err := h.projects.resolveProject(ctx, parts[0])
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	caller := RequiredSetCaller(ctx, h.appStore)
	service := h.service()
	rest := parts[2:]
	switch {
	case parts[1] == "reporting" && len(rest) == 0 && r.Method == http.MethodGet:
		view, err := service.GetSettings(ctx, caller, project)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, view)
	case parts[1] == "reporting" && len(rest) == 0 && (r.Method == http.MethodPut || r.Method == http.MethodPatch):
		var update reporting.ProjectSettingsUpdate
		if err := decodeReportingBody(r, &update); err != nil {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		view, err := service.UpdateProjectSettings(ctx, caller, project, update)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, view)
	case parts[1] == "agreement" && len(rest) == 0 && r.Method == http.MethodGet:
		since, err := ParseSince(r.URL.Query().Get("since"), time.Now().UTC())
		if err != nil {
			h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_argument", Message: err.Error()})
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		view, err := service.GetAgreement(ctx, caller, project, since, limit)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, view)
	case parts[1] == "path-acceptances" && len(rest) == 0 && r.Method == http.MethodGet:
		view, err := service.ListAcceptances(ctx, caller, project, r.URL.Query().Get("state"))
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, view)
	case parts[1] == "path-acceptances" && len(rest) == 0 && r.Method == http.MethodPost:
		var req reporting.AcceptanceRequest
		if err := decodeReportingBody(r, &req); err != nil {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		row, err := service.RequestAcceptance(ctx, caller, project, req)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusCreated, row)
	case parts[1] == "path-acceptances" && len(rest) == 2 && r.Method == http.MethodPost:
		var body struct {
			Reason string `json:"reason,omitempty"`
		}
		if r.ContentLength != 0 {
			if err := decodeReportingBody(r, &body); err != nil {
				h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
				return
			}
		}
		row, err := service.DecideAcceptance(ctx, caller, project, rest[0], rest[1], body.Reason)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, row)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

// OrgReportingSettingsBody is the body of GET and PUT
// .../organizations/{org}/reporting-settings.
type OrgReportingSettingsBody struct {
	ReportMode                string                 `json:"report_mode"`
	AgreementContexts         []string               `json:"agreement_contexts"`
	CodeownersImport          string                 `json:"codeowners_import"`
	CodeownersCacheTTLSeconds int                    `json:"codeowners_cache_ttl_seconds"`
	PathAcceptance            string                 `json:"path_acceptance"`
	PathOwners                []models.PathOwnerRule `json:"path_owners"`
	CanManage                 bool                   `json:"can_manage,omitempty"`
}

func orgSettingsBody(settings *models.OrgReportingSettings, canManage bool) OrgReportingSettingsBody {
	return OrgReportingSettingsBody{ReportMode: settings.ReportMode, AgreementContexts: append([]string{}, settings.AgreementContexts...),
		CodeownersImport: settings.CodeownersImport, CodeownersCacheTTLSeconds: settings.CodeownersCacheTTLSeconds,
		PathAcceptance: settings.PathAcceptance, PathOwners: append([]models.PathOwnerRule{}, settings.PathOwners...), CanManage: canManage}
}

// ServeOrganization serves GET and PUT of the organization values.
func (h *ReportingHandler) ServeOrganization(w http.ResponseWriter, r *http.Request, orgRef string) {
	ctx := r.Context()
	orgs, ok := h.appStore.(interface {
		GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
		GetOrganizationByID(ctx context.Context, orgID string) (*models.Organization, error)
	})
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("reporting settings are not available"))
		return
	}
	org, err := orgs.GetOrganizationByName(ctx, orgRef)
	if err != nil {
		if org, err = orgs.GetOrganizationByID(ctx, orgRef); err != nil {
			h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
	}
	caller := RequiredSetCaller(ctx, h.appStore)
	service := h.service()
	switch r.Method {
	case http.MethodGet:
		settings, canManage, err := service.GetOrgSettings(ctx, caller, org.OrgID)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, orgSettingsBody(settings, canManage))
	case http.MethodPut:
		var body OrgReportingSettingsBody
		if err := decodeReportingBody(r, &body); err != nil {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		settings, err := service.SetOrgSettings(ctx, caller, models.OrgReportingSettings{OrgID: org.OrgID, ReportMode: body.ReportMode,
			AgreementContexts: pq.StringArray(body.AgreementContexts), CodeownersImport: body.CodeownersImport,
			CodeownersCacheTTLSeconds: body.CodeownersCacheTTLSeconds, PathAcceptance: body.PathAcceptance, PathOwners: body.PathOwners})
		if err != nil {
			h.fail(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, orgSettingsBody(settings, true))
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

var agreementPassOnce sync.Once

// startAgreementPassOnce runs the agreement pass on the one replica that
// holds the agreement lease.
func startAgreementPassOnce(appStore store.Store, updater *vcs.JobStatusUpdater) {
	leases, ok := appStore.(store.LeaseStore)
	if _, reports := appStore.(store.ReportingStore); !ok || !reports || updater == nil {
		return
	}
	agreementPassOnce.Do(func() {
		pass := &vcs.AgreementPass{Updater: updater}
		go lease.NewManager(leases).Run(context.Background(), reporting.AgreementLeaseName, pass.Run)
	})
}
