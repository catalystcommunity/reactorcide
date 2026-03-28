package test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHealthCheck(t *testing.T) {
	resp, err := http.Get(webBaseURL + "/")
	if err != nil {
		t.Fatalf("Health check request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("Expected 'ok', got %q", string(body))
	}
}

func TestJobsListPage(t *testing.T) {
	jobID := insertTestJob(t, "integration-test-list")

	resp, err := http.Get(webBaseURL + "/app/jobs")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "integration-test-list") {
		t.Error("Jobs list page should contain the test job name")
	}
	if !strings.Contains(html, jobID) {
		t.Error("Jobs list page should contain job ID in link")
	}
	if !strings.Contains(html, "Reactorcide") {
		t.Error("Jobs list page should contain site title")
	}
}

func TestJobsListPageAtAppRoot(t *testing.T) {
	resp, err := http.Get(webBaseURL + "/app/")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Jobs") {
		t.Error("/app/ should show jobs list page")
	}
}

func TestJobDetailPage(t *testing.T) {
	jobID := insertTestJob(t, "integration-test-detail")

	resp, err := http.Get(webBaseURL + "/app/jobs/" + jobID)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "integration-test-detail") {
		t.Error("Job detail page should contain job name")
	}
	if !strings.Contains(html, jobID) {
		t.Error("Job detail page should contain job ID")
	}
	if !strings.Contains(html, "alpine:latest") {
		t.Error("Job detail page should contain runner image")
	}
	if !strings.Contains(html, "No logs available") {
		t.Error("Job detail page should indicate no logs for new job")
	}
}

func TestJobDetailNotFound(t *testing.T) {
	resp, err := http.Get(webBaseURL + "/app/jobs/nonexistent-id")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "not found") && !strings.Contains(html, "Error") && !strings.Contains(html, "Failed") {
		t.Error("Non-existent job should show error")
	}
}

func TestJobsListWithStatusFilter(t *testing.T) {
	insertTestJob(t, "integration-test-filter")

	resp, err := http.Get(webBaseURL + "/app/jobs?status=submitted")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}
}

func TestJobsListPagination(t *testing.T) {
	for i := 0; i < 3; i++ {
		insertTestJob(t, "integration-test-page")
	}

	resp, err := http.Get(webBaseURL + "/app/jobs?limit=1&offset=0")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "offset=1") {
		t.Error("Paginated list should contain next page link")
	}
}

func TestNotFoundReturns404(t *testing.T) {
	resp, err := http.Get(webBaseURL + "/nonexistent")
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected 404, got %d", resp.StatusCode)
	}
}
