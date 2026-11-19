package models

import (
	"time"
)

// Principal types for role_assignments and group membership.
const (
	PrincipalTypeUser  = "user"
	PrincipalTypeGroup = "group"
)

// Scope types for role_assignments.
const (
	ScopeTypeGlobal  = "global"
	ScopeTypeOrg     = "org"
	ScopeTypeProject = "project"
	// ScopeTypeWorkflow is one workflow of a project: ScopeID is the
	// project, ScopeWorkflow the workflow security ID. Only RoleOwner.
	ScopeTypeWorkflow = "workflow"
)

// Roles assignable via role_assignments.
const (
	RoleAdmin  = "admin"
	RoleOwner  = "owner"
	RoleMember = "member"
)

// Group is a named collection of users within an org (org_id == users.user_id
// today; there is no first-class orgs table yet).
type Group struct {
	GroupID     string `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"group_id"`
	OrgID       string `gorm:"type:uuid;not null" json:"org_id"`
	Name        string `gorm:"type:text;not null" json:"name"`
	Description string `gorm:"type:text" json:"description,omitempty"`
	// PriorityShift moves the priority of the work of the group members
	// (docs/priority.md). 0 is no shift. Change it only with
	// SetGroupPriorityShift.
	PriorityShift int       `gorm:"column:priority_shift;not null;default:0" json:"priority_shift"`
	CreatedAt     time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt     time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`

	Org User `gorm:"foreignKey:OrgID" json:"org,omitempty"`
}

// TableName specifies the table name for the model.
func (Group) TableName() string {
	return "groups"
}

// GroupMember links a principal into a group. UserID is set when the
// principal is a user (a database trigger fills it), for compatibility with
// queries that read group_members.user_id.
type GroupMember struct {
	GroupID     string    `gorm:"primaryKey;type:uuid" json:"group_id"`
	PrincipalID string    `gorm:"primaryKey;type:uuid" json:"principal_id"`
	UserID      *string   `gorm:"type:uuid;default:null" json:"user_id,omitempty"`
	AddedAt     time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"added_at"`
	// Source is GroupMemberSourceManual or GroupMemberSourceVCSSync
	// (group_vcs.go). Empty means manual.
	Source string `gorm:"type:text;not null;default:manual" json:"source,omitempty"`
}

// TableName specifies the table name for the model.
func (GroupMember) TableName() string {
	return "group_members"
}

// RoleAssignment grants a role to a principal (user or group) at a scope
// (global, org, or project). scope_id is NULL for the global scope.
type RoleAssignment struct {
	AssignmentID  string  `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"assignment_id"`
	PrincipalType string  `gorm:"type:text;not null" json:"principal_type"`
	PrincipalID   string  `gorm:"type:uuid;not null" json:"principal_id"`
	ScopeType     string  `gorm:"type:text;not null" json:"scope_type"`
	ScopeID       *string `gorm:"type:uuid" json:"scope_id,omitempty"`
	// ScopeWorkflow is the workflow security ID of a workflow scope.
	ScopeWorkflow *string   `gorm:"column:scope_workflow;type:text;default:null" json:"scope_workflow,omitempty"`
	Role          string    `gorm:"type:text;not null" json:"role"`
	CreatedAt     time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	CreatedBy     *string   `gorm:"type:uuid" json:"created_by,omitempty"`
}

// TableName specifies the table name for the model.
func (RoleAssignment) TableName() string {
	return "role_assignments"
}
