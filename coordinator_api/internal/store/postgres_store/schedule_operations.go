package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	_ store.LeaseStore    = PostgresDbStore{}
	_ store.ScheduleStore = PostgresDbStore{}
)

// TryAcquireLease is one conditional upsert. The WHERE clause on the update
// lets it win only when the row is ours or has expired; RETURNING tells us
// whether it won. Two replicas racing for an expired lease serialize on the
// row lock, and the second sees the first one's fresh expires_at and loses.
func (ps PostgresDbStore) TryAcquireLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	if name == "" || holder == "" || ttl <= 0 {
		return false, fmt.Errorf("lease name, holder and ttl are required")
	}
	var holders []string
	err := ps.getDB(ctx).Raw(`
INSERT INTO coordinator_leases (name, holder, acquired_at, renewed_at, expires_at)
VALUES (?, ?, now(), now(), now() + make_interval(secs => ?))
ON CONFLICT (name) DO UPDATE SET
    holder = EXCLUDED.holder,
    acquired_at = CASE WHEN coordinator_leases.holder = EXCLUDED.holder
        THEN coordinator_leases.acquired_at ELSE now() END,
    renewed_at = now(),
    expires_at = EXCLUDED.expires_at
WHERE coordinator_leases.holder = EXCLUDED.holder
   OR coordinator_leases.expires_at < now()
RETURNING holder`, name, holder, ttl.Seconds()).Scan(&holders).Error
	if err != nil {
		return false, fmt.Errorf("failed to acquire lease %q: %w", name, err)
	}
	return len(holders) == 1 && holders[0] == holder, nil
}

// ReleaseLease deletes the lease only when holder has it.
func (ps PostgresDbStore) ReleaseLease(ctx context.Context, name, holder string) error {
	if err := ps.getDB(ctx).Exec(`DELETE FROM coordinator_leases WHERE name = ? AND holder = ?`, name, holder).Error; err != nil {
		return fmt.Errorf("failed to release lease %q: %w", name, err)
	}
	return nil
}

func (ps PostgresDbStore) CreateSchedule(ctx context.Context, schedule *models.CISchedule) error {
	if err := ps.getDB(ctx).Create(schedule).Error; err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("schedule %q already exists in this project: %w", schedule.Name, store.ErrAlreadyExists)
		}
		return fmt.Errorf("failed to create schedule: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) GetSchedule(ctx context.Context, scheduleID string) (*models.CISchedule, error) {
	if !isValidUUID(scheduleID) {
		return nil, store.ErrNotFound
	}
	var schedule models.CISchedule
	if err := ps.getDB(ctx).Where("schedule_id = ?", scheduleID).First(&schedule).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get schedule: %w", err)
	}
	return &schedule, nil
}

func (ps PostgresDbStore) ListSchedulesByProject(ctx context.Context, projectID string) ([]models.CISchedule, error) {
	schedules := []models.CISchedule{}
	if !isValidUUID(projectID) {
		return schedules, nil
	}
	if err := ps.getDB(ctx).Where("project_id = ?", projectID).Order("name ASC").Find(&schedules).Error; err != nil {
		return nil, fmt.Errorf("failed to list schedules: %w", err)
	}
	return schedules, nil
}

