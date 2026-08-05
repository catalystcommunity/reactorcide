package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
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
	statusUpdater  vcs.JobStatusUpdaterInterface
}

// NewTriggerProcessor creates a new TriggerProcessor.
func NewTriggerProcessor(store store.Store, corndogsClient corndogs.ClientInterface) *TriggerProcessor {
	return &TriggerProcessor{
		store:          store,
		corndogsClient: corndogsClient,
	}
}

// SetStatusUpdater wires a VCS status updater so that newly-created child
// jobs get registered as pending checks on their commit the moment they
// exist in the database, before the worker picks them up.
func (tp *TriggerProcessor) SetStatusUpdater(u vcs.JobStatusUpdaterInterface) {
	tp.statusUpdater = u
}

// triggersFile represents the top-level structure of triggers.json.
type triggersFile struct {
	Type        string `json:"type"`
	OperationID string `json:"operation_id,omitempty"`
	TriggerType string `json:"trigger_type,omitempty"`
	// Workflows is the multi-workflow form: an eval emits one entry per matched
	// .reactorcide workflow YAML, so one event can spawn several independently
	// named workflows (per team/product/etc). Takes precedence over the legacy
	// single-workflow fields below when present.
	Workflows []triggerWorkflowSpec `json:"workflows,omitempty"`
	// Workflow + Jobs are the legacy single-workflow form (bare .reactorcide/
	// jobs), still accepted: they collapse to exactly one workflow.
	Workflow *triggerWorkflowSpec `json:"workflow,omitempty"`
	Jobs     []triggerJobSpec     `json:"jobs"`
}

type triggerWorkflowSpec struct {
	Name        string                 `json:"name"`
	Vars        map[string]interface{} `json:"vars"`
	OperationID string                 `json:"-"`
	TriggerType string                 `json:"-"`
	// Jobs are this workflow's nodes (multi-workflow form). Empty in the legacy
	// single-workflow form, where jobs live in triggersFile.Jobs instead.
	Jobs []triggerJobSpec `json:"jobs,omitempty"`
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
	CodeDir        string            `json:"code_dir"`
	JobDir         string            `json:"job_dir"`
	WorkingDir     string            `json:"working_dir"`
	RunAsUser      string            `json:"run_as_user"`
	Priority       *int              `json:"priority"`
	Timeout        *int              `json:"timeout"`
	Capabilities   []string          `json:"capabilities"`
	ForEach        []interface{}     `json:"for_each"`
	ItemVar        string            `json:"item_var"`

	// Characteristics/Resources, when set, override the parent (eval) job's
	// characteristics/resources for this triggered job. See
	// buildJobFromTrigger.
	Characteristics map[string]interface{} `json:"characteristics"`
	Resources       map[string]interface{} `json:"resources"`
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
	Image        string     `yaml:"image"`
	Command      string     `yaml:"command"`
	CodeDir      string     `yaml:"code_dir"`
	JobDir       string     `yaml:"job_dir"`
	WorkingDir   string     `yaml:"working_dir"`
	RunAs        *RunAsSpec `yaml:"run_as"`
	Timeout      *int       `yaml:"timeout"`
	Priority     *int       `yaml:"priority"`
	RawCommand   bool       `yaml:"raw_command"`
	Capabilities []string   `yaml:"capabilities"`
	// Characteristics/Resources -- see triggerJobSpec's doc comment.
	Characteristics map[string]interface{} `yaml:"characteristics"`
	Resources       map[string]interface{} `yaml:"resources"`
}

