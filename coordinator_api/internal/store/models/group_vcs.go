package models

import "time"

// Sources of a group membership (group_members.source).
const (
	// GroupMemberSourceManual is a member that an admin added. A sync never
	// removes it.
	GroupMemberSourceManual = "manual"
	// GroupMemberSourceVCSSync is a member that only a linked VCS team gives.
	GroupMemberSourceVCSSync = "vcs_sync"
)

// Membership history changes (group_membership_history.change).
const (
	GroupMembershipAdded   = "added"
	GroupMembershipRemoved = "removed"
)

// GroupVCSLink binds a group to one VCS team. The members of the team are
// members of the group (source vcs_sync) after a sync.
type GroupVCSLink struct {
	LinkID               string     `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"link_id"`
	GroupID              string     `gorm:"type:uuid;not null" json:"group_id"`
	Provider             string     `gorm:"type:text;not null" json:"provider"`
	Owner                string     `gorm:"type:text;not null" json:"owner"`
	TeamSlug             string     `gorm:"type:text;not null" json:"team_slug"`
	ExternalTeamID       *int64     `gorm:"column:external_team_id" json:"external_team_id,omitempty"`
	CreatedByPrincipalID *string    `gorm:"type:uuid" json:"created_by_principal_id,omitempty"`
	CreatedAt            time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt            time.Time  `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	LastSyncedAt         *time.Time `json:"last_synced_at,omitempty"`
	LastAttemptAt        *time.Time `json:"last_attempt_at,omitempty"`
	LastError            string     `gorm:"type:text;not null;default:''" json:"last_error"`
	FailureCount         int        `gorm:"not null;default:0" json:"failure_count"`
	NextSyncAt           *time.Time `json:"next_sync_at,omitempty"`
	MemberCount          int        `gorm:"not null;default:0" json:"member_count"`
}

func (GroupVCSLink) TableName() string { return "group_vcs_links" }

// Subject returns the policy subject of the team, vcs_team:OWNER/SLUG.
func (l GroupVCSLink) Subject() string { return "vcs_team:" + l.Owner + "/" + l.TeamSlug }

// GroupVCSLinkMember is one principal that a linked team has now.
type GroupVCSLinkMember struct {
	LinkID      string    `gorm:"primaryKey;type:uuid" json:"link_id"`
	PrincipalID string    `gorm:"primaryKey;type:uuid" json:"principal_id"`
	VCSLogin    string    `gorm:"column:vcs_login;type:text;not null" json:"vcs_login"`
	AddedAt     time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"added_at"`
}

func (GroupVCSLinkMember) TableName() string { return "group_vcs_link_members" }

// GroupMembershipChange is one row of group_membership_history.
type GroupMembershipChange struct {
	HistoryID   int64     `gorm:"primaryKey;autoIncrement" json:"history_id"`
	GroupID     string    `gorm:"type:uuid;not null" json:"group_id"`
	OrgID       *string   `gorm:"type:uuid" json:"org_id,omitempty"`
	GroupName   string    `gorm:"type:text;not null" json:"group_name"`
	PrincipalID string    `gorm:"type:uuid;not null" json:"principal_id"`
	Change      string    `gorm:"type:text;not null" json:"change"`
	Source      string    `gorm:"type:text;not null" json:"source"`
	ChangedAt   time.Time `json:"changed_at"`
}

func (GroupMembershipChange) TableName() string { return "group_membership_history" }

// GroupMemberDetail is one effective member of a group with its source and
// the linked teams that give it.
type GroupMemberDetail struct {
	PrincipalID string `json:"principal_id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	UserID      string `json:"user_id,omitempty"`
	Username    string `json:"username,omitempty"`
	Source      string `json:"source"`
	// Manual is true when an admin added the member. A member can be manual
	// and also in linked teams.
	Manual bool `json:"manual"`
	// Teams are the vcs_team subjects of the linked teams that have the
	// member, with the logins.
	Teams     []string  `json:"teams,omitempty"`
	VCSLogins []string  `json:"vcs_logins,omitempty"`
	AddedAt   time.Time `json:"added_at"`
}

// GroupVCSTeamMember is one member of a team as the VCS returned it, after
// resolution to a principal.
type GroupVCSTeamMember struct {
	PrincipalID string
	Login       string
}

// GroupVCSSyncResult is the outcome of one stored sync.
type GroupVCSSyncResult struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Members int      `json:"members"`
}
