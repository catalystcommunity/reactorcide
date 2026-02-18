package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"gopkg.in/yaml.v3"
)

// TriggerProcessor handles reading triggers.json from completed eval jobs
// and creating/submitting the triggered jobs to Corndogs.
type TriggerProcessor struct {
	store          store.Store
	corndogsClient corndogs.ClientInterface
}

// NewTriggerProcessor creates a new TriggerProcessor.
func NewTriggerProcessor(store store.Store, corndogsClient corndogs.ClientInterface) *TriggerProcessor {
	return &TriggerProcessor{
		store:          store,
		corndogsClient: corndogsClient,
	}
}

// triggersFile represents the top-level structure of triggers.json.
type triggersFile struct {
	Type string           `json:"type"`
	Jobs []triggerJobSpec  `json:"jobs"`
}

// triggerJobSpec represents a single triggered job from triggers.json.
type triggerJobSpec struct {
	JobFile        string            `json:"job_file"` // Path to YAML job definition, relative to source root
	JobName        string            `json:"job_name"`
	DependsOn      []string          `json:"depends_on"`
	Condition      string            `json:"condition"`
	Env            map[string]string `json:"env"`
	SourceType     string            `json:"source_type"`
	SourceURL      string            `json:"source_url"`
	SourceRef      string            `json:"source_ref"`
	CISourceType   string            `json:"ci_source_type"`
	CISourceURL    string            `json:"ci_source_url"`
	CISourceRef    string            `json:"ci_source_ref"`
	ContainerImage string            `json:"container_image"`
	JobCommand     string            `json:"job_command"`
	Priority       *int              `json:"priority"`
	Timeout        *int              `json:"timeout"`
	Capabilities   []string          `json:"capabilities"`
}

// jobDefinitionFile represents a YAML job definition file (e.g., .reactorcide/jobs/*.yaml).
type jobDefinitionFile struct {
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description"`
	Job         jobDefinitionJobConfig `yaml:"job"`
	Environment map[string]string      `yaml:"environment"`
}

// jobDefinitionJobConfig represents the job configuration within a YAML job definition.
type jobDefinitionJobConfig struct {
	Image        string   `yaml:"image"`
	Command      string   `yaml:"command"`
	Timeout      *int     `yaml:"timeout"`
	Priority     *int     `yaml:"priority"`
	RawCommand   bool     `yaml:"raw_command"`
	Capabilities []string `yaml:"capabilities"`
}

// ProcessTriggers reads triggers.json from the workspace directory of a completed
// eval job, creates the triggered jobs in the database, and submits them to Corndogs.
func (tp *TriggerProcessor) ProcessTriggers(ctx context.Context, workspaceDir string, parentJob *models.Job) error {
	triggersPath := filepath.Join(workspaceDir, "triggers.json")

	data, err := os.ReadFile(triggersPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No triggers file means no jobs to create - this is normal
			logging.Log.WithField("workspace", workspaceDir).Debug("No triggers.json found, skipping trigger processing")
			return nil
		}
		return fmt.Errorf("failed to read triggers file: %w", err)
	}

	_, err = tp.ProcessTriggersFromData(ctx, data, workspaceDir, parentJob)
	return err
}

// ProcessTriggersFromData processes raw trigger JSON data, creates the triggered jobs
// in the database, submits them to Corndogs, and returns the created job IDs.
// workspaceDir is the host workspace directory used to resolve job_file references.
func (tp *TriggerProcessor) ProcessTriggersFromData(ctx context.Context, data []byte, workspaceDir string, parentJob *models.Job) ([]string, error) {
	var tf triggersFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, fmt.Errorf("failed to parse triggers data: %w", err)
	}

	if tf.Type != "trigger_job" {
		return nil, fmt.Errorf("unexpected trigger type: %q", tf.Type)
	}

	if len(tf.Jobs) == 0 {
		logging.Log.WithField("parent_job_id", parentJob.JobID).Debug("Trigger data contains no jobs")
		return nil, nil
	}

	logger := logging.Log.WithField("parent_job_id", parentJob.JobID).WithField("trigger_count", len(tf.Jobs))
	logger.Info("Processing triggers from eval job")

	var createdJobIDs []string
	for _, spec := range tf.Jobs {
		// If job_file is specified, load the YAML definition as base and overlay inline fields
		if spec.JobFile != "" {
			baseSpec, err := tp.loadJobFile(workspaceDir, spec.JobFile)
			if err != nil {
				logger.WithError(err).WithField("job_file", spec.JobFile).Error("Failed to load job file")
				continue
			}
			spec = tp.overlaySpec(baseSpec, spec)
		}

		jobID, err := tp.createAndSubmitJob(ctx, spec, parentJob)
		if err != nil {
			logger.WithError(err).WithField("job_name", spec.JobName).Error("Failed to create triggered job")
			// Continue processing remaining triggers
			continue
		}
		createdJobIDs = append(createdJobIDs, jobID)
	}

	return createdJobIDs, nil
}

