package handlers

import (
	"fmt"
	"net/http"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// JobCancel handles POST /app/jobs/{id}/cancel: a graceful cancel (cleanup
// hooks run). Rendered only when job_detail.html's CanCancel is true (see
// WebHandler.JobDetail's capabilitiesForProject call), but the coordinator
// re-authorizes regardless of what the button's visibility implied.
func (h *WebHandler) JobCancel(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		http.NotFound(w, r)
		return
	}
	backTo := "/app/jobs/" + jobID
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	resp, err := h.uiClients.Ui.CancelJob(h.authContext(r), csilapi.CancelJobRequest{JobId: jobID})
	if err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Job cancelling (status: "+resp.Status+")", false)
}

// JobKill handles POST /app/jobs/{id}/kill: an immediate forced kill (no
// cleanup guarantee). Rendered only when CanKill is true.
func (h *WebHandler) JobKill(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		http.NotFound(w, r)
		return
	}
	backTo := "/app/jobs/" + jobID
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	resp, err := h.uiClients.Ui.KillJob(h.authContext(r), csilapi.KillJobRequest{JobId: jobID})
	if err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Job killed (status: "+resp.Status+")", false)
}

// WorkflowCancel handles POST /app/workflows/{id}/cancel: cancels every
// non-terminal node's job in the workflow. Rendered only when
// workflow_detail.html's CanCancel is true.
func (h *WebHandler) WorkflowCancel(w http.ResponseWriter, r *http.Request) {
	workflowID := r.PathValue("id")
	if workflowID == "" {
		http.NotFound(w, r)
		return
	}
	backTo := "/app/workflows/" + workflowID
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	resp, err := h.uiClients.Ui.CancelWorkflow(h.authContext(r), csilapi.CancelWorkflowRequest{WorkflowInstanceId: workflowID})
	if err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Workflow cancelling (status: "+resp.Status+")", false)
}

// JobRetry handles POST /app/jobs/{id}/retry: retries a single failed/
// cancelled job in place (same workflow/node, if any) — see
// jobcontrol.RetryJob. Rendered only when job_detail.html's CanRetry is true
// and the job is in a retryable status, but the coordinator re-authorizes
// and re-validates the status regardless. Redirects to the NEW job's detail
// page, since a retry creates a brand-new job row rather than mutating the
// original.
func (h *WebHandler) JobRetry(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		http.NotFound(w, r)
		return
	}
	backTo := "/app/jobs/" + jobID
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	resp, err := h.uiClients.Ui.RetryJob(h.authContext(r), csilapi.RetryJobRequest{JobId: jobID})
	if err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, "/app/jobs/"+resp.JobId, "Job retried (status: "+resp.Status+")", false)
}

// WorkflowRetry handles POST /app/workflows/{id}/retry: retries an entire
// failed/cancelled workflow as a brand-new instance (old instance/nodes/jobs
// untouched) — see jobcontrol.RetryWorkflow. Rendered only when
// workflow_detail.html's CanRetry is true and the workflow is in a
// retryable status. Redirects to the NEW workflow's detail page, since this
// creates a fresh instance rather than mutating the one the button was on.
func (h *WebHandler) WorkflowRetry(w http.ResponseWriter, r *http.Request) {
	workflowID := r.PathValue("id")
	if workflowID == "" {
		http.NotFound(w, r)
		return
	}
	backTo := "/app/workflows/" + workflowID
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	resp, err := h.uiClients.Ui.RetryWorkflow(h.authContext(r), csilapi.RetryWorkflowRequest{WorkflowInstanceId: workflowID})
	if err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, "/app/workflows/"+resp.WorkflowInstanceId, "Workflow retried (status: "+resp.Status+")", false)
}

// WorkflowRetryUnsuccessful handles POST /app/workflows/{id}/retry-unsuccessful:
// bulk-retries every failed/cancelled member job of the workflow, in place
// (same instance, same nodes) — see jobcontrol.RetryUnsuccessfulJobs.
// Rendered only when workflow_detail.html's CanRetry is true and at least
// one member job is failed/cancelled. Unlike JobRetry/WorkflowRetry, this
// mutates the SAME workflow instance in place, so it redirects back to the
// same workflow page rather than to a new one.
func (h *WebHandler) WorkflowRetryUnsuccessful(w http.ResponseWriter, r *http.Request) {
	workflowID := r.PathValue("id")
	if workflowID == "" {
		http.NotFound(w, r)
		return
	}
	backTo := "/app/workflows/" + workflowID
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	resp, err := h.uiClients.Ui.RetryUnsuccessfulJobs(h.authContext(r), csilapi.RetryUnsuccessfulJobsRequest{WorkflowInstanceId: workflowID})
	if err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	msg := fmt.Sprintf("Retried %d job(s), skipped %d", resp.RetriedCount, resp.SkippedCount)
	h.redirectFlash(w, r, backTo, msg, false)
}
