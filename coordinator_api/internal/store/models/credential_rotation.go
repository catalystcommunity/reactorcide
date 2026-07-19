package models

import (
	"time"
)

// ProjectWebhookSecret is one rotatable webhook-signing credential for a
// project+provider. Multiple rows may be active at once so a new secret can
// be added, observed via LastUsedAt, and only then have the old one
// deactivated. Legacy jsonb/text columns on Project remain as a fallback in
// credential_refs.go resolution order.
type ProjectWebhookSecret struct {
	ID            string     `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"id"`
	ProjectID     string     `gorm:"type:uuid;not null" json:"project_id"`
	Provider      string     `gorm:"type:text;not null" json:"provider"`
	Name          string     `gorm:"type:text;not null" json:"name"`
	SecretRef     string     `gorm:"type:text;not null" json:"secret_ref"`
	IsActive      bool       `gorm:"not null;default:true" json:"is_active"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	CreatedAt     time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	DeactivatedAt *time.Time `json:"deactivated_at,omitempty"`

	Project Project `gorm:"foreignKey:ProjectID" json:"project,omitempty"`
}

// TableName specifies the table name for the model.
func (ProjectWebhookSecret) TableName() string {
	return "project_webhook_secrets"
}

// ProjectVCSCredential is one rotatable VCS credential for a
// project+provider. Same shape and rotation semantics as
// ProjectWebhookSecret.
type ProjectVCSCredential struct {
	ID            string     `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"id"`
	ProjectID     string     `gorm:"type:uuid;not null" json:"project_id"`
	Provider      string     `gorm:"type:text;not null" json:"provider"`
	Name          string     `gorm:"type:text;not null" json:"name"`
	SecretRef     string     `gorm:"type:text;not null" json:"secret_ref"`
	IsActive      bool       `gorm:"not null;default:true" json:"is_active"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	CreatedAt     time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	DeactivatedAt *time.Time `json:"deactivated_at,omitempty"`

	Project Project `gorm:"foreignKey:ProjectID" json:"project,omitempty"`
}

// TableName specifies the table name for the model.
func (ProjectVCSCredential) TableName() string {
	return "project_vcs_credentials"
}
