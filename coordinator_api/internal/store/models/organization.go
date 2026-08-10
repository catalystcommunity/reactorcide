package models

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	OrganizationStatusActive    = "active"
	OrganizationStatusSuspended = "suspended"
	OrganizationStatusDisabled  = "disabled"
)

var organizationNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Organization struct {
	OrgID                string     `gorm:"column:org_id;primaryKey;type:uuid;default:generate_ulid()" json:"-"`
	Name                 string     `gorm:"type:text;not null;uniqueIndex" json:"name"`
	DisplayName          string     `gorm:"type:text;not null" json:"display_name"`
	IsPrivate            bool       `gorm:"not null;default:false" json:"is_private"`
	Status               string     `gorm:"type:text;not null;default:'active'" json:"status"`
	SecretsInitializedAt *time.Time `json:"secrets_initialized_at,omitempty"`
	CreatedAt            time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt            time.Time  `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (Organization) TableName() string { return "organizations" }

func NormalizeOrganizationName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !organizationNamePattern.MatchString(name) {
		return "", fmt.Errorf("organization name must match [a-z0-9][a-z0-9-]{0,62}")
	}
	return name, nil
}

func ValidateOrganizationStatus(status string) error {
	switch status {
	case OrganizationStatusActive, OrganizationStatusSuspended, OrganizationStatusDisabled:
		return nil
	default:
		return fmt.Errorf("invalid organization status %q", status)
	}
}
