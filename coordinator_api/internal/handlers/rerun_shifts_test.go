package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func retryRequest(t *testing.T, handler *JobHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/test-job-id/retry", bytes.NewBufferString(body))
	ctx := checkauth.SetUserContext(req.Context(), &models.User{UserID: "test-user-id"})
	ctx = context.WithValue(ctx, GetContextKey("job_id"), "test-job-id")
	w := httptest.NewRecorder()
	handler.RetryJob(w, req.WithContext(ctx))
	return w
}

func failedJobStore() *MockStore {
	return &MockStore{GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
		return &models.Job{JobID: jobID, Status: "failed", UserID: "test-user-id", JobCommand: "make test",
			ResourceCPURequest: "1", ResourceCPULimit: "2", ResourceMemoryLimit: "4Gi", ExecutionProfile: "",
			RunnerImage: "runner:1"}, nil
	}}
}

func TestRetryJobREST_ShiftsApplied(t *testing.T) {
	st := failedJobStore()
	w := retryRequest(t, NewJobHandler(st, corndogs.NewMockClient()), `{"shifts":{"memory":"8Gi","timeout":"2h"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(st.CreateJobCalls) != 1 {
		t.Fatalf("CreateJob calls = %d", len(st.CreateJobCalls))
	}
	job := st.CreateJobCalls[0]
	if job.ResourceMemoryLimit != "8Gi" || job.TimeoutSeconds != 7200 || job.RunnerImage != "runner:1" || job.JobCommand != "make test" {
		t.Fatalf("retried job = %+v", job)
	}
	var resp JobResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Shifts == nil || resp.Shifts.MemoryLimit != "8Gi" {
		t.Fatalf("response shifts = %+v", resp.Shifts)
	}
}

func TestRetryJobREST_RefusesOtherChanges(t *testing.T) {
	for body, code := range map[string]string{
		`{"image":"evil:latest"}`:                   "rerun_change_refused",
		`{"shifts":{"memory":"8Gi"},"env":{"A":1}}`: "rerun_change_refused",
		`{"shifts":{"command":"sh"}}`:               "rerun_change_refused",
		`{"shifts":{"memory":"lots"}}`:              "invalid_shift",
		`not json`:                                  "invalid_shift",
	} {
		st := failedJobStore()
		w := retryRequest(t, NewJobHandler(st, corndogs.NewMockClient()), body)
		var resp ErrorResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if w.Code != http.StatusBadRequest || resp.Error != code {
			t.Errorf("%s: status %d error %q, want 400 %q", body, w.Code, resp.Error, code)
		}
		if len(st.CreateJobCalls) != 0 {
			t.Errorf("%s: a refused retry created a job", body)
		}
	}
}

func TestRetryJobREST_EmptyBodyIsPlainRetry(t *testing.T) {
	st := failedJobStore()
	w := retryRequest(t, NewJobHandler(st, corndogs.NewMockClient()), "")
	if w.Code != http.StatusCreated || len(st.CreateJobCalls) != 1 || st.CreateJobCalls[0].Shifts != nil {
		t.Fatalf("status %d, calls %d", w.Code, len(st.CreateJobCalls))
	}
}
