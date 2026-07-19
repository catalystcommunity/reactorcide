package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// JSONB represents a JSON field that can be stored in PostgreSQL JSONB column
type JSONB map[string]interface{}

// Value implements driver.Valuer interface for database storage
func (j JSONB) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal(j)
}

// Scan implements sql.Scanner interface for database retrieval
func (j *JSONB) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}

	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("cannot scan %T into JSONB", value)
	}

	return json.Unmarshal(bytes, j)
}

// Job represents a job submission in the coordinator API
type Job struct {
	JobID     string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"job_id"`
	CreatedAt time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	UserID    string    `gorm:"type:uuid;not null" json:"user_id"`
	ProjectID *string   `gorm:"type:uuid" json:"project_id"`

	// Job metadata
	Name        string `gorm:"type:text;not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	JobFile     string `gorm:"type:text" json:"job_file,omitempty"`

	// Source configuration (untrusted code being tested - optional)
	// VCS-agnostic fields: works with git, mercurial, svn, or any source control
	SourceURL  *string     `gorm:"type:text" json:"source_url"`
	SourceRef  *string     `gorm:"type:text" json:"source_ref"`
	SourceType *SourceType `gorm:"type:source_type" json:"source_type"`
	SourcePath *string     `gorm:"type:text" json:"source_path"`

	// CI Source configuration (trusted CI pipeline code - optional)
	CISourceType *SourceType `gorm:"type:source_type" json:"ci_source_type"`
	CISourceURL  *string     `gorm:"type:text" json:"ci_source_url"`
	CISourceRef  *string     `gorm:"type:text" json:"ci_source_ref"`

	// Container configuration
	ContainerImage *string `gorm:"type:text" json:"container_image"` // Custom image per job

	// Runnerlib configuration
	CodeDir     string `gorm:"type:text;not null;default:'/job/src'" json:"code_dir"`
	JobDir      string `gorm:"type:text;not null;default:'/job/src'" json:"job_dir"`
	JobCommand  string `gorm:"type:text;not null" json:"job_command"`
	RunnerImage string `gorm:"type:text;not null;default:'quay.io/catalystcommunity/reactorcide_runner'" json:"runner_image"`
	JobEnvVars  JSONB  `gorm:"type:jsonb" json:"job_env_vars"`
	JobEnvFile  string `gorm:"type:text" json:"job_env_file"`

	// Job execution settings
	TimeoutSeconds int            `gorm:"default:3600" json:"timeout_seconds"`
	Priority       int            `gorm:"default:0" json:"priority"`
	Capabilities   pq.StringArray `gorm:"type:text[]" json:"capabilities"`
	RunAsUser      string         `gorm:"type:text" json:"run_as_user"`

	// Queue integration
	QueueName       string `gorm:"type:text;not null;default:'reactorcide-jobs'" json:"queue_name"`
	AutoTargetState string `gorm:"type:text;default:'running'" json:"auto_target_state"`

	// Current state
	//
	// "cancelling" is a transient state: it means a graceful cancel has been
	// requested (via CancelJob/CancelWorkflow) but the worker has not yet
	// confirmed the job container has stopped. The worker's heartbeat/
	// supervision loop observes this state and drives the job to its terminal
	// "cancelled" status. Requires the "cancelling" value to also be present
	// in the jobs.status CHECK constraint (owned by the schema/migration
	// wave, see coredb/migrations) — this Go-level enum is documentation-only
	// since models are hand-matched to SQL rather than AutoMigrated.
	Status         string  `gorm:"type:text;not null;default:'submitted';check:status IN ('submitted', 'queued', 'running', 'cancelling', 'completed', 'failed', 'cancelled', 'timeout')" json:"status"`
	CorndogsTaskID *string `gorm:"type:uuid" json:"corndogs_task_id"`

	// CancelMode records which kind of cancel request drove the job into
	// "cancelling": "cancel" (graceful — SIGTERM, runnerlib cleanup hooks,
	// forced kill only after the configured grace) or "kill" (immediate
	// forced Cleanup, no grace). Empty/NULL outside the "cancelling" status
	// (and briefly during "cancelled" finalization). See CanBeKilled and
	// IsKillRequested, and internal/jobcontrol.transitionJob, which is the
	// only writer. Requires "cancel_mode" to also exist as a column with a
	// CHECK constraint (coredb/migrations/000019_job_cancel_mode.sql) —
	// this Go-level enum is documentation-only, same caveat as Status above.
	CancelMode string `gorm:"type:text;check:cancel_mode IN ('cancel', 'kill')" json:"cancel_mode,omitempty"`

	// Execution metadata
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	ExitCode    *int       `json:"exit_code"`
	WorkerID    *string    `gorm:"type:text" json:"worker_id"`
	Notes       string     `gorm:"type:text" json:"notes"`
	RetryCount  int        `gorm:"default:0" json:"retry_count"`
	LastError   string     `gorm:"type:text" json:"last_error"`

	// Object store references
	LogsObjectKey      string `gorm:"type:text" json:"logs_object_key"`
	ArtifactsObjectKey string `gorm:"type:text" json:"artifacts_object_key"`

	// Event metadata for webhook-triggered jobs
	EventMetadata    JSONB   `gorm:"type:jsonb" json:"event_metadata"`
	ParentJobID      *string `gorm:"type:uuid" json:"parent_job_id"`
	WorkflowID       *string `gorm:"type:uuid" json:"workflow_id"`
	WorkflowNodeID   *string `gorm:"type:uuid" json:"workflow_node_id"`
	WorkflowRunID    *string `gorm:"type:uuid" json:"workflow_run_id"`
	WorkflowNodeName string  `gorm:"type:text" json:"workflow_node_name"`

	// Denormalized VCS metadata for fast lookup by (repo, pr, commit).
	// Populated at job-creation time from Notes JSON; Notes remains authoritative.
	VCSRepo   *string `gorm:"type:text" json:"vcs_repo,omitempty"`
	PRNumber  *int    `gorm:"type:integer" json:"pr_number,omitempty"`
	CommitSHA *string `gorm:"type:text" json:"commit_sha,omitempty"`

	// Relationships
	User      User     `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Project   *Project `gorm:"foreignKey:ProjectID" json:"project,omitempty"`
	ParentJob *Job     `gorm:"foreignKey:ParentJobID" json:"parent_job,omitempty"`
}

// TableName specifies the table name for the model
func (Job) TableName() string {
	return "jobs"
}

// IsRunning returns true if the job is in a running state
func (j *Job) IsRunning() bool {
	return j.Status == "running"
}

// IsCancelling returns true if a graceful cancel has been requested but the
// worker has not yet confirmed the job container has stopped. This is a
// transient, non-terminal state: IsCompleted() deliberately excludes it so
// callers (log tailing, WS status filters, etc.) keep treating the job as
// "still active" until the worker lands on the terminal "cancelled" status.
func (j *Job) IsCancelling() bool {
	return j.Status == "cancelling"
}

// IsCompleted returns true if the job has reached a terminal state (success,
// failure, or confirmed cancellation). "cancelling" is intentionally NOT
// included here — it is a transient state, not a terminal one.
func (j *Job) IsCompleted() bool {
	return j.Status == "completed" || j.Status == "failed" || j.Status == "cancelled" || j.Status == "timeout"
}

// CanBeCancelled returns true if the job can be moved into the cancel flow.
// Submitted/queued jobs haven't started a container yet, so cancellation is
// immediate (handled entirely by the API layer). Running jobs transition to
// "cancelling" so the worker can drive a graceful stop. Jobs already
// cancelling, or in any terminal state, cannot be cancelled again.
func (j *Job) CanBeCancelled() bool {
	return j.Status == "submitted" || j.Status == "queued" || j.Status == "running"
}

// CanBeKilled returns true if the job can be moved into (or escalated
// within) the kill flow. Unlike CanBeCancelled, this also admits a job
// already in "cancelling": a graceful cancel that's stuck (worker slow to
// observe it, grace period still running, etc.) can always be escalated to
// an immediate kill. It cannot be re-cancelled gracefully — CanBeCancelled
// stays false for "cancelling" — because a second graceful cancel request
// has nothing new to do that the first one hasn't already triggered.
func (j *Job) CanBeKilled() bool {
	return j.CanBeCancelled() || j.IsCancelling()
}

// IsKillRequested returns true if the job's cancel-in-progress is a kill
// (immediate forced Cleanup) rather than a graceful cancel (SIGTERM +
// grace). Only meaningful while IsCancelling() is true; CancelMode is
// otherwise empty. See CancelMode's doc comment for the full rationale.
func (j *Job) IsKillRequested() bool {
	return j.CancelMode == "kill"
}

// IsRetryable returns true if the job may be retried: status is exactly
// "failed" or "cancelled", nothing else. This is deliberately narrower than
// IsCompleted (which also admits "completed" and "timeout") — a
// successfully completed job has nothing to retry, and a job that hit its
// execution timeout without ever being cancelled is not retryable either,
// per the retry feature spec. Single source of truth for
// internal/jobcontrol.RetryJob and REST/CSIL retry authorization so they
// can't drift apart on which statuses qualify.
func (j *Job) IsRetryable() bool {
	return j.Status == "failed" || j.Status == "cancelled"
}
