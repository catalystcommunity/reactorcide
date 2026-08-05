package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

func TestJobLogsJSONForwardsCursorAndReturnsDelta(t *testing.T) {
	fc := newFakeCoordinator()
	fc.handle("ReactorcideUi", "get-job-logs", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeGetJobLogsRequest(payload)
		if err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Cursor == nil || *req.Cursor != "cursor-a" || req.MaxEntries == nil || *req.MaxEntries != 25 {
			t.Fatalf("request = %+v", req)
		}
		nextCursor, complete := "cursor-b", true
		return csilapi.EncodeGetJobLogsResponse(csilapi.GetJobLogsResponse{
			Entries:    []csilapi.JobLogEntry{{Timestamp: "2026-08-04T19:00:00Z", Stream: "stdout", Level: "info", Message: "delta"}},
			NextCursor: &nextCursor, Complete: &complete,
		}), "GetJobLogsResponse", false
	})
	h := newTestWebHandler(t, fc)
	req := httptest.NewRequest(http.MethodGet, "/app/jobs/job-a/logs?stream=combined&cursor=cursor-a&max_entries=25", nil)
	req.SetPathValue("id", "job-a")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	h.JobLogs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response csilapi.GetJobLogsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Entries) != 1 || response.Entries[0].Message != "delta" || response.NextCursor == nil || *response.NextCursor != "cursor-b" {
		t.Fatalf("response = %+v", response)
	}
}

func TestJobLogsJSONShowsCoordinatorFailure(t *testing.T) {
	fc := newFakeCoordinator()
	fc.handle("ReactorcideUi", "get-job-logs", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return csilapi.EncodeServiceError(csilapi.ServiceError{Code: "forbidden", Message: "not allowed"}), "ServiceError", true
	})
	h := newTestWebHandler(t, fc)
	req := httptest.NewRequest(http.MethodGet, "/app/jobs/job-a/logs?stream=combined", nil)
	req.SetPathValue("id", "job-a")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	h.JobLogs(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
