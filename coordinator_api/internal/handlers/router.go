package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/middleware"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"

	"github.com/rs/cors"
)

var (
	// Singleton instance of the app's ServeMux
	appMux *http.ServeMux
	// Corndogs client for the singleton
	singletoncorndogsClient corndogs.ClientInterface
)

// GetAppMux returns the application's HTTP ServeMux for both API and tests
// This ensures all tests use the same router configuration as the actual application
func GetAppMux() *http.ServeMux {
	return GetAppMuxWithClient(nil)
}

// GetAppMuxWithClient returns the application's HTTP ServeMux with optional Corndogs client
func GetAppMuxWithClient(corndogsClient corndogs.ClientInterface) *http.ServeMux {
	// Only create the mux once
	if appMux == nil {
		singletoncorndogsClient = corndogsClient
		appMux = createAppMux()
	}
	return appMux
}

// createAppMux creates and configures the application ServeMux with all routes
func createAppMux() *http.ServeMux {
	mux := http.NewServeMux()

	// Create handlers
	jobHandler := NewJobHandler(store.AppStore, singletoncorndogsClient)
	tokenHandler := NewTokenHandler(store.AppStore)
	webhookHandler := NewWebhookHandler(store.AppStore)

	// Apply middleware to all handlers
	transactionMiddleware := middleware.TransactionMiddleware
	authMiddleware := middleware.APITokenMiddleware(store.AppStore)

	// Health check endpoint
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		transactionMiddleware(http.HandlerFunc(healthHandler)).ServeHTTP(w, r)
	})

	// API v1 routes with API token authentication

	// Health check endpoint (v1, no auth required)
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		transactionMiddleware(http.HandlerFunc(healthHandler)).ServeHTTP(w, r)
	})

	// Metrics endpoint (v1, no auth required)
	mux.Handle("/api/v1/metrics", metrics.Handler())

	// Job routes (require auth)
	mux.HandleFunc("/api/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				jobHandler.ListJobs(w, r)
			case http.MethodPost:
				jobHandler.CreateJob(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})))
		handler.ServeHTTP(w, r)
	})

	mux.HandleFunc("/api/v1/jobs/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/jobs/")
		if path == "" {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}

		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Handle the special case for job_id/cancel
			if strings.HasSuffix(path, "/cancel") {
				jobID := strings.TrimSuffix(path, "/cancel")
				r = r.WithContext(setIDContext(r.Context(), "job_id", jobID))
				if r.Method == http.MethodPut {
					jobHandler.CancelJob(w, r)
					return
				}
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}

			// Regular job ID routes
			r = r.WithContext(setIDContext(r.Context(), "job_id", path))
			switch r.Method {
			case http.MethodGet:
				jobHandler.GetJob(w, r)
			case http.MethodDelete:
				jobHandler.DeleteJob(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})))
		handler.ServeHTTP(w, r)
	})

	// Token management routes (require auth)
	mux.HandleFunc("/api/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				tokenHandler.ListTokens(w, r)
			case http.MethodPost:
				tokenHandler.CreateToken(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})))
		handler.ServeHTTP(w, r)
	})

	mux.HandleFunc("/api/v1/tokens/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/tokens/")
		if path == "" {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}

		r = r.WithContext(setIDContext(r.Context(), "token_id", path))

		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodDelete:
				tokenHandler.DeleteToken(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})))
		handler.ServeHTTP(w, r)
	})

	// Webhook routes (no auth required but validated by signature)
	mux.HandleFunc("/api/v1/webhooks/github", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		transactionMiddleware(http.HandlerFunc(webhookHandler.HandleGitHubWebhook)).ServeHTTP(w, r)
	})

	mux.HandleFunc("/api/v1/webhooks/gitlab", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		transactionMiddleware(http.HandlerFunc(webhookHandler.HandleGitLabWebhook)).ServeHTTP(w, r)
	})

	return mux
}

// setIDContext adds an ID to the context for handlers to use
// This replaces the mux.Vars functionality from gorilla/mux
type contextKey string

func setIDContext(ctx context.Context, key, value string) context.Context {
	return context.WithValue(ctx, contextKey(key), value)
}

// GetIDFromContext gets an ID from the context
func GetIDFromContext(r *http.Request, key string) string {
	if value, ok := r.Context().Value(contextKey(key)).(string); ok {
		return value
	}
	return ""
}

// GetContextKey returns a context key of the same type used internally
func GetContextKey(key string) contextKey {
	return contextKey(key)
}

// NewRouter creates a new router for the API with CORS handling
// This is used by the API server
func NewRouter(corndogsClient corndogs.ClientInterface) http.Handler {
	mux := GetAppMuxWithClient(corndogsClient)

	// Set up CORS
	c := cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		AllowCredentials: true,
	})

	return c.Handler(mux)
}

// Add a health endpoint that includes verification info
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Get verification status from context
	verified := checkauth.GetVerifiedFromContext(r.Context())
	user := checkauth.GetUserFromContext(r.Context())

	response := map[string]interface{}{
		"status": "OK",
		"verification": map[string]interface{}{
			"verified":           verified,
			"user_authenticated": user != nil,
		},
	}

	// Include user info if available
	if user != nil {
		response["verification"].(map[string]interface{})["user_id"] = user.UserID
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}
