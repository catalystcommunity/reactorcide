package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// JobResult represents the result of job execution
type JobResult struct {
	ExitCode           int
	Output             string
	Error              string
	LogsObjectKey      string
	ArtifactsObjectKey string
	Duration           time.Duration
	RetryCount         int  // Number of retry attempts made
	Retryable          bool // Whether the failure was retryable
}

// HeartbeatFunc is a function that sends a heartbeat
// It should extend the timeout for the currently executing task
type HeartbeatFunc func(ctx context.Context) error

// JobProcessorConfig holds configuration for the job processor
type JobProcessorConfig struct {
	ObjectStore       objects.ObjectStore
	LogChunkInterval  time.Duration
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration
	RetryConfig       *RetryConfig

	// Optional: Callbacks for log updates and heartbeats
	OnLogUpdate func(jobID, objectKey string, bytesWritten int64) error

	// Secrets configuration
	// SecretsKeyManager is the master key manager for decrypting org keys
	SecretsKeyManager *secrets.MasterKeyManager
	// SecretsStorageType determines where secrets are stored: "database" (default), "local", or "none"
	SecretsStorageType string
	// SecretsLocalPath is the path for local secrets storage (only used when SecretsStorageType="local")
	SecretsLocalPath string
	// SecretsLocalPassword is the password for local secrets storage
	SecretsLocalPassword string
}

// JobExecutionContext holds context for job execution
type JobExecutionContext struct {
	HeartbeatFunc HeartbeatFunc // Optional: function to call for sending heartbeats
}

// JobProcessor handles the execution of individual jobs
type JobProcessor struct {
	store       store.Store
	runner      JobRunner
	dryRun      bool
	retryConfig *RetryConfig
	config      *JobProcessorConfig
}

// NewJobProcessor creates a new job processor
func NewJobProcessor(store store.Store, runner JobRunner, dryRun bool) *JobProcessor {
	return &JobProcessor{
		store:       store,
		runner:      runner,
		dryRun:      dryRun,
		retryConfig: DefaultRetryConfig(),
		config:      &JobProcessorConfig{},
	}
}

// NewJobProcessorWithConfig creates a new job processor with configuration
func NewJobProcessorWithConfig(store store.Store, runner JobRunner, dryRun bool, config *JobProcessorConfig) *JobProcessor {
	if config == nil {
		config = &JobProcessorConfig{}
	}
	if config.RetryConfig == nil {
		config.RetryConfig = DefaultRetryConfig()
	}
	return &JobProcessor{
		store:       store,
		runner:      runner,
		dryRun:      dryRun,
		retryConfig: config.RetryConfig,
		config:      config,
	}
}

// NewJobProcessorWithRetryConfig creates a new job processor with custom retry configuration
func NewJobProcessorWithRetryConfig(store store.Store, runner JobRunner, dryRun bool, retryConfig *RetryConfig) *JobProcessor {
	if retryConfig == nil {
		retryConfig = DefaultRetryConfig()
	}
	return &JobProcessor{
		store:       store,
		runner:      runner,
		dryRun:      dryRun,
		retryConfig: retryConfig,
		config:      &JobProcessorConfig{RetryConfig: retryConfig},
	}
}

// ProcessJob executes a job using runnerlib
func (jp *JobProcessor) ProcessJob(ctx context.Context, job *models.Job) *JobResult {
	return jp.ProcessJobWithContext(ctx, job, nil)
}

