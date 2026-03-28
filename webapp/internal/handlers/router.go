package handlers

import (
	"net/http"
)

// NewRouter creates the HTTP handler with all routes
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	client := NewAPIClient()
	webHandler := NewWebHandler(client)

	// Health check at root for k8s probes
	mux.HandleFunc("GET /", webHandler.HealthCheck)

	// Web UI routes under /app/
	mux.HandleFunc("GET /app/", webHandler.JobsList)
	mux.HandleFunc("GET /app/jobs", webHandler.JobsList)
	mux.HandleFunc("GET /app/jobs/{id}", webHandler.JobDetail)
	mux.HandleFunc("GET /app/jobs/{id}/logs", webHandler.JobLogs)

	return mux
}
