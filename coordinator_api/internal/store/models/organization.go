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
	// DuplicateEvents decides whether a second delivery of the same change
	// (same repo, event type, ref, commit and PR) runs again: "run" or "skip".
	DuplicateEvents string `gorm:"type:text;not null;default:'run'" json:"duplicate_events"`
	// DynamicJobs decides whether running jobs may create jobs at runtime
	// (runnerlib trigger): "allow" or "deny". The eval result is not a
	// dynamic job.
	DynamicJobs string `gorm:"type:text;not null;default:'allow'" json:"dynamic_jobs"`
	// InfraRetries is how many times the coordinator runs a job again
	// after an infra.platform failure, and the least budget of a lost job
	// (infra.worker_lost). See infra_retries.go. nil only on a struct that
	// predates the setting; the column is NOT NULL DEFAULT 1.
	InfraRetries *int `gorm:"type:integer;not null;default:1" json:"infra_retries"`
	// EgressControl sends all egress of the organization's jobs through the
	// egress proxy (docs/egress-proxy.md). Default off.
	EgressControl bool `gorm:"not null;default:false" json:"egress_control"`
	// DefaultsProfile is "legacy" (behavior before secure defaults) or
	// "secure" (see defaults_profile.go). New organizations are secure.
	DefaultsProfile string `gorm:"type:text;not null;default:'legacy'" json:"defaults_profile"`
	// DraftPRs decides what a draft pull request does: "run", "skip" or
	// "skip_when_shedding".
	DraftPRs string `gorm:"column:draft_prs;type:text;not null;default:'run'" json:"draft_prs"`
	// ShedLevel is the organization shed level: "normal", "degraded" or
	// "critical". The level in force is the higher of it and the global level.
	ShedLevel string `gorm:"type:text;not null;default:'normal'" json:"shed_level"`
	// ShedActions are the actions of each shed level. nil uses the built-in
	// actions (DefaultShedActions).
	ShedActions *ShedActionsConfig `gorm:"type:jsonb;serializer:json" json:"shed_actions,omitempty"`
	CreatedAt   time.Time          `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt   time.Time          `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
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
