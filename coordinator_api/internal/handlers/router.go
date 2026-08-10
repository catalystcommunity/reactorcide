package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/middleware"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcsreport"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi"
	workercsilapi "github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerauth"

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
	// Pub/sub bus for live updates — optional; nil disables the WS endpoints.
	singletonBus *pubsub.Bus
)

// SetPubSubBus sets the bus used by the WebSocket endpoints. Must be called
// before GetAppMux if WS routes should be registered.
func SetPubSubBus(b *pubsub.Bus) {
	singletonBus = b
}

// GetAppMux returns the application's HTTP ServeMux for both API and tests
// This ensures all tests use the same router configuration as the actual
// application
func GetAppMux() *http.ServeMux {
	return GetAppMuxWithClient(nil)
}

// GetAppMuxWithClient returns the application's HTTP ServeMux with optional
// Corndogs client
func GetAppMuxWithClient(corndogsClient corndogs.ClientInterface) *http.ServeMux {
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

// ResetAppMux resets the app mux singleton (useful for testing)
func ResetAppMux() {
	appMux = nil
	singletoncorndogsClient = nil
	singletonObjectStore = nil
	singletonKeyManager = nil
	singletonBus = nil
}

// createAppMux creates and configures the application ServeMux with all
// routes
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
	startTelemetryRetentionOnce(singletonObjectStore, store.AppStore)
	startAuditRetentionOnce(store.AppStore)

	// Create handlers
	jobHandler := NewJobHandlerWithObjectStore(store.AppStore, singletoncorndogsClient, singletonObjectStore)
	tokenHandler := NewTokenHandler(store.AppStore)
	webhookHandler := NewWebhookHandler(store.AppStore, singletoncorndogsClient)
	projectHandler := NewProjectHandler(store.AppStore)
	organizationHandler := NewOrganizationHandler(store.AppStore)
	profileHandler := NewProfileHandler(store.AppStore)
	auditHandler := NewAuditHandler(store.AppStore)
	ciSecurityHandler := NewCISecurityHandler(store.AppStore)
	workerClassHandler := NewWorkerClassHandler(store.AppStore)
	workflowHandler := NewWorkflowHandlerWithCorndogs(store.AppStore, singletoncorndogsClient)

	// Wire VCS clients into the webhook handler and the job handler's trigger
	// processor, so jobs submitted via /api/v1/jobs/{id}/triggers register as
	// pending checks on their commit at creation time.
	vcsManager := vcs.NewManager()
	for provider, client := range vcsManager.GetClients() {
		webhookHandler.AddVCSClient(provider, client)
	}
	jobHandler.SetStatusUpdater(vcsManager.GetStatusUpdater())
	webhookHandler.SetStatusUpdater(vcsManager.GetStatusUpdater())
	if reportStore, ok := store.AppStore.(vcsreport.Store); ok {
		reconciler := &vcsreport.Reconciler{Store: reportStore, Clients: vcs.NewReportClientResolver(vcsManager.GetStatusUpdater())}
		startVCSReportReconcilerOnce(reconciler)
	}

	// Wire per-project VCS token resolution into webhook handler. Deferred
	// until after the key manager is initialized below.
	wireWebhookTokenResolver := func(keyMgr *secrets.MasterKeyManager) {
		if keyMgr == nil {
			return
		}
		tokenResolver := makeTokenResolver(keyMgr)
		clientFactory := func(provider vcs.Provider, token string) (vcs.Client, error) {
			return vcsManager.CreateClientWithToken(provider, token)
		}
		webhookHandler.SetTokenResolver(tokenResolver)
		webhookHandler.SetClientFactory(clientFactory)
		statusUpdater := vcsManager.GetStatusUpdater()
		statusUpdater.SetProjectLookup(store.AppStore.GetProjectByID)
		statusUpdater.SetUserLookup(store.AppStore.GetUserByID)
		statusUpdater.SetTokenResolver(tokenResolver)
		statusUpdater.SetClientFactory(clientFactory)
		log.Println("Per-project VCS token resolution enabled for webhook handler")
	}

	// Create secrets handler - keys are loaded from env, DB, or
	// auto-generated
	var secretsHandler *SecretsHandler
	if singletonKeyManager == nil {
		if db := store.GetDB(); db != nil {
			// LoadOrCreateMasterKeys tries: env var → database →
			// auto-generate
			if keyMgr, err := secrets.LoadOrCreateMasterKeys(db); err == nil {
				singletonKeyManager = keyMgr
			}
			// If err != nil, secrets will be unavailable but app continues
		}
	}
	if singletonKeyManager != nil {
		secretsHandler = NewSecretsHandler(store.AppStore, singletonKeyManager)
		wireWebhookTokenResolver(singletonKeyManager)
	}

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
	mux.HandleFunc("/api/v1/organizations", func(w http.ResponseWriter, r *http.Request) {
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				organizationHandler.List(w, r)
			case http.MethodPost:
				organizationHandler.Create(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})))
		handler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/v1/audit", func(w http.ResponseWriter, r *http.Request) {
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			auditHandler.List(w, r)
		})))
		handler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/v1/vcs-identities", func(w http.ResponseWriter, r *http.Request) {
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ciSecurityHandler.Identities(w, r, "") })))
		handler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/v1/vcs-identities/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/vcs-identities/")
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ciSecurityHandler.Identities(w, r, id) })))
		handler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/v1/approvals", func(w http.ResponseWriter, r *http.Request) {
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", 405)
				return
			}
			ciSecurityHandler.Approve(w, r)
		})))
		handler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/v1/organizations/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/organizations/")
		if parts := strings.Split(path, "/"); len(parts) >= 2 && parts[1] == "profiles" {
			profileName := ""
			if len(parts) == 3 {
				profileName = parts[2]
			} else if len(parts) != 2 {
				http.Error(w, "Invalid path", http.StatusBadRequest)
				return
			}
			handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				profileHandler.ServeHTTP(w, r, parts[0], profileName)
			})))
			handler.ServeHTTP(w, r)
			return
		}
		if parts := strings.Split(path, "/"); len(parts) >= 2 && parts[1] == "worker-classes" {
			className, poolID := "", ""
			if len(parts) >= 3 {
				className = parts[2]
			}
			if len(parts) == 5 && parts[3] == "pools" {
				poolID = parts[4]
			} else if len(parts) > 3 {
				http.Error(w, "Invalid path", 400)
				return
			}
			handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				workerClassHandler.ServeHTTP(w, r, parts[0], className, poolID)
			})))
			handler.ServeHTTP(w, r)
			return
		}
		setDefault := strings.HasSuffix(path, "/default")
		if setDefault {
			path = strings.TrimSuffix(path, "/default")
		}
		if path == "" {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}
		r = r.WithContext(setIDContext(r.Context(), "organization_name", path))
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if setDefault && r.Method == http.MethodPut {
				organizationHandler.SetDefault(w, r)
				return
			}
			switch r.Method {
			case http.MethodGet:
				organizationHandler.Get(w, r)
			case http.MethodPut, http.MethodPatch:
				organizationHandler.Update(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})))
		handler.ServeHTTP(w, r)
	})

	// Workflow routes (require auth)
	mux.HandleFunc("/api/v1/workflows", func(w http.ResponseWriter, r *http.Request) {
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				workflowHandler.ListWorkflows(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})))
		handler.ServeHTTP(w, r)
	})

	mux.HandleFunc("/api/v1/workflows/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/workflows/")
		if path == "" {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}
		// Handle the special case for workflow_id/cancel
		if strings.HasSuffix(path, "/cancel") {
			workflowID := strings.TrimSuffix(path, "/cancel")
			r = r.WithContext(setIDContext(r.Context(), "workflow_id", workflowID))
			handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					workflowHandler.CancelWorkflow(w, r)
					return
				}
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			})))
			handler.ServeHTTP(w, r)
			return
		}

		// Handle the special case for workflow_id/retry-unsuccessful. Must be
		// checked before the plain "/retry" suffix below since neither is a
		// prefix of the other, but keeping this one first mirrors the
		// (more-specific-first) ordering used for jobs/{id}/{cancel,kill}.
		if strings.HasSuffix(path, "/retry-unsuccessful") {
			workflowID := strings.TrimSuffix(path, "/retry-unsuccessful")
			r = r.WithContext(setIDContext(r.Context(), "workflow_id", workflowID))
			handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					workflowHandler.RetryUnsuccessfulJobs(w, r)
					return
				}
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			})))
			handler.ServeHTTP(w, r)
			return
		}

		// Handle the special case for workflow_id/retry
		if strings.HasSuffix(path, "/retry") {
			workflowID := strings.TrimSuffix(path, "/retry")
			r = r.WithContext(setIDContext(r.Context(), "workflow_id", workflowID))
			handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					workflowHandler.RetryWorkflow(w, r)
					return
				}
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			})))
			handler.ServeHTTP(w, r)
			return
		}

		r = r.WithContext(setIDContext(r.Context(), "workflow_id", path))
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				workflowHandler.GetWorkflow(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})))
		handler.ServeHTTP(w, r)
	})

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

			// Handle the special case for job_id/kill
			if strings.HasSuffix(path, "/kill") {
				jobID := strings.TrimSuffix(path, "/kill")
				r = r.WithContext(setIDContext(r.Context(), "job_id", jobID))
				if r.Method == http.MethodPost {
					jobHandler.KillJob(w, r)
					return
				}
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}

			// Handle the special case for job_id/retry
			if strings.HasSuffix(path, "/retry") {
				jobID := strings.TrimSuffix(path, "/retry")
				r = r.WithContext(setIDContext(r.Context(), "job_id", jobID))
				if r.Method == http.MethodPost {
					jobHandler.RetryJob(w, r)
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

			// Handle the special case for job_id/metrics.
			if strings.HasSuffix(path, "/metrics") {
				jobID := strings.TrimSuffix(path, "/metrics")
				r = r.WithContext(setIDContext(r.Context(), "job_id", jobID))
				if r.Method == http.MethodGet {
					jobHandler.GetJobMetrics(w, r)
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

	// WebSocket streams for live job/log updates. Auth same as REST. The
	// upgrade handshake itself runs through the standard middleware stack;
	// everything after the upgrade is long-lived.
	if singletonBus != nil {
		wsHandler := NewWSHandler(singletonBus, store.AppStore)

		mux.HandleFunc("/api/v1/jobs/stream", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			authMiddleware(http.HandlerFunc(wsHandler.StreamAllJobs)).ServeHTTP(w, r)
		})

		mux.HandleFunc("/api/v1/jobs/stream/", func(w http.ResponseWriter, r *http.Request) {
			// /api/v1/jobs/stream/{job_id}
			if r.Method != http.MethodGet {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			jobID := strings.TrimPrefix(r.URL.Path, "/api/v1/jobs/stream/")
			if jobID == "" {
				http.Error(w, "Invalid path", http.StatusBadRequest)
				return
			}
			r = r.WithContext(setIDContext(r.Context(), "job_id", jobID))
			authMiddleware(http.HandlerFunc(wsHandler.StreamJob)).ServeHTTP(w, r)
		})
	}

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

	mux.HandleFunc("/api/v1/secret-grants", func(w http.ResponseWriter, r *http.Request) {
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				projectHandler.ListGlobalSecretGrants(w, r)
			case http.MethodPost:
				projectHandler.CreateGlobalSecretGrant(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		})))
		handler.ServeHTTP(w, r)
	})

	mux.HandleFunc("/api/v1/secret-grants/apply", func(w http.ResponseWriter, r *http.Request) {
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			projectHandler.ApplySecretGrants(w, r)
		})))
		handler.ServeHTTP(w, r)
	})

	mux.HandleFunc("/api/v1/secret-grants/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/secret-grants/")
		if path == "" || strings.Contains(path, "/") {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}
		r = r.WithContext(setIDContext(r.Context(), "grant_id", path))
		handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				projectHandler.GetGlobalSecretGrant(w, r)
			case http.MethodPatch, http.MethodPut:
				projectHandler.UpdateGlobalSecretGrant(w, r)
			case http.MethodDelete:
				projectHandler.DeleteGlobalSecretGrant(w, r)
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

		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) >= 2 && parts[1] == "secret-grants" {
			r = r.WithContext(setIDContext(r.Context(), "project_id", parts[0]))
			if len(parts) == 3 {
				r = r.WithContext(setIDContext(r.Context(), "grant_id", parts[2]))
			}
			handler := transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case len(parts) == 2 && r.Method == http.MethodGet:
					projectHandler.ListSecretGrants(w, r)
				case len(parts) == 2 && r.Method == http.MethodPost:
					projectHandler.CreateSecretGrant(w, r)
				case len(parts) == 3 && r.Method == http.MethodGet:
					projectHandler.GetSecretGrant(w, r)
				case len(parts) == 3 && (r.Method == http.MethodPatch || r.Method == http.MethodPut):
					projectHandler.UpdateSecretGrant(w, r)
				case len(parts) == 3 && r.Method == http.MethodDelete:
					projectHandler.DeleteSecretGrant(w, r)
				default:
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				}
			})))
			handler.ServeHTTP(w, r)
			return
		}

		if len(parts) != 1 {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}

		r = r.WithContext(setIDContext(r.Context(), "project_id", parts[0]))

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
		// GET    /api/v1/secrets?path=...                 - List keys in path
		// GET    /api/v1/secrets/value?path=...&key=...   - Get secret value
		// PUT    /api/v1/secrets/value?path=...&key=...   - Set secret value
		// DELETE /api/v1/secrets/value?path=...&key=...   - Delete secret
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
		// GET  /api/v1/admin/secrets/master-keys - List master keys
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

		// POST   /api/v1/admin/secrets/master-keys/{name}/rotate - Rotate to key
		// DELETE /api/v1/admin/secrets/master-keys/{name}        - Decommission key
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

	// CSIL-RPC UI/Auth endpoint. Real auth/authz/store-backed implementations
	// when store.AppStore satisfies uiapi.DataStore (always true for
	// *postgres_store.PostgresDbStore in production); stub implementations
	// (ServiceError{code:"unimplemented"} for every op) otherwise, e.g.
	// against a minimal test store that doesn't implement the full
	// rbac/rotation/settings/trusted-identity surface.
	var uiAuthImpl csilapi.ReactorcideAuth = uiapi.NewStubAuth()
	var uiUiImpl csilapi.ReactorcideUi = uiapi.NewStubUi()
	if uiStore, ok := store.AppStore.(uiapi.DataStore); ok {
		if deps := buildUIAPIDeps(uiStore, singletonKeyManager, singletoncorndogsClient, singletonObjectStore); deps != nil {
			uiAuthImpl = uiapi.NewAuthService(deps)
			uiUiImpl = uiapi.NewUiService(deps)
		}
	}

	// ReactorcideWorker is mounted on the SAME /csil/v1/rpc endpoint as
	// ReactorcideAuth/ReactorcideUi -- CSIL-RPC routes on the envelope's own
	// `service` field, so one HTTP handler serves every service sharing this
	// transport. nil (no worker ops registered; "ReactorcideWorker" resolves
	// as an unknown route) when store.AppStore doesn't satisfy
	// workerapi.DataStore, e.g. a minimal test store.
	var workerImpl workercsilapi.ReactorcideWorker
	if workerStore, ok := store.AppStore.(workerapi.DataStore); ok {
		workerDeps := buildWorkerAPIDeps(workerStore, singletoncorndogsClient, singletonKeyManager, singletonObjectStore)
		// Wire workflow progression into the coordinator-mediated job
		// lifecycle (ReportResult/RequestJob): a TriggerProcessor over the
		// concrete store + the same configured VCS status updater the
		// webhook/job handlers use, so completing a job's node re-evaluates
		// its workflow (submits ready downstream nodes, rolls
		// workflow_instances status, updates the PR check) instead of leaving
		// the workflow stuck "running".
		wfFinalizer := worker.NewTriggerProcessor(store.AppStore, singletoncorndogsClient)
		wfFinalizer.SetStatusUpdater(vcsManager.GetStatusUpdater())
		workerDeps.WorkflowFinalizer = wfFinalizer
		// Resolve a completing job's own VCS check (e.g. the eval's
		// "reactorcide/eval" pending check) to success/failure on
		// ReportResult.
		workerDeps.JobStatusReporter = vcsManager.GetStatusUpdater()
		workerSvc := workerapi.NewWorkerService(workerDeps)
		workerImpl = workerSvc
		startWorkerLeaseReaperOnce(workerSvc)
	}
	mux.Handle(uiapi.RpcPath, uiapi.NewHandlerWithWorker(uiAuthImpl, uiUiImpl, workerImpl))

	return mux
}