func (ps PostgresDbStore) UpdateSchedule(ctx context.Context, schedule *models.CISchedule) error {
	result := ps.getDB(ctx).Model(&models.CISchedule{}).
		Where("schedule_id = ?", schedule.ScheduleID).
		Updates(map[string]interface{}{
			"name":         schedule.Name,
			"branch":       schedule.Branch,
			"workflow_ref": schedule.WorkflowRef,
			"cron":         schedule.Cron,
			"timezone":     schedule.Timezone,
			"enabled":      schedule.Enabled,
			"next_fire_at": schedule.NextFireAt,
			"last_error":   schedule.LastError,
			"updated_at":   gorm.Expr("now()"),
		})
	if result.Error != nil {
		if isUniqueViolation(result.Error) {
			return fmt.Errorf("schedule %q already exists in this project: %w", schedule.Name, store.ErrAlreadyExists)
		}
		return fmt.Errorf("failed to update schedule: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) DeleteSchedule(ctx context.Context, scheduleID string) error {
	if !isValidUUID(scheduleID) {
		return store.ErrNotFound
	}
	result := ps.getDB(ctx).Where("schedule_id = ?", scheduleID).Delete(&models.CISchedule{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete schedule: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) ListScheduleFires(ctx context.Context, scheduleID string, limit int) ([]models.CIScheduleFire, error) {
	fires := []models.CIScheduleFire{}
	if !isValidUUID(scheduleID) || limit <= 0 {
		return fires, nil
	}
	if err := ps.getDB(ctx).Where("schedule_id = ?", scheduleID).Order("fire_time DESC").Limit(limit).Find(&fires).Error; err != nil {
		return nil, fmt.Errorf("failed to list schedule fires: %w", err)
	}
	return fires, nil
}

// planErrorRetry is how long a schedule whose plan failed (a cron or timezone
// that no longer parses) waits before the scheduler tries it again, so one
// broken row does not spin the loop.
const planErrorRetry = time.Hour

func (ps PostgresDbStore) ClaimDueScheduleFires(ctx context.Context, now time.Time, limit int, plan store.ScheduleFirePlan) ([]store.ClaimedScheduleFire, error) {
	if limit <= 0 {
		return nil, nil
	}
	var claimed []store.ClaimedScheduleFire
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		var due []models.CISchedule
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("enabled AND next_fire_at IS NOT NULL AND next_fire_at <= ?", now).
			Order("next_fire_at ASC").Limit(limit).Find(&due).Error; err != nil {
			return fmt.Errorf("failed to select due schedules: %w", err)
		}
		for _, schedule := range due {
			fireTime, next, planErr := plan(schedule, now)
			if planErr != nil {
				retry := now.Add(planErrorRetry)
				if err := tx.Model(&models.CISchedule{}).Where("schedule_id = ?", schedule.ScheduleID).
					Updates(map[string]interface{}{"next_fire_at": retry, "last_error": planErr.Error(), "updated_at": gorm.Expr("now()")}).Error; err != nil {
					return fmt.Errorf("failed to record schedule plan error: %w", err)
				}
				continue
			}
			result := tx.Exec(`INSERT INTO ci_schedule_fires (schedule_id, fire_time, status) VALUES (?, ?, ?)
ON CONFLICT (schedule_id, fire_time) DO NOTHING`, schedule.ScheduleID, fireTime, models.ScheduleFireFiring)
			if result.Error != nil {
				return fmt.Errorf("failed to insert schedule fire: %w", result.Error)
			}
			if err := tx.Model(&models.CISchedule{}).Where("schedule_id = ?", schedule.ScheduleID).
				Updates(map[string]interface{}{"next_fire_at": next, "updated_at": gorm.Expr("now()")}).Error; err != nil {
				return fmt.Errorf("failed to advance schedule: %w", err)
			}
			if result.RowsAffected == 1 {
				claimed = append(claimed, store.ClaimedScheduleFire{Schedule: schedule, FireTime: fireTime})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func (ps PostgresDbStore) RecordScheduleFireResult(ctx context.Context, scheduleID string, fireTime time.Time, eventID *string, errMessage string) error {
	// event_id is a UUID column. A malformed ID must not leave the fire row
	// stuck in "firing": record the fire as failed instead.
	if eventID != nil && !isValidUUID(*eventID) {
		eventID = nil
		if errMessage == "" {
			errMessage = "the fire returned an invalid event ID"
		}
	}
	status := models.ScheduleFireFired
	if errMessage != "" {
		status = models.ScheduleFireFailed
	}
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.CIScheduleFire{}).
			Where("schedule_id = ? AND fire_time = ?", scheduleID, fireTime).
			Updates(map[string]interface{}{"status": status, "event_id": eventID, "error": errMessage, "completed_at": gorm.Expr("now()")}).Error; err != nil {
			return fmt.Errorf("failed to complete schedule fire: %w", err)
		}
		updates := map[string]interface{}{"last_fire_at": fireTime, "last_error": errMessage, "updated_at": gorm.Expr("now()")}
		if eventID != nil {
			updates["last_event_id"] = *eventID
		}
		if err := tx.Model(&models.CISchedule{}).Where("schedule_id = ?", scheduleID).Updates(updates).Error; err != nil {
			return fmt.Errorf("failed to record schedule result: %w", err)
		}
		return nil
	})
}
