package models

import "time"

type VCSIdentityLink struct {
	LinkID          string    `gorm:"column:link_id;primaryKey;type:uuid;default:generate_ulid()" json:"link_id"`
	Provider        string    `gorm:"type:text;not null" json:"provider"`
	ExternalSubject string    `gorm:"type:text;not null" json:"external_subject"`
	UserID          string    `gorm:"type:uuid;not null" json:"user_id"`
	VerifiedBy      string    `gorm:"type:text;not null" json:"verified_by"`
	CreatedAt       time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt       time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (VCSIdentityLink) TableName() string { return "vcs_identity_links" }
