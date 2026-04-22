package handlers

import (
	"net/http"
)

// NewRouter creates the HTTP handler with all routes
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	client := NewAPIClient()
	webHandler := NewWebHandler(client)
	wsProxy := NewWSProxy()

	// Health check at root for k8s probes
	mux.HandleFunc("GET /", webHandler.HealthCheck)

	// Web UI routes under /app/
	mux.HandleFunc("GET /app/", webHandler.JobsList)
	mux.HandleFunc("GET /app/jobs", webHandler.JobsList)
	mux.HandleFunc("GET /app/jobs/{id}", webHandler.JobDetail)
	mux.HandleFunc("GET /app/jobs/{id}/logs", webHandler.JobLogs)

	// WebSocket streams. The browser connects here; we proxy to the
	// coordinator's WS endpoints using the server-side service token so
	// the token never reaches client JS.
	mux.HandleFunc("GET /app/ws/jobs", wsProxy.AllJobsStream)
	mux.HandleFunc("GET /app/ws/jobs/{id}", wsProxy.JobStream)

	return mux
}