// ProcessJobWithContext executes a job with optional execution context (e.g., heartbeat function)
func (jp *JobProcessor) ProcessJobWithContext(ctx context.Context, job *models.Job, execCtx *JobExecutionContext) *JobResult {
	startTime := time.Now()
	logger := logging.Log.WithField("job_id", job.JobID)

	result := &JobResult{
		ExitCode: -1, // Default to error
	}

	// Validate job configuration
	if err := jp.validateJob(job); err != nil {
		logger.WithFields(map[string]interface{}{
			"error":   err.Error(),
			"queue":   job.QueueName,
			"command": job.JobCommand,
		}).Error("Job validation failed")
		result.Error = fmt.Sprintf("Job validation failed: %v", err)
		result.ExitCode = 1
		result.Duration = time.Since(startTime)
		return result
	}

	// If dry run, just simulate success
	if jp.dryRun {
		logger.WithFields(map[string]interface{}{
			"command": job.JobCommand,
			"image":   job.RunnerImage,
		}).Info("Dry run mode - simulating job execution")
		result.ExitCode = 0
		result.Output = "Dry run - job would have been executed"
		result.Duration = time.Since(startTime)
		return result
	}

	// Execute the job with retry logic
	logger.WithFields(map[string]interface{}{
		"command":     job.JobCommand,
		"image":       job.RunnerImage,
		"max_retries": jp.retryConfig.MaxRetries,
	}).Info("Executing job with runnerlib")

	var execResult *JobResult
	var retryCount int
	retryErr := RetryWithBackoffCounter(ctx, jp.retryConfig, fmt.Sprintf("job_%s", job.JobID), func(attempt int) error {
		retryCount = attempt
		execResult = jp.executeWithRunnerlib(ctx, job, execCtx)

		// Check if the result indicates a retryable error
		if execResult.ExitCode != 0 {
			retryableErr := ClassifyExecutionError(nil, execResult.ExitCode)
			if retryableErr != nil && retryableErr.Retryable {
				execResult.Retryable = true
				// Record retry metric
				if attempt > 0 {
					workerID := ""
					if job.WorkerID != nil {
						workerID = *job.WorkerID
					}
					metrics.RecordJobRetry(job.QueueName, workerID)
				}
				// Record error metric
				metrics.RecordJobError(job.QueueName, fmt.Sprintf("exit_code_%d", execResult.ExitCode), true)
				return retryableErr
			} else if retryableErr != nil {
				// Non-retryable error
				execResult.Retryable = false
				// Record error metric
				metrics.RecordJobError(job.QueueName, fmt.Sprintf("exit_code_%d", execResult.ExitCode), false)
				return fmt.Errorf("non-retryable error: %w", retryableErr)
			}
		}

		return nil
	})

	if retryErr != nil {
		logger.WithError(retryErr).Error("Job execution failed after retries")
		if execResult == nil {
			// If we don't have any result, create a failure result
			result.ExitCode = 1
			result.Error = retryErr.Error()
			result.RetryCount = retryCount
		} else {
			result = execResult
			result.RetryCount = retryCount
		}
	} else {
		result = execResult
		result.RetryCount = retryCount
	}

	result.Duration = time.Since(startTime)

	logger.WithField("exit_code", result.ExitCode).WithField("duration", result.Duration).
		Info("Job execution completed")

	return result
}

// validateJob validates the job configuration
func (jp *JobProcessor) validateJob(job *models.Job) error {
	if job.JobCommand == "" {
		return fmt.Errorf("job command is required")
	}

	// Source type is optional now (can run without source checkout)
	if job.SourceType != nil {
		sourceType := string(*job.SourceType)
		if sourceType != "git" && sourceType != "copy" && sourceType != "none" {
			return fmt.Errorf("invalid source type: %s", sourceType)
		}

		if sourceType == "git" && (job.SourceURL == nil || *job.SourceURL == "") {
			return fmt.Errorf("source URL is required for git source type")
		}

		if sourceType == "copy" && (job.SourcePath == nil || *job.SourcePath == "") {
			return fmt.Errorf("source path is required for copy source type")
		}
	}

	return nil
}

// buildJobEnv creates an environment variable map from the job configuration
func (jp *JobProcessor) buildJobEnv(job *models.Job) map[string]string {
	env := make(map[string]string)

	// Add system environment variables
	env["REACTORCIDE_JOB_ID"] = job.JobID
	env["REACTORCIDE_QUEUE"] = job.QueueName

	// Add source configuration if present
	if job.SourceType != nil {
		env["REACTORCIDE_SOURCE_TYPE"] = string(*job.SourceType)
		if job.SourceURL != nil {
			env["REACTORCIDE_SOURCE_URL"] = *job.SourceURL
		}
		if job.SourceRef != nil {
			env["REACTORCIDE_SOURCE_REF"] = *job.SourceRef
		}
		if job.SourcePath != nil {
			env["REACTORCIDE_SOURCE_PATH"] = *job.SourcePath
		}
	}

	// Add CI source configuration if present
	if job.CISourceType != nil {
		env["REACTORCIDE_CI_SOURCE_TYPE"] = string(*job.CISourceType)
		if job.CISourceURL != nil {
			env["REACTORCIDE_CI_SOURCE_URL"] = *job.CISourceURL
		}
		if job.CISourceRef != nil {
			env["REACTORCIDE_CI_SOURCE_REF"] = *job.CISourceRef
		}
	}

	// Add job-specific environment variables
	if job.JobEnvVars != nil && len(job.JobEnvVars) > 0 {
		for key, value := range job.JobEnvVars {
			// Convert value to string
			var valueStr string
			switch v := value.(type) {
			case string:
				valueStr = v
			case int, int64, float64, bool:
				valueStr = fmt.Sprintf("%v", v)
			default:
				// For complex types, try JSON marshaling
				if jsonBytes, err := json.Marshal(v); err == nil {
					valueStr = string(jsonBytes)
				} else {
					valueStr = fmt.Sprintf("%v", v)
				}
			}
			env[key] = valueStr
		}
	}

	return env
}

