package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// TestJobDetailTemplate_CancelKillButtonsGatedByCapability renders
// job_detail.html directly (matching the existing TestJobDetailTemplate
// pattern in web_handler_test.go) with CanCancel/CanKill toggled, since
// JobDetail itself depends on the REST APIClient rather than uiClients.
func TestJobDetailTemplate_CancelKillButtonsGatedByCapability(t *testing.T) {
	handler := NewWebHandler(NewAPIClient(), nil)
	jobData := func(canCancel, canKill bool) map[string]interface{} {
		return map[string]interface{}{
			"Title": "build",
			"Job": &JobResponse{
				JobID:     "job-1",
				Name:      "build",
				Status:    "running",
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
			"CanCancel": canCancel,
			"CanKill":   canKill,
		}
	}

	t.Run("no capability shows no buttons", func(t *testing.T) {
		var buf strings.Builder
		if err := handler.templates.ExecuteTemplate(&buf, "job_detail.html", jobData(false, false)); err != nil {
			t.Fatalf("render error: %v", err)
		}
		body := buf.String()
		if strings.Contains(body, `action="/app/jobs/job-1/cancel"`) {
			t.Errorf("cancel form should not render without CanCancel, got: %s", body)
		}
		if strings.Contains(body, `action="/app/jobs/job-1/kill"`) {
			t.Errorf("kill form should not render without CanKill, got: %s", body)
		}
	})

	t.Run("cancel capability shows only cancel button", func(t *testing.T) {
		var buf strings.Builder
		if err := handler.templates.ExecuteTemplate(&buf, "job_detail.html", jobData(true, false)); err != nil {
			t.Fatalf("render error: %v", err)
		}
		body := buf.String()
		if !strings.Contains(body, `action="/app/jobs/job-1/cancel"`) {
			t.Errorf("expected cancel form to render, got: %s", body)
		}
		if strings.Contains(body, `action="/app/jobs/job-1/kill"`) {
			t.Errorf("kill form should not render without CanKill, got: %s", body)
		}
	})

	t.Run("kill capability shows kill button with confirm", func(t *testing.T) {
		var buf strings.Builder
		if err := handler.templates.ExecuteTemplate(&buf, "job_detail.html", jobData(false, true)); err != nil {
			t.Fatalf("render error: %v", err)
		}
		body := buf.String()
		if !strings.Contains(body, `action="/app/jobs/job-1/kill"`) {
			t.Errorf("expected kill form to render, got: %s", body)
		}
		if !strings.Contains(body, "data-confirm=") {
			t.Errorf("expected the kill button to carry a data-confirm attribute, got: %s", body)
		}
	})
}

func TestWorkflowDetailTemplate_CancelButtonGatedByCapability(t *testing.T) {
	handler := NewWebHandler(NewAPIClient(), nil)
	wfData := func(canCancel bool) map[string]interface{} {
		return map[string]interface{}{
			"Title":     "release",
			"Workflow":  &WorkflowSummary{WorkflowID: "wf-1", Name: "release", Status: "running", CreatedAt: time.Now()},
			"Jobs":      []JobResponse{},
			"CanCancel": canCancel,
		}
	}

	var bufNo strings.Builder
	if err := handler.templates.ExecuteTemplate(&bufNo, "workflow_detail.html", wfData(false)); err != nil {
		t.Fatalf("render error: %v", err)
	}
	if strings.Contains(bufNo.String(), `action="/app/workflows/wf-1/cancel"`) {
		t.Errorf("cancel form should not render without CanCancel, got: %s", bufNo.String())
	}

	var bufYes strings.Builder
	if err := handler.templates.ExecuteTemplate(&bufYes, "workflow_detail.html", wfData(true)); err != nil {
		t.Fatalf("render error: %v", err)
	}
	if !strings.Contains(bufYes.String(), `action="/app/workflows/wf-1/cancel"`) {
		t.Errorf("expected cancel form to render, got: %s", bufYes.String())
	}
}

func TestJobCancel_HappyPathHitsFakeAndRedirectsWithStatus(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var sawJobID string
	fc.handle("ReactorcideUi", "cancel-job", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeCancelJobRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		sawJobID = req.JobId
		resp := csilapi.CancelJobResponse{JobId: req.JobId, Status: "cancelling"}
		return csilapi.EncodeCancelJobResponse(resp), "CancelJobResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/jobs/job-1/cancel", nil)
	req.SetPathValue("id", "job-1")
	rec := httptest.NewRecorder()
	h.withSession(h.JobCancel)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if sawJobID != "job-1" {
		t.Errorf("fake coordinator did not receive job_id=job-1, got %q", sawJobID)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/app/jobs/job-1?msg=") {
		t.Errorf("Location = %q, want a redirect back to the job page with a flash", loc)
	}
}

func TestJobKill_ConflictRedirectsWithErrFlash(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	fc.handle("ReactorcideUi", "kill-job", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return fakeServiceErrorPayload("conflict", "job cannot be cancelled in its current state"), "ServiceError", true
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/jobs/job-1/kill", nil)
	req.SetPathValue("id", "job-1")
	rec := httptest.NewRecorder()
	h.withSession(h.JobKill)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/app/jobs/job-1?") || !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want a redirect back to the job page with an err flash", loc)
	}
}

