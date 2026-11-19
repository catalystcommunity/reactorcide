package models

import "time"

// Diagnosis target types (docs/diagnosis.md).
const (
	DiagnosisTargetJob      = "job"
	DiagnosisTargetWorkflow = "workflow"
)

// DiagnosisLink is one named link of a diagnosis entry.
type DiagnosisLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// DiagnosisReport is one diagnosis entry that a principal posted for a job
// (or a workflow node) or a workflow. One principal has at most one entry
// for each target. A new post replaces it and increments Revision.
type DiagnosisReport struct {
	DiagnosisID       string          `gorm:"column:diagnosis_id;primaryKey;type:uuid;default:generate_ulid()" json:"diagnosis_id"`
	OrgID             string          `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	ProjectID         *string         `gorm:"column:project_id;type:uuid" json:"project_id,omitempty"`
	TargetType        string          `gorm:"column:target_type;type:text;not null" json:"target_type"`
	JobID             *string         `gorm:"column:job_id;type:uuid" json:"job_id,omitempty"`
	WorkflowID        *string         `gorm:"column:workflow_id;type:uuid" json:"workflow_id,omitempty"`
	NodeName          string          `gorm:"column:node_name;type:text;not null;default:''" json:"node_name,omitempty"`
	AuthorPrincipalID string          `gorm:"column:author_principal_id;type:uuid;not null" json:"author_principal_id"`
	AuthorKind        string          `gorm:"column:author_kind;type:text;not null" json:"author_kind"`
	AuthorDisplayName string          `gorm:"column:author_display_name;type:text;not null;default:''" json:"author_display_name"`
	Title             string          `gorm:"column:title;type:text;not null" json:"title"`
	Body              string          `gorm:"column:body;type:text;not null;default:''" json:"body"`
	Confidence        string          `gorm:"column:confidence;type:text;not null;default:''" json:"confidence,omitempty"`
	Category          string          `gorm:"column:category;type:text;not null;default:''" json:"category,omitempty"`
	Links             []DiagnosisLink `gorm:"column:links;type:jsonb;serializer:json;not null" json:"links"`
	SuggestedShifts   *RunShifts      `gorm:"column:suggested_shifts;type:jsonb;serializer:json" json:"suggested_shifts,omitempty"`
	Revision          int             `gorm:"column:revision;not null;default:1" json:"revision"`
	CreatedAt         time.Time       `gorm:"column:created_at;autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt         time.Time       `gorm:"column:updated_at;autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (DiagnosisReport) TableName() string { return "diagnosis_reports" }
