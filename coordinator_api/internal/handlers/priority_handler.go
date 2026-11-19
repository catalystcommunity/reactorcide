package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/priority"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// priorityAdminStore is the store surface of the priority lane routes.
type priorityAdminStore interface {
	store.PriorityStore
	GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
	ListProjects(ctx context.Context, limit, offset int) ([]models.Project, error)
	GetJobByID(ctx context.Context, jobID string) (*models.Job, error)
}

// PriorityHandler serves priority lanes and explain (docs/priority.md). The
// routes work with no logins (the admin token):
//
//	GET            /api/v1/organizations/{org}/priority-lanes
//	GET|PUT|DELETE /api/v1/organizations/{org}/priority-lanes/{name}
//	POST           /api/v1/organizations/{org}/priority/explain
//	GET            /api/v1/projects/{project}/priority-lanes
//	GET|PUT|DELETE /api/v1/projects/{project}/priority-lanes/{name}
//	POST           /api/v1/projects/{project}/priority/explain
//
// Reading needs projects:read in the organization. An organization lane
// needs policies:manage and an org admin. A project lane needs
// policies:manage and a project owner. The group shift route is in
// GroupHandler.
type PriorityHandler struct {
	BaseHandler
	store    priorityAdminStore
	appStore store.Store
}

func NewPriorityHandler(appStore store.Store) *PriorityHandler {
	value, _ := appStore.(priorityAdminStore)
	return &PriorityHandler{store: value, appStore: appStore}
}

// PriorityLaneMatch is the match facts of a lane in a request.
type PriorityLaneMatch = priority.LaneMatch

// PriorityLaneRequest is the body of PUT .../priority-lanes/{name}. With no
// match and no position, a lane with the name of a built-in lane (or, for a
// project, of an organization lane) keeps the match and the position of that
// lane: only the base changes. Send "match": {} for a lane that matches
// every job.
type PriorityLaneRequest = priority.LaneInput

// PriorityLanesResponse lists the lanes of one scope, the inherited
// organization lanes (project listing), the built-in lanes, and the
// effective lanes in evaluation order.
type PriorityLanesResponse struct {
	Lanes     []models.PriorityLane `json:"lanes"`
	Inherited []models.PriorityLane `json:"inherited,omitempty"`
	Builtin   []priority.Lane       `json:"builtin"`
	Effective []priority.Lane       `json:"effective"`
	Bounds    PriorityBounds        `json:"bounds"`
}

// PriorityBounds are the limits of lane bases and group shifts.
type PriorityBounds struct {
	MinBase       int `json:"min_base"`
	MaxBase       int `json:"max_base"`
	MinGroupShift int `json:"min_group_shift"`
	MaxGroupShift int `json:"max_group_shift"`
	MaxRequested  int `json:"max_requested"`
	ShedPRShift   int `json:"shed_pr_shift"`
}

// DefaultPriorityBounds returns the bounds.
func DefaultPriorityBounds() PriorityBounds {
	return PriorityBounds{MinBase: priority.MinBase, MaxBase: priority.MaxBase, MinGroupShift: priority.MinGroupShift,
		MaxGroupShift: priority.MaxGroupShift, MaxRequested: priority.MaxRequested, ShedPRShift: priority.ShedPRShift}
}

// PriorityExplainRequest is the body of POST .../priority/explain. With
// JobID, the coordinator explains that stored job. Otherwise the facts come
// from the body; actor is a principal UUID whose groups apply.
type PriorityExplainRequest struct {
	JobID       string   `json:"job_id,omitempty"`
	RefClasses  []string `json:"ref_classes,omitempty"`
	EventType   string   `json:"event_type,omitempty"`
	EventSource string   `json:"event_source,omitempty"`
	Draft       bool     `json:"draft,omitempty"`
	JobKind     string   `json:"job_kind,omitempty"`
	Retry       bool     `json:"retry,omitempty"`
	Actor       string   `json:"actor,omitempty"`
	Requested   int      `json:"requested,omitempty"`
}

