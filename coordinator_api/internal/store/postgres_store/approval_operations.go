package postgres_store

import (
	"context"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (ps PostgresDbStore) CreateCIApproval(ctx context.Context, approval *models.CIApproval) error {
	if approval == nil {
		return store.ErrInvalidInput
	}
	if err := approval.Validate(); err != nil {
		return fmt.Errorf("invalid CI approval: %w", err)
	}
	result := ps.getDB(ctx).Clauses(clause.OnConflict{DoNothing: true}, clause.Returning{}).Create(approval)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return store.ErrAlreadyExists
	}
	return nil
}

func (ps PostgresDbStore) GetCIApprovalByID(ctx context.Context, approvalID string) (*models.CIApproval, error) {
	var approval models.CIApproval
	if err := ps.getDB(ctx).Where("approval_id = ?", approvalID).First(&approval).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &approval, nil
}

func (ps PostgresDbStore) FindValidCIApprovals(ctx context.Context, projectID string, prNumber int, headSHA, baseSHA, revision, workflow string, now time.Time) ([]models.CIApproval, error) {
	var approvals []models.CIApproval
	err := ps.getDB(ctx).Where(`project_id = ? AND pr_number = ? AND head_sha = ? AND base_sha = ?
		AND policy_revision = ? AND workflow_scope IN (?, '*') AND invalidated_at IS NULL
		AND (expires_at IS NULL OR expires_at > ?)`, projectID, prNumber, headSHA, baseSHA, revision, workflow, now).Find(&approvals).Error
	return approvals, err
}

// ListActiveCIApprovalsForTarget returns candidate approvals for evaluation.
// The evaluator binds each candidate to the trusted policy revision, workflow,
// and execution profile before it can grant authority.
func (ps PostgresDbStore) ListActiveCIApprovalsForTarget(ctx context.Context, projectID string, prNumber int, headSHA, baseSHA string, now time.Time) ([]models.CIApproval, error) {
	var approvals []models.CIApproval
	err := ps.getDB(ctx).Where(`project_id = ? AND pr_number = ? AND head_sha = ? AND base_sha = ?
		AND invalidated_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`,
		projectID, prNumber, headSHA, baseSHA, now).Find(&approvals).Error
	return approvals, err
}

func (ps PostgresDbStore) InvalidateCIApprovalsForNewHead(ctx context.Context, projectID string, prNumber int, headSHA string) (int64, error) {
	result := ps.getDB(ctx).Model(&models.CIApproval{}).
		Where("project_id = ? AND pr_number = ? AND head_sha <> ? AND invalidated_at IS NULL", projectID, prNumber, headSHA).
		Update("invalidated_at", time.Now().UTC())
	return result.RowsAffected, result.Error
}
