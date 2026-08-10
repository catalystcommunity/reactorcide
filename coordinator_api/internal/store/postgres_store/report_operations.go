package postgres_store

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/ctxkey"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (ps PostgresDbStore) UpsertVCSReportTarget(ctx context.Context, target *models.VCSReportTarget) error {
	if target.CurrentGeneration <= 0 {
		target.CurrentGeneration = 1
	}
	target.GenerationComplete = true
	return ps.getDB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "provider"}, {Name: "repository"}, {Name: "target_type"}, {Name: "external_target_id"}, {Name: "root_marker"}},
		DoUpdates: clause.AssignmentColumns([]string{"org_id", "project_id", "updated_at"}),
	}, clause.Returning{}).Create(target).Error
}

func (ps PostgresDbStore) StartVCSReportGeneration(ctx context.Context, target *models.VCSReportTarget, generationKey string) error {
	target.GenerationKey = &generationKey
	target.GenerationComplete = false
	return ps.getDB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "provider"}, {Name: "repository"}, {Name: "target_type"}, {Name: "external_target_id"}, {Name: "root_marker"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"org_id": target.OrgID, "project_id": target.ProjectID,
			"current_generation": gorm.Expr("CASE WHEN vcs_report_targets.generation_key IS DISTINCT FROM EXCLUDED.generation_key THEN vcs_report_targets.current_generation + 1 ELSE vcs_report_targets.current_generation END"),
			"generation_key":     generationKey, "generation_complete": false, "updated_at": gorm.Expr("timezone('utc', now())"),
		}),
	}, clause.Returning{}).Create(target).Error
}

func (ps PostgresDbStore) CompleteVCSReportGeneration(ctx context.Context, targetID string, generation int64) error {
	result := ps.getDB(ctx).Model(&models.VCSReportTarget{}).
		Where("report_target_id = ? AND current_generation = ?", targetID, generation).
		Updates(map[string]interface{}{"generation_complete": true, "desired_revision": gorm.Expr("desired_revision + 1"), "dirty": true})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return store.ErrNotFound
	}
	return nil
}

// UpsertVCSReportEntry writes structured state and marks the target dirty in
// one transaction. Provider calls are deliberately outside this transaction.
func (ps PostgresDbStore) UpsertVCSReportEntry(ctx context.Context, entry *models.VCSReportEntry) error {
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "report_target_id"}, {Name: "entry_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"workflow_id", "generation", "status", "structured_state", "updated_at"}),
		}).Create(entry).Error; err != nil {
			return err
		}
		result := tx.Model(&models.VCSReportTarget{}).Where("report_target_id = ?", entry.ReportTargetID).
			Updates(map[string]interface{}{"desired_revision": gorm.Expr("desired_revision + 1"), "dirty": true, "last_error": nil})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (ps PostgresDbStore) GetVCSReportTarget(ctx context.Context, id string) (*models.VCSReportTarget, error) {
	var target models.VCSReportTarget
	err := ps.getDB(ctx).Where("report_target_id = ?", id).First(&target).Error
	if err == gorm.ErrRecordNotFound {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &target, nil
}

func (ps PostgresDbStore) ListDirtyVCSReportTargets(ctx context.Context, limit int) ([]models.VCSReportTarget, error) {
	if limit <= 0 {
		limit = 50
	}
	var targets []models.VCSReportTarget
	err := ps.getDB(ctx).Where("dirty").Order("updated_at").Limit(limit).Find(&targets).Error
	return targets, err
}

func (ps PostgresDbStore) ListVCSReportEntries(ctx context.Context, targetID string) ([]models.VCSReportEntry, error) {
	var entries []models.VCSReportEntry
	err := ps.getDB(ctx).Where("report_target_id = ?", targetID).Order("entry_key").Find(&entries).Error
	return entries, err
}

// WithVCSReportTargetLock uses both halves of the UUID as the two PostgreSQL
// advisory-lock keys. The transaction owns the lock until fn returns.
func (ps PostgresDbStore) WithVCSReportTargetLock(ctx context.Context, targetID string, fn func(context.Context, *models.VCSReportTarget, []models.VCSReportEntry) error) error {
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		var locked bool
		lockSQL := `SELECT pg_try_advisory_xact_lock(
			(('x' || substr(replace(?::text, '-', ''), 1, 8))::bit(32)::int),
			(('x' || substr(replace(?::text, '-', ''), 9, 8))::bit(32)::int))`
		if err := tx.Raw(lockSQL, targetID, targetID).Scan(&locked).Error; err != nil {
			return err
		}
		if !locked {
			return fmt.Errorf("report target is locked")
		}
		lockedCtx := context.WithValue(ctx, ctxkey.TxKey(), tx)
		lockedStore := ps
		target, err := lockedStore.GetVCSReportTarget(lockedCtx, targetID)
		if err != nil {
			return err
		}
		entries, err := lockedStore.ListVCSReportEntries(lockedCtx, targetID)
		if err != nil {
			return err
		}
		return fn(lockedCtx, target, entries)
	})
}

func (ps PostgresDbStore) SetVCSReportRendered(ctx context.Context, targetID, commentID string, revision int64) error {
	updates := map[string]interface{}{"provider_comment_id": commentID, "rendered_revision": revision, "dirty": false, "last_error": nil}
	return ps.getDB(ctx).Model(&models.VCSReportTarget{}).Where("report_target_id = ? AND desired_revision <= ?", targetID, revision).Updates(updates).Error
}

func (ps PostgresDbStore) RecordVCSReportError(ctx context.Context, targetID string, reportErr error) error {
	message := ""
	if reportErr != nil {
		message = reportErr.Error()
	}
	return ps.getDB(ctx).Model(&models.VCSReportTarget{}).Where("report_target_id = ?", targetID).Updates(map[string]interface{}{"dirty": true, "last_error": message}).Error
}

// BumpVCSReportRevisionForPR marks an existing report target dirty after a
// retry or cancel changes state before the normal status publisher runs.
func (ps PostgresDbStore) BumpVCSReportRevisionForPR(ctx context.Context, orgID string, projectID *string, repository string, prNumber int) error {
	query := ps.getDB(ctx).Model(&models.VCSReportTarget{}).
		Where("target_type = ? AND external_target_id = ?", "pull_request", fmt.Sprintf("%d", prNumber))
	if projectID != nil && *projectID != "" {
		query = query.Where("project_id = ?", *projectID)
	} else {
		query = query.Where("org_id = ? AND repository = ?", orgID, repository)
	}
	return query.Updates(map[string]interface{}{"desired_revision": gorm.Expr("desired_revision + 1"), "dirty": true}).Error
}