// workerLeaseReaperOnce ensures the worker-lease reaper background loop
// starts at most once per process, even though createAppMux may run more than
// once in tests via ResetAppMux/ GetAppMuxWithClient.
var workerLeaseReaperOnce sync.Once
var telemetryRetentionOnce sync.Once
var auditRetentionOnce sync.Once
var vcsReportReconcilerOnce sync.Once

func startVCSReportReconcilerOnce(reconciler *vcsreport.Reconciler) {
	vcsReportReconcilerOnce.Do(func() { go reconciler.Run(context.Background(), 5*time.Second) })
}

type auditPruner interface {
	PruneAuditEvents(context.Context, time.Time) (int64, error)
}

func startAuditRetentionOnce(appStore store.Store) {
	pruner, ok := appStore.(auditPruner)
	if !ok || config.AuditRetentionDays <= 0 {
		return
	}
	auditRetentionOnce.Do(func() {
		go func() {
			for {
				cutoff := time.Now().UTC().Add(-time.Duration(config.AuditRetentionDays) * 24 * time.Hour)
				if _, err := pruner.PruneAuditEvents(context.Background(), cutoff); err != nil {
					log.Printf("WARNING: audit retention failed: %v", err)
				}
				time.Sleep(24 * time.Hour)
			}
		}()
	})
}

