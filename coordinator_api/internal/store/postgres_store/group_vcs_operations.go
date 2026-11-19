package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Groups linked to VCS teams (plan item B3, migration 000054). See
// store.GroupVCSStore for the rules.

var _ store.GroupVCSStore = PostgresDbStore{}

func (ps PostgresDbStore) CreateGroupVCSLink(ctx context.Context, link *models.GroupVCSLink) error {
	if link == nil || !isValidUUID(link.GroupID) {
		return store.ErrInvalidInput
	}
	link.Owner = strings.ToLower(strings.TrimSpace(link.Owner))
	link.TeamSlug = strings.ToLower(strings.TrimSpace(link.TeamSlug))
	link.Provider = strings.ToLower(strings.TrimSpace(link.Provider))
	if link.Owner == "" || link.TeamSlug == "" || link.Provider == "" {
		return store.ErrInvalidInput
	}
	// A savepoint: a duplicate must not abort the transaction of the caller.
	if err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error { return tx.Create(link).Error }); err != nil {
		if isUniqueViolation(err) {
			return store.ErrAlreadyExists
		}
		return fmt.Errorf("failed to create group VCS link: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) GetGroupVCSLink(ctx context.Context, linkID string) (*models.GroupVCSLink, error) {
	if !isValidUUID(linkID) {
		return nil, store.ErrNotFound
	}
	var link models.GroupVCSLink
	if err := ps.getDB(ctx).Where("link_id = ?", linkID).First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get group VCS link: %w", err)
	}
	return &link, nil
}

func (ps PostgresDbStore) ListGroupVCSLinks(ctx context.Context, groupID string) ([]models.GroupVCSLink, error) {
	links := []models.GroupVCSLink{}
	if !isValidUUID(groupID) {
		return links, nil
	}
	if err := ps.getDB(ctx).Where("group_id = ?", groupID).
		Order("provider ASC, owner ASC, team_slug ASC").Find(&links).Error; err != nil {
		return nil, fmt.Errorf("failed to list group VCS links: %w", err)
	}
	return links, nil
}

