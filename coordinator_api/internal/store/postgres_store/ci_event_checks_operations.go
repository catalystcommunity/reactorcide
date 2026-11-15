package postgres_store

import (
	"context"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/ctxkey"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

var _ store.CIEventChecksStore = PostgresDbStore{}

// WithCIEventChecksLock serializes the checks recompute of one event across
// replicas. The lock is released when the transaction ends.
func (ps PostgresDbStore) WithCIEventChecksLock(ctx context.Context, eventID string, fn func(ctx context.Context) error) error {
	if !isValidUUID(eventID) {
		return store.ErrNotFound
	}
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?)::bigint)", "ci_event_checks:"+eventID).Error; err != nil {
			return fmt.Errorf("acquiring event checks lock: %w", err)
		}
		return fn(context.WithValue(ctx, ctxkey.TxKey(), tx))
	})
}

func (ps PostgresDbStore) ListRootWorkflowInstancesForEvent(ctx context.Context, eventID string) ([]models.WorkflowInstance, error) {
	if !isValidUUID(eventID) {
		return nil, nil
	}
	// The root workflows the event created, and the workflows of earlier
	// events that it carries (a re-evaluation that did not change their
	// decision).
	var workflows []models.WorkflowInstance
	if err := ps.getDB(ctx).Where(`parent_workflow_id IS NULL AND (event_id = ? OR workflow_id IN (
			SELECT workflow_id FROM ci_event_workflow_decisions
			 WHERE event_id = ? AND decision = ? AND workflow_id IS NOT NULL))`,
		eventID, eventID, models.WorkflowDecisionCarried).
		Order("created_at ASC").Find(&workflows).Error; err != nil {
		return nil, fmt.Errorf("failed to list event workflows: %w", err)
	}
	return workflows, nil
}

func (ps PostgresDbStore) SaveCIEventChecks(ctx context.Context, eventID string, checks models.CIEventChecks) error {
	return ps.updateCIEvent(ctx, eventID, map[string]interface{}{
		"checks_state":        checks.State,
		"checks_description":  checks.Description,
		"checks_target_url":   checks.TargetURL,
		"checks_published_at": time.Now().UTC(),
	})
}

func (ps PostgresDbStore) SetCIEventPolicyViolationCount(ctx context.Context, eventID string, count int) error {
	return ps.updateCIEvent(ctx, eventID, map[string]interface{}{"policy_violation_count": count})
}

// NewestCIEventForCommit returns the newest event that is not a duplicate for
// the same project, commit, and pull request as eventID. It returns eventID
// itself when no newer event exists.
func (ps PostgresDbStore) NewestCIEventForCommit(ctx context.Context, eventID string) (string, error) {
	if !isValidUUID(eventID) {
		return eventID, nil
	}
	var row struct{ EventID string }
	err := ps.getDB(ctx).Raw(`SELECT n.event_id FROM ci_events e
		JOIN ci_events n ON n.project_id IS NOT DISTINCT FROM e.project_id AND n.commit_sha = e.commit_sha
		 AND n.pr_number IS NOT DISTINCT FROM e.pr_number AND n.status <> ?
		WHERE e.event_id = ? AND e.commit_sha <> ''
		ORDER BY n.received_at DESC, n.event_id DESC LIMIT 1`, models.CIEventStatusDuplicate, eventID).Scan(&row).Error
	if err != nil {
		return "", fmt.Errorf("finding the newest event for the commit: %w", err)
	}
	if row.EventID == "" {
		return eventID, nil
	}
	return row.EventID, nil
}