func startTelemetryRetentionOnce(objectStore objects.ObjectStore, appStore store.Store) {
	if objectStore == nil || appStore == nil || config.TelemetryRetentionDays <= 0 {
		return
	}
	telemetryRetentionOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				cutoff := time.Now().UTC().Add(-time.Duration(config.TelemetryRetentionDays) * 24 * time.Hour)
				if _, err := jobtelemetry.PruneBefore(context.Background(), objectStore, cutoff, appStore.GetJobByID); err != nil {
					log.Printf("WARNING: telemetry retention failed: %v", err)
				}
				<-ticker.C
			}
		}()
	})
}

func startWorkerLeaseReaperOnce(svc *workerapi.WorkerService) {
	workerLeaseReaperOnce.Do(func() {
		go svc.RunLeaseReaper(context.Background())
	})
}

// buildWorkerAPIDeps wires the CSIL Worker service's dependencies,
// reusing the exact same store/corndogs/key-manager/object-store singletons
// buildUIAPIDeps wires for the sibling ReactorcideAuth/ReactorcideUi service,
// plus a dedicated pubsub.Publisher built the same way other coordinator-side
// publishers are wired (pubsub.NewPublisher is nil-pool-safe, so a deployment
// without a pgx pool simply gets a Publisher that drops every publish).
func buildWorkerAPIDeps(workerStore workerapi.DataStore, corndogsClient corndogs.ClientInterface, keyManager *secrets.MasterKeyManager, objectStore objects.ObjectStore) *workerapi.Deps {
	enrollment := workerauth.NewEnrollment(workerStore)
	sessions := workerauth.NewWorkerSessions(workerStore)
	publisher := pubsub.NewPublisher(postgres_store.PgxPool())
	return workerapi.NewDeps(workerStore, corndogsClient, enrollment, sessions, keyManager, objectStore, publisher)
}

