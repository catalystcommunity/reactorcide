package models

import "time"

type VCSReportTarget struct {
	ReportTargetID     string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"report_target_id"`
	OrgID              string    `gorm:"type:uuid;not null" json:"org_id"`
	ProjectID          *string   `gorm:"type:uuid" json:"project_id,omitempty"`
	Provider           string    `gorm:"type:text;not null" json:"provider"`
	Repository         string    `gorm:"type:text;not null" json:"repository"`
	TargetType         string    `gorm:"type:text;not null" json:"target_type"`
	ExternalTargetID   string    `gorm:"type:text;not null" json:"external_target_id"`
	RootMarker         string    `gorm:"type:text;not null" json:"root_marker"`
	ProviderCommentID  *string   `gorm:"type:text" json:"provider_comment_id,omitempty"`
	CurrentGeneration  int64     `gorm:"not null" json:"current_generation"`
	GenerationKey      *string   `gorm:"type:text" json:"generation_key,omitempty"`
	GenerationComplete bool      `gorm:"not null" json:"generation_complete"`
	DesiredRevision    int64     `gorm:"not null" json:"desired_revision"`
	RenderedRevision   int64     `gorm:"not null" json:"rendered_revision"`
	Dirty              bool      `gorm:"not null" json:"dirty"`
	LastError          *string   `gorm:"type:text" json:"last_error,omitempty"`
	CreatedAt          time.Time `gorm:"autoCreateTime:false" json:"created_at"`
	UpdatedAt          time.Time `gorm:"autoUpdateTime:false" json:"updated_at"`
}

func (VCSReportTarget) TableName() string { return "vcs_report_targets" }

type VCSReportEntry struct {
	ReportTargetID  string    `gorm:"primaryKey;type:uuid" json:"report_target_id"`
	EntryKey        string    `gorm:"primaryKey;type:text" json:"entry_key"`
	WorkflowID      *string   `gorm:"type:uuid" json:"workflow_id,omitempty"`
	Generation      int64     `gorm:"not null" json:"generation"`
	Status          string    `gorm:"type:text;not null" json:"status"`
	StructuredState JSONB     `gorm:"type:jsonb" json:"structured_state"`
	UpdatedAt       time.Time `gorm:"autoUpdateTime:false" json:"updated_at"`
}

func (VCSReportEntry) TableName() string { return "vcs_report_entries" }
