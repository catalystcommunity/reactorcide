package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobcontrol"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// workflowInstanceGetter is the narrow store capability CancelWorkflow needs
// to load a workflow instance for the pre-mutation authz check. Same
// consumer-defined-narrow-interface pattern as workflowSummaryStore above.
type workflowInstanceGetter interface {
	GetWorkflowInstance(ctx context.Context, workflowID string) (*models.WorkflowInstance, error)
}

type workflowSummaryStore interface {
	ListWorkflowSummaries(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.WorkflowSummary, error)
	GetWorkflowSummary(ctx context.Context, workflowID string) (*models.WorkflowSummary, error)
}

// workflowSummaryVisibleToStore is workflowSummaryStore's SQL-side-visibility
// counterpart to jobsVisibleToStore (see job_handler.go's doc comment on
// that type for the full "pagination before visibility filtering breaks
// lists" rationale). See postgres_store/visibility_operations.go's
// ListWorkflowSummariesVisibleTo.
type workflowSummaryVisibleToStore interface {
	ListWorkflowSummariesVisibleTo(ctx context.Context, viewerID string, isGlobalAdmin bool, filters map[string]interface{}, limit, offset int) ([]models.WorkflowSummary, int64, error)
}

type WorkflowHandler struct {
	BaseHandler
	store          store.Store
	corndogsClient corndogs.ClientInterface
	// visibility is the same additive, read-only authz hook as
	// JobHandler.visibility (see job_handler.go) — nil unless store
	// satisfies authz.RoleStore. CancelWorkflow (a mutation) never consults
	// it; only ListWorkflows/GetWorkflow do.
	visibility *authz.Resolver
}

type ListWorkflowsResponse struct {
	Workflows []models.WorkflowSummary `json:"workflows"`
	Total     int                      `json:"total"`
	Limit     int                      `json:"limit"`
	Offset    int                      `json:"offset"`
}

// NewWorkflowHandler creates a workflow handler without a Corndogs client
// wired in. CancelWorkflow still works in this configuration — non-terminal
// jobs are still cancelled in the DB — but the corresponding Corndogs tasks
// won't be told to stop tracking not-yet-started jobs. Prefer
// NewWorkflowHandlerWithCorndogs where a client is available.
func NewWorkflowHandler(store store.Store) *WorkflowHandler {
	return &WorkflowHandler{store: store, visibility: roleStoreResolver(store, "WorkflowHandler")}
}

// NewWorkflowHandlerWithCorndogs creates a workflow handler with a Corndogs
// client wired in, so CancelWorkflow can also cancel the Corndogs tasks of
// jobs that were submitted but never started.
func NewWorkflowHandlerWithCorndogs(store store.Store, corndogsClient corndogs.ClientInterface) *WorkflowHandler {
	return &WorkflowHandler{store: store, corndogsClient: corndogsClient, visibility: roleStoreResolver(store, "WorkflowHandler")}
}

func (h *WorkflowHandler) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	limit, offset := h.parsePagination(r)

	// Primary path: SQL-side visibility filtering with exact pagination and
	// Total — see workflowSummaryVisibleToStore and JobHandler.ListJobs'
	// jobsVisibleToStore doc comment for the full rationale.
	if wvs, ok := h.store.(workflowSummaryVisibleToStore); ok && h.visibility != nil {
		id := authz.IdentityFromUser(user)
		isGlobalAdmin, err := h.visibility.IsGlobalAdmin(r.Context(), id)
		if err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}

		filters := h.parseWorkflowFilters(r, user)
		summaries, total, err := wvs.ListWorkflowSummariesVisibleTo(r.Context(), user.UserID, isGlobalAdmin, filters, limit, offset)
		if err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		h.respondWithJSON(w, http.StatusOK, ListWorkflowsResponse{
			Workflows: summaries,
			Total:     int(total),
			Limit:     limit,
			Offset:    offset,
		})
		return
	}

	// Fallback: strict pre-authz own-workflows-only scoping, no post-query
	// filter — see JobHandler.ListJobs' fallback branch for why this must
	// never be paired with a relaxed filter.
	filters := h.parseWorkflowFiltersStrict(r, user)

	var summaries []models.WorkflowSummary
	if ws, ok := h.store.(workflowSummaryStore); ok {
		rows, err := ws.ListWorkflowSummaries(r.Context(), filters, limit, offset)
		if err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		summaries = rows
	} else {
		rows, err := h.looseJobSummaries(r.Context(), filters, limit, offset)
		if err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		summaries = rows
	}

	h.respondWithJSON(w, http.StatusOK, ListWorkflowsResponse{
		Workflows: summaries,
		Total:     len(summaries),
		Limit:     limit,
		Offset:    offset,
	})
}