// buildUIAPIDeps wires the CSIL UI service's dependencies. It seeds the
// trusted-identity admission list from config, selects a LoginBackend
// matching auth.CurrentMode() (falling back to the none-mode sentinel backend
// — login unavailable, but every other op still works — if the configured
// mode's backend can't be constructed, e.g. missing/invalid LinkKeys config),
// and builds a *uiapi.Deps. Returns nil only if ValidateUIAuthMode itself
// fails (a misconfigured auth mode), since in that case none of the
// auth/authz surface can be trusted to behave as configured.
func buildUIAPIDeps(uiStore uiapi.DataStore, keyManager *secrets.MasterKeyManager, corndogsClient corndogs.ClientInterface, objectStore objects.ObjectStore) *uiapi.Deps {
	if err := config.ValidateUIAuthMode(); err != nil {
		log.Printf("WARNING: REACTORCIDE_UI_AUTH_MODE is misconfigured, CSIL UI service will return unimplemented: %v", err)
		return nil
	}

	ctx := context.Background()
	if err := auth.SeedTrustedIdentitiesFromConfig(ctx, uiStore, config.TrustedIdentities); err != nil {
		log.Printf("WARNING: failed to seed trusted identities from REACTORCIDE_TRUSTED_IDENTITIES: %v", err)
	}

	var backend auth.LoginBackend = auth.NewNoneBackend()
	switch auth.CurrentMode() {
	case auth.ModeLocalRP:
		if keyManager == nil {
			log.Printf("WARNING: REACTORCIDE_UI_AUTH_MODE=local-rp but secrets are not configured; login will be unavailable")
			break
		}
		b, err := auth.NewLocalRPBackend(ctx, uiStore, keyManager)
		if err != nil {
			log.Printf("WARNING: failed to initialize local-rp login backend, login will be unavailable: %v", err)
			break
		}
		backend = b
	case auth.ModeRP:
		if keyManager == nil {
			log.Printf("WARNING: REACTORCIDE_UI_AUTH_MODE=rp but secrets are not configured; login will be unavailable")
			break
		}
		apiKey, err := auth.LoadOrBootstrapRPAPIKey(ctx, uiStore, keyManager)
		if err != nil {
			log.Printf("WARNING: failed to load rp api key, login will be unavailable: %v", err)
			break
		}
		b, err := auth.NewRPBackend(apiKey, nil)
		if err != nil {
			log.Printf("WARNING: failed to initialize rp login backend, login will be unavailable: %v", err)
			break
		}
		backend = b
	}

	deps := uiapi.NewDeps(uiStore, backend, keyManager, corndogsClient)
	deps.ObjectStore = objectStore
	return deps
}

