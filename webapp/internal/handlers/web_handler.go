package handlers

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/webapp/internal/templates"
	"github.com/sirupsen/logrus"
)

// WebHandler serves the HTML UI
type WebHandler struct {
	client    *APIClient
	templates *template.Template
}

func NewWebHandler(client *APIClient) *WebHandler {
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
	}

	tmpl := template.Must(template.New("").Funcs(funcMap).ParseFS(templates.FS, "*.html"))

	return &WebHandler{
		client:    client,
		templates: tmpl,
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

// JobsList renders the jobs list page
func (h *WebHandler) JobsList(w http.ResponseWriter, r *http.Request) {
	limit := 50
	offset := 0
	status := ""

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

	jobs, err := h.client.ListJobs(limit, offset, status)
	if err != nil {
		logrus.WithError(err).Error("Failed to fetch jobs")
		h.renderError(w, http.StatusBadGateway, "Failed to fetch jobs from API", err)
		return
	}

	data := map[string]interface{}{
		"Title":         "Jobs",
		"Jobs":          jobs.Jobs,
		"Total":         jobs.Total,
		"Limit":         limit,
		"Offset":        offset,
		"StatusFilter":  status,
		"HasPrev":       offset > 0,
		"HasNext":       len(jobs.Jobs) == limit,
		"PrevOffset":    max(0, offset-limit),
		"NextOffset":    offset + limit,
	}

	h.render(w, "jobs_list.html", data)
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
			h.renderError(w, http.StatusNotFound, "Job not found", nil)
			return
		}
		logrus.WithError(err).Error("Failed to fetch job")
		h.renderError(w, http.StatusBadGateway, "Failed to fetch job", err)
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

	data := map[string]interface{}{
		"Title":  job.Name,
		"Job":    job,
		"Logs":   logs,
		"Stream": stream,
	}

	h.render(w, "job_detail.html", data)
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
		h.renderError(w, http.StatusBadGateway, "Failed to fetch logs", err)
		return
	}

	data := map[string]interface{}{
		"Title":  "Logs",
		"Logs":   logs,
		"Stream": stream,
	}

	h.render(w, "logs_fragment.html", data)
}

func (h *WebHandler) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		logrus.WithError(err).Errorf("Failed to render template %s", name)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *WebHandler) renderError(w http.ResponseWriter, status int, message string, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	data := map[string]interface{}{
		"Title":   "Error",
		"Status":  status,
		"Message": message,
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
	case "completed":
		return "status-completed"
	case "failed", "timeout":
		return "status-failed"
	case "running":
		return "status-running"
	case "queued", "submitted":
		return "status-queued"
	case "cancelled":
		return "status-cancelled"
	default:
		return "status-unknown"
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05 UTC")
}
