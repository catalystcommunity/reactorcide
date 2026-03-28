package handlers

import (
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/webapp/internal/templates"
)

func TestStatusClass(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"completed", "status-completed"},
		{"failed", "status-failed"},
		{"timeout", "status-failed"},
		{"running", "status-running"},
		{"queued", "status-queued"},
		{"submitted", "status-queued"},
		{"cancelled", "status-cancelled"},
		{"unknown", "status-unknown"},
		{"", "status-unknown"},
	}
	for _, tc := range tests {
		got := statusClass(tc.status)
		if got != tc.expected {
			t.Errorf("statusClass(%q) = %q, want %q", tc.status, got, tc.expected)
		}
	}
}

func TestFormatTime(t *testing.T) {
	ts := time.Date(2026, 3, 15, 14, 30, 45, 0, time.UTC)
	got := formatTime(ts)
	if got != "2026-03-15 14:30:45 UTC" {
		t.Errorf("formatTime() = %q, want %q", got, "2026-03-15 14:30:45 UTC")
	}

	got = formatTime(time.Time{})
	if got != "-" {
		t.Errorf("formatTime(zero) = %q, want %q", got, "-")
	}
}

func TestTemplatesParse(t *testing.T) {
	funcMap := template.FuncMap{
		"statusClass":    statusClass,
		"formatTime":     formatTime,
		"formatDuration": func(start, end *time.Time) string { return "1m 30s" },
		"exitCodeClass":  func(code *int) string { return "" },
		"derefInt":       func(p *int) int { return 0 },
		"derefStr":       func(p *string) string { return "" },
		"add":            func(a, b int) int { return a + b },
		"sub":            func(a, b int) int { return a - b },
		"streamClass":    func(stream string) string { return "" },
	}

	tmpl, err := template.New("").Funcs(funcMap).ParseFS(templates.FS, "*.html")
	if err != nil {
		t.Fatalf("Failed to parse templates: %v", err)
	}

	expectedTemplates := []string{"head", "foot", "jobs_list.html", "job_detail.html", "error.html", "logs_fragment.html"}
	for _, name := range expectedTemplates {
		if tmpl.Lookup(name) == nil {
			t.Errorf("Template %q not found", name)
		}
	}
}

func TestJobsListTemplate(t *testing.T) {
	handler := NewWebHandler(NewAPIClient())

	var buf strings.Builder
	exitCode := 0
	data := map[string]interface{}{
		"Title":        "Jobs",
		"Jobs": []JobResponse{
			{
				JobID:     "test-123",
				Name:      "test-job",
				Status:    "completed",
				CreatedAt: time.Date(2026, 3, 15, 14, 0, 0, 0, time.UTC),
				ExitCode:  &exitCode,
				SourceRef: "main",
			},
		},
		"Total":        1,
		"Limit":        50,
		"Offset":       0,
		"StatusFilter": "",
		"HasPrev":      false,
		"HasNext":      false,
		"PrevOffset":   0,
		"NextOffset":   50,
	}

	err := handler.templates.ExecuteTemplate(&buf, "jobs_list.html", data)
	if err != nil {
		t.Fatalf("Failed to render jobs_list.html: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "test-job") {
		t.Error("jobs_list.html should contain job name")
	}
	if !strings.Contains(html, "test-123") {
		t.Error("jobs_list.html should contain job ID in link")
	}
	if !strings.Contains(html, "status-completed") {
		t.Error("jobs_list.html should contain status class")
	}
}

func TestJobDetailTemplate(t *testing.T) {
	handler := NewWebHandler(NewAPIClient())

	var buf strings.Builder
	exitCode := 0
	data := map[string]interface{}{
		"Title": "build-project",
		"Job": &JobResponse{
			JobID:       "test-456",
			Name:        "build-project",
			Description: "Build the project",
			Status:      "completed",
			CreatedAt:   time.Date(2026, 3, 15, 14, 0, 0, 0, time.UTC),
			UpdatedAt:   time.Date(2026, 3, 15, 14, 5, 0, 0, time.UTC),
			ExitCode:    &exitCode,
			SourceRef:   "feature-branch",
			RunnerImage: "golang:1.25",
			QueueName:   "reactorcide-jobs",
		},
		"Logs": []LogEntry{
			{Timestamp: "2026-03-15T14:01:00Z", Stream: "stdout", Message: "Building..."},
			{Timestamp: "2026-03-15T14:02:00Z", Stream: "stderr", Message: "warning: unused var"},
		},
		"Stream": "combined",
	}

	err := handler.templates.ExecuteTemplate(&buf, "job_detail.html", data)
	if err != nil {
		t.Fatalf("Failed to render job_detail.html: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "build-project") {
		t.Error("job_detail.html should contain job name")
	}
	if !strings.Contains(html, "Building...") {
		t.Error("job_detail.html should contain log messages")
	}
	if !strings.Contains(html, "log-stderr") {
		t.Error("job_detail.html should contain stderr log class")
	}
	if !strings.Contains(html, "feature-branch") {
		t.Error("job_detail.html should contain source ref")
	}
}

func TestErrorTemplate(t *testing.T) {
	handler := NewWebHandler(NewAPIClient())

	var buf strings.Builder
	data := map[string]interface{}{
		"Title":   "Error",
		"Status":  404,
		"Message": "Job not found",
	}

	err := handler.templates.ExecuteTemplate(&buf, "error.html", data)
	if err != nil {
		t.Fatalf("Failed to render error.html: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "404") {
		t.Error("error.html should contain status code")
	}
	if !strings.Contains(html, "Job not found") {
		t.Error("error.html should contain error message")
	}
}

func TestEmptyJobsList(t *testing.T) {
	handler := NewWebHandler(NewAPIClient())

	var buf strings.Builder
	data := map[string]interface{}{
		"Title":        "Jobs",
		"Jobs":         []JobResponse{},
		"Total":        0,
		"Limit":        50,
		"Offset":       0,
		"StatusFilter": "",
		"HasPrev":      false,
		"HasNext":      false,
		"PrevOffset":   0,
		"NextOffset":   50,
	}

	err := handler.templates.ExecuteTemplate(&buf, "jobs_list.html", data)
	if err != nil {
		t.Fatalf("Failed to render empty jobs_list.html: %v", err)
	}

	if !strings.Contains(buf.String(), "No jobs found") {
		t.Error("Empty jobs list should show 'No jobs found'")
	}
}