func (ps PostgresDbStore) DeleteGroupVCSLink(ctx context.Context, linkID string) error {
	if !isValidUUID(linkID) {
		return store.ErrNotFound
	}
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		var link models.GroupVCSLink
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("link_id = ?", linkID).First(&link).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return store.ErrNotFound
			}
			return err
		}
		var principalIDs []string
		if err := tx.Model(&models.GroupVCSLinkMember{}).Where("link_id = ?", linkID).
			Pluck("principal_id", &principalIDs).Error; err != nil {
			return err
		}
		if err := tx.Where("link_id = ?", linkID).Delete(&models.GroupVCSLink{}).Error; err != nil {
			return err
		}
		for _, principalID := range principalIDs {
			if _, err := refreshSyncedMembership(tx, link.GroupID, principalID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (ps PostgresDbStore) ListDueGroupVCSLinks(ctx context.Context, now time.Time, limit int) ([]models.GroupVCSLink, error) {
	if limit <= 0 {
		limit = 50
	}
	links := []models.GroupVCSLink{}
	if err := ps.getDB(ctx).Where("next_sync_at IS NULL OR next_sync_at <= ?", now).
		Order("next_sync_at ASC NULLS FIRST, link_id ASC").Limit(limit).Find(&links).Error; err != nil {
		return nil, fmt.Errorf("failed to list due group VCS links: %w", err)
	}
	return links, nil
}

func (ps PostgresDbStore) RequestGroupVCSSync(ctx context.Context, linkIDs []string) error {
	valid := []string{}
	for _, id := range linkIDs {
		if isValidUUID(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return nil
	}
	return ps.getDB(ctx).Model(&models.GroupVCSLink{}).Where("link_id IN ?", valid).
		Update("next_sync_at", gorm.Expr("timezone('utc', now())")).Error
}

func (ps PostgresDbStore) FindGroupVCSLinksByTeam(ctx context.Context, provider, owner, slug string, teamID int64) ([]models.GroupVCSLink, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	owner = strings.ToLower(strings.TrimSpace(owner))
	slug = strings.ToLower(strings.TrimSpace(slug))
	links := []models.GroupVCSLink{}
	query := ps.getDB(ctx).Where("provider = ? AND owner = ?", provider, owner)
	switch {
	case teamID != 0 && slug != "":
		query = query.Where("external_team_id = ? OR team_slug = ?", teamID, slug)
	case teamID != 0:
		query = query.Where("external_team_id = ?", teamID)
	case slug != "":
		query = query.Where("team_slug = ?", slug)
	default:
		return links, nil
	}
	if err := query.Find(&links).Error; err != nil {
		return nil, fmt.Errorf("failed to find group VCS links: %w", err)
	}
	return links, nil
}

func (ps PostgresDbStore) RenameGroupVCSLinkTeam(ctx context.Context, linkID, slug string) error {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !isValidUUID(linkID) || slug == "" {
		return store.ErrInvalidInput
	}
	var affected int64
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.GroupVCSLink{}).Where("link_id = ?", linkID).
			Updates(map[string]any{"team_slug": slug, "updated_at": gorm.Expr("timezone('utc', now())")})
		affected = result.RowsAffected
		return result.Error
	})
	if err != nil {
		if isUniqueViolation(err) {
			return store.ErrAlreadyExists
		}
		return fmt.Errorf("failed to rename group VCS link: %w", err)
	}
	if affected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) ApplyGroupVCSSync(ctx context.Context, linkID string, members []models.GroupVCSTeamMember, teamID *int64, syncedAt, nextSyncAt time.Time) (*models.GroupVCSSyncResult, error) {
	if !isValidUUID(linkID) {
		return nil, store.ErrNotFound
	}
	var result *models.GroupVCSSyncResult
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		link, err := lockGroupVCSLink(tx, linkID)
		if err != nil {
			return err
		}
		result, err = replaceLinkMembers(tx, link, members)
		if err != nil {
			return err
		}
		updates := map[string]any{
			"last_synced_at": syncedAt, "last_attempt_at": syncedAt, "last_error": "",
			"failure_count": 0, "next_sync_at": nextSyncAt, "member_count": result.Members,
			"updated_at": gorm.Expr("timezone('utc', now())"),
		}
		if teamID != nil && *teamID != 0 {
			updates["external_team_id"] = *teamID
		}
		return tx.Model(&models.GroupVCSLink{}).Where("link_id = ?", linkID).Updates(updates).Error
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to store group VCS sync: %w", err)
	}
	return result, nil
}

func (ps PostgresDbStore) RecordGroupVCSSyncFailure(ctx context.Context, linkID, message string, attemptAt, nextSyncAt time.Time, clearMembers bool) (*models.GroupVCSSyncResult, error) {
	if !isValidUUID(linkID) {
		return nil, store.ErrNotFound
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	result := &models.GroupVCSSyncResult{Added: []string{}, Removed: []string{}}
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		link, err := lockGroupVCSLink(tx, linkID)
		if err != nil {
			return err
		}
		updates := map[string]any{
			"last_attempt_at": attemptAt, "last_error": message,
			"failure_count": gorm.Expr("failure_count + 1"), "next_sync_at": nextSyncAt,
			"updated_at": gorm.Expr("timezone('utc', now())"),
		}
		if clearMembers {
			result, err = replaceLinkMembers(tx, link, nil)
			if err != nil {
				return err
			}
			updates["member_count"] = 0
		} else {
			result.Members = link.MemberCount
		}
		return tx.Model(&models.GroupVCSLink{}).Where("link_id = ?", linkID).Updates(updates).Error
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to record group VCS sync failure: %w", err)
	}
	return result, nil
}

