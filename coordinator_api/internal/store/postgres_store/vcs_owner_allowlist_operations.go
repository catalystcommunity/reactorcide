package postgres_store

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/lib/pq"
)

// The VCS owner allowlist of an organization (migration 000054): the VCS
// owners for which the group VCS sync may use the global VCS credential.
// Raw SQL only: the Organization model does not map the column, so a
// whole-struct save cannot reset it.

// GetOrganizationVCSOwnerAllowlist returns the allowlist, sorted. A missing
// organization is store.ErrNotFound.
func (ps PostgresDbStore) GetOrganizationVCSOwnerAllowlist(ctx context.Context, orgID string) ([]string, error) {
	if !isValidUUID(orgID) {
		return nil, store.ErrNotFound
	}
	var rows []struct {
		Allowlist pq.StringArray `gorm:"column:vcs_owner_allowlist"`
	}
	if err := ps.getDB(ctx).Raw(`SELECT vcs_owner_allowlist FROM organizations WHERE org_id = ?`, orgID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to read the VCS owner allowlist: %w", err)
	}
	if len(rows) == 0 {
		return nil, store.ErrNotFound
	}
	result := append([]string{}, rows[0].Allowlist...)
	sort.Strings(result)
	return result, nil
}

// SetOrganizationVCSOwnerAllowlist replaces the allowlist. Owners are
// trimmed, lowercased, and deduplicated.
func (ps PostgresDbStore) SetOrganizationVCSOwnerAllowlist(ctx context.Context, orgID string, owners []string) ([]string, error) {
	if !isValidUUID(orgID) {
		return nil, store.ErrNotFound
	}
	seen := map[string]bool{}
	clean := []string{}
	for _, owner := range owners {
		owner = strings.ToLower(strings.TrimSpace(owner))
		if owner == "" || seen[owner] {
			continue
		}
		seen[owner] = true
		clean = append(clean, owner)
	}
	sort.Strings(clean)
	result := ps.getDB(ctx).Exec(`UPDATE organizations SET vcs_owner_allowlist = ? WHERE org_id = ?`, pq.StringArray(clean), orgID)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to set the VCS owner allowlist: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, store.ErrNotFound
	}
	return clean, nil
}