// CancelWorkflow handles PUT /api/v1/workflows/{workflow_id}/cancel.
//
// Marks the workflow instance cancelling, cancels every non-terminal member
// job via the same jobcontrol.CancelJob logic used by JobHandler.CancelJob,
// and marks pending/waiting nodes (no job ever submitted) cancelled
// directly. See internal/jobcontrol.CancelWorkflow — the shared
// implementation this handler and the future CSIL UI service both call —
// and UI_AUTH_PLAN.md's Cancel vs Kill section for the full design.
//
// Authz here is unchanged from the pre-existing job/workflow ownership
// check (owner or admin) — real RBAC lands in a later wave.
func (h *WorkflowHandler) CancelWorkflow(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	workflowID := h.getID(r, "workflow_id")
	if workflowID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	wig, ok := h.store.(workflowInstanceGetter)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, jobcontrol.ErrWorkflowsUnsupported)
		return
	}
	existing, err := wig.GetWorkflowInstance(r.Context(), workflowID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if !h.isAdmin(user) && existing.UserID != user.UserID {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}

	wf, err := jobcontrol.CancelWorkflow(r.Context(), h.store, h.corndogsClient, workflowID, false)
	if err != nil {
		if errors.Is(err, jobcontrol.ErrWorkflowsUnsupported) {
			h.respondWithError(w, http.StatusNotImplemented, err)
			return
		}
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	h.respondWithJSON(w, http.StatusOK, wf)
}

// RetryWorkflow handles POST /api/v1/workflows/{workflow_id}/retry.
//
// Retries an entire workflow as a brand-new instance (fresh workflow_id,
// fresh nodes, fresh jobs) — the old instance is left untouched for
// history. See internal/jobcontrol.RetryWorkflow, the shared implementation
// this handler and the future CSIL UI service both call.
//
// Authz here matches CancelWorkflow's pre-existing owner-or-admin check.
func (h *WorkflowHandler) RetryWorkflow(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	workflowID := h.getID(r, "workflow_id")
	if workflowID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	wig, ok := h.store.(workflowInstanceGetter)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, jobcontrol.ErrWorkflowsUnsupported)
		return
	}
	existing, err := wig.GetWorkflowInstance(r.Context(), workflowID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if !h.isAdmin(user) && existing.UserID != user.UserID {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	if !existing.IsRetryable() {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	wf, err := jobcontrol.RetryWorkflow(r.Context(), h.store, h.corndogsClient, workflowID)
	if err != nil {
		if errors.Is(err, jobcontrol.ErrWorkflowsUnsupported) {
			h.respondWithError(w, http.StatusNotImplemented, err)
			return
		}
		if errors.Is(err, jobcontrol.ErrWorkflowNotRetryable) {
			h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
			return
		}
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}

	h.respondWithJSON(w, http.StatusCreated, wf)
}

// RetryUnsuccessfulResponse is RetryUnsuccessfulJobs' response body: the
// jobs that were successfully retried, plus (if any individual retry
// failed) an aggregated error description. A partial success still returns
// 200 with both fields populated — see RetryUnsuccessfulJobs' doc comment
// for why an all-or-nothing failure would be the wrong shape for a bulk
// operation like this one.
type RetryUnsuccessfulResponse struct {
	Jobs  []*models.Job `json:"jobs"`
	Error string        `json:"error,omitempty"`
}

// RetryUnsuccessfulJobs handles POST
// /api/v1/workflows/{workflow_id}/retry-unsuccessful.
//
// Job-retries every failed/cancelled member job of the workflow in place
// (same workflow instance, same nodes) — compare RetryWorkflow, which
// starts an entirely new instance instead. See
// internal/jobcontrol.RetryUnsuccessfulJobs.
//
// Authz matches RetryWorkflow/CancelWorkflow's owner-or-admin check. The
// workflow itself must also be failed/cancelled (same gate as
// RetryWorkflow): bulk-retrying member jobs of a workflow that's still
// actively running/evaluating isn't a supported operation.
func (h *WorkflowHandler) RetryUnsuccessfulJobs(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	workflowID := h.getID(r, "workflow_id")
	if workflowID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	wig, ok := h.store.(workflowInstanceGetter)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, jobcontrol.ErrWorkflowsUnsupported)
		return
	}
	existing, err := wig.GetWorkflowInstance(r.Context(), workflowID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if !h.isAdmin(user) && existing.UserID != user.UserID {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	if !existing.IsRetryable() {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}

	jobs, retryErr := jobcontrol.RetryUnsuccessfulJobs(r.Context(), h.store, h.corndogsClient, workflowID)
	if retryErr != nil {
		if errors.Is(retryErr, jobcontrol.ErrWorkflowsUnsupported) {
			h.respondWithError(w, http.StatusNotImplemented, retryErr)
			return
		}
		if len(jobs) == 0 {
			// Every retry attempted failed outright (as opposed to a
			// partial success) — surface it as a real error rather than a
			// 200 with an empty jobs array and a buried error string.
			h.respondWithError(w, http.StatusInternalServerError, retryErr)
			return
		}
	}

	response := RetryUnsuccessfulResponse{Jobs: jobs}
	if retryErr != nil {
		response.Error = retryErr.Error()
	}
	h.respondWithJSON(w, http.StatusOK, response)
}

func (h *WorkflowHandler) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	user := checkauth.GetUserFromContext(r.Context())
	if user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return
	}

	workflowID := h.getID(r, "workflow_id")
	if workflowID == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	if ws, ok := h.store.(workflowSummaryStore); ok {
		summary, err := ws.GetWorkflowSummary(r.Context(), workflowID)
		if err != nil {
			h.respondWithError(w, http.StatusNotFound, err)
			return
		}
		if !h.canUserViewWorkflow(r.Context(), user, summary) {
			h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
			return
		}
		h.respondWithJSON(w, http.StatusOK, summary)
		return
	}

	job, err := h.store.GetJobByID(r.Context(), workflowID)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if !h.canUserViewJob(r.Context(), user, job) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	summary := workflowSummaryFromLooseJob(job)
	h.respondWithJSON(w, http.StatusOK, summary)
}

