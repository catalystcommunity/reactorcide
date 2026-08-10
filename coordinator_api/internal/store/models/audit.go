package models

import "time"

type AuditEvent struct {
	AuditEventID        string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"audit_event_id"`
	OrgID               *string   `gorm:"type:uuid" json:"org_id,omitempty"`
	ActorCredentialID   *string   `gorm:"type:uuid" json:"actor_credential_id,omitempty"`
	ActorCredentialType string    `gorm:"type:text" json:"actor_credential_type,omitempty"`
	ActorUserID         *string   `gorm:"type:uuid" json:"actor_user_id,omitempty"`
	Action              string    `gorm:"type:text;not null" json:"action"`
	SubjectType         string    `gorm:"type:text;not null" json:"subject_type"`
	SubjectID           *string   `gorm:"type:text" json:"subject_id,omitempty"`
	Details             JSONB     `gorm:"type:jsonb" json:"details"`
	CreatedAt           time.Time `gorm:"autoCreateTime:false" json:"created_at"`
}

func (AuditEvent) TableName() string { return "audit_events" }
