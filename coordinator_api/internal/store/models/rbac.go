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
	GroupID     string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"group_id"`
	OrgID       string    `gorm:"type:uuid;not null" json:"org_id"`
	Name        string    `gorm:"type:text;not null" json:"name"`
	Description string    `gorm:"type:text" json:"description,omitempty"`
	CreatedAt   time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`

	Org User `gorm:"foreignKey:OrgID" json:"org,omitempty"`
}

// TableName specifies the table name for the model.
func (Group) TableName() string {
	return "groups"
}

// GroupMember links a user into a group.
type GroupMember struct {
	GroupID string    `gorm:"primaryKey;type:uuid" json:"group_id"`
	UserID  string    `gorm:"primaryKey;type:uuid" json:"user_id"`
	AddedAt time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"added_at"`

	Group Group `gorm:"foreignKey:GroupID" json:"group,omitempty"`
	User  User  `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// TableName specifies the table name for the model.
func (GroupMember) TableName() string {
	return "group_members"
}

// RoleAssignment grants a role to a principal (user or group) at a scope
// (global, org, or project). scope_id is NULL for the global scope.
type RoleAssignment struct {
	AssignmentID  string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"assignment_id"`
	PrincipalType string    `gorm:"type:text;not null" json:"principal_type"`
	PrincipalID   string    `gorm:"type:uuid;not null" json:"principal_id"`
	ScopeType     string    `gorm:"type:text;not null" json:"scope_type"`
	ScopeID       *string   `gorm:"type:uuid" json:"scope_id,omitempty"`
	Role          string    `gorm:"type:text;not null" json:"role"`
	CreatedAt     time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	CreatedBy     *string   `gorm:"type:uuid" json:"created_by,omitempty"`
}

// TableName specifies the table name for the model.
func (RoleAssignment) TableName() string {
	return "role_assignments"
}
