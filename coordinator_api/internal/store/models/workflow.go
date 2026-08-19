package models

import (
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

type WorkflowInstance struct {
	WorkflowID         string     `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"workflow_id"`
	CreatedAt          time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt          time.Time  `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	UserID             string     `gorm:"type:uuid;default:null" json:"user_id,omitempty"`
	OrgID              string     `gorm:"column:org_id;type:uuid;not null" json:"-"`
	ProjectID          *string    `gorm:"type:uuid" json:"project_id"`
	ParentJobID        *string    `gorm:"type:uuid" json:"parent_job_id"`
	RootWorkflowID     *string    `gorm:"type:uuid" json:"root_workflow_id,omitempty"`
	ParentWorkflowID   *string    `gorm:"type:uuid" json:"parent_workflow_id,omitempty"`
	OriginJobID        *string    `gorm:"type:uuid" json:"origin_job_id,omitempty"`
	OriginType         string     `gorm:"type:text" json:"origin_type,omitempty"`
	TriggerOperationID string     `gorm:"type:text" json:"trigger_operation_id,omitempty"`
	TriggerType        string     `gorm:"type:text;not null;default:'runnerlib'" json:"trigger_type,omitempty"`
	Name               string     `gorm:"type:text;not null" json:"name"`
	WorkflowSecurityID string     `gorm:"type:text;not null" json:"workflow_security_id"`
	SourceFile         string     `gorm:"type:text" json:"source_file,omitempty"`
	Status             string     `gorm:"type:text;not null;default:'evaluating'" json:"status"`
	QueueName          string     `gorm:"type:text;not null;default:'reactorcide-jobs'" json:"queue_name"`
	VCSProvider        string     `gorm:"type:text" json:"vcs_provider"`
	VCSRepo            string     `gorm:"type:text" json:"vcs_repo"`
	PRNumber           *int       `gorm:"type:integer" json:"pr_number"`
	CommitSHA          string     `gorm:"type:text" json:"commit_sha"`
	StatusContext      string     `gorm:"type:text;not null;default:'Reactorcide Jobs'" json:"status_context"`
	CommentMarker      string     `gorm:"type:text" json:"comment_marker"`
	CompletedAt        *time.Time `json:"completed_at"`
	LastError          string     `gorm:"type:text" json:"last_error"`
	CIOrigin           string     `gorm:"type:text;default:null" json:"ci_origin,omitempty"`
	CIRepository       string     `gorm:"type:text;default:null" json:"ci_repository,omitempty"`
	CISHA              string     `gorm:"column:ci_sha;type:text;default:null" json:"ci_sha,omitempty"`
	ExecutionProfile   string     `gorm:"type:text;default:null" json:"execution_profile,omitempty"`
	WorkerClass        string     `gorm:"type:text" json:"worker_class,omitempty"`
	PolicyRevision     string     `gorm:"type:text;default:null" json:"policy_revision,omitempty"`
	PolicyRuleID       string     `gorm:"type:text;default:null" json:"policy_rule_id,omitempty"`
	ApprovalID         *string    `gorm:"type:uuid" json:"approval_id,omitempty"`
}

// OwnershipOrgID returns the organization that owns the workflow. UserID is
// a compatibility fallback for rows that predate first-class organizations.
func (w *WorkflowInstance) OwnershipOrgID() string {
	if w == nil {
		return ""
	}
	if w.OrgID != "" {
		return w.OrgID
	}
	return w.UserID
}

func (WorkflowInstance) TableName() string {
	return "workflow_instances"
}

// BeforeCreate maps legacy creator attribution to organization ownership.
// New workflow creation paths must set OrgID from the parent.
func (w *WorkflowInstance) BeforeCreate(_ *gorm.DB) error {
	if w.OrgID == "" {
		w.OrgID = w.UserID
	}
	return nil
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
	// Per-node authority override. Empty values mean the node inherits the
	// workflow-level authority. Only coordinator policy can set an override.
	CIOrigin         string  `gorm:"type:text" json:"ci_origin"`
	CIRepository     string  `gorm:"type:text" json:"ci_repository"`
	CISHA            string  `gorm:"column:ci_sha;type:text" json:"ci_sha"`
	ExecutionProfile string  `gorm:"type:text" json:"execution_profile"`
	WorkerClass      string  `gorm:"type:text" json:"worker_class"`
	PolicyRevision   string  `gorm:"type:text" json:"policy_revision"`
	PolicyRuleID     string  `gorm:"type:text" json:"policy_rule_id"`
	ApprovalID       *string `gorm:"type:uuid" json:"approval_id,omitempty"`
}

func (WorkflowNode) TableName() string {
	return "workflow_nodes"
}

// HasAuthorityOverride reports whether this node carries its own recorded
// authority instead of inheriting the workflow-level authority.
func (n *WorkflowNode) HasAuthorityOverride() bool {
	return n.CIOrigin != "" || n.ExecutionProfile != "" || n.WorkerClass != ""
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
	WorkflowID         string            `json:"workflow_id"`
	Kind               string            `json:"kind"`
	Name               string            `json:"name"`
	Status             string            `json:"status"`
	UserID             string            `json:"-"`
	OrgID              string            `json:"-"`
	ProjectID          *string           `json:"project_id,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
	CompletedAt        *time.Time        `json:"completed_at,omitempty"`
	QueueName          string            `json:"queue_name"`
	VCSRepo            string            `json:"vcs_repo,omitempty"`
	PRNumber           *int              `json:"pr_number,omitempty"`
	CommitSHA          string            `json:"commit_sha,omitempty"`
	JobCount           int               `json:"job_count"`
	RunningCount       int               `json:"running_count"`
	CompletedCount     int               `json:"completed_count"`
	FailedCount        int               `json:"failed_count"`
	SkippedCount       int               `json:"skipped_count"`
	LooseJobID         *string           `json:"loose_job_id,omitempty"`
	LooseJobExit       *int              `json:"loose_job_exit,omitempty"`
	DecisionSummary    string            `json:"decision_summary,omitempty"`
	ParentJobID        *string           `json:"parent_job_id,omitempty"`
	RootWorkflowID     *string           `json:"root_workflow_id,omitempty"`
	ParentWorkflowID   *string           `json:"parent_workflow_id,omitempty"`
	OriginJobID        *string           `json:"origin_job_id,omitempty"`
	OriginType         string            `json:"origin_type,omitempty"`
	TriggerOperationID string            `json:"trigger_operation_id,omitempty"`
	TriggerType        string            `json:"trigger_type,omitempty"`
	CIOrigin           string            `json:"ci_origin,omitempty"`
	ExecutionProfile   string            `json:"execution_profile,omitempty"`
	WorkerClass        string            `json:"worker_class,omitempty"`
	Children           []WorkflowSummary `gorm:"-" json:"children,omitempty"`
}

// OwnershipOrgID returns the organization that owns the summary. UserID is
// a compatibility fallback for rows that predate first-class organizations.
func (w *WorkflowSummary) OwnershipOrgID() string {
	if w == nil {
		return ""
	}
	if w.OrgID != "" {
		return w.OrgID
	}
	return w.UserID
}
