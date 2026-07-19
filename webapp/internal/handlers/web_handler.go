package handlers

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/webapp/internal/templates"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
	"github.com/sirupsen/logrus"
)

// WebHandler serves the HTML UI
type WebHandler struct {
	client    *APIClient
	templates *template.Template

	// uiClients is the CSIL-RPC carrier to the coordinator's
	// ReactorcideAuth/ReactorcideUi services (session, capabilities,
	// management ops). Nil in tests that only exercise page rendering —
	// every uiClients-dependent path treats nil as "anonymous, auth mode
	// none, no coordinator calls made".
	uiClients *uiclient.Clients

	// authConfigMu guards the short-lived get-auth-config cache (see
	// getAuthConfig in session.go). Auth mode is coordinator-wide config,
	// not per-user, so caching it across requests/users is safe.
	authConfigMu  sync.Mutex
	authConfigVal csilapi.GetAuthConfigResponse
	authConfigErr error
	authConfigAt  time.Time
}

func NewWebHandler(client *APIClient, uiClients *uiclient.Clients) *WebHandler {
	funcMap := template.FuncMap{
		"statusClass": statusClass,
		"formatTime":  formatTime,
		"formatDuration": func(start, end *time.Time) string {
			if start == nil {
				return "-"
			}
			endTime := time.Now()
			if end != nil {
				endTime = *end
			}
			d := endTime.Sub(*start)
			if d < time.Minute {
				return fmt.Sprintf("%.0fs", d.Seconds())
			}
			return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
		},
		"formatWorkflowDuration": formatWorkflowDuration,
		"exitCodeClass": func(code *int) string {
			if code == nil {
				return ""
			}
			if *code == 0 {
				return "exit-success"
			}
			return "exit-failure"
		},
		"derefInt": func(p *int) int {
			if p == nil {
				return -1
			}
			return *p
		},
		"derefStr": func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		},
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"streamClass": func(stream string) string {
			if stream == "stderr" {
				return "log-stderr"
			}
			return "log-stdout"
		},
		"workflowLink":   workflowLink,
		"shortSHA":       shortSHA,
		"joinStringList": joinStringList,
		"isRetryable":    isRetryableStatus,
	}

	tmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(templates.FS, "*.html"))

	return &WebHandler{
		client:    client,
		templates: tmpl,
		uiClients: uiClients,
	}
}

func (h *WebHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	// Root serves health check — only exact match
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("ok"))
}

func (h *WebHandler) RedirectToAppRoot(w http.ResponseWriter, r *http.Request) {
	target := "/app/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// JobsList renders the main workflow list page. Loose jobs are represented
// by the API as single-job workflows.
func (h *WebHandler) JobsList(w http.ResponseWriter, r *http.Request) {
	limit := 50
	offset := 0
	status := ""
	projectID := ""

	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	if v := r.URL.Query().Get("status"); v != "" {
		status = v
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		projectID = v
	}

	workflows, err := h.client.ListWorkflows(limit, offset, status, projectID)
	if err != nil {
		logrus.WithError(err).Error("Failed to fetch workflows")
		h.renderError(w, r, http.StatusBadGateway, "Failed to fetch workflows from API", err)
		return
	}
	projects, err := h.client.ListProjects(100, 0)
	if err != nil {
		logrus.WithError(err).Warn("Failed to fetch projects")
		projects = &ListProjectsResponse{}
	}

	data := map[string]interface{}{
		"Title":           "Workflows",
		"Workflows":       workflows.Workflows,
		"Projects":        projects.Projects,
		"SelectedProject": projectID,
		"Total":           workflows.Total,
		"Limit":           limit,
		"Offset":          offset,
		"StatusFilter":    status,
		"HasPrev":         offset > 0,
		"HasNext":         len(workflows.Workflows) == limit,
		"PrevOffset":      max(0, offset-limit),
		"NextOffset":      offset + limit,
	}

	h.render(w, r, "jobs_list.html", data)
}

func (h *WebHandler) WorkflowDetail(w http.ResponseWriter, r *http.Request) {
	workflowID := r.PathValue("id")
	if workflowID == "" {
		http.NotFound(w, r)
		return
	}

	workflow, err := h.client.GetWorkflow(workflowID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			h.renderError(w, r, http.StatusNotFound, "Workflow not found", nil)
			return
		}
		logrus.WithError(err).Error("Failed to fetch workflow")
		h.renderError(w, r, http.StatusBadGateway, "Failed to fetch workflow", err)
		return
	}
	if workflow.LooseJobID != nil {
		http.Redirect(w, r, "/app/jobs/"+*workflow.LooseJobID, http.StatusFound)
		return
	}

	jobs, err := h.client.ListJobsForWorkflow(workflowID, 200, 0)
	if err != nil {
		logrus.WithError(err).Error("Failed to fetch workflow jobs")
		h.renderError(w, r, http.StatusBadGateway, "Failed to fetch workflow jobs", err)
		return
	}

	// Capability hints for Task I's cancel/kill/retry buttons: scoped to this
	// workflow's project when known, since cancel/kill/retry authorization is
	// project/org-scoped (see UI_AUTH_PLAN.md's permission matrix). Display
	// only — the coordinator re-authorizes the actual cancel-workflow/
	// retry-workflow/retry-unsuccessful-jobs call.
	caps := h.capabilitiesForProject(r, workflow.ProjectID)
	msg, errMsg := flashFromQuery(r)

	data := map[string]interface{}{
		"Title":               workflow.Name,
		"Workflow":            workflow,
		"Jobs":                jobs.Jobs,
		"CanCancel":           caps.CancelJob,
		"CanKill":             caps.KillJob,
		"CanRetry":            caps.RetryJob,
		"HasUnsuccessfulJobs": hasUnsuccessfulJobs(jobs.Jobs),
		"FormMsg":             msg,
		"FormError":           errMsg,
	}
	h.render(w, r, "workflow_detail.html", data)
}

