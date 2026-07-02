package models

import (
	"time"

	"github.com/lib/pq"
)

// SourceType represents the type of source code preparation
type SourceType string

const (
	SourceTypeGit  SourceType = "git"
	SourceTypeCopy SourceType = "copy"
	SourceTypeNone SourceType = "none"
)

// Project represents a repository configuration for CI/CD
type Project struct {
	ProjectID string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"project_id"`
	CreatedAt time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	UserID    *string   `gorm:"type:uuid" json:"user_id,omitempty"`

	// Project identification
	Name        string `gorm:"type:text;not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	// RepoURL in canonical form: github.com/org/repo (no protocol, no .git suffix)
	RepoURL string `gorm:"type:text;not null;uniqueIndex" json:"repo_url"`

	// Event filtering configuration
	Enabled           bool           `gorm:"default:true;not null" json:"enabled"`
	TargetBranches    pq.StringArray `gorm:"type:text[];default:ARRAY['main','master','develop']" json:"target_branches"`
	AllowedEventTypes pq.StringArray `gorm:"type:text[];default:ARRAY['push','pull_request_opened','pull_request_updated','tag_created']" json:"allowed_event_types"`

	// Default CI source configuration (trusted CI code)
	DefaultCISourceType SourceType `gorm:"type:source_type;default:'git'" json:"default_ci_source_type"`
	DefaultCISourceURL  string     `gorm:"type:text" json:"default_ci_source_url"`
	DefaultCISourceRef  string     `gorm:"type:text;default:'main'" json:"default_ci_source_ref"`

	// VCS integration — stores "path:key" references into the secrets store
	VCSTokenSecret string `gorm:"type:text" json:"vcs_token_secret"`
	// VCSCredentialSecrets maps provider names (for example "github") to
	// "path:key" secret refs. VCSTokenSecret remains as legacy/default fallback.
	VCSCredentialSecrets JSONB `gorm:"column:vcs_token_secrets;type:jsonb;default:'{}'" json:"vcs_token_secrets,omitempty"`
	// WebhookSecret stores a "path:key" reference to the webhook signing secret
	// for this project. When set, incoming webhooks are validated using this
	// per-project secret instead of the global REACTORCIDE_VCS_GITHUB_SECRET.
	WebhookSecret string `gorm:"type:text" json:"webhook_secret"`
	// WebhookSecrets maps provider names to "path:key" secret refs.
	WebhookSecrets JSONB `gorm:"type:jsonb;default:'{}'" json:"webhook_secrets,omitempty"`

	// Job defaults
	DefaultRunnerImage    string `gorm:"type:text;default:'quay.io/catalystcommunity/reactorcide_runner'" json:"default_runner_image"`
	DefaultJobCommand     string `gorm:"type:text" json:"default_job_command"`
	DefaultTimeoutSeconds int    `gorm:"default:3600" json:"default_timeout_seconds"`
	DefaultQueueName      string `gorm:"type:text;default:'reactorcide-jobs'" json:"default_queue_name"`
}

// TableName specifies the table name for the model
func (Project) TableName() string {
	return "projects"
}

const (
	SecretGrantMatchAny    = "any"
	SecretGrantMatchExact  = "exact"
	SecretGrantMatchPrefix = "prefix"
	SecretGrantMatchGlob   = "glob"
	SecretGrantMatchRegex  = "regex"
)

// SecretGrant allows an API/worker job to resolve secrets under a configured path pattern.
type SecretGrant struct {
	GrantID           string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"grant_id"`
	CreatedAt         time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt         time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	Name              string    `gorm:"type:text;not null" json:"name"`
	UserID            string    `gorm:"type:uuid;not null" json:"user_id"`
	ProjectID         *string   `gorm:"type:uuid" json:"project_id,omitempty"`
	SecretPathMatch   string    `gorm:"type:text;not null;default:'prefix'" json:"secret_path_match"`
	SecretPathPattern string    `gorm:"type:text;not null" json:"secret_path_pattern"`
	JobNameMatch      string    `gorm:"type:text;not null;default:'any'" json:"job_name_match"`
	JobNamePattern    string    `gorm:"type:text;not null;default:''" json:"job_name_pattern,omitempty"`
	Description       string    `gorm:"type:text" json:"description,omitempty"`

	User    User     `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Project *Project `gorm:"foreignKey:ProjectID" json:"project,omitempty"`
}

func (SecretGrant) TableName() string {
	return "secret_grants"
}

// ShouldProcessEvent checks if an event should trigger CI based on filtering rules
func (p *Project) ShouldProcessEvent(eventType string, targetBranch string) bool {
	if !p.Enabled {
		return false
	}

	// Check if event type is allowed
	eventAllowed := false
	for _, allowedType := range p.AllowedEventTypes {
		if allowedType == eventType {
			eventAllowed = true
			break
		}
	}
	if !eventAllowed {
		return false
	}

	// Check if branch is in target branches
	// Empty target branches means allow all branches
	if len(p.TargetBranches) == 0 {
		return true
	}

	for _, branch := range p.TargetBranches {
		if branch == targetBranch {
			return true
		}
	}

	return false
}