func (ps PostgresDbStore) ListGroupVCSLinkMembers(ctx context.Context, linkID string) ([]models.GroupVCSLinkMember, error) {
	members := []models.GroupVCSLinkMember{}
	if !isValidUUID(linkID) {
		return members, nil
	}
	if err := ps.getDB(ctx).Where("link_id = ?", linkID).Order("vcs_login ASC").Find(&members).Error; err != nil {
		return nil, fmt.Errorf("failed to list group VCS link members: %w", err)
	}
	return members, nil
}

func (ps PostgresDbStore) ListGroupMemberDetails(ctx context.Context, groupID string) ([]models.GroupMemberDetail, error) {
	details := []models.GroupMemberDetail{}
	if !isValidUUID(groupID) {
		return details, nil
	}
	type row struct {
		PrincipalID string
		Kind        string
		Name        string
		DisplayName string
		UserID      *string
		Username    *string
		Source      string
		AddedAt     time.Time
	}
	var rows []row
	if err := ps.getDB(ctx).Raw(`
		SELECT gm.principal_id, p.kind, p.name, p.display_name, gm.user_id, u.username, gm.source, gm.added_at
		  FROM group_members gm
		  JOIN principals p ON p.principal_id = gm.principal_id
		  LEFT JOIN users u ON u.user_id = gm.user_id
		 WHERE gm.group_id = ?
		 ORDER BY coalesce(u.username, p.name) ASC, gm.principal_id ASC`, groupID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list group member details: %w", err)
	}
	type teamRow struct {
		PrincipalID string
		Owner       string
		TeamSlug    string
		VCSLogin    string `gorm:"column:vcs_login"`
	}
	var teams []teamRow
	if err := ps.getDB(ctx).Raw(`
		SELECT lm.principal_id, l.owner, l.team_slug, lm.vcs_login
		  FROM group_vcs_link_members lm JOIN group_vcs_links l ON l.link_id = lm.link_id
		 WHERE l.group_id = ?
		 ORDER BY l.owner, l.team_slug`, groupID).Scan(&teams).Error; err != nil {
		return nil, fmt.Errorf("failed to list group team members: %w", err)
	}
	byPrincipal := map[string][]teamRow{}
	for _, team := range teams {
		byPrincipal[team.PrincipalID] = append(byPrincipal[team.PrincipalID], team)
	}
	for _, r := range rows {
		detail := models.GroupMemberDetail{PrincipalID: r.PrincipalID, Kind: r.Kind, Name: r.Name,
			DisplayName: r.DisplayName, Source: r.Source, Manual: r.Source == models.GroupMemberSourceManual, AddedAt: r.AddedAt}
		if r.UserID != nil {
			detail.UserID = *r.UserID
		}
		if r.Username != nil {
			detail.Username = *r.Username
		}
		logins := map[string]bool{}
		for _, team := range byPrincipal[r.PrincipalID] {
			detail.Teams = append(detail.Teams, "vcs_team:"+team.Owner+"/"+team.TeamSlug)
			if !logins[team.VCSLogin] {
				logins[team.VCSLogin] = true
				detail.VCSLogins = append(detail.VCSLogins, team.VCSLogin)
			}
		}
		details = append(details, detail)
	}
	return details, nil
}

func (ps PostgresDbStore) ListGroupMembershipHistory(ctx context.Context, filter store.GroupMembershipHistoryFilter) ([]models.GroupMembershipChange, error) {
	changes := []models.GroupMembershipChange{}
	query := ps.getDB(ctx).Model(&models.GroupMembershipChange{})
	if filter.GroupID != "" {
		if !isValidUUID(filter.GroupID) {
			return changes, nil
		}
		query = query.Where("group_id = ?", filter.GroupID)
	}
	if filter.PrincipalID != "" {
		if !isValidUUID(filter.PrincipalID) {
			return changes, nil
		}
		query = query.Where("principal_id = ?", filter.PrincipalID)
	}
	if filter.Since != nil {
		query = query.Where("changed_at >= ?", *filter.Since)
	}
	if filter.Until != nil {
		query = query.Where("changed_at <= ?", *filter.Until)
	}
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if err := query.Order("changed_at DESC, history_id DESC").Limit(limit).Find(&changes).Error; err != nil {
		return nil, fmt.Errorf("failed to list group membership history: %w", err)
	}
	return changes, nil
}

