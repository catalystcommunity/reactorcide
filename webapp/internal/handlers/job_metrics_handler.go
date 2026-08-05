package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

func (h *WebHandler) getJobLogs(r *http.Request, jobID, stream string) ([]LogEntry, error) {
	if h.uiClients == nil {
		return h.client.GetJobLogs(jobID, stream)
	}
	entries := make([]LogEntry, 0)
	var cursor *string
	pageSize := int64(5000)
	for {
		response, err := h.uiClients.Ui.GetJobLogs(h.authContext(r), csilapi.GetJobLogsRequest{JobId: jobID, Stream: stream, Cursor: cursor, MaxEntries: &pageSize})
		if err != nil {
			return nil, err
		}
		for _, entry := range response.Entries {
			entries = append(entries, LogEntry{Timestamp: entry.Timestamp, Stream: entry.Stream, Level: entry.Level, Message: entry.Message})
		}
		if response.HasMore == nil || !*response.HasMore || response.NextCursor == nil {
			break
		}
		cursor = response.NextCursor
	}
	return entries, nil
}

func (h *WebHandler) authorizeJobView(w http.ResponseWriter, r *http.Request, jobID string) bool {
	if h.uiClients == nil {
		return true
	}
	_, err := h.uiClients.Ui.GetJobMetrics(h.authContext(r), csilapi.GetJobMetricsRequest{
		JobId: jobID, MaxPoints: 1,
	})
	if err != nil {
		h.renderServiceError(w, r, err)
		return false
	}
	return true
}

// JobMetrics returns graph-ready data through the caller's UI session.
func (h *WebHandler) JobMetrics(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		http.NotFound(w, r)
		return
	}
	if h.uiClients == nil {
		http.Error(w, "metrics are unavailable", http.StatusServiceUnavailable)
		return
	}
	maxPoints := int64(900)
	if raw := r.URL.Query().Get("max_points"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 || parsed > 2000 {
			http.Error(w, "invalid max_points", http.StatusBadRequest)
			return
		}
		maxPoints = parsed
	}
	query := r.URL.Query()
	req := csilapi.GetJobMetricsRequest{
		JobId: jobID, Metrics: query["metric"], MaxPoints: maxPoints,
	}
	if value := query.Get("from"); value != "" {
		req.FromTime = &value
	}
	if value := query.Get("to"); value != "" {
		req.ToTime = &value
	}
	if value := query.Get("cursor"); value != "" {
		req.Cursor = &value
	}
	response, err := h.uiClients.Ui.GetJobMetrics(h.authContext(r), req)
	if err != nil {
		status, detail, _ := serviceErrorDetail(err)
		http.Error(w, detail, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}
