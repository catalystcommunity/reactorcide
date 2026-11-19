package models

import (
	"time"

	"github.com/lib/pq"
)

// Required set (docs/required-set.md, plan items A1 and A4). The required set
// of a project names the workflows that count toward "Reactorcide Checks".
// With the set off (the default), every workflow counts.

// Required set entry sources.
const (
	RequiredWorkflowSourceSeed      = "seed"
	RequiredWorkflowSourcePromotion = "promotion"
)

// Proposal actions and states.
const (
	RequiredSetActionPromote = "promote"
	RequiredSetActionDemote  = "demote"

	RequiredSetProposalPending   = "pending"
	RequiredSetProposalApproved  = "approved"
	RequiredSetProposalRejected  = "rejected"
	RequiredSetProposalWithdrawn = "withdrawn"
)

// DefaultMaxQuarantineDays is the longest quarantine when an organization
// sets no value.
const DefaultMaxQuarantineDays = 30

// RequiredSetApproverRule names the approvers for target branches. The first
// rule with a matching branch glob wins.
type RequiredSetApproverRule struct {
	Branches  []string `json:"branches"`
	Approvers []string `json:"approvers"`
}

// OrgRequiredSetSettings are the organization defaults.
type OrgRequiredSetSettings struct {
	OrgID             string                    `gorm:"column:org_id;primaryKey;type:uuid" json:"org_id"`
	Approvers         pq.StringArray            `gorm:"type:text[]" json:"approvers"`
	ApproverRules     []RequiredSetApproverRule `gorm:"type:jsonb;serializer:json;not null" json:"approver_rules"`
	MaxQuarantineDays int                       `gorm:"not null;default:30" json:"max_quarantine_days"`
	UpdatedAt         time.Time                 `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (OrgRequiredSetSettings) TableName() string { return "org_required_set_settings" }

// ProjectRequiredSetSettings are the settings of one project. Approvers nil
// and ApproverRules nil inherit the organization values.
type ProjectRequiredSetSettings struct {
	ProjectID     string                     `gorm:"column:project_id;primaryKey;type:uuid" json:"project_id"`
	OrgID         string                     `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	Enabled       bool                       `gorm:"not null;default:false" json:"enabled"`
	Approvers     pq.StringArray             `gorm:"type:text[]" json:"approvers"`
	ApproverRules *[]RequiredSetApproverRule `gorm:"type:jsonb;serializer:json" json:"approver_rules"`
	EnabledAt     *time.Time                 `json:"enabled_at,omitempty"`
	EnabledBy     *string                    `gorm:"type:uuid" json:"enabled_by,omitempty"`
	UpdatedAt     time.Time                  `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (ProjectRequiredSetSettings) TableName() string { return "project_required_set_settings" }

// RequiredWorkflow is one member of a required set. BranchPattern "" means
// every target branch.
type RequiredWorkflow struct {
	ProjectID          string     `gorm:"column:project_id;primaryKey;type:uuid" json:"project_id"`
	OrgID              string     `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	WorkflowSecurityID string     `gorm:"primaryKey;type:text" json:"workflow_security_id"`
	BranchPattern      string     `gorm:"primaryKey;type:text;not null;default:''" json:"branch_pattern"`
	Source             string     `gorm:"type:text;not null;default:'promotion'" json:"source"`
	AddedBy            *string    `gorm:"type:uuid" json:"added_by,omitempty"`
	AddedAt            time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"added_at"`
	Reason             string     `gorm:"type:text;not null;default:''" json:"reason,omitempty"`
	ProposalID         *string    `gorm:"type:uuid" json:"proposal_id,omitempty"`
	QuarantinedUntil   *time.Time `json:"quarantined_until,omitempty"`
	QuarantineReason   string     `gorm:"type:text;not null;default:''" json:"quarantine_reason,omitempty"`
	QuarantinedBy      *string    `gorm:"type:uuid" json:"quarantined_by,omitempty"`
	QuarantinedAt      *time.Time `json:"quarantined_at,omitempty"`
}

func (RequiredWorkflow) TableName() string { return "project_required_workflows" }

// IsQuarantined reports whether the entry is quarantined at now. A quarantine
// ends by itself at its expiry: the state is computed when it is read.
func (w RequiredWorkflow) IsQuarantined(now time.Time) bool {
	return w.QuarantinedUntil != nil && w.QuarantinedUntil.After(now)
}

// RequiredSetProposal is a promotion into, or a demotion out of, the set.
type RequiredSetProposal struct {
	ProposalID         string     `gorm:"column:proposal_id;primaryKey;type:uuid;default:generate_ulid()" json:"proposal_id"`
	ProjectID          string     `gorm:"column:project_id;type:uuid;not null" json:"project_id"`
	OrgID              string     `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	WorkflowSecurityID string     `gorm:"type:text;not null" json:"workflow_security_id"`
	BranchPattern      string     `gorm:"type:text;not null;default:''" json:"branch_pattern"`
	Action             string     `gorm:"type:text;not null" json:"action"`
	State              string     `gorm:"type:text;not null;default:'pending'" json:"state"`
	Reason             string     `gorm:"type:text;not null;default:''" json:"reason,omitempty"`
	ProposedBy         *string    `gorm:"type:uuid" json:"proposed_by,omitempty"`
	ProposedIdentity   string     `gorm:"type:text;not null;default:''" json:"proposed_identity,omitempty"`
	ProposedAt         time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"proposed_at"`
	DecidedBy          *string    `gorm:"type:uuid" json:"decided_by,omitempty"`
	DecidedAt          *time.Time `json:"decided_at,omitempty"`
	DecisionReason     string     `gorm:"type:text;not null;default:''" json:"decision_reason,omitempty"`
}

func (RequiredSetProposal) TableName() string { return "required_set_proposals" }

// KnownWorkflow is a workflow security ID that the project ran or decided
// on at least once.
type KnownWorkflow struct {
	WorkflowSecurityID string    `json:"workflow_security_id"`
	Name               string    `json:"name"`
	LastSeen           time.Time `json:"last_seen"`
}
