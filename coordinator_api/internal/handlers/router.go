package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/middleware"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"

	"github.com/rs/cors"
)

var (
	// Singleton instance of the app's ServeMux
	appMux *http.ServeMux
	// Corndogs client for the singleton
	singletoncorndogsClient corndogs.ClientInterface
	// Master key manager for secrets (singleton)
	singletonKeyManager *secrets.MasterKeyManager
	// Object store for logs and artifacts (singleton)
	singletonObjectStore objects.ObjectStore
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

// SetObjectStore sets the singleton object store (useful for testing)
func SetObjectStore(store objects.ObjectStore) {
	singletonObjectStore = store
}

// GetObjectStore returns the singleton object store
func GetObjectStore() objects.ObjectStore {
	return singletonObjectStore
}

// ResetAppMux resets the app mux singleton (useful for testing)
func ResetAppMux() {
	appMux = nil
	singletoncorndogsClient = nil
	singletonObjectStore = nil
	singletonKeyManager = nil
}

// createAppMux creates and configures the application ServeMux with all routes
func createAppMux() *http.ServeMux {
	mux := http.NewServeMux()

	// Initialize object store if not already done
	if singletonObjectStore == nil {
		objectStoreConfig := objects.ObjectStoreConfig{
			Type: config.ObjectStoreType,
			Config: map[string]string{
				"base_path": config.ObjectStoreBasePath,
				"bucket":    config.ObjectStoreBucket,
				"prefix":    config.ObjectStorePrefix,
			},
		}
		var err error
		singletonObjectStore, err = objects.NewObjectStore(objectStoreConfig)
		if err != nil {
			log.Printf("WARNING: Failed to initialize object store: %v - log retrieval will be unavailable", err)
		}
	}

	// Create handlers
	jobHandler := NewJobHandlerWithObjectStore(store.AppStore, singletoncorndogsClient, singletonObjectStore)
	tokenHandler := NewTokenHandler(store.AppStore)
	webhookHandler := NewWebhookHandler(store.AppStore, singletoncorndogsClient)
	projectHandler := NewProjectHandler(store.AppStore)

	// Wire VCS clients into the webhook handler
	vcsManager := vcs.NewManager()
	for provider, client := range vcsManager.GetClients() {
		webhookHandler.AddVCSClient(provider, client)
	}
	if secret := vcsManager.GetWebhookSecret(); secret != "" {
		webhookHandler.SetWebhookSecret(secret)
	}

	// Create secrets handler - keys are loaded from env, DB, or auto-generated
	var secretsHandler *SecretsHandler
	if singletonKeyManager == nil {
		if db := store.GetDB(); db != nil {
			// LoadOrCreateMasterKeys tries: env var → database → auto-generate
			if keyMgr, err := secrets.LoadOrCreateMasterKeys(db); err == nil {
				singletonKeyManager = keyMgr
			}
			// If err != nil, secrets will be unavailable but app continues
		}
	}
	if singletonKeyManager != nil {
		secretsHandler = NewSecretsHandler(store.AppStore, singletonKeyManager)
	}

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

			// Handle the special case for job_id/logs
			if strings.HasSuffix(path, "/logs") {
				jobID := strings.TrimSuffix(path, "/logs")
				r = r.WithContext(setIDContext(r.Context(), "job_id", jobID))
				if r.Method == http.MethodGet {
					jobHandler.GetJobLogs(w, r)
					return
				}
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}

			// Handle the special case for job_id/triggers
			if strings.HasSuffix(path, "/triggers") {
				jobID := strings.TrimSuffix(path, "/triggers")
				r = r.WithContext(setIDContext(r.Context(), "job_id", jobID))
				if r.Method == http.MethodPost {
					jobHandler.SubmitTriggers(w, r)
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

	// Project routes (require auth)
	mux.HandleFunc("/api/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				projectHandler.ListProjects(w, r)
			case http.MethodPost:
				projectHandler.CreateProject(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})))
		handler.ServeHTTP(w, r)
	})

	mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/projects/")
		if path == "" {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}

		r = r.WithContext(setIDContext(r.Context(), "project_id", path))

		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				projectHandler.GetProject(w, r)
			case http.MethodPut:
				projectHandler.UpdateProject(w, r)
			case http.MethodDelete:
				projectHandler.DeleteProject(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})))
		handler.ServeHTTP(w, r)
	})

	// Secrets routes (require auth and master keys to be configured)
	if secretsHandler != nil {
		// GET /api/v1/secrets?path=... - List keys in path
		// GET /api/v1/secrets/value?path=...&key=... - Get secret value
		// PUT /api/v1/secrets/value?path=...&key=... - Set secret value
		// DELETE /api/v1/secrets/value?path=...&key=... - Delete secret
		mux.HandleFunc("/api/v1/secrets", func(w http.ResponseWriter, r *http.Request) {
			handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					secretsHandler.ListKeys(w, r)
				} else {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				}
			})))
			handler.ServeHTTP(w, r)
		})

		mux.HandleFunc("/api/v1/secrets/value", func(w http.ResponseWriter, r *http.Request) {
			handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					secretsHandler.GetSecret(w, r)
				case http.MethodPut:
					secretsHandler.SetSecret(w, r)
				case http.MethodDelete:
					secretsHandler.DeleteSecret(w, r)
				default:
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				}
			})))
			handler.ServeHTTP(w, r)
		})

		// GET /api/v1/secrets/paths - List all paths
		mux.HandleFunc("/api/v1/secrets/paths", func(w http.ResponseWriter, r *http.Request) {
			handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					secretsHandler.ListPaths(w, r)
				} else {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				}
			})))
			handler.ServeHTTP(w, r)
		})

		// POST /api/v1/secrets/init - Initialize secrets
		mux.HandleFunc("/api/v1/secrets/init", func(w http.ResponseWriter, r *http.Request) {
			handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					secretsHandler.InitSecrets(w, r)
				} else {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				}
			})))
			handler.ServeHTTP(w, r)
		})

		// POST /api/v1/secrets/batch/get - Batch get secrets
		mux.HandleFunc("/api/v1/secrets/batch/get", func(w http.ResponseWriter, r *http.Request) {
			handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					secretsHandler.BatchGet(w, r)
				} else {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				}
			})))
			handler.ServeHTTP(w, r)
		})

		// POST /api/v1/secrets/batch/set - Batch set secrets
		mux.HandleFunc("/api/v1/secrets/batch/set", func(w http.ResponseWriter, r *http.Request) {
			handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					secretsHandler.BatchSet(w, r)
				} else {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				}
			})))
			handler.ServeHTTP(w, r)
		})

		// Admin endpoints for master key management (require admin role)
		adminMiddleware := middleware.RequireRoleMiddleware("admin")

		// POST /api/v1/admin/secrets/master-keys - Create master key
		// GET /api/v1/admin/secrets/master-keys - List master keys
		mux.HandleFunc("/api/v1/admin/secrets/master-keys", func(w http.ResponseWriter, r *http.Request) {
			handler := transactionMiddleware(authMiddleware(adminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodPost:
					secretsHandler.CreateMasterKey(w, r)
				case http.MethodGet:
					secretsHandler.ListMasterKeys(w, r)
				default:
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				}
			}))))
			handler.ServeHTTP(w, r)
		})

		// POST /api/v1/admin/secrets/master-keys/{name}/rotate - Rotate to key
		// DELETE /api/v1/admin/secrets/master-keys/{name} - Decommission key
		mux.HandleFunc("/api/v1/admin/secrets/master-keys/", func(w http.ResponseWriter, r *http.Request) {
			path := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/secrets/master-keys/")
			if path == "" {
				http.Error(w, "Invalid path", http.StatusBadRequest)
				return
			}

			handler := transactionMiddleware(authMiddleware(adminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Handle {name}/rotate
				if strings.HasSuffix(path, "/rotate") {
					keyName := strings.TrimSuffix(path, "/rotate")
					r = r.WithContext(setIDContext(r.Context(), "key_name", keyName))
					if r.Method == http.MethodPost {
						secretsHandler.RotateMasterKey(w, r)
						return
					}
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
					return
				}

				// Handle {name} for DELETE (decommission)
				r = r.WithContext(setIDContext(r.Context(), "key_name", path))
				if r.Method == http.MethodDelete {
					secretsHandler.DecommissionMasterKey(w, r)
					return
				}
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}))))
			handler.ServeHTTP(w, r)
		})

		// POST /api/v1/admin/secrets/sync-primary - Sync primary from env
		mux.HandleFunc("/api/v1/admin/secrets/sync-primary", func(w http.ResponseWriter, r *http.Request) {
			handler := transactionMiddleware(authMiddleware(adminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					secretsHandler.SyncPrimary(w, r)
				} else {
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				}
			}))))
			handler.ServeHTTP(w, r)
		})
	}

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
