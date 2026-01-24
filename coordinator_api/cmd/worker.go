package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/urfave/cli/v2"
)

var WorkerCommand = &cli.Command{
	Name:  "worker",
	Usage: "Run the job processing worker",
	Flags: append(flags, workerFlags...),
	Action: func(ctx *cli.Context) error {
		return RunWorker(ctx)
	},
}

var workerFlags = []cli.Flag{
	&cli.StringFlag{
		Name:    "queue",
		Aliases: []string{"q"},
		Value:   "reactorcide-jobs",
		Usage:   "Queue name to process jobs from",
		EnvVars: []string{"REACTORCIDE_WORKER_QUEUE", "WORKER_QUEUE"},
	},
	&cli.IntFlag{
		Name:    "poll-interval",
		Aliases: []string{"p"},
		Value:   5,
		Usage:   "Poll interval in seconds for checking new jobs",
		EnvVars: []string{"REACTORCIDE_WORKER_POLL_INTERVAL", "WORKER_POLL_INTERVAL"},
	},
	&cli.IntFlag{
		Name:    "concurrency",
		Aliases: []string{"c"},
		Value:   1,
		Usage:   "Number of jobs to process concurrently",
		EnvVars: []string{"REACTORCIDE_WORKER_CONCURRENCY", "WORKER_CONCURRENCY"},
	},
	&cli.BoolFlag{
		Name:    "dry-run",
		Aliases: []string{"d"},
		Value:   false,
		Usage:   "Dry run mode - don't actually execute jobs",
		EnvVars: []string{"REACTORCIDE_WORKER_DRY_RUN", "WORKER_DRY_RUN"},
	},
	&cli.StringFlag{
		Name:    "container-runtime",
		Aliases: []string{"r"},
		Value:   "auto",
		Usage:   "Container runtime backend: docker, containerd, kubernetes, or auto",
		EnvVars: []string{"REACTORCIDE_CONTAINER_RUNTIME", "CONTAINER_RUNTIME"},
	},
}

func RunWorker(ctx *cli.Context) error {
	// Set up stores
	store.AppStore = postgres_store.PostgresStore

	// Initialize stores
	deferredStoreFuncs := initStores()
	for _, deferredFunc := range deferredStoreFuncs {
		defer deferredFunc()
	}

	// Get worker configuration from CLI flags
	queueName := ctx.String("queue")
	pollInterval := time.Duration(ctx.Int("poll-interval")) * time.Second
	concurrency := ctx.Int("concurrency")
	dryRun := ctx.Bool("dry-run")
	containerRuntime := ctx.String("container-runtime")

	// Log startup information
	logging.Log.Infof("Starting worker for queue: %s", queueName)
	logging.Log.Infof("Poll interval: %v", pollInterval)
	logging.Log.Infof("Concurrency: %d", concurrency)
	logging.Log.Infof("Dry run mode: %t", dryRun)
	logging.Log.Infof("Container runtime: %s", containerRuntime)

	// Create worker configuration
	workerConfig := &worker.Config{
		QueueName:        queueName,
		PollInterval:     pollInterval,
		Concurrency:      concurrency,
		DryRun:           dryRun,
		Store:            store.AppStore,
		ContainerRuntime: containerRuntime,
	}

	// Set up graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start worker in a goroutine
	workerCtx, workerCancel := context.WithCancel(context.Background())
	workerErrChan := make(chan error, 1)

	// Determine which worker to use based on Corndogs configuration
	if config.CornDogsBaseURL != "" {
		// Use Corndogs-based worker
		logging.Log.Info("Using Corndogs-based worker")

		// Initialize Corndogs client
		corndogsClient, err := corndogs.NewClient(corndogs.Config{
			BaseURL:      config.CornDogsBaseURL,
			QueueName:    queueName,
			Timeout:      time.Duration(config.DefaultTimeout) * time.Second,
			MaxRetries:   3,
			RetryBackoff: time.Second,
		})
		if err != nil {
			logging.Log.WithError(err).Fatal("Failed to initialize Corndogs client")
			return err
		}
		defer corndogsClient.Close()

		w := worker.NewCornDogsWorker(workerConfig, corndogsClient)
		go func() {
			workerErrChan <- w.Start(workerCtx)
		}()
	} else {
		// Use legacy database-polling worker
		logging.Log.Warn("Using legacy database-polling worker (Corndogs not configured)")

		w := worker.New(workerConfig)
		go func() {
			workerErrChan <- w.Start(workerCtx)
		}()
	}

	// Wait for shutdown signal or worker error
	select {
	case sig := <-sigChan:
		logging.Log.Infof("Received signal %v, shutting down gracefully...", sig)
		workerCancel()

		// Wait for worker to finish with timeout
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		select {
		case err := <-workerErrChan:
			if err != nil && err != context.Canceled {
				logging.Log.WithError(err).Error("Worker stopped with error")
				return err
			}
			logging.Log.Info("Worker stopped gracefully")
			return nil
		case <-shutdownCtx.Done():
			logging.Log.Warn("Worker shutdown timeout exceeded")
			return shutdownCtx.Err()
		}
	case err := <-workerErrChan:
		if err != nil {
			logging.Log.WithError(err).Error("Worker stopped with error")
			return err
		}
		logging.Log.Info("Worker stopped")
		return nil
	}
}