// loadJobFile reads a YAML job definition file from the workspace and converts it to a triggerJobSpec.
func (tp *TriggerProcessor) loadJobFile(workspaceDir, jobFile string) (triggerJobSpec, error) {
	filePath := filepath.Join(workspaceDir, "src", jobFile)

	data, err := os.ReadFile(filePath)
	if err != nil {
		return triggerJobSpec{}, fmt.Errorf("failed to read job file %q: %w", filePath, err)
	}

	var def jobDefinitionFile
	if err := yaml.Unmarshal(data, &def); err != nil {
		return triggerJobSpec{}, fmt.Errorf("failed to parse job file %q: %w", filePath, err)
	}

	spec := triggerJobSpec{
		JobName:        def.Name,
		ContainerImage: def.Job.Image,
		JobCommand:     def.Job.Command,
		Timeout:        def.Job.Timeout,
		Priority:       def.Job.Priority,
		Capabilities:   def.Job.Capabilities,
		Env:            def.Environment,
	}

	return spec, nil
}

// overlaySpec overlays non-zero inline fields from the original trigger spec onto the base spec loaded from a job file.
func (tp *TriggerProcessor) overlaySpec(base, overlay triggerJobSpec) triggerJobSpec {
	result := base

	// Overlay simple string fields if non-empty
	if overlay.JobName != "" {
		result.JobName = overlay.JobName
	}
	if overlay.ContainerImage != "" {
		result.ContainerImage = overlay.ContainerImage
	}
	if overlay.JobCommand != "" {
		result.JobCommand = overlay.JobCommand
	}
	if overlay.SourceType != "" {
		result.SourceType = overlay.SourceType
	}
	if overlay.SourceURL != "" {
		result.SourceURL = overlay.SourceURL
	}
	if overlay.SourceRef != "" {
		result.SourceRef = overlay.SourceRef
	}
	if overlay.CISourceType != "" {
		result.CISourceType = overlay.CISourceType
	}
	if overlay.CISourceURL != "" {
		result.CISourceURL = overlay.CISourceURL
	}
	if overlay.CISourceRef != "" {
		result.CISourceRef = overlay.CISourceRef
	}
	if overlay.Condition != "" {
		result.Condition = overlay.Condition
	}

	// Overlay pointer fields if non-nil
	if overlay.Priority != nil {
		result.Priority = overlay.Priority
	}
	if overlay.Timeout != nil {
		result.Timeout = overlay.Timeout
	}

	// Overlay slices if non-empty
	if len(overlay.DependsOn) > 0 {
		result.DependsOn = overlay.DependsOn
	}
	if len(overlay.Capabilities) > 0 {
		result.Capabilities = overlay.Capabilities
	}

	// Merge env vars: base first, then overlay on top
	if len(overlay.Env) > 0 {
		if result.Env == nil {
			result.Env = make(map[string]string)
		}
		for k, v := range overlay.Env {
			result.Env[k] = v
		}
	}

	return result
}

// createAndSubmitJob creates a single job from a trigger spec and submits it to Corndogs.
// Returns the created job ID on success.
func (tp *TriggerProcessor) createAndSubmitJob(ctx context.Context, spec triggerJobSpec, parentJob *models.Job) (string, error) {
	job := tp.buildJobFromTrigger(spec, parentJob)

	if err := tp.store.CreateJob(ctx, job); err != nil {
		return "", fmt.Errorf("failed to create job in database: %w", err)
	}

	if tp.corndogsClient == nil {
		return job.JobID, nil
	}

	taskPayload := tp.buildTaskPayload(job)

	task, err := tp.corndogsClient.SubmitTask(ctx, taskPayload, int64(job.Priority))
	if err != nil {
		logging.Log.WithError(err).WithField("job_id", job.JobID).Error("Failed to submit triggered job to Corndogs")
		job.Status = "failed"
		job.LastError = fmt.Sprintf("failed to submit to Corndogs: %v", err)
	} else {
		taskID := task.Uuid
		job.CorndogsTaskID = &taskID
		job.Status = task.CurrentState
	}

	if err := tp.store.UpdateJob(ctx, job); err != nil {
		logging.Log.WithError(err).WithField("job_id", job.JobID).Error("Failed to update triggered job after Corndogs submission")
	}

	logging.Log.WithFields(map[string]interface{}{
		"job_id":        job.JobID,
		"job_name":      job.Name,
		"parent_job_id": parentJob.JobID,
		"status":        job.Status,
	}).Info("Created triggered job")

	return job.JobID, nil
}

