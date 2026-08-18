package models

import "time"

// CIPolicy is the coordinator-managed CI admission policy for one project.
// Repository content cannot create or change this record.
type CIPolicy struct {
	PolicyID  string    `gorm:"column:policy_id;primaryKey;type:uuid;default:generate_ulid()" json:"policy_id"`
	OrgID     string    `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	ProjectID string    `gorm:"column:project_id;type:uuid;not null;uniqueIndex" json:"project_id"`
	Document  JSONB     `gorm:"column:document;type:jsonb;not null" json:"document"`
	Revision  string    `gorm:"column:revision;type:text;not null" json:"revision"`
	UpdatedBy *string   `gorm:"column:updated_by;type:uuid" json:"updated_by,omitempty"`
	CreatedAt time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (CIPolicy) TableName() string { return "ci_policies" }
