package store

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// GroupVCSStore stores groups linked to VCS teams (plan item B3,
// migration 000054). A synced member is a group_members row with source
// vcs_sync. It exists while at least one linked team of the group has the
// principal and no manual row exists. A sync never changes a manual row.
type GroupVCSStore interface {
	// CreateGroupVCSLink returns ErrAlreadyExists for the same team twice.
	CreateGroupVCSLink(ctx context.Context, link *models.GroupVCSLink) error
	GetGroupVCSLink(ctx context.Context, linkID string) (*models.GroupVCSLink, error)
	ListGroupVCSLinks(ctx context.Context, groupID string) ([]models.GroupVCSLink, error)
	// DeleteGroupVCSLink removes the link and the memberships that only it
	// gave.
	DeleteGroupVCSLink(ctx context.Context, linkID string) error
	// ListDueGroupVCSLinks returns links whose next_sync_at is NULL or at or
	// before now, oldest first.
	ListDueGroupVCSLinks(ctx context.Context, now time.Time, limit int) ([]models.GroupVCSLink, error)
	// RequestGroupVCSSync makes the links due now.
	RequestGroupVCSSync(ctx context.Context, linkIDs []string) error
	// FindGroupVCSLinksByTeam returns the links of a team by slug, or by
	// the external team ID when teamID is not zero.
	FindGroupVCSLinksByTeam(ctx context.Context, provider, owner, slug string, teamID int64) ([]models.GroupVCSLink, error)
	// RenameGroupVCSLinkTeam changes the slug of a link after a team
	// rename. ErrAlreadyExists when the group already links the new slug.
	RenameGroupVCSLinkTeam(ctx context.Context, linkID, slug string) error
	// ApplyGroupVCSSync stores the members of a successful sync and the
	// sync status in one transaction.
	ApplyGroupVCSSync(ctx context.Context, linkID string, members []models.GroupVCSTeamMember, teamID *int64, syncedAt, nextSyncAt time.Time) (*models.GroupVCSSyncResult, error)
	// RecordGroupVCSSyncFailure stores a failed sync. With clearMembers the
	// memberships that only this link gives are removed (the team is gone,
	// or the members are too old to trust).
	RecordGroupVCSSyncFailure(ctx context.Context, linkID, message string, attemptAt, nextSyncAt time.Time, clearMembers bool) (*models.GroupVCSSyncResult, error)
	// ListGroupVCSLinkMembers lists the principals one link has now.
	ListGroupVCSLinkMembers(ctx context.Context, linkID string) ([]models.GroupVCSLinkMember, error)
	// ListGroupMemberDetails lists the effective members of a group with
	// their source and teams.
	ListGroupMemberDetails(ctx context.Context, groupID string) ([]models.GroupMemberDetail, error)
	// ListGroupMembershipHistory lists changes, newest first. Empty filters
	// are not applied.
	ListGroupMembershipHistory(ctx context.Context, filter GroupMembershipHistoryFilter) ([]models.GroupMembershipChange, error)
	// ListGroupsOfPrincipalAt returns the groups that the principal was a
	// member of at a time, from the history (for usage attribution).
	ListGroupsOfPrincipalAt(ctx context.Context, principalID string, at time.Time) ([]models.GroupMembershipChange, error)
	// ListOrgProjectsForTeamSync lists the projects of an organization. A
	// project whose repository is under the team owner gives the VCS
	// credential of the sync.
	ListOrgProjectsForTeamSync(ctx context.Context, orgID string) ([]models.Project, error)
	// ListGroupsLinkedToVCSTeam returns the groups of an organization that
	// link the team ("OWNER/SLUG" or "vcs_team:OWNER/SLUG").
	ListGroupsLinkedToVCSTeam(ctx context.Context, orgID, team string) ([]models.Group, error)
}

// GroupMembershipHistoryFilter filters ListGroupMembershipHistory.
type GroupMembershipHistoryFilter struct {
	GroupID     string
	PrincipalID string
	Since       *time.Time
	Until       *time.Time
	Limit       int
}
