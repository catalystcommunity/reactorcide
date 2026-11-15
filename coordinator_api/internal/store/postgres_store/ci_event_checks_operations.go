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
	var workflows []models.WorkflowInstance
	if err := ps.getDB(ctx).Where("event_id = ? AND parent_workflow_id IS NULL", eventID).
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
