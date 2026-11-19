package models

import (
	"strings"
	"time"
)

// GitHub Apps with a pool of Apps (plan item B6, decision D2, migration
// 000057, docs/github-app.md).

// GitHubApp is one registered GitHub App. The private key and the webhook
// secret are encrypted; they never leave the coordinator.
type GitHubApp struct {
	AppID                   int64   `gorm:"column:app_id;primaryKey;autoIncrement:false" json:"app_id"`
	Slug                    string  `gorm:"not null;default:''" json:"slug"`
	Name                    string  `gorm:"not null;default:''" json:"name"`
	ClientID                string  `gorm:"column:client_id;not null;default:''" json:"client_id"`
	PrivateKeyKeyName       string  `gorm:"column:private_key_key_name;not null" json:"-"`
	PrivateKeyCiphertext    []byte  `gorm:"column:private_key_ciphertext;not null" json:"-"`
	WebhookSecretKeyName    string  `gorm:"column:webhook_secret_key_name;not null" json:"-"`
	WebhookSecretCiphertext []byte  `gorm:"column:webhook_secret_ciphertext;not null" json:"-"`
	DefaultOrgID            *string `gorm:"column:default_org_id;type:uuid" json:"default_org_id,omitempty"`
	// OpenRegistration: a new project of an account with no account
	// mapping goes into DefaultOrgID enabled. False: the project is
	// created disabled and waits for approval (docs/github-app.md).
	OpenRegistration bool              `gorm:"column:open_registration;not null;default:false" json:"open_registration"`
	Enabled          bool              `gorm:"not null;default:true" json:"enabled"`
	RateLimit        RateLimitSnapshot `gorm:"embedded;embeddedPrefix:ratelimit_" json:"rate_limit"`
	CreatedAt        time.Time         `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt        time.Time         `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (GitHubApp) TableName() string { return "github_apps" }

// RateLimitSnapshot is the last x-ratelimit-* header set of an App or an
// installation. A nil field means no response carried the header.
type RateLimitSnapshot struct {
	Limit      *int       `gorm:"column:limit" json:"limit,omitempty"`
	Remaining  *int       `gorm:"column:remaining" json:"remaining,omitempty"`
	Used       *int       `gorm:"column:used" json:"used,omitempty"`
	ResetAt    *time.Time `gorm:"column:reset_at" json:"reset_at,omitempty"`
	ObservedAt *time.Time `gorm:"column:observed_at" json:"observed_at,omitempty"`
}

// Headroom returns the fraction of the limit that is left at now, from 0 to
// 1. A snapshot with no data, or one whose reset time is past, has full
// headroom.
func (s RateLimitSnapshot) Headroom(now time.Time) float64 {
	if s.Limit == nil || s.Remaining == nil || *s.Limit <= 0 {
		return 1
	}
	if s.ResetAt != nil && !now.Before(*s.ResetAt) {
		return 1
	}
	value := float64(*s.Remaining) / float64(*s.Limit)
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	}
	return value
}

// GitHubAppInstallation is one installation of an App on an account.
type GitHubAppInstallation struct {
	InstallationID      int64             `gorm:"column:installation_id;primaryKey;autoIncrement:false" json:"installation_id"`
	AppID               int64             `gorm:"column:app_id;not null" json:"app_id"`
	AccountLogin        string            `gorm:"not null;default:''" json:"account_login"`
	AccountType         string            `gorm:"not null;default:''" json:"account_type"`
	RepositorySelection string            `gorm:"not null;default:''" json:"repository_selection"`
	Permissions         map[string]string `gorm:"type:jsonb;serializer:json;not null" json:"permissions"`
	Suspended           bool              `gorm:"not null;default:false" json:"suspended"`
	DeletedAt           *time.Time        `gorm:"column:deleted_at" json:"deleted_at,omitempty"`
	RateLimit           RateLimitSnapshot `gorm:"embedded;embeddedPrefix:ratelimit_" json:"rate_limit"`
	LastSeenAt          time.Time         `gorm:"column:last_seen_at;autoUpdateTime:false;default:timezone('utc', now())" json:"last_seen_at"`
	CreatedAt           time.Time         `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	// RepositoryCount is filled by list queries only.
	RepositoryCount int `gorm:"->;column:repository_count;-:migration" json:"repository_count"`
}

func (GitHubAppInstallation) TableName() string { return "github_app_installations" }

// Active reports whether the installation can give tokens.
func (i GitHubAppInstallation) Active() bool {
	return !i.Suspended && i.DeletedAt == nil
}

// GitHubAppRepository binds one repository to one installation.
type GitHubAppRepository struct {
	RepoFullName      string     `gorm:"column:repo_full_name;primaryKey" json:"repo_full_name"`
	InstallationID    int64      `gorm:"column:installation_id;not null" json:"installation_id"`
	RepositoryID      *int64     `gorm:"column:repository_id" json:"repository_id,omitempty"`
	Private           bool       `gorm:"not null;default:false" json:"private"`
	ProjectID         *string    `gorm:"column:project_id;type:uuid" json:"project_id,omitempty"`
	AutoRegistered    bool       `gorm:"column:auto_registered;not null;default:false" json:"auto_registered"`
	RemovedAt         *time.Time `gorm:"column:removed_at" json:"removed_at,omitempty"`
	DisabledByRemoval bool       `gorm:"column:disabled_by_removal;not null;default:false" json:"disabled_by_removal"`
	// PendingApproval: the registration created the project disabled. It
	// waits until an administrator enables it.
	PendingApproval bool      `gorm:"column:pending_approval;not null;default:false" json:"pending_approval"`
	AddedAt         time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"added_at"`
	UpdatedAt       time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (GitHubAppRepository) TableName() string { return "github_app_installation_repositories" }

// GitHubAccountOrg maps a GitHub account to the organization of its new
// projects.
type GitHubAccountOrg struct {
	AccountLogin string    `gorm:"column:account_login;primaryKey" json:"account_login"`
	OrgID        string    `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	CreatedAt    time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
}

func (GitHubAccountOrg) TableName() string { return "github_account_orgs" }

// GitHubAppBinding is the App and installation that hold one repository.
type GitHubAppBinding struct {
	App          GitHubApp
	Installation GitHubAppInstallation
	Repository   GitHubAppRepository
}

// NormalizeGitHubRepoFullName returns owner/name in lowercase.
func NormalizeGitHubRepoFullName(fullName string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(fullName), "/"))
}
