package handlers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/backlog"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resourcehistory"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// BacklogHandler serves GET /api/v1/backlog: the waiting work of each queue,
// for autoscalers and operators (docs/workers.md "Backlog and autoscaling").
type BacklogHandler struct {
	BaseHandler
	appStore store.Store
	source   backlog.Source
}

// NewBacklogHandler returns a BacklogHandler. A store without backlog
// support answers 503.
func NewBacklogHandler(appStore store.Store) *BacklogHandler {
	return &BacklogHandler{appStore: appStore, source: backlog.SourceFrom(appStore)}
}

type backlogOrgStore interface {
	GetOrganizationByName(context.Context, string) (*models.Organization, error)
}

// ServeHTTP returns a backlog snapshot.
//
// Without ?org, the token needs workers:read or workers:manage for all
// organizations, and the result has every queue. With ?org=<name>, the token
// needs one of those capabilities for that organization, and the result has
// only the queues of that organization.
func (h *BacklogHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.source == nil {
		h.respondWithError(w, http.StatusServiceUnavailable, store.ErrServiceUnavailable)
		return
	}
	if checkauth.GetPrincipalFromContext(r.Context()) == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	var filter backlog.Filter
	if orgName := r.URL.Query().Get("org"); orgName != "" {
		orgs, ok := h.appStore.(backlogOrgStore)
		if !ok {
			h.respondWithError(w, http.StatusServiceUnavailable, store.ErrServiceUnavailable)
			return
		}
		org, err := orgs.GetOrganizationByName(r.Context(), orgName)
		if err != nil {
			h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
			return
		}
		if !principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.WorkersRead) &&
			!principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.WorkersManage) {
			h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		orgID := org.OrgID
		filter = func(queue models.Queue) bool { return queue.OrgID == orgID }
	} else if !principalAllowsGlobal(r.Context(), h.appStore, tokencaps.WorkersRead) &&
		!principalAllowsGlobal(r.Context(), h.appStore, tokencaps.WorkersManage) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	snapshot, err := backlog.Load(r.Context(), h.source, filter, time.Now())
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, snapshot)
}

// resourceSuggestionResponse is the body of
// GET /api/v1/jobs/{id}/resource-suggestion.
type resourceSuggestionResponse struct {
	JobID     string  `json:"job_id"`
	ProjectID *string `json:"project_id"`
	Name      string  `json:"name"`
	resourcehistory.Suggestion
}

// GetJobResourceSuggestion returns a p95-based right-sizing suggestion for
// the job of job_id: every finished run with the same organization, project,
// and name. ?limit sets how many of the newest runs to read (default 50,
// 5 through 1000). Access is the same as for the job itself (jobs:read).
// Only stored summaries are used; a run with no summary yet is not counted.
func (h *JobHandler) GetJobResourceSuggestion(w http.ResponseWriter, r *http.Request) {
	jobID := h.getID(r, "job_id")
	if jobID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	job, err := h.store.GetJobByID(r.Context(), jobID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil && checkauth.GetPrincipalFromContext(r.Context()) == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}
	if !h.canSubjectAccessJob(r.Context(), user, job, tokencaps.JobsRead) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	summaries, ok := h.store.(store.ResourceSummaryStore)
	if !ok {
		h.respondWithError(w, http.StatusServiceUnavailable, store.ErrServiceUnavailable)
		return
	}
	limit := resourcehistory.SuggestionDefaultRuns
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < resourcehistory.SuggestionMinRuns || parsed > 1000 {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		limit = parsed
	}
	runs, err := summaries.ListJobRunsWithSummaries(r.Context(), store.JobRunHistoryFilter{
		OrgID: job.OrgID, ProjectID: job.ProjectID, Name: job.Name, Limit: limit,
	})
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, resourceSuggestionResponse{
		JobID: job.JobID, ProjectID: job.ProjectID, Name: job.Name, Suggestion: resourcehistory.Suggest(runs),
	})
}
