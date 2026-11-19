package models

import (
	"time"

	"github.com/lib/pq"
)

// RefClassRule is an operator rule that adds ref classes to an event
// (docs/ref-classes.md). ProjectID nil makes it an organization rule. A rule
// matches when every configured fact matches; an empty or nil fact is not a
// condition. A rule only adds classes. It never removes a class.
type RefClassRule struct {
	RuleID      string    `gorm:"column:rule_id;primaryKey;type:uuid;default:generate_ulid()" json:"rule_id"`
	OrgID       string    `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	ProjectID   *string   `gorm:"column:project_id;type:uuid" json:"project_id,omitempty"`
	Name        string    `gorm:"type:text;not null" json:"name"`
	Position    int       `gorm:"not null;default:0" json:"position"`
	Description string    `gorm:"type:text;not null;default:''" json:"description,omitempty"`
	CreatedAt   time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`

	// Match facts.
	EventTypes      pq.StringArray `gorm:"type:text[];not null" json:"event_types"`
	BranchGlob      string         `gorm:"type:text;not null;default:''" json:"branch_glob,omitempty"`
	TagGlob         string         `gorm:"type:text;not null;default:''" json:"tag_glob,omitempty"`
	BaseBranchGlob  string         `gorm:"type:text;not null;default:''" json:"base_branch_glob,omitempty"`
	IsPR            *bool          `gorm:"column:is_pr" json:"is_pr,omitempty"`
	IsFork          *bool          `gorm:"column:is_fork" json:"is_fork,omitempty"`
	IsDefaultBranch *bool          `gorm:"column:is_default_branch" json:"is_default_branch,omitempty"`

	// Classes are the labels that the rule adds.
	Classes pq.StringArray `gorm:"type:text[];not null" json:"classes"`
	Enabled bool           `gorm:"not null" json:"enabled"`
}

func (RefClassRule) TableName() string { return "ref_class_rules" }

// RefClassFacts are the trusted facts that the classifier used for an event.
// The coordinator stores them on the event, so "explain" can show them.
type RefClassFacts struct {
	Source    string `json:"source,omitempty"`
	EventType string `json:"event_type,omitempty"`
	// Branch is the branch of a push, a schedule, or a manual submit. A
	// pull request has no Branch: its branches are HeadBranch (untrusted,
	// the author names it) and BaseBranch.
	Branch          string `json:"branch,omitempty"`
	HeadBranch      string `json:"head_branch,omitempty"`
	Tag             string `json:"tag,omitempty"`
	BaseBranch      string `json:"base_branch,omitempty"`
	DefaultBranch   string `json:"default_branch,omitempty"`
	IsPR            bool   `json:"is_pr,omitempty"`
	IsFork          bool   `json:"is_fork,omitempty"`
	IsDefaultBranch bool   `json:"is_default_branch,omitempty"`
}

// RefClassReason tells why one class applies: the built-in rule or the
// operator rule that added it.
type RefClassReason struct {
	Class string `json:"class"`
	// RuleID is "builtin:NAME" for a built-in rule, else the UUID of the
	// operator rule.
	RuleID   string `json:"rule_id"`
	RuleName string `json:"rule_name,omitempty"`
	// Scope is "builtin", "org", or "project".
	Scope string `json:"scope"`
}