func (ps PostgresDbStore) ListGroupsOfPrincipalAt(ctx context.Context, principalID string, at time.Time) ([]models.GroupMembershipChange, error) {
	changes := []models.GroupMembershipChange{}
	if !isValidUUID(principalID) {
		return changes, nil
	}
	if err := ps.getDB(ctx).Raw(`
		SELECT * FROM (
		  SELECT DISTINCT ON (group_id) *
		    FROM group_membership_history
		   WHERE principal_id = ? AND changed_at <= ?
		   ORDER BY group_id, changed_at DESC, history_id DESC
		) last WHERE last.change = 'added' ORDER BY last.group_name`, principalID, at).Scan(&changes).Error; err != nil {
		return nil, fmt.Errorf("failed to list groups of principal at a time: %w", err)
	}
	return changes, nil
}

func lockGroupVCSLink(tx *gorm.DB, linkID string) (*models.GroupVCSLink, error) {
	var link models.GroupVCSLink
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("link_id = ?", linkID).First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &link, nil
}

// replaceLinkMembers makes the members of one link equal to members and
// updates the derived group memberships. It reports the principals whose
// group membership changed.
func replaceLinkMembers(tx *gorm.DB, link *models.GroupVCSLink, members []models.GroupVCSTeamMember) (*models.GroupVCSSyncResult, error) {
	desired := map[string]string{}
	for _, member := range members {
		if !isValidUUID(member.PrincipalID) || member.PrincipalID == models.SystemPrincipalID {
			continue
		}
		if _, seen := desired[member.PrincipalID]; !seen {
			desired[member.PrincipalID] = member.Login
		}
	}
	var current []models.GroupVCSLinkMember
	if err := tx.Where("link_id = ?", link.LinkID).Find(&current).Error; err != nil {
		return nil, err
	}
	have := map[string]string{}
	for _, row := range current {
		have[row.PrincipalID] = row.VCSLogin
	}
	affected := []string{}
	for principalID := range have {
		if _, keep := desired[principalID]; !keep {
			if err := tx.Where("link_id = ? AND principal_id = ?", link.LinkID, principalID).
				Delete(&models.GroupVCSLinkMember{}).Error; err != nil {
				return nil, err
			}
			affected = append(affected, principalID)
		}
	}
	for principalID, login := range desired {
		previous, exists := have[principalID]
		switch {
		case !exists:
			row := &models.GroupVCSLinkMember{LinkID: link.LinkID, PrincipalID: principalID, VCSLogin: login}
			if err := tx.Create(row).Error; err != nil {
				return nil, err
			}
			affected = append(affected, principalID)
		case previous != login:
			if err := tx.Model(&models.GroupVCSLinkMember{}).Where("link_id = ? AND principal_id = ?", link.LinkID, principalID).
				Update("vcs_login", login).Error; err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(affected)
	result := &models.GroupVCSSyncResult{Added: []string{}, Removed: []string{}, Members: len(desired)}
	for _, principalID := range affected {
		change, err := refreshSyncedMembership(tx, link.GroupID, principalID)
		if err != nil {
			return nil, err
		}
		switch change {
		case models.GroupMembershipAdded:
			result.Added = append(result.Added, principalID)
		case models.GroupMembershipRemoved:
			result.Removed = append(result.Removed, principalID)
		}
	}
	return result, nil
}

// refreshSyncedMembership makes the group_members row of one principal agree
// with the linked teams: a vcs_sync row exists while a linked team of the
// group has the principal. A manual row is never changed. It returns
// "added", "removed", or "".
func refreshSyncedMembership(tx *gorm.DB, groupID, principalID string) (string, error) {
	linked, err := principalHasLinkedTeam(tx, groupID, principalID)
	if err != nil {
		return "", err
	}
	if linked {
		result := tx.Exec(`INSERT INTO group_members (group_id, principal_id, source)
			VALUES (?, ?, 'vcs_sync') ON CONFLICT DO NOTHING`, groupID, principalID)
		if result.Error != nil {
			return "", result.Error
		}
		if result.RowsAffected > 0 {
			return models.GroupMembershipAdded, nil
		}
		return "", nil
	}
	result := tx.Exec(`DELETE FROM group_members WHERE group_id = ? AND principal_id = ? AND source = 'vcs_sync'`, groupID, principalID)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected > 0 {
		return models.GroupMembershipRemoved, nil
	}
	return "", nil
}

// principalHasLinkedTeam reports whether a linked team of the group has the
// principal.
func principalHasLinkedTeam(tx *gorm.DB, groupID, principalID string) (bool, error) {
	var linked bool
	err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM group_vcs_link_members lm
		JOIN group_vcs_links l ON l.link_id = lm.link_id
		WHERE l.group_id = ? AND lm.principal_id = ?)`, groupID, principalID).Scan(&linked).Error
	return linked, err
}

// makeMembershipManual turns a synced membership into a manual one after a
// manual add. A manual add of an existing synced member creates no row.
func makeMembershipManual(db *gorm.DB, groupID, principalID string) error {
	return db.Exec(`UPDATE group_members SET source = 'manual'
		WHERE group_id = ? AND principal_id = ? AND source = 'vcs_sync'`, groupID, principalID).Error
}

// removeManualMembership removes the manual part of a membership. When a
// linked team still has the principal, the row stays as vcs_sync. A row that
// only a linked team gives is ErrConflict: remove the person from the team,
// or unlink the team.
func removeManualMembership(db *gorm.DB, groupID string, member *models.GroupMember) error {
	if member.Source == models.GroupMemberSourceVCSSync {
		return store.ErrConflict
	}
	linked, err := principalHasLinkedTeam(db, groupID, member.PrincipalID)
	if err != nil {
		return err
	}
	if linked {
		return db.Exec(`UPDATE group_members SET source = 'vcs_sync' WHERE group_id = ? AND principal_id = ?`,
			groupID, member.PrincipalID).Error
	}
	return db.Where("group_id = ? AND principal_id = ?", groupID, member.PrincipalID).Delete(&models.GroupMember{}).Error
}

func (ps PostgresDbStore) ListOrgProjectsForTeamSync(ctx context.Context, orgID string) ([]models.Project, error) {
	projects := []models.Project{}
	if !isValidUUID(orgID) {
		return projects, nil
	}
	if err := ps.getDB(ctx).Where("org_id = ?", orgID).Order("name ASC").Find(&projects).Error; err != nil {
		return nil, fmt.Errorf("failed to list organization projects: %w", err)
	}
	return projects, nil
}

// ListGroupsLinkedToVCSTeam returns the groups of an organization that link
// a VCS team. team is "OWNER/SLUG" or "vcs_team:OWNER/SLUG" (any provider).
// A vcs_team workflow owner uses it for workflow authority and the lane
// priority shift.
func (ps PostgresDbStore) ListGroupsLinkedToVCSTeam(ctx context.Context, orgID, team string) ([]models.Group, error) {
	groups := []models.Group{}
	parts := strings.Split(strings.ToLower(strings.TrimPrefix(strings.TrimSpace(team), "vcs_team:")), "/")
	if !isValidUUID(orgID) || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return groups, nil
	}
	if err := ps.getDB(ctx).
		Where("groups.org_id = ? AND EXISTS (SELECT 1 FROM group_vcs_links l WHERE l.group_id = groups.group_id AND l.owner = ? AND l.team_slug = ?)",
			orgID, parts[0], parts[1]).
		Order("groups.name ASC").Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("failed to list groups linked to a VCS team: %w", err)
	}
	return groups, nil
}
