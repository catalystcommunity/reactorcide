package models

import (
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

// SourceType represents the type of source code preparation
type SourceType string

const (
	SourceTypeGit  SourceType = "git"
	SourceTypeCopy SourceType = "copy"
	SourceTypeNone SourceType = "none"
)

const (
	CheckoutModeIsolated = "isolated"
	CheckoutModeShared   = "shared"
)

func IsValidCheckoutMode(value string, allowEmpty bool) bool {
	return allowEmpty && value == "" || value == CheckoutModeIsolated || value == CheckoutModeShared
}

// Project represents a repository configuration for CI/CD
type Project struct {
	ProjectID string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"project_id"`
	CreatedAt time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	UserID    *string   `gorm:"type:uuid" json:"user_id,omitempty"`
	OrgID     string    `gorm:"column:org_id;type:uuid;not null" json:"-"`

	// Project identification
	Name        string `gorm:"type:text;not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	// RepoURL in canonical form: github.com/org/repo (no protocol, no .git suffix)
	RepoURL string `gorm:"type:text;not null;uniqueIndex" json:"repo_url"`
	// VCSRepositoryID is the numeric repository ID of the VCS (GitHub), when
	// known. A repository with the same name and another ID is another
	// repository (a deleted or renamed repository, then a new one with the
	// name). Migration 000057.
	VCSRepositoryID *int64 `gorm:"column:vcs_repository_id" json:"vcs_repository_id,omitempty"`

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
	DefaultRunnerImage    string `gorm:"type:text;default:'containers.catalystsquad.com/public/reactorcide/runnerbase:dev'" json:"default_runner_image"`
	DefaultJobCommand     string `gorm:"type:text" json:"default_job_command"`
	DefaultTimeoutSeconds int    `gorm:"default:3600" json:"default_timeout_seconds"`
	DefaultQueueName      string `gorm:"type:text;default:'reactorcide-jobs'" json:"default_queue_name"`
	// CheckoutMode selects how runnerlib prepares the source views for CI
	// evaluation. An empty value inherits the coordinator default.
	CheckoutMode string `gorm:"type:text" json:"checkout_mode,omitempty"`

	// IsPrivate marks the project as private. Effective visibility is
	// IsPrivate OR the owning org's (user's) IsPrivate.
	IsPrivate bool `gorm:"not null;default:false" json:"is_private"`

	// DuplicateEvents overrides the organization setting: "inherit", "run" or
	// "skip".
	DuplicateEvents string `gorm:"type:text;not null;default:'inherit'" json:"duplicate_events,omitempty"`

	// DynamicJobs overrides the organization setting: "inherit", "allow" or
	// "deny".
	DynamicJobs string `gorm:"type:text;not null;default:'inherit'" json:"dynamic_jobs,omitempty"`
	// DraftPRs overrides the organization draft_prs setting: "inherit",
	// "run", "skip" or "skip_when_shedding".
	DraftPRs string `gorm:"column:draft_prs;type:text;not null;default:'inherit'" json:"draft_prs,omitempty"`

	// InfraRetries overrides the organization infra_retries setting. NULL
	// inherits the organization value.
	InfraRetries *int `gorm:"type:integer" json:"infra_retries,omitempty"`

	// EgressControl sends all egress of the project's jobs through the
	// egress proxy (docs/egress-proxy.md). The organization or the execution
	// profile can also turn it on; none of them can turn it off for another.
	EgressControl bool `gorm:"not null;default:false" json:"egress_control"`
}

// TableName specifies the table name for the model
func (Project) TableName() string {
	return "projects"
}

// BeforeCreate maps the former ownership field to the first-class
// organization field for compatibility with callers that construct a Project
// directly. New callers must set OrgID.
func (p *Project) BeforeCreate(_ *gorm.DB) error {
	if p.OrgID == "" && p.UserID != nil {
		p.OrgID = *p.UserID
	}
	return nil
}

// OwnershipOrgID returns the first-class organization ID. The UserID
// fallback keeps old in-memory callers and pre-migration data readable.
func (p *Project) OwnershipOrgID() string {
	if p == nil {
		return ""
	}
	if p.OrgID != "" {
		return p.OrgID
	}
	if p.UserID != nil {
		return *p.UserID
	}
	return ""
}

const (
	SecretGrantMatchAny    = "any"
	SecretGrantMatchExact  = "exact"
	SecretGrantMatchPrefix = "prefix"
	SecretGrantMatchGlob   = "glob"
	SecretGrantMatchRegex  = "regex"
)

// Secret grant delivery modes (SecretGrant.Delivery).
const (
	SecretDeliveryEnv   = "env"
	SecretDeliveryProxy = "proxy"
)

// EffectiveDelivery returns the delivery mode of the grant; empty is "env".
func (g SecretGrant) EffectiveDelivery() string {
	if g.Delivery == SecretDeliveryProxy {
		return SecretDeliveryProxy
	}
	return SecretDeliveryEnv
}

// SecretGrant allows an API/worker job to resolve secrets under a configured path pattern.
type SecretGrant struct {
	GrantID           string         `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"grant_id"`
	CreatedAt         time.Time      `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt         time.Time      `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	Name              string         `gorm:"type:text;not null" json:"name"`
	UserID            string         `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	ProjectID         *string        `gorm:"type:uuid" json:"project_id,omitempty"`
	SecretPathMatch   string         `gorm:"type:text;not null;default:'prefix'" json:"secret_path_match"`
	SecretPathPattern string         `gorm:"type:text;not null" json:"secret_path_pattern"`
	JobNameMatch      string         `gorm:"type:text;not null;default:'any'" json:"job_name_match"`
	JobNamePattern    string         `gorm:"type:text;not null;default:''" json:"job_name_pattern,omitempty"`
	Description       string         `gorm:"type:text" json:"description,omitempty"`
	ExecutionProfiles pq.StringArray `gorm:"type:text[]" json:"execution_profiles,omitempty"`
	CIOrigins         pq.StringArray `gorm:"type:text[]" json:"ci_origins,omitempty"`
	// RefClassesAny and RefClassesNone are ref class conditions
	// (docs/ref-classes.md). With RefClassesAny, the grant applies only to a
	// job that has one of the classes. With RefClassesNone, the grant never
	// applies to a job that has one of the classes. Empty is no condition.
	RefClassesAny  pq.StringArray `gorm:"type:text[]" json:"ref_classes_any,omitempty"`
	RefClassesNone pq.StringArray `gorm:"type:text[]" json:"ref_classes_none,omitempty"`
	// Delivery is how a job gets the secrets of this grant: "env" (the
	// value is in the job environment) or "proxy" (the job gets a
	// placeholder; the egress proxy puts the value in ProxyHeader for the
	// hosts in ProxyHosts only). See docs/egress-proxy.md.
	Delivery          string         `gorm:"type:text;not null;default:'env'" json:"delivery,omitempty"`
	ProxyHosts        pq.StringArray `gorm:"type:text[];not null;default:'{}'" json:"proxy_hosts,omitempty"`
	ProxyHeader       string         `gorm:"type:text;not null;default:''" json:"proxy_header,omitempty"`
	ProxyHeaderFormat string         `gorm:"type:text;not null;default:''" json:"proxy_header_format,omitempty"`

	User    User     `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Project *Project `gorm:"foreignKey:ProjectID" json:"project,omitempty"`
}

func (SecretGrant) TableName() string {
	return "secret_grants"
}

// IsEffectivelyPrivate reports whether the project should be treated as
// private: either the project itself is marked private, or its owning org
// (user, since users act as orgs today) is marked private. orgIsPrivate
// should be false when the project has no owning user (UserID is nil).
func (p *Project) IsEffectivelyPrivate(orgIsPrivate bool) bool {
	return p.IsPrivate || orgIsPrivate
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

	// TargetBranches limits branch and pull request events. A tag has no
	// target branch. The explicit tag_created event grant is its project-level
	// gate, and the trusted workflow definition can apply a tag pattern.
	if eventType == "tag_created" {
		return true
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
