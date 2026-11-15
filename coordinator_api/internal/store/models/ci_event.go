package models

import (
	"time"

	"gorm.io/gorm"
)

// CI event sources.
const (
	CIEventSourceWebhook  = "webhook"
	CIEventSourceSchedule = "schedule"
	CIEventSourceManual   = "manual"
	CIEventSourceRetry    = "retry"
)

// CI event statuses.
const (
	CIEventStatusReceived   = "received"
	CIEventStatusEvaluating = "evaluating"
	CIEventStatusEvaluated  = "evaluated"
	CIEventStatusFailed     = "failed"
	CIEventStatusDuplicate  = "duplicate"
)

// Duplicate-event settings. A project value of "inherit" uses the
// organization value; the organization default is "run" (owner decision D1).
const (
	DuplicateEventsInherit = "inherit"
	DuplicateEventsRun     = "run"
	DuplicateEventsSkip    = "skip"
)

// Workflow decisions an eval records for one workflow definition.
const (
	WorkflowDecisionRun         = "run"
	WorkflowDecisionSkipped     = "skipped"
	WorkflowDecisionInvalid     = "invalid"
	WorkflowDecisionNotAdmitted = "not_admitted"
)

// CIEvent is one incoming event: a webhook delivery, a schedule fire, or a
// manual submit. Its eval job answers "what must run for this event?".
type CIEvent struct {
	EventID            string         `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"event_id"`
	OrgID              string         `gorm:"column:org_id;type:uuid;not null" json:"-"`
	ProjectID          *string        `gorm:"type:uuid" json:"project_id,omitempty"`
	Source             string         `gorm:"type:text;not null" json:"source"`
	Provider           string         `gorm:"type:text;not null;default:''" json:"provider"`
	DeliveryID         string         `gorm:"type:text;not null;default:''" json:"delivery_id,omitempty"`
	EventType          string         `gorm:"type:text;not null" json:"event_type"`
	Repo               string         `gorm:"type:text;not null;default:''" json:"repo"`
	Ref                string         `gorm:"type:text;not null;default:''" json:"ref,omitempty"`
	Branch             string         `gorm:"type:text;not null;default:''" json:"branch,omitempty"`
	CommitSHA          string         `gorm:"type:text;not null;default:''" json:"commit_sha"`
	BaseSHA            string         `gorm:"type:text;not null;default:''" json:"base_sha,omitempty"`
	PRNumber           *int           `gorm:"type:integer" json:"pr_number,omitempty"`
	Actor              string         `gorm:"type:text;not null;default:''" json:"actor,omitempty"`
	ScheduleID         *string        `gorm:"type:uuid" json:"schedule_id,omitempty"`
	ReceivedAt         time.Time      `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"received_at"`
	UpdatedAt          time.Time      `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	Status             string         `gorm:"type:text;not null;default:'received'" json:"status"`
	EvalJobID          *string        `gorm:"type:uuid" json:"eval_job_id,omitempty"`
	DuplicateOfEventID *string        `gorm:"type:uuid" json:"duplicate_of_event_id,omitempty"`
	IdempotencyKey     *string        `gorm:"type:text" json:"-"`
	SemanticKey        string         `gorm:"type:text;not null;default:''" json:"-"`
	Summary            CIEventSummary `gorm:"type:jsonb;serializer:json;not null" json:"summary"`
	// The last published "Reactorcide Checks" commit status of this event.
	ChecksState          string     `gorm:"type:text;not null;default:''" json:"checks_state,omitempty"`
	ChecksDescription    string     `gorm:"type:text;not null;default:''" json:"checks_description,omitempty"`
	ChecksTargetURL      string     `gorm:"type:text;not null;default:''" json:"checks_target_url,omitempty"`
	ChecksPublishedAt    *time.Time `json:"checks_published_at,omitempty"`
	PolicyViolationCount int        `gorm:"not null;default:0" json:"policy_violation_count"`
}

// CIEventChecksContext is the commit status context of a CI event's aggregate
// result. Branch protection requires this one name.
const CIEventChecksContext = "Reactorcide Checks"

// CIEventChecks is one computed or published state of an event's checks.
type CIEventChecks struct {
	State       string
	Description string
	TargetURL   string
}

func (CIEvent) TableName() string { return "ci_events" }

// CIEventSummary holds display-safe facts about an event. It must never hold
// secrets or a raw provider payload.
type CIEventSummary struct {
	ChangedFilesCount   int      `json:"changed_files_count"`
	ChangedFilesPreview []string `json:"changed_files_preview,omitempty"`
	// ChangedFilesKey is the object-storage key of the full changed-file list,
	// when one was stored.
	ChangedFilesKey string `json:"changed_files_key,omitempty"`
	Action          string `json:"action,omitempty"`
	HeadRef         string `json:"head_ref,omitempty"`
	BaseRef         string `json:"base_ref,omitempty"`
}

// DecisionReason is one structured reason why a workflow (or later a job)
// runs or does not run. Kind selects which optional fields are set:
//
//   - event:     Matched, Value (the event), Patterns (configured events)
//   - branch:    Matched, Value (the branch), Patterns
//   - paths:     Matched, Include, Exclude, MatchedFiles (capped), MatchedCount
//   - policy:    CIOrigin, Profile, WorkerClass, RuleID, PolicyRevision, Reasons
//   - admission: Detail (why the coordinator refused or failed the workflow)
//   - jobs:      Detail (for example "no jobs")
//   - invalid:   Detail (the definition error)
//   - schedule:  Value (cron), Detail
//   - manual:    Value (actor)
type DecisionReason struct {
	Kind           string   `json:"kind"`
	Matched        *bool    `json:"matched,omitempty"`
	Value          string   `json:"value,omitempty"`
	Patterns       []string `json:"patterns,omitempty"`
	Include        []string `json:"include,omitempty"`
	Exclude        []string `json:"exclude,omitempty"`
	MatchedFiles   []string `json:"matched_files,omitempty"`
	MatchedCount   *int     `json:"matched_count,omitempty"`
	Detail         string   `json:"detail,omitempty"`
	RuleID         string   `json:"rule_id,omitempty"`
	CIOrigin       string   `json:"ci_origin,omitempty"`
	Profile        string   `json:"profile,omitempty"`
	WorkerClass    string   `json:"worker_class,omitempty"`
	PolicyRevision string   `json:"policy_revision,omitempty"`
	Reasons        []string `json:"reasons,omitempty"`
	// Condition and Deps describe a node's dependency outcome (kind
	// "dependency").
	Condition string              `json:"condition,omitempty"`
	Deps      []DependencyOutcome `json:"deps,omitempty"`
	// Required is set for kind "schedule": true means only on schedule
	// events, false means never on schedule events.
	Required *bool `json:"required,omitempty"`
	// ScheduleID and FireTime are set for kind "schedule" on a workflow that
	// a schedule fired (Value is the cron expression).
	ScheduleID string `json:"schedule_id,omitempty"`
	FireTime   string `json:"fire_time,omitempty"`
}

// DependencyOutcome is the status of one dependency instance of a node.
type DependencyOutcome struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// CIEventWorkflowDecision is the eval's decision for one workflow definition.
type CIEventWorkflowDecision struct {
	EventID            string           `gorm:"primaryKey;type:uuid" json:"event_id"`
	WorkflowSecurityID string           `gorm:"primaryKey;type:text" json:"workflow_security_id"`
	WorkflowName       string           `gorm:"type:text;not null;default:''" json:"workflow_name"`
	SourceFile         string           `gorm:"type:text;not null;default:''" json:"source_file,omitempty"`
	Decision           string           `gorm:"type:text;not null" json:"decision"`
	Reasons            []DecisionReason `gorm:"type:jsonb;serializer:json;not null" json:"reasons"`
	WorkflowID         *string          `gorm:"type:uuid" json:"workflow_id,omitempty"`
	CreatedAt          time.Time        `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt          time.Time        `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (CIEventWorkflowDecision) TableName() string { return "ci_event_workflow_decisions" }

// EffectiveDuplicateEvents resolves a project setting against its
// organization setting.
func EffectiveDuplicateEvents(projectSetting, orgSetting string) string {
	switch projectSetting {
	case DuplicateEventsRun, DuplicateEventsSkip:
		return projectSetting
	}
	if orgSetting == DuplicateEventsSkip {
		return DuplicateEventsSkip
	}
	return DuplicateEventsRun
}

// BeforeSave keeps a project saved from a struct that predates the setting
// (an empty value) inside the column's CHECK constraint.
func (p *Project) BeforeSave(*gorm.DB) error {
	if p.DuplicateEvents == "" {
		p.DuplicateEvents = DuplicateEventsInherit
	}
	if p.DynamicJobs == "" {
		p.DynamicJobs = DynamicJobsInherit
	}
	return nil
}

// BeforeSave is the organization counterpart of Project.BeforeSave.
func (o *Organization) BeforeSave(*gorm.DB) error {
	if o.DuplicateEvents == "" {
		o.DuplicateEvents = DuplicateEventsRun
	}
	if o.DynamicJobs == "" {
		o.DynamicJobs = DynamicJobsAllow
	}
	return nil
}