// ServeOrganization routes /api/v1/organizations/{org}/priority-lanes[/...]
// and /api/v1/organizations/{org}/priority/explain. rest is the path after
// "{org}/".
func (h *PriorityHandler) ServeOrganization(w http.ResponseWriter, r *http.Request, orgName, rest string) {
	if h.store == nil {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("priority lanes are not available"))
		return
	}
	org, err := h.store.GetOrganizationByName(r.Context(), orgName)
	if err != nil {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	h.serve(w, r, org.OrgID, nil, rest)
}

// ServeProject routes /api/v1/projects/{project}/priority-lanes[/...] and
// /api/v1/projects/{project}/priority/explain. projectRef is a project ID,
// name, or repository URL.
func (h *PriorityHandler) ServeProject(w http.ResponseWriter, r *http.Request, projectRef, rest string) {
	if h.store == nil {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("priority lanes are not available"))
		return
	}
	project, err := h.resolveProject(r.Context(), projectRef)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	h.serve(w, r, project.OwnershipOrgID(), project, rest)
}

func (h *PriorityHandler) resolveProject(ctx context.Context, ref string) (*models.Project, error) {
	if project, err := h.store.GetProjectByID(ctx, ref); err == nil {
		return project, nil
	}
	projects, err := h.store.ListProjects(ctx, 10000, 0)
	if err != nil {
		return nil, err
	}
	for i := range projects {
		if projects[i].Name == ref || projects[i].RepoURL == ref {
			return &projects[i], nil
		}
	}
	return nil, store.ErrNotFound
}