// getSecretsProvider returns a secrets provider for the given job's user.
// Returns nil if secrets are disabled or not configured.
func (jp *JobProcessor) getSecretsProvider(ctx context.Context, job *models.Job) (secrets.Provider, error) {
	storageType := jp.config.SecretsStorageType
	if storageType == "" {
		storageType = "database" // Default to database
	}

	switch storageType {
	case "none", "disabled":
		// Secrets disabled
		return nil, nil

	case "local":
		// Use local provider
		if jp.config.SecretsLocalPassword == "" {
			return nil, fmt.Errorf("local secrets password not configured")
		}
		return secrets.NewLocalProvider(jp.config.SecretsLocalPath, jp.config.SecretsLocalPassword)

	case "database":
		// Use database provider with per-user encryption
		if jp.config.SecretsKeyManager == nil {
			return nil, fmt.Errorf("secrets key manager not configured")
		}

		// Get the org encryption key for this job's user
		db := store.GetDB()
		if db == nil {
			return nil, fmt.Errorf("database not available")
		}

		orgKey, err := jp.config.SecretsKeyManager.GetOrgEncryptionKey(db, job.UserID)
		if err != nil {
			// If not initialized, secrets aren't available for this user
			if err == secrets.ErrNotInitialized {
				return nil, nil
			}
			return nil, fmt.Errorf("failed to get org encryption key: %w", err)
		}

		return secrets.NewDatabaseProvider(db, job.UserID, orgKey)

	default:
		return nil, fmt.Errorf("unknown secrets storage type: %s", storageType)
	}
}

// resolveJobSecrets resolves all ${secret:path:key} references in the job environment.
// Returns the resolved environment and the list of secret values for masking.
func (jp *JobProcessor) resolveJobSecrets(ctx context.Context, job *models.Job, env map[string]string) (map[string]string, []string, error) {
	// Check if any environment variables contain secret references
	hasSecrets := false
	for _, v := range env {
		if HasSecretRefs(v) {
			hasSecrets = true
			break
		}
	}

	if !hasSecrets {
		// No secrets to resolve
		return env, nil, nil
	}

	// Get secrets provider
	provider, err := jp.getSecretsProvider(ctx, job)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get secrets provider: %w", err)
	}

	if provider == nil {
		// Secrets are disabled or not available
		return nil, nil, fmt.Errorf("job contains secret references but secrets are not configured")
	}

	// Create getter function for ResolveSecretsInEnv
	getSecret := func(path, key string) (string, error) {
		return provider.Get(ctx, path, key)
	}

	// Resolve secrets in environment
	resolvedEnv, secretValues, err := ResolveSecretsInEnv(env, getSecret)
	if err != nil {
		return nil, nil, err
	}

	return resolvedEnv, secretValues, nil
}

// buildJobConfig creates a JobConfig from a models.Job
// The job command is executed directly with the entrypoint cleared.
// Users can either:
// 1. Run their own commands directly (e.g., "sh -c 'make build'")
// 2. Use runnerlib as a command for source prep and lifecycle hooks
//    (e.g., "runnerlib run --source-url ... --job-command 'make build'")
func (jp *JobProcessor) buildJobConfig(job *models.Job, workspaceDir string) *JobConfig {
	// Determine container image (use job-specific or default)
	image := DefaultRunnerImage
	if job.ContainerImage != nil && *job.ContainerImage != "" {
		image = *job.ContainerImage
	} else if job.RunnerImage != "" {
		// Backwards compatibility with RunnerImage field
		image = job.RunnerImage
	}

	// Parse the job command using shell-style quoting rules
	command := ParseCommand(job.JobCommand)

	// Build environment variables
	env := jp.buildJobEnv(job)

	// Determine working directory
	workingDir := "/job"
	if job.CodeDir != "" {
		workingDir = job.CodeDir
	}

	// Create job config
	config := &JobConfig{
		Image:        image,
		Command:      command,
		Env:          env,
		WorkspaceDir: workspaceDir,
		WorkingDir:   workingDir,
		JobID:        job.JobID,
		QueueName:    job.QueueName,
	}

	// Add timeout if specified
	if job.TimeoutSeconds > 0 {
		config.TimeoutSeconds = job.TimeoutSeconds
	}

	// TODO: Add resource limits support (CPULimit, MemoryLimit) once fields are added to Job model

	return config
}

