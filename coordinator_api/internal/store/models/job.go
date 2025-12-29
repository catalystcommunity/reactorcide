package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
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
	TimeoutSeconds int `gorm:"default:3600" json:"timeout_seconds"`
	Priority       int `gorm:"default:0" json:"priority"`

	// Queue integration
	QueueName       string `gorm:"type:text;not null;default:'reactorcide-jobs'" json:"queue_name"`
	AutoTargetState string `gorm:"type:text;default:'running'" json:"auto_target_state"`

	// Current state
	Status         string  `gorm:"type:text;not null;default:'submitted';check:status IN ('submitted', 'queued', 'running', 'completed', 'failed', 'cancelled', 'timeout')" json:"status"`
	CorndogsTaskID *string `gorm:"type:uuid" json:"corndogs_task_id"`

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

	// Relationships
	User    User     `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Project *Project `gorm:"foreignKey:ProjectID" json:"project,omitempty"`
}

// TableName specifies the table name for the model
func (Job) TableName() string {
	return "jobs"
}

// IsRunning returns true if the job is in a running state
func (j *Job) IsRunning() bool {
	return j.Status == "running"
}

// IsCompleted returns true if the job has finished (success or failure)
func (j *Job) IsCompleted() bool {
	return j.Status == "completed" || j.Status == "failed" || j.Status == "cancelled" || j.Status == "timeout"
}

// CanBeCancelled returns true if the job can be cancelled
func (j *Job) CanBeCancelled() bool {
	return j.Status == "submitted" || j.Status == "queued" || j.Status == "running"
}