func TestJobKill_ForbiddenRendersErrorPage(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	fc.handle("ReactorcideUi", "kill-job", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return fakeServiceErrorPayload("forbidden", "you do not have permission to kill this job"), "ServiceError", true
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/jobs/job-1/kill", nil)
	req.SetPathValue("id", "job-1")
	rec := httptest.NewRecorder()
	h.withSession(h.JobKill)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestJobDetailTemplate_RetryButtonGatedByCapabilityAndStatus verifies the
// retry button only renders when both CanRetry is true AND the job's status
// is failed/cancelled — unlike cancel/kill, retry is additionally gated on
// job status since a running job can't be retried.
func TestJobDetailTemplate_RetryButtonGatedByCapabilityAndStatus(t *testing.T) {
	handler := NewWebHandler(NewAPIClient(), nil)
	jobData := func(canRetry bool, status string) map[string]interface{} {
		return map[string]interface{}{
			"Title": "build",
			"Job": &JobResponse{
				JobID:     "job-1",
				Name:      "build",
				Status:    status,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
			"CanRetry": canRetry,
		}
	}
	renders := func(t *testing.T, data map[string]interface{}) bool {
		t.Helper()
		var buf strings.Builder
		if err := handler.templates.ExecuteTemplate(&buf, "job_detail.html", data); err != nil {
			t.Fatalf("render error: %v", err)
		}
		return strings.Contains(buf.String(), `action="/app/jobs/job-1/retry"`)
	}

	if renders(t, jobData(false, "failed")) {
		t.Error("retry form should not render without CanRetry, even on a failed job")
	}
	if renders(t, jobData(true, "running")) {
		t.Error("retry form should not render on a running job, even with CanRetry")
	}
	if !renders(t, jobData(true, "failed")) {
		t.Error("expected retry form to render for a failed job with CanRetry")
	}
	if !renders(t, jobData(true, "cancelled")) {
		t.Error("expected retry form to render for a cancelled job with CanRetry")
	}
}

// TestWorkflowDetailTemplate_RetryButtonsGatedByCapabilityAndStatus mirrors
// the job-detail retry gating test for the workflow-level "Retry workflow"
// and "Retry all unsuccessful jobs" buttons — the latter is gated on
// HasUnsuccessfulJobs rather than the workflow's own status.
func TestWorkflowDetailTemplate_RetryButtonsGatedByCapabilityAndStatus(t *testing.T) {
	handler := NewWebHandler(NewAPIClient(), nil)
	wfData := func(canRetry bool, status string, hasUnsuccessful bool) map[string]interface{} {
		return map[string]interface{}{
			"Title":               "release",
			"Workflow":            &WorkflowSummary{WorkflowID: "wf-1", Name: "release", Status: status, CreatedAt: time.Now()},
			"Jobs":                []JobResponse{},
			"CanRetry":            canRetry,
			"HasUnsuccessfulJobs": hasUnsuccessful,
		}
	}
	render := func(t *testing.T, data map[string]interface{}) string {
		t.Helper()
		var buf strings.Builder
		if err := handler.templates.ExecuteTemplate(&buf, "workflow_detail.html", data); err != nil {
			t.Fatalf("render error: %v", err)
		}
		return buf.String()
	}

	body := render(t, wfData(false, "failed", true))
	if strings.Contains(body, `action="/app/workflows/wf-1/retry"`) {
		t.Error("retry-workflow form should not render without CanRetry")
	}
	if strings.Contains(body, `action="/app/workflows/wf-1/retry-unsuccessful"`) {
		t.Error("retry-unsuccessful form should not render without CanRetry")
	}

	body = render(t, wfData(true, "running", true))
	if strings.Contains(body, `action="/app/workflows/wf-1/retry"`) {
		t.Error("retry-workflow form should not render on a running workflow")
	}
	if !strings.Contains(body, `action="/app/workflows/wf-1/retry-unsuccessful"`) {
		t.Error("retry-unsuccessful form should still render on a running workflow if a member job is unsuccessful")
	}

	body = render(t, wfData(true, "failed", false))
	if !strings.Contains(body, `action="/app/workflows/wf-1/retry"`) {
		t.Error("expected retry-workflow form to render for a failed workflow with CanRetry")
	}
	if strings.Contains(body, `action="/app/workflows/wf-1/retry-unsuccessful"`) {
		t.Error("retry-unsuccessful form should not render when no member job is unsuccessful")
	}
}

func TestJobRetry_HappyPathRedirectsToNewJob(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var sawJobID string
	fc.handle("ReactorcideUi", "retry-job", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeRetryJobRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		sawJobID = req.JobId
		resp := csilapi.RetryJobResponse{JobId: "job-2", Status: "submitted"}
		return csilapi.EncodeRetryJobResponse(resp), "RetryJobResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/jobs/job-1/retry", nil)
	req.SetPathValue("id", "job-1")
	rec := httptest.NewRecorder()
	h.withSession(h.JobRetry)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if sawJobID != "job-1" {
		t.Errorf("fake coordinator did not receive job_id=job-1, got %q", sawJobID)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/app/jobs/job-2?msg=") {
		t.Errorf("Location = %q, want a redirect to the NEW job's page (job-2), not the original", loc)
	}
}

func TestJobRetry_NotRetryableRedirectsWithErrFlash(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	fc.handle("ReactorcideUi", "retry-job", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return fakeServiceErrorPayload("conflict", "job cannot be retried in its current state"), "ServiceError", true
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/jobs/job-1/retry", nil)
	req.SetPathValue("id", "job-1")
	rec := httptest.NewRecorder()
	h.withSession(h.JobRetry)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/app/jobs/job-1?") || !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want a redirect back to the original job page with an err flash", loc)
	}
}

func TestWorkflowRetry_HappyPathRedirectsToNewWorkflow(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var sawID string
	fc.handle("ReactorcideUi", "retry-workflow", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeRetryWorkflowRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		sawID = req.WorkflowInstanceId
		resp := csilapi.RetryWorkflowResponse{WorkflowInstanceId: "wf-2", Status: "running"}
		return csilapi.EncodeRetryWorkflowResponse(resp), "RetryWorkflowResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/workflows/wf-1/retry", nil)
	req.SetPathValue("id", "wf-1")
	rec := httptest.NewRecorder()
	h.withSession(h.WorkflowRetry)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if sawID != "wf-1" {
		t.Errorf("fake coordinator did not receive workflow_instance_id=wf-1, got %q", sawID)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/app/workflows/wf-2?msg=") {
		t.Errorf("Location = %q, want a redirect to the NEW workflow's page (wf-2), not the original", loc)
	}
}

func TestWorkflowRetryUnsuccessful_HappyPathRedirectsToSameWorkflow(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var sawID string
	fc.handle("ReactorcideUi", "retry-unsuccessful-jobs", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeRetryUnsuccessfulJobsRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		sawID = req.WorkflowInstanceId
		resp := csilapi.RetryUnsuccessfulJobsResponse{JobIds: []string{"job-2", "job-3"}, RetriedCount: 2, SkippedCount: 1}
		return csilapi.EncodeRetryUnsuccessfulJobsResponse(resp), "RetryUnsuccessfulJobsResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/workflows/wf-1/retry-unsuccessful", nil)
	req.SetPathValue("id", "wf-1")
	rec := httptest.NewRecorder()
	h.withSession(h.WorkflowRetryUnsuccessful)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if sawID != "wf-1" {
		t.Errorf("fake coordinator did not receive workflow_instance_id=wf-1, got %q", sawID)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/app/workflows/wf-1?msg=") {
		t.Errorf("Location = %q, want a redirect back to the SAME workflow's page (in-place retry)", loc)
	}
}

func TestWorkflowCancel_HappyPathHitsFake(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var sawID string
	fc.handle("ReactorcideUi", "cancel-workflow", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeCancelWorkflowRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		sawID = req.WorkflowInstanceId
		resp := csilapi.CancelWorkflowResponse{WorkflowInstanceId: req.WorkflowInstanceId, Status: "cancelling"}
		return csilapi.EncodeCancelWorkflowResponse(resp), "CancelWorkflowResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/workflows/wf-1/cancel", nil)
	req.SetPathValue("id", "wf-1")
	rec := httptest.NewRecorder()
	h.withSession(h.WorkflowCancel)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if sawID != "wf-1" {
		t.Errorf("fake coordinator did not receive workflow_instance_id=wf-1, got %q", sawID)
	}
}
