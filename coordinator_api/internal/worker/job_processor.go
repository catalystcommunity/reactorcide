package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
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

// JobProcessor handles the execution of individual jobs
type JobProcessor struct {
	store       store.Store
	dryRun      bool
	retryConfig *RetryConfig
}

// NewJobProcessor creates a new job processor
func NewJobProcessor(store store.Store, dryRun bool) *JobProcessor {
	return &JobProcessor{
		store:       store,
		dryRun:      dryRun,
		retryConfig: DefaultRetryConfig(),
	}
}

// NewJobProcessorWithRetryConfig creates a new job processor with custom retry configuration
func NewJobProcessorWithRetryConfig(store store.Store, dryRun bool, retryConfig *RetryConfig) *JobProcessor {
	if retryConfig == nil {
		retryConfig = DefaultRetryConfig()
	}
	return &JobProcessor{
		store:       store,
		dryRun:      dryRun,
		retryConfig: retryConfig,
	}
}

// ProcessJob executes a job using runnerlib
func (jp *JobProcessor) ProcessJob(ctx context.Context, job *models.Job) *JobResult {
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
		execResult = jp.executeWithRunnerlib(ctx, job)

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

	if job.SourceType != "git" && job.SourceType != "copy" {
		return fmt.Errorf("invalid source type: %s", job.SourceType)
	}

	if job.SourceType == "git" && job.GitURL == "" {
		return fmt.Errorf("git URL is required for git source type")
	}

	if job.SourceType == "copy" && job.SourcePath == "" {
		return fmt.Errorf("source path is required for copy source type")
	}

	return nil
}

// executeWithRunnerlib executes the job using the runnerlib Python package
func (jp *JobProcessor) executeWithRunnerlib(ctx context.Context, job *models.Job) *JobResult {
	logger := logging.Log.WithField("job_id", job.JobID)

	// Create a job-specific secret masker
	// This is an extra safety layer - Python's runnerlib does the primary masking
	masker := secrets.NewMasker()

	// Register all job environment variable VALUES as secrets to mask
	if job.JobEnvVars != nil && len(job.JobEnvVars) > 0 {
		masker.RegisterEnvVars(job.JobEnvVars)
	}

	// Create a temporary working directory for this job
	workDir, err := os.MkdirTemp("", fmt.Sprintf("reactorcide-job-%s-*", job.JobID))
	if err != nil {
		logger.WithError(err).Error("Failed to create work directory")
		return &JobResult{
			ExitCode: 1,
			Error:    fmt.Sprintf("Failed to create work directory: %v", err),
		}
	}
	defer os.RemoveAll(workDir)

	logger.WithField("work_dir", workDir).Info("Created work directory")

	// Build runnerlib command
	cmd := jp.buildRunnerlibCommand(job, workDir)
	cmd.Dir = workDir

	// Set up context cancellation
	if ctx != nil {
		cmdCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		cmd = exec.CommandContext(cmdCtx, cmd.Args[0], cmd.Args[1:]...)
		cmd.Dir = workDir
	}

	// Execute the command - mask any secrets in the command for logging
	// Note: This is extra safety - the actual secrets are in files, not the command line
	maskedCmd := masker.MaskCommandArgs(cmd.Args)
	logger.WithField("command", strings.Join(maskedCmd, " ")).Info("Executing runnerlib command")

	output, err := cmd.CombinedOutput()
	exitCode := 0

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = 1
		}
	}

	result := &JobResult{
		ExitCode: exitCode,
		Output:   string(output),
	}

	if err != nil {
		result.Error = err.Error()
		logger.WithError(err).WithField("exit_code", exitCode).Error("Job execution failed")
	} else {
		logger.Info("Job execution succeeded")
	}

	// TODO: Store logs and artifacts in object store
	// Mask any secrets that might appear in the output before logging
	// Note: Python's runnerlib should already mask most secrets, this is extra safety
	maskedOutput := masker.MaskString(string(output))
	logger.WithField("output", maskedOutput).Debug("Job output")

	return result
}

// buildRunnerlibCommand builds the runnerlib command with job configuration
func (jp *JobProcessor) buildRunnerlibCommand(job *models.Job, workDir string) *exec.Cmd {
	args := []string{"python", "-m", "runnerlib.cli", "run"}

	// Add work directory for job isolation
	args = append(args, "--work-dir", workDir)

	// Add source configuration
	if job.SourceType == "git" {
		args = append(args, "--git-url", job.GitURL)
		if job.GitRef != "" {
			args = append(args, "--git-ref", job.GitRef)
		}
	} else if job.SourceType == "copy" {
		args = append(args, "--source-path", job.SourcePath)
	}

	// Add job configuration
	args = append(args, "--job-command", job.JobCommand)

	if job.RunnerImage != "" {
		args = append(args, "--runner-image", job.RunnerImage)
	}

	if job.CodeDir != "" {
		args = append(args, "--code-dir", job.CodeDir)
	}

	if job.JobDir != "" {
		args = append(args, "--job-dir", job.JobDir)
	}

	// Add timeout
	if job.TimeoutSeconds > 0 {
		args = append(args, "--timeout", fmt.Sprintf("%d", job.TimeoutSeconds))
	}

	// Add environment variables
	if job.JobEnvVars != nil && len(job.JobEnvVars) > 0 {
		// Create job directory structure to match what runnerlib expects
		jobDir := filepath.Join(workDir, "job")
		os.MkdirAll(jobDir, 0755)

		// Write env file to the job directory
		envFile := filepath.Join(jobDir, "job.env")
		if err := jp.writeEnvFile(envFile, job.JobEnvVars); err == nil {
			// Pass as relative path starting with ./job/
			args = append(args, "--job-env", "./job/job.env")
		}
	} else if job.JobEnvFile != "" {
		args = append(args, "--job-env-file", job.JobEnvFile)
	}

	return exec.Command(args[0], args[1:]...)
}

// writeEnvFile writes environment variables to a file
func (jp *JobProcessor) writeEnvFile(filename string, envVars map[string]interface{}) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	for key, value := range envVars {
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

		// Write environment variable in KEY=VALUE format
		_, err := fmt.Fprintf(file, "%s=%s\n", key, valueStr)
		if err != nil {
			return err
		}
	}

	return nil
}
