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

func (ps PostgresDbStore) CreateCIApproval(ctx context.Context, approval *models.CIApproval) error {
	if approval == nil {
		return store.ErrInvalidInput
	}
	if err := approval.Validate(); err != nil {
		return fmt.Errorf("invalid CI approval: %w", err)
	}
	if approval.Subjects == nil {
		approval.Subjects = []string{}
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

func (ps PostgresDbStore) ListCIApprovalsForHead(ctx context.Context, projectID string, prNumber int, headSHA string) ([]models.CIApproval, error) {
	var approvals []models.CIApproval
	err := ps.getDB(ctx).Where(`project_id = ? AND pr_number = ? AND head_sha = ? AND person <> ''
		AND invalidated_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`,
		projectID, prNumber, headSHA, time.Now().UTC()).
		Order("created_at ASC, approval_id ASC").Find(&approvals).Error
	return approvals, err
}

func (ps PostgresDbStore) InvalidateCIApprovalsForNewHead(ctx context.Context, projectID string, prNumber int, headSHA string) (int64, error) {
	result := ps.getDB(ctx).Model(&models.CIApproval{}).
		Where("project_id = ? AND pr_number = ? AND head_sha <> ? AND invalidated_at IS NULL", projectID, prNumber, headSHA).
		Update("invalidated_at", time.Now().UTC())
	return result.RowsAffected, result.Error
}

func (ps PostgresDbStore) GetCITrustSettings(ctx context.Context, orgID string, projectID *string) (*models.CITrustSettings, error) {
	var settings models.CITrustSettings
	query := ps.getDB(ctx).Where("org_id = ?", orgID)
	if projectID == nil {
		query = query.Where("project_id IS NULL")
	} else {
		query = query.Where("project_id = ?", *projectID)
	}
	if err := query.First(&settings).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &settings, nil
}

func (ps PostgresDbStore) PutCITrustSettings(ctx context.Context, settings *models.CITrustSettings) error {
	if settings == nil || settings.OrgID == "" {
		return store.ErrInvalidInput
	}
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("%w: %v", store.ErrInvalidInput, err)
	}
	settings.UpdatedAt = time.Now().UTC()
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		existing := models.CITrustSettings{}
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ?", settings.OrgID)
		if settings.ProjectID == nil {
			query = query.Where("project_id IS NULL")
		} else {
			query = query.Where("project_id = ?", *settings.ProjectID)
		}
		err := query.First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			settings.SettingsID = ""
			return tx.Create(settings).Error
		case err != nil:
			return err
		}
		settings.SettingsID = existing.SettingsID
		// Select("*") writes NULL columns too: a NULL means "inherit".
		return tx.Model(&models.CITrustSettings{}).Where("settings_id = ?", existing.SettingsID).
			Select("run_gate", "gated_run_authority", "run_approvers", "override_approvers", "override_profile",
				"required_reviews", "updated_at", "updated_by").Updates(settings).Error
	})
}

func (ps PostgresDbStore) EffectiveCITrust(ctx context.Context, orgID, projectID string) (models.EffectiveTrustSettings, error) {
	org, err := ps.GetCITrustSettings(ctx, orgID, nil)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return models.EffectiveTrustSettings{}, err
	}
	project, err := ps.GetCITrustSettings(ctx, orgID, &projectID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return models.EffectiveTrustSettings{}, err
	}
	return models.EffectiveTrust(org, project), nil
}

func (ps PostgresDbStore) ListPRHeadUpdateActors(ctx context.Context, projectID string, prNumber int) ([]store.PRHeadUpdate, error) {
	var rows []struct {
		Actor     string
		CommitSHA string
	}
	err := ps.getDB(ctx).Model(&models.CIEvent{}).Select("actor, commit_sha").
		Where(`project_id = ? AND pr_number = ? AND source = ? AND actor <> ''
			AND summary->>'action' IN ('opened', 'synchronize')`, projectID, prNumber, models.CIEventSourceWebhook).
		Order("received_at DESC").Limit(500).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]store.PRHeadUpdate, 0, len(rows))
	for _, row := range rows {
		result = append(result, store.PRHeadUpdate{Actor: row.Actor, HeadSHA: row.CommitSHA})
	}
	return result, nil
}

func (ps PostgresDbStore) ListCIEventsForPRHead(ctx context.Context, projectID string, prNumber int, headSHA string) ([]models.CIEvent, error) {
	var events []models.CIEvent
	err := ps.getDB(ctx).Where("project_id = ? AND pr_number = ? AND commit_sha = ? AND status <> ?",
		projectID, prNumber, headSHA, models.CIEventStatusDuplicate).
		Order("received_at DESC").Limit(100).Find(&events).Error
	return events, err
}
