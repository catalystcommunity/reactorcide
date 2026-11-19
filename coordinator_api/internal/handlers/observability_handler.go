package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/observability"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// observabilityAdminStore is the store surface of the observability routes.
type observabilityAdminStore interface {
	store.ObservabilityStore
	GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
	ListProjectsByOrg(ctx context.Context, orgID string, limit, offset int) ([]models.Project, error)
}

// ObservabilityHandler serves rollups, dashboards, alert rules, and SLOs
// (docs/observability.md). The routes work with no logins (the admin
// token):
//
//	GET                /api/v1/organizations/{org}/observability/{rollups,pipeline,infrastructure,usage,status}
//	GET|POST           /api/v1/organizations/{org}/alert-rules
//	GET|PATCH|DELETE   /api/v1/organizations/{org}/alert-rules/{name}
//	GET                /api/v1/organizations/{org}/slos
//	GET                /api/v1/organizations/{org}/slos/status
//	PUT|DELETE         /api/v1/organizations/{org}/slos/{sli}[?lane=]
//
// Reading needs projects:read in the organization. A project or workflow
// alert rule needs projects:manage and a project owner; an owner_group rule
// with no project and an SLO target need organizations:manage and an org
// admin.
type ObservabilityHandler struct {
	BaseHandler
	store    observabilityAdminStore
	appStore store.Store
	now      func() time.Time
}

func NewObservabilityHandler(appStore store.Store) *ObservabilityHandler {
	value, _ := appStore.(observabilityAdminStore)
	return &ObservabilityHandler{store: value, appStore: appStore, now: func() time.Time { return time.Now().UTC() }}
}

// IsObservabilityPath reports whether rest (the path after "{org}/") is an
// observability route.
func IsObservabilityPath(rest string) bool {
	first := strings.SplitN(strings.Trim(rest, "/"), "/", 2)[0]
	return first == "observability" || first == "alert-rules" || first == "slos"
}