func (h *WorkflowHandler) parsePagination(r *http.Request) (limit, offset int) {
	limit = 20
	offset = 0
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}
	return limit, offset
}

// commonWorkflowQueryFilters parses the filter query parameters
// ListWorkflows honors regardless of user-scoping policy.
func (h *WorkflowHandler) commonWorkflowQueryFilters(r *http.Request) map[string]interface{} {
	filters := make(map[string]interface{})
	if status := r.URL.Query().Get("status"); status != "" {
		filters["status"] = status
	}
	if projectID := r.URL.Query().Get("project_id"); projectID != "" {
		filters["project_id"] = projectID
	}
	return filters
}

// parseWorkflowFilters builds ListWorkflows' filter set for the
// SQL-side-visibility primary path (workflowSummaryVisibleToStore). See
// JobHandler.parseFilters — the visibility predicate
// ListWorkflowSummariesVisibleTo evaluates in SQL is the actual
// authorization decision for every row, so user_id is left unset unless the
// caller explicitly asks to narrow it.
func (h *WorkflowHandler) parseWorkflowFilters(r *http.Request, user *models.User) map[string]interface{} {
	filters := h.commonWorkflowQueryFilters(r)
	if userID := r.URL.Query().Get("user_id"); userID != "" {
		filters["user_id"] = userID
	}
	return filters
}

