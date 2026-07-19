package models

import (
	"time"

	"github.com/lib/pq"
)

type WorkflowInstance struct {
	WorkflowID    string     `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"workflow_id"`
	CreatedAt     time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	UserID        string     `gorm:"type:uuid;not null" json:"user_id"`
	ProjectID     *string    `gorm:"type:uuid" json:"project_id"`
	ParentJobID   *string    `gorm:"type:uuid" json:"parent_job_id"`
	Name          string     `gorm:"type:text;not null" json:"name"`
	Status        string     `gorm:"type:text;not null;default:'evaluating'" json:"status"`
	QueueName     string     `gorm:"type:text;not null;default:'reactorcide-jobs'" json:"queue_name"`
	VCSProvider   string     `gorm:"type:text" json:"vcs_provider"`
	VCSRepo       string     `gorm:"type:text" json:"vcs_repo"`
	PRNumber      *int       `gorm:"type:integer" json:"pr_number"`
	CommitSHA     string     `gorm:"type:text" json:"commit_sha"`
	StatusContext string     `gorm:"type:text;not null;default:'Reactorcide Jobs'" json:"status_context"`
	CommentMarker string     `gorm:"type:text" json:"comment_marker"`
	CompletedAt   *time.Time `json:"completed_at"`
	LastError     string     `gorm:"type:text" json:"last_error"`
}

func (WorkflowInstance) TableName() string {
	return "workflow_instances"
}

// IsRetryable returns true if the workflow instance may be retried into a
// brand-new instance: status is exactly "failed" or "cancelled". Unlike
// Job.IsRetryable, this deliberately does NOT admit "timeout": a
// WorkflowInstance's Status is never set to "timeout" in the first place —
// worker/workflow_runtime.go's computeWorkflowStatus folds a "timeout" node
// status into the workflow's aggregate "failed" status (see
// isWorkflowNodeFailure/computeWorkflowStatus's fail-fast branch), so
// "failed" already covers workflows containing a timed-out node. A workflow
// that finished "success" or "skipped" has nothing to retry, and one still
// "evaluating"/"running"/"cancelling" isn't in a terminal state yet.
func (w *WorkflowInstance) IsRetryable() bool {
	return w.Status == "failed" || w.Status == "cancelled"
}

type WorkflowNode struct {
	NodeID                   string         `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"node_id"`
	CreatedAt                time.Time      `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt                time.Time      `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	WorkflowID               string         `gorm:"type:uuid;not null" json:"workflow_id"`
	Name                     string         `gorm:"type:text;not null" json:"name"`
	DisplayName              string         `gorm:"type:text;not null" json:"display_name"`
	Status                   string         `gorm:"type:text;not null;default:'pending'" json:"status"`
	DependsOn                pq.StringArray `gorm:"type:text[]" json:"depends_on"`
	Condition                string         `gorm:"type:text;not null;default:'all_success'" json:"condition"`
	JobID                    *string        `gorm:"type:uuid" json:"job_id"`
	JobSpec                  JSONB          `gorm:"type:jsonb" json:"job_spec"`
	ItemIndex                *int           `gorm:"type:integer" json:"item_index"`
	ItemValue                JSONB          `gorm:"type:jsonb" json:"item_value"`
	ItemVar                  string         `gorm:"type:text" json:"item_var"`
	DecisionReason           string         `gorm:"type:text" json:"decision_reason"`
	CompletedAt              *time.Time     `json:"completed_at"`
	LastSuccessfulDurationMs *int64         `gorm:"type:bigint" json:"last_successful_duration_ms"`
}

func (WorkflowNode) TableName() string {
	return "workflow_nodes"
}

type WorkflowVar struct {
	WorkflowID   string    `gorm:"primaryKey;type:uuid" json:"workflow_id"`
	Key          string    `gorm:"primaryKey;type:text" json:"key"`
	Value        JSONB     `gorm:"type:jsonb" json:"value"`
	ValueHash    string    `gorm:"type:text;not null" json:"value_hash"`
	SourceNodeID *string   `gorm:"type:uuid" json:"source_node_id"`
	SourceJobID  *string   `gorm:"type:uuid" json:"source_job_id"`
	CreatedAt    time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt    time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (WorkflowVar) TableName() string {
	return "workflow_vars"
}

type WorkflowEvent struct {
	EventID    string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"event_id"`
	CreatedAt  time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	WorkflowID string    `gorm:"type:uuid;not null" json:"workflow_id"`
	NodeID     *string   `gorm:"type:uuid" json:"node_id"`
	JobID      *string   `gorm:"type:uuid" json:"job_id"`
	EventType  string    `gorm:"type:text;not null" json:"event_type"`
	Reason     string    `gorm:"type:text" json:"reason"`
	Details    JSONB     `gorm:"type:jsonb" json:"details"`
}

func (WorkflowEvent) TableName() string {
	return "workflow_events"
}

type WorkflowSummary struct {
	WorkflowID      string     `json:"workflow_id"`
	Kind            string     `json:"kind"`
	Name            string     `json:"name"`
	Status          string     `json:"status"`
	UserID          string     `json:"-"`
	ProjectID       *string    `json:"project_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	QueueName       string     `json:"queue_name"`
	VCSRepo         string     `json:"vcs_repo,omitempty"`
	PRNumber        *int       `json:"pr_number,omitempty"`
	CommitSHA       string     `json:"commit_sha,omitempty"`
	JobCount        int        `json:"job_count"`
	RunningCount    int        `json:"running_count"`
	CompletedCount  int        `json:"completed_count"`
	FailedCount     int        `json:"failed_count"`
	SkippedCount    int        `json:"skipped_count"`
	LooseJobID      *string    `json:"loose_job_id,omitempty"`
	LooseJobExit    *int       `json:"loose_job_exit,omitempty"`
	DecisionSummary string     `json:"decision_summary,omitempty"`
}