func runAsUserFromSpec(spec *RunAsSpec) string {
	if spec == nil {
		return ""
	}
	return spec.User
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

	// Normalize to a list of workflow batches. The multi-workflow form
	// (tf.Workflows) wins; otherwise the legacy single-workflow form
	// (tf.Workflow + tf.Jobs) collapses to exactly one batch.
	batches := tf.Workflows
	if len(batches) == 0 {
		batch := triggerWorkflowSpec{Jobs: tf.Jobs}
		if tf.Workflow != nil {
			batch.Name = tf.Workflow.Name
			batch.Vars = tf.Workflow.Vars
		}
		batches = []triggerWorkflowSpec{batch}
	}
	for i := range batches {
		batches[i].OperationID = strings.TrimSpace(tf.OperationID)
		batches[i].TriggerType = strings.TrimSpace(tf.TriggerType)
		if batches[i].TriggerType == "" {
			batches[i].TriggerType = "runnerlib"
		}
	}

	logger := logging.Log.WithField("parent_job_id", parentJob.JobID).WithField("workflow_count", len(batches))
	logger.Info("Processing triggers from eval job")

	// No workflow store (narrow test stores): fall back to submitting each
	// batch's jobs standalone, preserving the pre-workflow behavior.
	if _, err := tp.workflowStore(); err != nil {
		var createdJobIDs []string
		for i := range batches {
			specs, err := tp.resolveJobSpecs(batches[i].Jobs, workspaceDir)
			if err != nil {
				return createdJobIDs, err
			}
			for _, spec := range specs {
				jobID, err := tp.createAndSubmitJob(ctx, spec, parentJob)
				if err != nil {
					logger.WithError(err).WithField("job_name", spec.JobName).Error("Failed to create triggered job")
					continue
				}
				createdJobIDs = append(createdJobIDs, jobID)
			}
		}
		return createdJobIDs, nil
	}

	var createdJobIDs []string
	for i := range batches {
		ids, err := tp.processWorkflowBatch(ctx, parentJob, &batches[i], workspaceDir)
		if err != nil {
			return createdJobIDs, err
		}
		createdJobIDs = append(createdJobIDs, ids...)
	}
	return createdJobIDs, nil
}