// setIDContext adds an ID to the context for handlers to use This replaces
// the mux.Vars functionality from gorilla/mux
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

// NewRouter creates a new router for the API with CORS handling This is used
// by the API server
func NewRouter(corndogsClient corndogs.ClientInterface) http.Handler {
	mux := GetAppMuxWithClient(corndogsClient)

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

// makeTokenResolver creates a TokenResolverFunc that resolves "path:key"
// secret references using the database secrets provider under the default
// organization. Project-specific paths replace this with an org resolver.
func makeTokenResolver(keyManager *secrets.MasterKeyManager) vcs.TokenResolverFunc {
	return func(ctx context.Context, secretRef string) (string, error) {
		parts := strings.SplitN(secretRef, ":", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid secret reference %q: expected path:key", secretRef)
		}
		path, key := parts[0], parts[1]

		db := store.GetDB()
		if db == nil {
			return "", fmt.Errorf("database not available for secret resolution")
		}

		organizationStore, ok := store.AppStore.(interface {
			GetDefaultOrganization(context.Context) (*models.Organization, error)
		})
		if !ok {
			return "", fmt.Errorf("organization store is unavailable")
		}
		var (
			organization *models.Organization
			err          error
		)
		if orgID, scoped := vcs.OrganizationFromContext(ctx); scoped {
			byID, ok := store.AppStore.(interface {
				GetOrganizationByID(context.Context, string) (*models.Organization, error)
			})
			if !ok {
				return "", fmt.Errorf("organization store cannot resolve an organization ID")
			}
			organization, err = byID.GetOrganizationByID(ctx, orgID)
		} else {
			organization, err = organizationStore.GetDefaultOrganization(ctx)
		}
		if err != nil {
			return "", fmt.Errorf("resolving organization: %w", err)
		}
		orgKey, err := keyManager.GetOrgEncryptionKey(db, organization.OrgID)
		if err != nil {
			return "", fmt.Errorf("resolving org encryption key: %w", err)
		}

		provider, err := secrets.NewDatabaseProvider(db, organization.OrgID, orgKey)
		if err != nil {
			return "", fmt.Errorf("creating secrets provider: %w", err)
		}

		return provider.Get(ctx, path, key)
	}
}
