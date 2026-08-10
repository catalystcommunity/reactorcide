package postgres_store

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func (ps PostgresDbStore) AppendAuditEvent(ctx context.Context, event *models.AuditEvent) error {
	return ps.getDB(ctx).Create(event).Error
}

func (ps PostgresDbStore) ListAuditEvents(ctx context.Context, orgID string, limit int) ([]models.AuditEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var events []models.AuditEvent
	err := ps.getDB(ctx).Where("org_id = ?", orgID).Order("created_at DESC").Limit(limit).Find(&events).Error
	return events, err
}

func (ps PostgresDbStore) PruneAuditEvents(ctx context.Context, before time.Time) (int64, error) {
	result := ps.getDB(ctx).Where("created_at < ?", before).Delete(&models.AuditEvent{})
	return result.RowsAffected, result.Error
}
