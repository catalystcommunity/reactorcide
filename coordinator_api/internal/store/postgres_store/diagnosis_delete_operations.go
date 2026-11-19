package postgres_store

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ store.DiagnosisDeleteStore = PostgresDbStore{}

func (ps PostgresDbStore) DeleteDiagnosisReport(ctx context.Context, jobID, workflowID, authorPrincipalID string, entryKey func(*models.DiagnosisReport) string) (*models.DiagnosisReport, error) {
	if !isValidUUID(authorPrincipalID) {
		return nil, store.ErrNotFound
	}
	var condition string
	var targetID string
	switch {
	case jobID != "" && isValidUUID(jobID):
		condition, targetID = "target_type = 'job' AND job_id = ?", jobID
	case jobID == "" && workflowID != "" && isValidUUID(workflowID):
		condition, targetID = "target_type = 'workflow' AND workflow_id = ?", workflowID
	default:
		return nil, store.ErrNotFound
	}
	var removed []models.DiagnosisReport
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.Returning{}).Where("author_principal_id = ?", authorPrincipalID).
			Where(condition, targetID).Delete(&removed)
		if result.Error != nil {
			return result.Error
		}
		if len(removed) == 0 {
			return store.ErrNotFound
		}
		if entryKey == nil {
			return nil
		}
		// The pull request section goes away at the next render of each
		// report that has it.
		key := entryKey(&removed[0])
		var targets []string
		if err := tx.Model(&models.VCSReportEntry{}).Where("entry_key = ?", key).Distinct().Pluck("report_target_id", &targets).Error; err != nil {
			return err
		}
		if len(targets) == 0 {
			return nil
		}
		if err := tx.Where("entry_key = ?", key).Delete(&models.VCSReportEntry{}).Error; err != nil {
			return err
		}
		return tx.Model(&models.VCSReportTarget{}).Where("report_target_id IN ?", targets).
			Updates(map[string]interface{}{"desired_revision": gorm.Expr("desired_revision + 1"), "dirty": true}).Error
	})
	if err != nil {
		if err == store.ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("delete diagnosis entry: %w", err)
	}
	return &removed[0], nil
}