// executeWithRunnerlib executes the job using a container runner
func (jp *JobProcessor) executeWithRunnerlib(ctx context.Context, job *models.Job, execCtx *JobExecutionContext) *JobResult {
	logger := logging.Log.WithField("job_id", job.JobID)

	// Create a job-specific secret masker
	masker := secrets.NewMasker()

	// Register all job environment variable VALUES as secrets to mask
	if job.JobEnvVars != nil && len(job.JobEnvVars) > 0 {
		masker.RegisterEnvVars(job.JobEnvVars)
	}

	// Create a temporary workspace directory for this job
	workspaceDir, err := os.MkdirTemp("", fmt.Sprintf("reactorcide-job-%s-*", job.JobID))
	if err != nil {
		logger.WithError(err).Error("Failed to create workspace directory")
		return &JobResult{
			ExitCode: 1,
			Error:    fmt.Sprintf("Failed to create workspace directory: %v", err),
		}
	}
	defer os.RemoveAll(workspaceDir)

	logger.WithField("workspace_dir", workspaceDir).Info("Created workspace directory")

	// Build job configuration for container runner
	jobConfig := jp.buildJobConfig(job, workspaceDir)

	// Resolve secret references in environment variables
	resolvedEnv, secretValues, err := jp.resolveJobSecrets(ctx, job, jobConfig.Env)
	if err != nil {
		logger.WithError(err).Error("Failed to resolve secrets in job environment")
		return &JobResult{
			ExitCode: 1,
			Error:    fmt.Sprintf("Failed to resolve secrets: %v", err),
		}
	}
	if resolvedEnv != nil {
		jobConfig.Env = resolvedEnv
	}

	// Register resolved secret values for masking
	for _, secretValue := range secretValues {
		masker.RegisterSecret(secretValue)
	}

	// Mask command for logging
	maskedCmd := masker.MaskCommandArgs(jobConfig.Command)
	logger.WithFields(map[string]interface{}{
		"image":   jobConfig.Image,
		"command": strings.Join(maskedCmd, " "),
	}).Info("Spawning job container")

	// Spawn the job container
	containerID, err := jp.runner.SpawnJob(ctx, jobConfig)
	if err != nil {
		logger.WithError(err).Error("Failed to spawn job container")
		return &JobResult{
			ExitCode: 1,
			Error:    fmt.Sprintf("Failed to spawn job container: %v", err),
		}
	}

	// Ensure cleanup happens
	defer func() {
		cleanupCtx := context.Background() // Use background context for cleanup to avoid cancellation
		if err := jp.runner.Cleanup(cleanupCtx, containerID); err != nil {
			logger.WithError(err).Warn("Failed to cleanup job container")
		}
	}()

	logger.WithField("container_id", containerID).Info("Job container spawned successfully")

	// Start heartbeat goroutine if heartbeat function is provided
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)

	if execCtx != nil && execCtx.HeartbeatFunc != nil && jp.config.HeartbeatInterval > 0 {
		go jp.sendHeartbeats(ctx, execCtx.HeartbeatFunc, heartbeatDone)
	}

	// Stream logs from the container
	stdout, stderr, err := jp.runner.StreamLogs(ctx, containerID)
	if err != nil {
		logger.WithError(err).Error("Failed to stream logs from container")
		// Continue anyway - we still want to wait for completion
	}

	// Create log shippers for streaming logs to object storage
	var stdoutKey, stderrKey string
	var stdoutBytes, stderrBytes int64
	var logWg sync.WaitGroup
	var logShipErrors []error
	var logShipErrorMu sync.Mutex

	if jp.config.ObjectStore != nil {
		// Create callback for log updates
		onChunkUploaded := func(objectKey string, bytesWritten int64) error {
			if jp.config.OnLogUpdate != nil {
				return jp.config.OnLogUpdate(job.JobID, objectKey, bytesWritten)
			}
			return nil
		}

		// Create log shipper for stdout
		if stdout != nil {
			stdoutShipper := NewLogShipper(LogShipperConfig{
				ObjectStore:     jp.config.ObjectStore,
				JobID:           job.JobID,
				StreamType:      "stdout",
				ChunkInterval:   jp.config.LogChunkInterval,
				OnChunkUploaded: onChunkUploaded,
			}, masker)

			logWg.Add(1)
			go func() {
				defer logWg.Done()
				key, bytes, err := stdoutShipper.StreamAndShip(ctx, stdout)
				stdoutKey = key
				stdoutBytes = bytes
				if err != nil {
					logShipErrorMu.Lock()
					logShipErrors = append(logShipErrors, fmt.Errorf("stdout log shipping error: %w", err))
					logShipErrorMu.Unlock()
				}
			}()
		}

		// Create log shipper for stderr
		if stderr != nil {
			stderrShipper := NewLogShipper(LogShipperConfig{
				ObjectStore:     jp.config.ObjectStore,
				JobID:           job.JobID,
				StreamType:      "stderr",
				ChunkInterval:   jp.config.LogChunkInterval,
				OnChunkUploaded: onChunkUploaded,
			}, masker)

			logWg.Add(1)
			go func() {
				defer logWg.Done()
				key, bytes, err := stderrShipper.StreamAndShip(ctx, stderr)
				stderrKey = key
				stderrBytes = bytes
				if err != nil {
					logShipErrorMu.Lock()
					logShipErrors = append(logShipErrors, fmt.Errorf("stderr log shipping error: %w", err))
					logShipErrorMu.Unlock()
				}
			}()
		}
	} else {
		// Fallback: Capture logs in memory (old behavior)
		var outputBuilder strings.Builder

		if stdout != nil {
			logWg.Add(1)
			go func() {
				defer logWg.Done()
				defer stdout.Close()
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					line := scanner.Text()
					maskedLine := masker.MaskString(line)
					logger.WithField("stream", "stdout").Info(maskedLine)
					outputBuilder.WriteString(line)
					outputBuilder.WriteString("\n")
				}
			}()
		}

		if stderr != nil {
			logWg.Add(1)
			go func() {
				defer logWg.Done()
				defer stderr.Close()
				scanner := bufio.NewScanner(stderr)
				for scanner.Scan() {
					line := scanner.Text()
					maskedLine := masker.MaskString(line)
					logger.WithField("stream", "stderr").Warn(maskedLine)
					outputBuilder.WriteString(line)
					outputBuilder.WriteString("\n")
				}
			}()
		}
	}

	// Wait for the container to complete
	exitCode, err := jp.runner.WaitForCompletion(ctx, containerID)

	// Wait for log streaming/shipping to finish
	logWg.Wait()

	result := &JobResult{
		ExitCode: exitCode,
	}

	// Set log object keys if logs were shipped
	if stdoutKey != "" || stderrKey != "" {
		// Use stdout key as primary log key (stderr is separate)
		if stdoutKey != "" {
			result.LogsObjectKey = stdoutKey
		} else {
			result.LogsObjectKey = stderrKey
		}

		logger.WithFields(map[string]interface{}{
			"stdout_key":   stdoutKey,
			"stdout_bytes": stdoutBytes,
			"stderr_key":   stderrKey,
			"stderr_bytes": stderrBytes,
		}).Info("Logs shipped to object storage")
	}

	// Check for log shipping errors
	if len(logShipErrors) > 0 {
		logger.WithField("error_count", len(logShipErrors)).Warn("Log shipping encountered errors")
		// Don't fail the job due to log shipping errors, but log them
		for _, logErr := range logShipErrors {
			logger.WithError(logErr).Warn("Log shipping error")
		}
	}

	if err != nil {
		result.Error = err.Error()
		logger.WithError(err).WithField("exit_code", exitCode).Error("Job execution failed")
	} else {
		logger.WithField("exit_code", exitCode).Info("Job execution completed")
	}

	return result
}

// sendHeartbeats sends periodic heartbeats to prevent task timeout
func (jp *JobProcessor) sendHeartbeats(ctx context.Context, heartbeatFunc HeartbeatFunc, done chan struct{}) {
	ticker := time.NewTicker(jp.config.HeartbeatInterval)
	defer ticker.Stop()

	logger := logging.Log.WithField("heartbeat_interval", jp.config.HeartbeatInterval)
	logger.Debug("Starting heartbeat goroutine")

	for {
		select {
		case <-ctx.Done():
			logger.Debug("Heartbeat goroutine stopping - context cancelled")
			return
		case <-done:
			logger.Debug("Heartbeat goroutine stopping - job completed")
			return
		case <-ticker.C:
			if err := heartbeatFunc(ctx); err != nil {
				logger.WithError(err).Warn("Failed to send heartbeat")
				// Continue anyway - don't stop heartbeats due to single failure
			} else {
				logger.Debug("Heartbeat sent successfully")
			}
		}
	}
}