func (h *ObservabilityHandler) ServeOrganization(w http.ResponseWriter, r *http.Request, orgName, rest string) {
	if h.store == nil {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("observability is not available"))
		return
	}
	org, err := h.store.GetOrganizationByName(r.Context(), orgName)
	if err != nil {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	if !principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.ProjectsRead) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	parts := []string{}
	for _, part := range strings.Split(strings.Trim(rest, "/"), "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	switch {
	case len(parts) == 2 && parts[0] == "observability" && r.Method == http.MethodGet:
		h.dashboard(w, r, org.OrgID, parts[1])
	case len(parts) == 1 && parts[0] == "alert-rules" && r.Method == http.MethodGet:
		rules, err := h.store.ListAlertRules(r.Context(), org.OrgID)
		if err != nil {
			h.observabilityError(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, map[string]any{"rules": rules})
	case len(parts) == 1 && parts[0] == "alert-rules" && r.Method == http.MethodPost:
		h.createRule(w, r, org.OrgID)
	case len(parts) == 2 && parts[0] == "alert-rules":
		h.rule(w, r, org.OrgID, parts[1])
	case len(parts) == 1 && parts[0] == "slos" && r.Method == http.MethodGet:
		stored, err := h.store.ListSLOTargets(r.Context(), org.OrgID)
		if err != nil {
			h.observabilityError(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, map[string]any{"targets": observability.EffectiveTargets(org.OrgID, stored)})
	case len(parts) == 2 && parts[0] == "slos" && parts[1] == "status" && r.Method == http.MethodGet:
		statuses, err := observability.SLOStatuses(r.Context(), h.store, store.RollupFilter{OrgID: org.OrgID, AllProjects: true}, h.now())
		if err != nil {
			h.observabilityError(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, map[string]any{"slos": statuses})
	case len(parts) == 2 && parts[0] == "slos" && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		if !principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.OrganizationsManage) {
			h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		h.setSLO(w, r, org.OrgID, parts[1])
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// resolveProject finds a project of the organization by ID or name.
func (h *ObservabilityHandler) resolveProject(ctx context.Context, orgID, ref string) (*models.Project, error) {
	if project, err := h.store.GetProjectByID(ctx, ref); err == nil && project.OwnershipOrgID() == orgID {
		return project, nil
	}
	projects, err := h.store.ListProjectsByOrg(ctx, orgID, 10000, 0)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		if projects[i].Name == ref || projects[i].RepoURL == ref {
			return &projects[i], nil
		}
	}
	return nil, fmt.Errorf("%w: project %q", store.ErrNotFound, ref)
}

func (h *ObservabilityHandler) scope(r *http.Request, orgID string) (store.RollupFilter, error) {
	f := store.RollupFilter{OrgID: orgID, AllProjects: true}
	q := r.URL.Query()
	if ref := q.Get("project"); ref != "" {
		project, err := h.resolveProject(r.Context(), orgID, ref)
		if err != nil {
			return f, err
		}
		f.AllProjects, f.ProjectIDs = false, []string{project.ProjectID}
	}
	f.WorkflowSecurityID = q.Get("workflow")
	f.OwnerGroup = q.Get("owner_group")
	if q.Has("lane") {
		lane := q.Get("lane")
		f.Lane = &lane
	}
	f.RefClassBucket = q.Get("ref_class")
	return f, nil
}

func (h *ObservabilityHandler) dashboard(w http.ResponseWriter, r *http.Request, orgID, view string) {
	ctx, now := r.Context(), h.now()
	scope, err := h.scope(r, orgID)
	if err != nil {
		h.observabilityError(w, err)
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	var out any
	switch view {
	case "rollups":
		out, err = h.rollups(r, scope, days, now)
	case "pipeline":
		out, err = observability.BuildPipelineDashboard(ctx, h.store, scope, days, now)
	case "infrastructure":
		var d *observability.InfraDashboard
		if d, err = observability.BuildInfraDashboard(ctx, h.store, scope, days, now); err == nil {
			out = struct {
				*observability.InfraDashboard
				Capacity observability.Capacity `json:"capacity"`
			}{d, observability.LoadCapacity(ctx, h.appStore, orgID, now)}
		}
	case "usage":
		out, err = observability.BuildUsageDashboard(ctx, h.store, scope, days, r.URL.Query().Get("attribution"), now)
		if errors.Is(err, observability.ErrActorAttributionScope) {
			h.badRequest(w, err)
			return
		}
	case "status":
		states := []*models.ObservabilityState{}
		for _, name := range []string{observability.StateJobs, observability.StateSummaries, observability.StateEvents} {
			st, err := h.store.GetObservabilityState(ctx, name)
			if err != nil {
				h.respondWithError(w, http.StatusInternalServerError, err)
				return
			}
			if st != nil {
				states = append(states, st)
			}
		}
		out = map[string]any{"states": states}
	default:
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.observabilityError(w, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, out)
}

func (h *ObservabilityHandler) rollups(r *http.Request, scope store.RollupFilter, days int, now time.Time) (any, error) {
	q := r.URL.Query()
	granularity := q.Get("granularity")
	if granularity == "" {
		granularity = string(observability.Daily)
	}
	if granularity != string(observability.Hourly) && granularity != string(observability.Daily) {
		return nil, fmt.Errorf("%w: granularity must be hour or day", store.ErrInvalidInput)
	}
	if days <= 0 {
		days = 7
	}
	if days > 400 {
		return nil, fmt.Errorf("%w: days must be at most 400", store.ErrInvalidInput)
	}
	scope.From, scope.To = observability.DayRange(now, days)
	for key, target := range map[string]*time.Time{"from": &scope.From, "to": &scope.To} {
		if v := q.Get(key); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return nil, fmt.Errorf("%w: %s must be RFC 3339", store.ErrInvalidInput, key)
			}
			*target = t.UTC()
		}
	}
	rows, err := h.store.ListJobRollups(r.Context(), granularity, scope)
	if err != nil {
		return nil, err
	}
	return map[string]any{"granularity": granularity, "from": scope.From, "to": scope.To,
		"bounds_seconds": observability.Bounds, "rows": rows, "totals": observability.Summarize(observability.AggregateRows(rows))}, nil
}

// AlertRuleRequest is the body of POST and PATCH .../alert-rules. In a
// PATCH, an absent field keeps its value; scope fields cannot change.
type AlertRuleRequest struct {
	Name               string   `json:"name"`
	Description        *string  `json:"description,omitempty"`
	Scope              string   `json:"scope"`
	Project            string   `json:"project,omitempty"`
	WorkflowSecurityID string   `json:"workflow_security_id,omitempty"`
	OwnerGroup         string   `json:"owner_group,omitempty"`
	Condition          string   `json:"condition"`
	RefClassBucket     *string  `json:"ref_class_bucket,omitempty"`
	Threshold          *float64 `json:"threshold,omitempty"`
	Factor             *float64 `json:"factor,omitempty"`
	WindowHours        *int     `json:"window_hours,omitempty"`
	BaselineDays       *int     `json:"baseline_days,omitempty"`
	MinRuns            *int     `json:"min_runs,omitempty"`
	ResolveMargin      *float64 `json:"resolve_margin,omitempty"`
	FireAfter          *int     `json:"fire_after,omitempty"`
	ResolveAfter       *int     `json:"resolve_after,omitempty"`
	Enabled            *bool    `json:"enabled,omitempty"`
}

func (req *AlertRuleRequest) apply(rule *models.AlertRule) {
	if req.Description != nil {
		rule.Description = *req.Description
	}
	if req.Condition != "" {
		rule.Condition = req.Condition
	}
	if req.RefClassBucket != nil {
		rule.RefClassBucket = *req.RefClassBucket
	}
	setF := func(dst *float64, src *float64) {
		if src != nil {
			*dst = *src
		}
	}
	setI := func(dst *int, src *int) {
		if src != nil {
			*dst = *src
		}
	}
	setF(&rule.Threshold, req.Threshold)
	setF(&rule.Factor, req.Factor)
	setF(&rule.ResolveMargin, req.ResolveMargin)
	setI(&rule.WindowHours, req.WindowHours)
	setI(&rule.BaselineDays, req.BaselineDays)
	setI(&rule.MinRuns, req.MinRuns)
	setI(&rule.FireAfter, req.FireAfter)
	setI(&rule.ResolveAfter, req.ResolveAfter)
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
}

// canManageRule: a rule with a project needs a project owner; an
// owner_group rule with no project needs an org admin.
func (h *ObservabilityHandler) canManageRule(ctx context.Context, orgID string, rule *models.AlertRule) bool {
	if rule.ProjectID == nil {
		return principalAllowsOrg(ctx, h.appStore, orgID, tokencaps.OrganizationsManage)
	}
	project, err := h.store.GetProjectByID(ctx, *rule.ProjectID)
	if err != nil {
		return false
	}
	return principalAllowsProjectOwner(ctx, h.appStore, project, tokencaps.ProjectsManage)
}

func observabilityCallerID(ctx context.Context) *string {
	if p := checkauth.GetPrincipalFromContext(ctx); p != nil && p.PrincipalID != "" {
		id := p.PrincipalID
		return &id
	}
	return nil
}

func (h *ObservabilityHandler) createRule(w http.ResponseWriter, r *http.Request, orgID string) {
	var req AlertRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.badRequest(w, fmt.Errorf("invalid body: %w", err))
		return
	}
	rule := &models.AlertRule{OrgID: orgID, Name: req.Name, Scope: req.Scope, WorkflowSecurityID: req.WorkflowSecurityID,
		OwnerGroup: req.OwnerGroup, Enabled: true, RefClassBucket: models.RefClassBucketDefault, ResolveMargin: 0.05,
		CreatedBy: observabilityCallerID(r.Context())}
	if req.Project != "" {
		project, err := h.resolveProject(r.Context(), orgID, req.Project)
		if err != nil {
			h.observabilityError(w, err)
			return
		}
		rule.ProjectID = &project.ProjectID
	}
	req.apply(rule)
	observability.ApplyAlertDefaults(rule)
	if err := observability.ValidateAlertRule(rule); err != nil {
		h.badRequest(w, err)
		return
	}
	if !h.canManageRule(r.Context(), orgID, rule) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	if err := h.store.CreateAlertRule(r.Context(), rule); err != nil {
		h.observabilityError(w, err)
		return
	}
	created, err := h.store.GetAlertRule(r.Context(), orgID, rule.Name)
	if err != nil {
		h.observabilityError(w, err)
		return
	}
	audit.Record(r.Context(), h.appStore, orgID, "alert_rule.create", "alert_rule", created.RuleID, models.JSONB{
		"name": created.Name, "scope": created.Scope, "condition": created.Condition})
	h.respondWithJSON(w, http.StatusCreated, created)
}

func (h *ObservabilityHandler) rule(w http.ResponseWriter, r *http.Request, orgID, name string) {
	rule, err := h.store.GetAlertRule(r.Context(), orgID, name)
	if err != nil {
		h.observabilityError(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		transitions, err := h.store.ListAlertTransitions(r.Context(), rule.RuleID, 50)
		if err != nil {
			h.observabilityError(w, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, map[string]any{"rule": rule, "transitions": transitions})
	case http.MethodPatch:
		if !h.canManageRule(r.Context(), orgID, rule) {
			h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		var req AlertRuleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			h.badRequest(w, fmt.Errorf("invalid body: %w", err))
			return
		}
		if (req.Scope != "" && req.Scope != rule.Scope) || req.Project != "" || req.WorkflowSecurityID != "" || req.OwnerGroup != "" {
			h.badRequest(w, errors.New("the scope of a rule cannot change; delete it and create a new rule"))
			return
		}
		before := *rule
		req.apply(rule)
		if err := observability.ValidateAlertRule(rule); err != nil {
			h.badRequest(w, err)
			return
		}
		reset := before.Condition != rule.Condition || before.RefClassBucket != rule.RefClassBucket || (before.Enabled && !rule.Enabled)
		if err := h.store.UpdateAlertRuleConfig(r.Context(), rule, reset); err != nil {
			h.observabilityError(w, err)
			return
		}
		updated, _ := h.store.GetAlertRule(r.Context(), orgID, rule.RuleID)
		audit.Record(r.Context(), h.appStore, orgID, "alert_rule.update", "alert_rule", rule.RuleID, models.JSONB{"name": rule.Name})
		h.respondWithJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		if !h.canManageRule(r.Context(), orgID, rule) {
			h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		if err := h.store.DeleteAlertRule(r.Context(), orgID, rule.RuleID); err != nil {
			h.observabilityError(w, err)
			return
		}
		audit.Record(r.Context(), h.appStore, orgID, "alert_rule.delete", "alert_rule", rule.RuleID, models.JSONB{"name": rule.Name})
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// SLOTargetRequest is the body of PUT .../slos/{sli}.
type SLOTargetRequest struct {
	Objective        float64 `json:"objective"`
	ThresholdSeconds float64 `json:"threshold_seconds,omitempty"`
	WindowDays       int     `json:"window_days,omitempty"`
}

func (h *ObservabilityHandler) setSLO(w http.ResponseWriter, r *http.Request, orgID, sli string) {
	lane := r.URL.Query().Get("lane")
	if r.Method == http.MethodDelete {
		if err := h.store.DeleteSLOTarget(r.Context(), orgID, sli, lane); err != nil {
			h.observabilityError(w, err)
			return
		}
		audit.Record(r.Context(), h.appStore, orgID, "slo_target.delete", "slo_target", sli+":"+lane, models.JSONB{"sli": sli, "lane": lane})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var req SLOTargetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.badRequest(w, fmt.Errorf("invalid body: %w", err))
		return
	}
	target := &models.SLOTarget{OrgID: orgID, SLI: sli, Lane: lane, Objective: req.Objective, ThresholdSeconds: req.ThresholdSeconds,
		WindowDays: req.WindowDays, UpdatedBy: observabilityCallerID(r.Context())}
	if target.ThresholdSeconds == 0 && (sli == models.SLIEvalLatency || sli == models.SLIQueueWait) {
		target.ThresholdSeconds = observability.DefaultTargets[sli].ThresholdSeconds
	}
	if err := observability.ValidateSLOTarget(target); err != nil {
		h.badRequest(w, err)
		return
	}
	if err := h.store.UpsertSLOTarget(r.Context(), target); err != nil {
		h.observabilityError(w, err)
		return
	}
	audit.Record(r.Context(), h.appStore, orgID, "slo_target.set", "slo_target", sli+":"+lane, models.JSONB{
		"sli": sli, "lane": lane, "objective": target.Objective, "threshold_seconds": target.ThresholdSeconds, "window_days": target.WindowDays})
	h.respondWithJSON(w, http.StatusOK, target)
}

// badRequest writes a 400 answer with the reason: the operator needs it to
// correct a rule or a target.
func (h *ObservabilityHandler) badRequest(w http.ResponseWriter, err error) {
	h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: err.Error()})
}

// observabilityError writes a store error; an invalid input keeps its reason.
func (h *ObservabilityHandler) observabilityError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrInvalidInput) {
		h.badRequest(w, err)
		return
	}
	h.respondWithError(w, refClassErrorStatus(err), err)
}
