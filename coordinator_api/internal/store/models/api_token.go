package models

import (
	"time"
)

// APIToken represents an API token for authentication
type APIToken struct {
	TokenID          string     `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"token_id"`
	CreatedAt        time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	UserID           string     `gorm:"type:uuid;default:null" json:"user_id,omitempty"`
	TokenHash        []byte     `gorm:"type:bytea;not null" json:"-"` // SHA256 hash, never return in JSON
	Name             string     `gorm:"type:text;not null" json:"name"`
	ExpiresAt        *time.Time `json:"expires_at"`
	LastUsedAt       *time.Time `json:"last_used_at"`
	IsActive         bool       `gorm:"not null" json:"is_active"`
	SubjectType      string     `gorm:"type:text;not null;default:'user_token'" json:"subject_type"`
	OwnerOrgID       *string    `gorm:"type:uuid" json:"owner_org_id,omitempty"`
	AllOrganizations bool       `gorm:"not null;default:false" json:"all_organizations"`
	AllCapabilities  bool       `gorm:"not null;default:false" json:"all_capabilities"`
	BoundJobID       *string    `gorm:"type:uuid" json:"bound_job_id,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	OrganizationIDs  []string   `gorm:"-" json:"organization_ids,omitempty"`
	Capabilities     []string   `gorm:"-" json:"capabilities,omitempty"`

	// Relationships
	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// TableName specifies the table name for the model
func (APIToken) TableName() string {
	return "api_tokens"
}

// IsExpired returns true if the token has expired
func (t *APIToken) IsExpired() bool {
	if t.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*t.ExpiresAt)
}

// IsValid returns true if the token is active and not expired
func (t *APIToken) IsValid() bool {
	return t.IsActive && t.RevokedAt == nil && !t.IsExpired()
}