// JobDetail renders the job detail page with logs
func (h *WebHandler) JobDetail(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		http.NotFound(w, r)
		return
	}

	job, err := h.client.GetJob(jobID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			h.renderError(w, r, http.StatusNotFound, "Job not found", nil)
			return
		}
		logrus.WithError(err).Error("Failed to fetch job")
		h.renderError(w, r, http.StatusBadGateway, "Failed to fetch job", err)
		return
	}

	stream := r.URL.Query().Get("stream")
	if stream == "" {
		stream = "combined"
	}

	logs, err := h.client.GetJobLogs(jobID, stream)
	if err != nil {
		logrus.WithError(err).Warn("Failed to fetch logs")
		// Don't fail the whole page, just show no logs
	}

	// Capability hints for Task I's cancel/kill/retry buttons; see the
	// identical comment in WorkflowDetail.
	caps := h.capabilitiesForProject(r, job.ProjectID)
	msg, errMsg := flashFromQuery(r)

	data := map[string]interface{}{
		"Title":     job.Name,
		"Job":       job,
		"Logs":      logs,
		"Stream":    stream,
		"CanCancel": caps.CancelJob,
		"CanKill":   caps.KillJob,
		"CanRetry":  caps.RetryJob,
		"FormMsg":   msg,
		"FormError": errMsg,
	}

	h.render(w, r, "job_detail.html", data)
}

// JobLogs returns just the log content (for potential AJAX refresh)
func (h *WebHandler) JobLogs(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		http.NotFound(w, r)
		return
	}

	stream := r.URL.Query().Get("stream")
	if stream == "" {
		stream = "combined"
	}

	logs, err := h.client.GetJobLogs(jobID, stream)
	if err != nil {
		h.renderError(w, r, http.StatusBadGateway, "Failed to fetch logs", err)
		return
	}

	data := map[string]interface{}{
		"Title":  "Logs",
		"Logs":   logs,
		"Stream": stream,
	}

	h.render(w, r, "logs_fragment.html", data)
}

// render executes a named template with data, auto-injecting the current
// request's SessionInfo as "Session" (used by the "head"/"foot" layout
// templates for the nav bar auth area) unless the caller already set one.
func (h *WebHandler) render(w http.ResponseWriter, r *http.Request, name string, data map[string]interface{}) {
	if data == nil {
		data = map[string]interface{}{}
	}
	if _, ok := data["Session"]; !ok {
		data["Session"] = h.sessionInfo(r)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		logrus.WithError(err).Errorf("Failed to render template %s", name)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *WebHandler) renderError(w http.ResponseWriter, r *http.Request, status int, message string, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	data := map[string]interface{}{
		"Title":   "Error",
		"Status":  status,
		"Message": message,
		"Session": h.sessionInfo(r),
	}
	if err != nil {
		data["Detail"] = err.Error()
	}
	if tmplErr := h.templates.ExecuteTemplate(w, "error.html", data); tmplErr != nil {
		http.Error(w, message, status)
	}
}

func statusClass(status string) string {
	switch status {
	case "completed", "success", "skipped":
		return "status-completed"
	case "failed", "timeout":
		return "status-failed"
	case "running", "evaluating":
		return "status-running"
	case "queued", "submitted":
		return "status-queued"
	case "cancelled":
		return "status-cancelled"
	default:
		return "status-unknown"
	}
}

// isRetryableStatus mirrors coordinator_api's models.Job.IsRetryable /
// models.WorkflowInstance.IsRetryable (failed or cancelled only) for gating
// the retry buttons' visibility. Display only — the coordinator
// re-validates the status itself when the retry op is actually called.
func isRetryableStatus(status string) bool {
	return status == "failed" || status == "cancelled"
}

// hasUnsuccessfulJobs reports whether any job in a workflow's member-job
// list is failed/cancelled, for gating WorkflowDetail's "Retry all
// unsuccessful jobs" button.
func hasUnsuccessfulJobs(jobs []JobResponse) bool {
	for _, j := range jobs {
		if isRetryableStatus(j.Status) {
			return true
		}
	}
	return false
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05 UTC")
}

func formatWorkflowDuration(start time.Time, end *time.Time) string {
	if start.IsZero() {
		return "-"
	}
	endTime := time.Now()
	if end != nil {
		endTime = *end
	}
	d := endTime.Sub(start)
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
}

func workflowLink(w WorkflowSummary) string {
	if w.LooseJobID != nil && *w.LooseJobID != "" {
		return "/app/jobs/" + *w.LooseJobID
	}
	return "/app/workflows/" + w.WorkflowID
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