func (h *PriorityHandler) serve(w http.ResponseWriter, r *http.Request, orgID string, project *models.Project, rest string) {
	var projectID *string
	if project != nil {
		projectID = &project.ProjectID
	}
	parts := []string{}
	for _, part := range strings.Split(strings.Trim(rest, "/"), "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if !principalAllowsOrg(r.Context(), h.appStore, orgID, tokencaps.ProjectsRead) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	switch {
	case len(parts) == 2 && parts[0] == "priority" && parts[1] == "explain" && r.Method == http.MethodPost:
		h.explain(w, r, orgID, projectID)
	case len(parts) == 1 && parts[0] == "priority-lanes" && r.Method == http.MethodGet:
		h.list(w, r, orgID, projectID)
	case len(parts) == 2 && parts[0] == "priority-lanes" && r.Method == http.MethodGet:
		lane, err := h.store.GetPriorityLane(r.Context(), orgID, projectID, parts[1])
		if err != nil {
			h.respondWithError(w, refClassErrorStatus(err), err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, lane)
	case len(parts) == 2 && parts[0] == "priority-lanes" && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		if !h.canManage(r.Context(), orgID, project) {
			h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		if r.Method == http.MethodPut {
			h.set(w, r, orgID, projectID, parts[1])
		} else {
			h.delete(w, r, orgID, projectID, parts[1])
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// canManage: an org lane needs an org admin, a project lane a project owner.
func (h *PriorityHandler) canManage(ctx context.Context, orgID string, project *models.Project) bool {
	if project == nil {
		return principalAllowsOrg(ctx, h.appStore, orgID, tokencaps.PoliciesManage)
	}
	return principalAllowsProjectOwner(ctx, h.appStore, project, tokencaps.PoliciesManage)
}

// ListPriorityLanesForScope builds the listing of one scope. The UI uses it
// too.
func ListPriorityLanesForScope(ctx context.Context, st store.PriorityStore, orgID string, projectID *string) (*PriorityLanesResponse, error) {
	lanes, err := st.ListPriorityLanes(ctx, orgID, projectID)
	if err != nil {
		return nil, err
	}
	response := &PriorityLanesResponse{Lanes: emptyLanes(lanes), Builtin: priority.Builtins(), Bounds: DefaultPriorityBounds()}
	if projectID != nil {
		inherited, err := st.ListPriorityLanes(ctx, orgID, nil)
		if err != nil {
			return nil, err
		}
		response.Inherited = emptyLanes(inherited)
		response.Effective = priority.Effective(inherited, lanes)
	} else {
		response.Effective = priority.Effective(lanes, nil)
	}
	return response, nil
}

func (h *PriorityHandler) list(w http.ResponseWriter, r *http.Request, orgID string, projectID *string) {
	response, err := ListPriorityLanesForScope(r.Context(), h.store, orgID, projectID)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, response)
}

func emptyLanes(lanes []models.PriorityLane) []models.PriorityLane {
	if lanes == nil {
		return []models.PriorityLane{}
	}
	return lanes
}

func (h *PriorityHandler) set(w http.ResponseWriter, r *http.Request, orgID string, projectID *string, name string) {
	var req PriorityLaneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	lane, err := priority.BuildLane(r.Context(), h.store, orgID, projectID, name, req)
	if err != nil {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: err.Error()})
		return
	}
	if err := h.store.UpsertPriorityLane(r.Context(), &lane); err != nil {
		h.respondWithError(w, refClassErrorStatus(err), err)
		return
	}
	audit.Record(r.Context(), h.appStore, orgID, "priority_lane.set", "priority_lane", lane.LaneID, models.JSONB{
		"name": lane.Name, "project_id": projectID, "base": lane.Base, "position": lane.Position, "enabled": lane.Enabled,
	})
	h.respondWithJSON(w, http.StatusOK, lane)
}

func (h *PriorityHandler) delete(w http.ResponseWriter, r *http.Request, orgID string, projectID *string, name string) {
	lane, err := h.store.GetPriorityLane(r.Context(), orgID, projectID, name)
	if err != nil {
		h.respondWithError(w, refClassErrorStatus(err), err)
		return
	}
	if err := h.store.DeletePriorityLane(r.Context(), orgID, projectID, name); err != nil {
		h.respondWithError(w, refClassErrorStatus(err), err)
		return
	}
	audit.Record(r.Context(), h.appStore, orgID, "priority_lane.delete", "priority_lane", lane.LaneID, models.JSONB{
		"name": lane.Name, "project_id": projectID,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *PriorityHandler) explain(w http.ResponseWriter, r *http.Request, orgID string, projectID *string) {
	var req PriorityExplainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	response, err := ExplainPriority(r.Context(), h.appStore, orgID, projectID, req)
	if err != nil {
		h.respondWithError(w, refClassErrorStatus(err), err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, response)
}

// ExplainPriority computes the explain output for a request. A job ID must
// name a job of the organization (and of the project, when given).
func ExplainPriority(ctx context.Context, st any, orgID string, projectID *string, req PriorityExplainRequest) (*priority.Explanation, error) {
	if strings.TrimSpace(req.JobID) != "" {
		jobs, ok := st.(interface {
			GetJobByID(ctx context.Context, jobID string) (*models.Job, error)
		})
		if !ok {
			return nil, store.ErrNotFound
		}
		job, err := jobs.GetJobByID(ctx, strings.TrimSpace(req.JobID))
		if err != nil || job.OwnershipOrgID() != orgID {
			return nil, store.ErrNotFound
		}
		if projectID != nil && (job.ProjectID == nil || *job.ProjectID != *projectID) {
			return nil, store.ErrNotFound
		}
		explanation := priority.ExplainJob(ctx, st, job)
		return &explanation, nil
	}
	switch req.JobKind {
	case "", models.PriorityJobKindEval, models.PriorityJobKindWork:
	default:
		return nil, fmt.Errorf("%w: job_kind must be eval or work", store.ErrInvalidInput)
	}
	facts := priority.Facts{OrgID: orgID, ProjectID: projectID, RefClasses: req.RefClasses, EventType: strings.TrimSpace(req.EventType),
		EventSource: strings.TrimSpace(req.EventSource), Draft: req.Draft, JobKind: req.JobKind, Retry: req.Retry,
		ActorPrincipalID: strings.TrimSpace(req.Actor), Requested: req.Requested}
	explanation := priority.ExplainFacts(ctx, st, facts)
	return &explanation, nil
}
