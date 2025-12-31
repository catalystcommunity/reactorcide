package cmd

import (
	"fmt"
	"net/http"
	"time"

	"github.com/catalystcommunity/app-utils-go/errorutils"
	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/gammazero/workerpool"
)

var Server *http.ServeMux

func Serve() error {
	// Run migrations first (with advisory lock for concurrent safety)
	if err := RunMigrations(); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	// set stores
	store.AppStore = postgres_store.PostgresStore

	// init stores and defer any functions we need to
	deferredStoreFuncs := initStores()
	for _, deferredFunc := range deferredStoreFuncs {
		defer deferredFunc()
	}

	// Initialize Corndogs client if configured
	var corndogsClient *corndogs.Client
	if config.CornDogsBaseURL != "" {
		client, err := corndogs.NewClient(corndogs.Config{
			BaseURL:      config.CornDogsBaseURL,
			QueueName:    config.DefaultQueueName,
			Timeout:      time.Duration(config.DefaultTimeout) * time.Second,
			MaxRetries:   3,
			RetryBackoff: time.Second,
		})
		if err != nil {
			logging.Log.WithError(err).Error("Failed to initialize Corndogs client")
			// Continue without Corndogs - jobs will be created but not queued
		} else {
			corndogsClient = client
			defer client.Close()
			logging.Log.Info("Corndogs client initialized")
		}
	} else {
		logging.Log.Warn("Corndogs not configured - jobs will not be queued")
	}

	// Create the handler with routes
	handler := handlers.NewRouter(corndogsClient)

	// Log startup information
	logging.Log.Infof("Starting HTTP server on port %d", config.Port)

	// Start the HTTP server
	err := http.ListenAndServe(fmt.Sprintf(":%d", config.Port), handler)

	// ListenAndServe always eventually errors out, so we log it and return it
	errorutils.LogOnErr(nil, "ListenAndServe exited with: ", err)
	return err
}

func initStores() []func() {
	// initialize stores using a worker pool to speed up startup
	pool := workerpool.New(5)
	deferredFunctions := []func(){}

	pool.Submit(func() {
		deferredFunc, err := store.AppStore.Initialize()
		errorutils.PanicOnErr(nil, "error initializing app store", err)
		if deferredFunc != nil {
			deferredFunctions = append(deferredFunctions, deferredFunc)
		}
		logging.Log.Info("app store initialized")

		// Ensure default user exists if configured
		if err := store.AppStore.EnsureDefaultUser(); err != nil {
			logging.Log.WithError(err).Error("Failed to ensure default user")
		} else {
			logging.Log.Info("Default user check completed")
		}
	})

	pool.StopWait()
	return deferredFunctions
}
