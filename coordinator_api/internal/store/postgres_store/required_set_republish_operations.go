package postgres_store

import (
	"context"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/lib/pq"
)

// requiredSetChangeActions are the audit actions that change which
// workflows of a project are required.
var requiredSetChangeActions = []string{
	"required_set.enable", "required_set.disable", "required_set.promote",
	"required_set.remove", "required_set.quarantine", "required_set.unquarantine",
}

func (ps PostgresDbStore) ListRequiredSetChangedProjects(ctx context.Context, since, now time.Time) ([]string, error) {
	var ids []string
	err := ps.getDB(ctx).Raw(`
SELECT DISTINCT project_id FROM (
    SELECT details->>'project_id' AS project_id FROM audit_events
     WHERE action = ANY(?) AND created_at > ?
    UNION
    SELECT project_id::text FROM project_required_workflows
     WHERE quarantined_until IS NOT NULL AND quarantined_until > ? AND quarantined_until <= ?
) changed
WHERE project_id IS NOT NULL AND project_id <> ''`,
		pq.StringArray(requiredSetChangeActions), since.UTC(), since.UTC(), now.UTC()).Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list required set changes: %w", err)
	}
	return ids, nil
}

func (ps PostgresDbStore) ListOpenPullRequestEventIDs(ctx context.Context, projectID string, since time.Time, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 200
	}
	var ids []string
	err := ps.getDB(ctx).Raw(`
SELECT event_id FROM (
    SELECT DISTINCT ON (pr_number) event_id::text AS event_id, event_type, received_at
      FROM ci_events
     WHERE project_id = ? AND pr_number IS NOT NULL AND received_at > ? AND status <> ?
     ORDER BY pr_number, received_at DESC
) newest
WHERE event_type NOT IN ('pull_request_closed', 'pull_request_merged')
ORDER BY received_at DESC
LIMIT ?`, projectID, since.UTC(), models.CIEventStatusDuplicate, limit).Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list open pull request events: %w", err)
	}
	return ids, nil
}