// buildJobFromTrigger creates a models.Job from a trigger spec and parent job.
func (tp *TriggerProcessor) buildJobFromTrigger(spec triggerJobSpec, parentJob *models.Job) *models.Job {
	now := time.Now().UTC()
	parentJobID := parentJob.JobID

	// Merge env vars: start with parent's env vars, overlay trigger's env vars
	envVars := models.JSONB{}
	if parentJob.JobEnvVars != nil {
		for k, v := range parentJob.JobEnvVars {
			envVars[k] = v
		}
	}
	for k, v := range spec.Env {
		envVars[k] = v
	}

	job := &models.Job{
		CreatedAt:   now,
		UpdatedAt:   now,
		UserID:      parentJob.UserID,
		ProjectID:   parentJob.ProjectID,
		ParentJobID: &parentJobID,
		Name:        spec.JobName,
		Description: fmt.Sprintf("Triggered by eval job %s", parentJob.JobID),
		Status:      "submitted",
		QueueName:   parentJob.QueueName,
		JobEnvVars:  envVars,
	}

	// Source configuration
	if spec.SourceType != "" {
		st := models.SourceType(spec.SourceType)
		job.SourceType = &st
	}
	if spec.SourceURL != "" {
		job.SourceURL = &spec.SourceURL
	}
	if spec.SourceRef != "" {
		job.SourceRef = &spec.SourceRef
	}

	// CI source configuration
	if spec.CISourceType != "" {
		cst := models.SourceType(spec.CISourceType)
		job.CISourceType = &cst
	}
	if spec.CISourceURL != "" {
		job.CISourceURL = &spec.CISourceURL
	}
	if spec.CISourceRef != "" {
		job.CISourceRef = &spec.CISourceRef
	}

	// Container and execution configuration
	if spec.ContainerImage != "" {
		job.RunnerImage = spec.ContainerImage
	} else {
		job.RunnerImage = parentJob.RunnerImage
	}
	if spec.JobCommand != "" {
		job.JobCommand = spec.JobCommand
	}
	if spec.Timeout != nil {
		job.TimeoutSeconds = *spec.Timeout
	} else {
		job.TimeoutSeconds = parentJob.TimeoutSeconds
	}
	if spec.Priority != nil {
		job.Priority = *spec.Priority
	}
	if len(spec.Capabilities) > 0 {
		job.Capabilities = spec.Capabilities
	}

	// Copy event metadata from parent
	if parentJob.EventMetadata != nil {
		job.EventMetadata = parentJob.EventMetadata
	}

	// Copy VCS metadata (Notes) so child jobs can report commit status.
	// Strip the IsEval flag so child jobs actually update commit status.
	// Set the StatusContext to the job name so each job gets a distinct GitHub status check.
	if parentJob.Notes != "" {
		var metadata vcs.JobMetadata
		if err := json.Unmarshal([]byte(parentJob.Notes), &metadata); err == nil {
			metadata.IsEval = false
			metadata.StatusContext = spec.JobName
			if updated, err := json.Marshal(metadata); err == nil {
				job.Notes = string(updated)
			} else {
				job.Notes = parentJob.Notes
			}
		} else {
			job.Notes = parentJob.Notes
		}
	}

	return job
}

// buildTaskPayload creates a Corndogs TaskPayload from a job.
func (tp *TriggerProcessor) buildTaskPayload(job *models.Job) *corndogs.TaskPayload {
	sourceTypeStr := ""
	if job.SourceType != nil {
		sourceTypeStr = string(*job.SourceType)
	}
	sourceURL := ""
	if job.SourceURL != nil {
		sourceURL = *job.SourceURL
	}
	sourceRef := ""
	if job.SourceRef != nil {
		sourceRef = *job.SourceRef
	}
	sourcePath := ""
	if job.SourcePath != nil {
		sourcePath = *job.SourcePath
	}

	payload := &corndogs.TaskPayload{
		JobID:   job.JobID,
		JobType: "run",
		Config: map[string]interface{}{
			"image":       job.RunnerImage,
			"command":     job.JobCommand,
			"working_dir": job.JobDir,
			"timeout":     job.TimeoutSeconds,
			"code_dir":    job.CodeDir,
			"job_dir":     job.JobDir,
		},
		Source: map[string]interface{}{
			"type":        sourceTypeStr,
			"url":         sourceURL,
			"ref":         sourceRef,
			"source_path": sourcePath,
		},
		Metadata: map[string]interface{}{
			"user_id":      job.UserID,
			"submitted_at": job.CreatedAt,
			"name":         job.Name,
			"description":  job.Description,
		},
	}

	if job.JobEnvVars != nil {
		payload.Config["environment"] = job.JobEnvVars
	}

	return payload
}
