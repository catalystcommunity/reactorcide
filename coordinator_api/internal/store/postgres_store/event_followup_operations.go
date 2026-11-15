package postgres_store

import (
	"context"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

var (
	_ store.EventSettingsStore       = PostgresDbStore{}
	_ store.CIEventClaimStore        = PostgresDbStore{}
	_ store.ScheduleMaintenanceStore = PostgresDbStore{}
)

// ciEventClaimStaleAfter is how long a claim without an eval job blocks a
// redelivery. Creating an eval job takes well under a second; a claim this
// old belongs to a coordinator that stopped part way.
const ciEventClaimStaleAfter = 5 * time.Minute

func (ps PostgresDbStore) EffectiveDynamicJobs(ctx context.Context, orgID string, projectID *string) (string, error) {
	var orgSetting string
	if isValidUUID(orgID) {
		var row struct{ DynamicJobs string }
		if err := ps.getDB(ctx).Raw(`SELECT dynamic_jobs FROM organizations WHERE org_id = ?`, orgID).Scan(&row).Error; err != nil {
			return "", fmt.Errorf("reading organization dynamic_jobs: %w", err)
		}
		orgSetting = row.DynamicJobs
	}
	var projectSetting string
	if projectID != nil && isValidUUID(*projectID) {
		var row struct{ DynamicJobs string }
		if err := ps.getDB(ctx).Raw(`SELECT dynamic_jobs FROM projects WHERE project_id = ?`, *projectID).Scan(&row).Error; err != nil {
			return "", fmt.Errorf("reading project dynamic_jobs: %w", err)
		}
		projectSetting = row.DynamicJobs
	}
	return models.EffectiveDynamicJobs(projectSetting, orgSetting), nil
}

func (ps PostgresDbStore) EffectiveDuplicateEvents(ctx context.Context, projectID string) (string, error) {
	if !isValidUUID(projectID) {
		return "", store.ErrNotFound
	}
	return duplicateEventsSetting(ps.getDB(ctx), projectID)
}

func (ps PostgresDbStore) ClaimCIEventForEval(ctx context.Context, eventID string) (bool, error) {
	if !isValidUUID(eventID) {
		return false, store.ErrNotFound
	}
	result := ps.getDB(ctx).Exec(`UPDATE ci_events
SET status = ?, updated_at = timezone('utc', now())
WHERE event_id = ? AND eval_job_id IS NULL
  AND (status = ? OR (status = ? AND updated_at < timezone('utc', now()) - make_interval(secs => ?)))`,
		models.CIEventStatusEvaluating, eventID,
		models.CIEventStatusReceived, models.CIEventStatusEvaluating, ciEventClaimStaleAfter.Seconds())
	if result.Error != nil {
		return false, fmt.Errorf("claiming event: %w", result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (ps PostgresDbStore) ReleaseCIEventClaim(ctx context.Context, eventID string) error {
	if !isValidUUID(eventID) {
		return store.ErrNotFound
	}
	err := ps.getDB(ctx).Exec(`UPDATE ci_events
SET status = ?, updated_at = timezone('utc', now())
WHERE event_id = ? AND eval_job_id IS NULL AND status = ?`,
		models.CIEventStatusReceived, eventID, models.CIEventStatusEvaluating).Error
	if err != nil {
		return fmt.Errorf("releasing event claim: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) RelinkCIEventEvalJob(ctx context.Context, eventID, jobID string) error {
	return ps.updateCIEvent(ctx, eventID, map[string]interface{}{
		"eval_job_id": jobID, "status": models.CIEventStatusEvaluating,
	})
}

func (ps PostgresDbStore) FailStaleScheduleFires(ctx context.Context, olderThan time.Duration, message string) (int64, error) {
	var changed int64
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		var stale []models.CIScheduleFire
		if err := tx.Raw(`UPDATE ci_schedule_fires
SET status = ?, error = ?, completed_at = now()
WHERE status = ? AND created_at < now() - make_interval(secs => ?)
RETURNING schedule_id, fire_time`,
			models.ScheduleFireFailed, message, models.ScheduleFireFiring, olderThan.Seconds()).Scan(&stale).Error; err != nil {
			return fmt.Errorf("failing stale schedule fires: %w", err)
		}
		changed = int64(len(stale))
		for _, fire := range stale {
			if err := tx.Exec(`UPDATE ci_schedules
SET last_error = ?, last_fire_at = GREATEST(COALESCE(last_fire_at, ?), ?), updated_at = now()
WHERE schedule_id = ?`, message, fire.FireTime, fire.FireTime, fire.ScheduleID).Error; err != nil {
				return fmt.Errorf("recording stale fire on schedule: %w", err)
			}
		}
		return nil
	})
	return changed, err
}

func (ps PostgresDbStore) SetScheduleLastError(ctx context.Context, scheduleID, message string) error {
	if !isValidUUID(scheduleID) {
		return store.ErrNotFound
	}
	if err := ps.getDB(ctx).Exec(`UPDATE ci_schedules SET last_error = ?, updated_at = now() WHERE schedule_id = ?`,
		message, scheduleID).Error; err != nil {
		return fmt.Errorf("setting schedule last_error: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) FailScheduleFireForEvent(ctx context.Context, eventID, message string) error {
	if !isValidUUID(eventID) {
		return store.ErrNotFound
	}
	if err := ps.getDB(ctx).Exec(`UPDATE ci_schedule_fires
SET status = ?, error = ?, completed_at = COALESCE(completed_at, now())
WHERE event_id = ?`, models.ScheduleFireFailed, message, eventID).Error; err != nil {
		return fmt.Errorf("failing schedule fire of event: %w", err)
	}
	return nil
}