// parseWorkflowFiltersStrict is ListWorkflows' fallback-path filter builder
// — see JobHandler.parseFiltersStrict.
func (h *WorkflowHandler) parseWorkflowFiltersStrict(r *http.Request, user *models.User) map[string]interface{} {
	filters := h.commonWorkflowQueryFilters(r)
	if !h.isAdmin(user) {
		filters["user_id"] = user.UserID
	} else if userID := r.URL.Query().Get("user_id"); userID != "" {
		filters["user_id"] = userID
	}
	return filters
}

func (h *WorkflowHandler) looseJobSummaries(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.WorkflowSummary, error) {
	jobs, err := h.store.ListJobs(ctx, filters, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]models.WorkflowSummary, 0, len(jobs))
	for i := range jobs {
		if jobs[i].WorkflowID != nil {
			continue
		}
		out = append(out, workflowSummaryFromLooseJob(&jobs[i]))
	}
	return out, nil
}

func workflowSummaryFromLooseJob(job *models.Job) models.WorkflowSummary {
	running := 0
	completed := 0
	failed := 0
	switch job.Status {
	case "running":
		running = 1
	case "completed":
		completed = 1
	case "failed", "timeout", "cancelled":
		failed = 1
	}
	return models.WorkflowSummary{
		WorkflowID:      job.JobID,
		Kind:            "job",
		Name:            job.Name,
		Status:          job.Status,
		UserID:          job.UserID,
		ProjectID:       job.ProjectID,
		CreatedAt:       job.CreatedAt,
		UpdatedAt:       job.UpdatedAt,
		CompletedAt:     job.CompletedAt,
		QueueName:       job.QueueName,
		JobCount:        1,
		RunningCount:    running,
		CompletedCount:  completed,
		FailedCount:     failed,
		LooseJobID:      &job.JobID,
		LooseJobExit:    job.ExitCode,
		DecisionSummary: job.LastError,
	}
}

func (h *WorkflowHandler) canUserAccessWorkflow(user *models.User, summary *models.WorkflowSummary) bool {
	if h.isAdmin(user) {
		return true
	}
	return summary.UserID == user.UserID
}

// canUserViewWorkflow is canUserAccessWorkflow plus public visibility
// (additive, GetWorkflow/ListWorkflows only — see the WorkflowHandler.
// visibility field doc and JobHandler.canUserViewJob for the same pattern).
func (h *WorkflowHandler) canUserViewWorkflow(ctx context.Context, user *models.User, summary *models.WorkflowSummary) bool {
	if h.canUserAccessWorkflow(user, summary) {
		return true
	}
	if h.visibility == nil {
		return false
	}
	visible, err := h.visibility.CanViewWorkflowSummary(ctx, authz.IdentityFromUser(user), summary)
	return err == nil && visible
}

// canUserViewJob is canUserAccessJob plus public visibility — used by
// GetWorkflow's loose-job fallback path only; CancelWorkflow (a mutation)
// keeps its own separate, unchanged owner-or-admin check.
func (h *WorkflowHandler) canUserViewJob(ctx context.Context, user *models.User, job *models.Job) bool {
	if h.canUserAccessJob(user, job) {
		return true
	}
	if h.visibility == nil {
		return false
	}
	visible, err := h.visibility.CanViewJob(ctx, authz.IdentityFromUser(user), job)
	return err == nil && visible
}

func (h *WorkflowHandler) canUserAccessJob(user *models.User, job *models.Job) bool {
	if h.isAdmin(user) {
		return true
	}
	return job.UserID == user.UserID
}

func (h *WorkflowHandler) isAdmin(user *models.User) bool {
	for _, role := range user.Roles {
		if role == "admin" || role == "system_admin" {
			return true
		}
	}
	return false
}