// resolveJobSpecs loads any job_file references (a workflow node can reference a
// reusable .reactorcide/jobs/*.yaml and overlay inline fields on top) for a
// batch of job specs.
func (tp *TriggerProcessor) resolveJobSpecs(jobs []triggerJobSpec, workspaceDir string) ([]triggerJobSpec, error) {
	specs := make([]triggerJobSpec, 0, len(jobs))
	for _, spec := range jobs {
		if spec.JobFile != "" {
			if workspaceDir == "" {
				return nil, fmt.Errorf("job_file %q requires workspace-backed trigger processing", spec.JobFile)
			}
			baseSpec, err := tp.loadJobFile(workspaceDir, spec.JobFile)
			if err != nil {
				logging.Log.WithError(err).WithField("job_file", spec.JobFile).Error("Failed to load job file")
				continue
			}
			jobFile := spec.JobFile
			spec = tp.overlaySpec(baseSpec, spec)
			spec.JobFile = jobFile
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// processWorkflowBatch creates one workflow instance (find-or-create by name for
// this parent eval) from a batch, registers its nodes, and evaluates it. The
// eval is the workflow's parent but does NOT join it, so a single event can
// spawn several independently-named workflows.
func (tp *TriggerProcessor) processWorkflowBatch(ctx context.Context, parentJob *models.Job, spec *triggerWorkflowSpec, workspaceDir string) ([]string, error) {
	if len(spec.Jobs) == 0 {
		return nil, nil
	}
	specs, err := tp.resolveJobSpecs(spec.Jobs, workspaceDir)
	if err != nil {
		return nil, err
	}
	wf, err := tp.ensureWorkflow(ctx, parentJob, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow: %w", err)
	}
	if len(spec.Vars) > 0 {
		if err := tp.addWorkflowVars(ctx, wf, spec.Vars, nil, &parentJob.JobID); err != nil {
			return nil, fmt.Errorf("failed to add workflow vars: %w", err)
		}
	}
	existingNodes, err := tp.workflowStore()
	if err != nil {
		return nil, err
	}
	registered, err := existingNodes.ListWorkflowNodes(ctx, wf.WorkflowID)
	if err != nil {
		return nil, err
	}
	if len(registered) > 0 {
		return tp.evaluateWorkflow(ctx, wf)
	}
	if err := tp.createWorkflowNodes(ctx, wf, specs); err != nil {
		return nil, fmt.Errorf("failed to create workflow nodes: %w", err)
	}
	if err := tp.refreshRootForChildRegistration(ctx, wf); err != nil {
		return nil, fmt.Errorf("failed to register child workflow with root: %w", err)
	}
	return tp.evaluateWorkflow(ctx, wf)
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
		JobName:         def.Name,
		ContainerImage:  def.Job.Image,
		JobCommand:      def.Job.Command,
		CodeDir:         def.Job.CodeDir,
		JobDir:          def.Job.JobDir,
		WorkingDir:      def.Job.WorkingDir,
		RunAsUser:       runAsUserFromSpec(def.Job.RunAs),
		Timeout:         def.Job.Timeout,
		Priority:        def.Job.Priority,
		Capabilities:    def.Job.Capabilities,
		Env:             def.Environment,
		Characteristics: def.Job.Characteristics,
		Resources:       def.Job.Resources,
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
	if overlay.JobFile != "" {
		result.JobFile = overlay.JobFile
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
	if overlay.CodeDir != "" {
		result.CodeDir = overlay.CodeDir
	}
	if overlay.JobDir != "" {
		result.JobDir = overlay.JobDir
	}
	if overlay.WorkingDir != "" {
		result.WorkingDir = overlay.WorkingDir
	}
	if overlay.RunAsUser != "" {
		result.RunAsUser = overlay.RunAsUser
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
	if len(overlay.ForEach) > 0 {
		result.ForEach = overlay.ForEach
	}
	if overlay.ItemVar != "" {
		result.ItemVar = overlay.ItemVar
	}
	if len(overlay.Characteristics) > 0 {
		result.Characteristics = overlay.Characteristics
	}
	if len(overlay.Resources) > 0 {
		result.Resources = overlay.Resources
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

// queueResolvingStore is the narrow store capability trigger/workflow job
// submission uses to resolve a job's characteristics to a queue UUID
// (find-or-create) before it is persisted/submitted. Defined here on the
// consumer side (repo convention: narrow interface + type assertion on the
// store), mirroring internal/handlers/job_handler.go's identical interface
// for the REST submit path. The concrete PostgresDbStore satisfies it via
// internal/store/postgres_store/queue_operations.go.
type queueResolvingStore interface {
	FindOrCreateQueueByCharacteristics(ctx context.Context, chars characteristics.Characteristics) (*models.Queue, error)
}

// resolveJobQueue resolves job.Characteristics to a queue (find-or-create)
// and sets job.QueueName to the resolved Queue.QueueUUID. A no-op (QueueName
// left as buildJobFromTrigger set it -- the parent's or an override's) when
// the store doesn't implement queueResolvingStore.
func (tp *TriggerProcessor) resolveJobQueue(ctx context.Context, job *models.Job) error {
	qs, ok := tp.store.(queueResolvingStore)
	if !ok {
		return nil
	}
	queue, err := qs.FindOrCreateQueueByCharacteristics(ctx, job.Characteristics)
	if err != nil {
		return fmt.Errorf("resolving queue: %w", err)
	}
	job.QueueName = queue.QueueUUID
	return nil
}

// createAndSubmitJob creates a single job from a trigger spec and submits it to Corndogs.
// Returns the created job ID on success.
func (tp *TriggerProcessor) createAndSubmitJob(ctx context.Context, spec triggerJobSpec, parentJob *models.Job) (string, error) {
	job, err := tp.buildJobFromTrigger(spec, parentJob)
	if err != nil {
		return "", fmt.Errorf("failed to build triggered job: %w", err)
	}

	if err := tp.resolveJobQueue(ctx, job); err != nil {
		return "", err
	}

	if err := tp.store.CreateJob(ctx, job); err != nil {
		return "", fmt.Errorf("failed to create job in database: %w", err)
	}

	// Register as a pending check on the commit immediately, before Corndogs
	// submission, so branch protection sees every child as a required check
	// without waiting for a worker to pick it up.
	if tp.statusUpdater != nil {
		if err := tp.statusUpdater.UpdateJobStatus(ctx, job); err != nil {
			logging.Log.WithError(err).WithField("job_id", job.JobID).Warn("Failed to register pending check for triggered job")
		}
	}

	if tp.corndogsClient == nil {
		return job.JobID, nil
	}

	taskPayload := tp.buildTaskPayload(job)

	task, err := tp.corndogsClient.SubmitTaskToQueue(ctx, job.QueueName, taskPayload, int64(job.Priority))
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

// buildJobFromTrigger creates a models.Job from a trigger spec and parent
// job. Returns an error only when the spec's own `characteristics`/
// `resources` block fails validation (internal/characteristics.
// ParseJobCharacteristics / internal/resources.ParseResources) -- every
// other field is either copied verbatim or defaulted, so it never fails.
func (tp *TriggerProcessor) buildJobFromTrigger(spec triggerJobSpec, parentJob *models.Job) (*models.Job, error) {
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
		JobFile:     spec.JobFile,
		Description: fmt.Sprintf("Triggered by eval job %s", parentJob.JobID),
		Status:      "submitted",
		QueueName:   parentJob.QueueName,
		JobEnvVars:  envVars,
		CodeDir:     DefaultJobCodeDir(parentJob.CodeDir),
		JobDir:      DefaultJobDir(parentJob.CodeDir, parentJob.JobDir),
	}

	// Characteristics: the child spec overrides the parent's when it declares
	// its own `characteristics` block, otherwise it inherits the parent
	// (eval) job's characteristics wholesale. This mirrors QueueName's
	// inheritance above; createAndSubmitJob/submitWorkflowNode re-resolve
	// QueueName from this value right before submitting, so QueueName here is
	// only a starting point for stores that don't support queue resolution.
	if len(spec.Characteristics) > 0 {
		chars, err := characteristics.ParseJobCharacteristics(spec.Characteristics)
		if err != nil {
			return nil, fmt.Errorf("invalid characteristics in triggered job %q: %w", spec.JobName, err)
		}
		job.Characteristics = chars
	} else {
		job.Characteristics = parentJob.Characteristics
	}

	// Resources: only set when the child spec explicitly declares a
	// `resources` block; otherwise left empty so the resource_cpu_request/
	// resource_cpu_limit/resource_memory_limit column defaults apply on
	// insert (same "leave empty for DB defaults" behavior as every other
	// submit path -- see internal/resources.ParseResources).
	if len(spec.Resources) > 0 {
		cpuRequest, cpuLimit, memoryLimit, err := resources.ParseResources(spec.Resources)
		if err != nil {
			return nil, fmt.Errorf("invalid resources in triggered job %q: %w", spec.JobName, err)
		}
		job.ResourceCPURequest = cpuRequest
		job.ResourceCPULimit = cpuLimit
		job.ResourceMemoryLimit = memoryLimit
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
	if spec.CodeDir != "" {
		job.CodeDir = DefaultJobCodeDir(spec.CodeDir)
		if spec.JobDir == "" && spec.WorkingDir == "" {
			job.JobDir = DefaultJobDir(job.CodeDir, "")
		}
	}
	if spec.JobDir != "" {
		job.JobDir = DefaultJobDir(job.CodeDir, spec.JobDir)
	}
	if spec.WorkingDir != "" {
		job.JobDir = spec.WorkingDir
	}
	if spec.RunAsUser != "" {
		job.RunAsUser = spec.RunAsUser
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
			if err := metadata.ApplyToJob(job); err != nil {
				job.Notes = parentJob.Notes
			}
		} else {
			job.Notes = parentJob.Notes
		}
	}

	return job, nil
}

// buildTaskPayload creates a Corndogs TaskPayload from a job.
func (tp *TriggerProcessor) buildTaskPayload(job *models.Job) *corndogs.TaskPayload {
	return BuildTaskPayload(job)
}

// BuildTaskPayload is the exported, receiver-free form of buildTaskPayload:
// it depends only on the job, not on any TriggerProcessor field, so it's
// safe to call from other packages that need to mirror the exact submission
// shape trigger_processor.go/workflow_runtime.go use — currently
// internal/jobcontrol.RetryJob, which resubmits a cloned job the same way a
// freshly triggered or workflow-node job is submitted.
func BuildTaskPayload(job *models.Job) *corndogs.TaskPayload {
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
			"run_as_user": job.RunAsUser,
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
