package postgres_store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Reporting and ownership storage (migration 000056,
// docs/reporting-and-ownership.md).

var _ store.ReportingStore = PostgresDbStore{}

func (ps PostgresDbStore) GetOrgReportingSettings(ctx context.Context, orgID string) (*models.OrgReportingSettings, error) {
	var settings models.OrgReportingSettings
	result := ps.getDB(ctx).Where("org_id = ?", orgID).Limit(1).Find(&settings)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get organization reporting settings: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	if settings.PathOwners == nil {
		settings.PathOwners = []models.PathOwnerRule{}
	}
	if settings.AgreementContexts == nil {
		settings.AgreementContexts = pq.StringArray{}
	}
	return &settings, nil
}

func (ps PostgresDbStore) GetProjectReportingSettings(ctx context.Context, projectID string) (*models.ProjectReportingSettings, error) {
	var settings models.ProjectReportingSettings
	result := ps.getDB(ctx).Where("project_id = ?", projectID).Limit(1).Find(&settings)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get project reporting settings: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &settings, nil
}

func (ps PostgresDbStore) UpsertOrgReportingSettings(ctx context.Context, settings *models.OrgReportingSettings) error {
	settings.UpdatedAt = time.Now().UTC()
	if settings.PathOwners == nil {
		settings.PathOwners = []models.PathOwnerRule{}
	}
	if settings.AgreementContexts == nil {
		settings.AgreementContexts = pq.StringArray{}
	}
	err := ps.getDB(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "org_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"report_mode", "agreement_contexts", "codeowners_import",
			"codeowners_cache_ttl_seconds", "path_acceptance", "path_owners", "updated_at"})}).Create(settings).Error
	if err != nil {
		return fmt.Errorf("failed to store organization reporting settings: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) UpsertProjectReportingSettings(ctx context.Context, settings *models.ProjectReportingSettings) error {
	settings.UpdatedAt = time.Now().UTC()
	// Explicit assignments: a nil pointer must store NULL (inherit), and GORM
	// skips nil fields in an ordinary update.
	values := map[string]any{
		"project_id": settings.ProjectID, "org_id": settings.OrgID, "updated_at": settings.UpdatedAt,
		"report_mode": settings.ReportMode, "codeowners_import": settings.CodeownersImport,
		"codeowners_cache_ttl_seconds": settings.CodeownersCacheTTLSeconds, "path_acceptance": settings.PathAcceptance,
		"agreement_contexts": nil, "path_owners": nil,
	}
	if settings.AgreementContexts != nil {
		values["agreement_contexts"] = pq.StringArray(append([]string{}, (*settings.AgreementContexts)...))
	}
	if settings.PathOwners != nil {
		encoded, err := reportingJSONText(*settings.PathOwners)
		if err != nil {
			return err
		}
		values["path_owners"] = gorm.Expr("?::jsonb", encoded)
	}
	columns := []string{"org_id", "report_mode", "agreement_contexts", "codeowners_import",
		"codeowners_cache_ttl_seconds", "path_acceptance", "path_owners", "updated_at"}
	err := ps.getDB(ctx).Model(&models.ProjectReportingSettings{}).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "project_id"}}, DoUpdates: clause.AssignmentColumns(columns)}).Create(values).Error
	if err != nil {
		return fmt.Errorf("failed to store project reporting settings: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) RecordCIAgreementResult(ctx context.Context, row *models.CIAgreement) error {
	now := time.Now().UTC()
	if row.ReactorcideCompletedAt.IsZero() {
		row.ReactorcideCompletedAt = now
	}
	row.Status = models.AgreementStatusPending
	row.Attempts = 0
	row.NextCheckAt = &now
	row.Agree = nil
	row.OtherResult = ""
	row.ObservedAt = nil
	row.LastError = ""
	if row.OtherResults == nil {
		row.OtherResults = []models.AgreementOtherResult{}
	}
	row.CreatedAt, row.UpdatedAt = now, now
	// The same result of the same event again (a re-publish) keeps the
	// comparison that is in progress.
	err := ps.getDB(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "project_id"}, {Name: "commit_sha"}},
		Where: clause.Where{Exprs: []clause.Expression{gorm.Expr(
			"ci_agreement.reactorcide_result IS DISTINCT FROM EXCLUDED.reactorcide_result OR ci_agreement.event_id IS DISTINCT FROM EXCLUDED.event_id")}},
		DoUpdates: clause.AssignmentColumns([]string{"org_id", "event_id", "provider", "repository", "pr_number", "branch",
			"reactorcide_result", "reactorcide_description", "other_results", "other_result", "agree", "status", "attempts",
			"next_check_at", "last_error", "reactorcide_completed_at", "observed_at", "updated_at"}),
	}).Create(row).Error
	if err != nil {
		return fmt.Errorf("failed to record the agreement result: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) ListDueCIAgreements(ctx context.Context, now time.Time, limit int) ([]models.CIAgreement, error) {
	if limit <= 0 {
		limit = 50
	}
	rows := []models.CIAgreement{}
	err := ps.getDB(ctx).Where("status = ? AND next_check_at IS NOT NULL AND next_check_at <= ?", models.AgreementStatusPending, now).
		Order("next_check_at").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list due agreements: %w", err)
	}
	return rows, nil
}

func (ps PostgresDbStore) UpdateCIAgreementObservation(ctx context.Context, row *models.CIAgreement) error {
	if row.OtherResults == nil {
		row.OtherResults = []models.AgreementOtherResult{}
	}
	encoded, err := reportingJSONText(row.OtherResults)
	if err != nil {
		return err
	}
	result := ps.getDB(ctx).Model(&models.CIAgreement{}).
		Where("project_id = ? AND commit_sha = ? AND reactorcide_completed_at = ?", row.ProjectID, row.CommitSHA, row.ReactorcideCompletedAt).
		Updates(map[string]any{"other_results": gorm.Expr("?::jsonb", encoded), "other_result": row.OtherResult, "agree": row.Agree,
			"status": row.Status, "attempts": row.Attempts, "next_check_at": row.NextCheckAt, "last_error": row.LastError,
			"observed_at": row.ObservedAt, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("failed to update the agreement: %w", result.Error)
	}
	return nil
}

func (ps PostgresDbStore) ListCIAgreements(ctx context.Context, projectID string, since *time.Time, limit int) ([]models.CIAgreement, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	query := ps.getDB(ctx).Where("project_id = ?", projectID)
	if since != nil {
		query = query.Where("reactorcide_completed_at >= ?", *since)
	}
	rows := []models.CIAgreement{}
	if err := query.Order("reactorcide_completed_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list agreements: %w", err)
	}
	return rows, nil
}

func (ps PostgresDbStore) CreatePathAcceptance(ctx context.Context, row *models.PathAcceptance) error {
	now := time.Now().UTC()
	row.CreatedAt, row.UpdatedAt = now, now
	// A savepoint: a duplicate must not abort the transaction of the caller.
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error { return tx.Create(row).Error })
	if err != nil {
		if isUniqueViolation(err) {
			return store.ErrAlreadyExists
		}
		return fmt.Errorf("failed to create path acceptance: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) GetPathAcceptance(ctx context.Context, acceptanceID string) (*models.PathAcceptance, error) {
	var row models.PathAcceptance
	result := ps.getDB(ctx).Where("acceptance_id = ?", acceptanceID).Limit(1).Find(&row)
	if result.Error != nil {
		if strings.Contains(result.Error.Error(), "invalid input syntax for type uuid") {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get path acceptance: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, store.ErrNotFound
	}
	return &row, nil
}

func (ps PostgresDbStore) ListPathAcceptances(ctx context.Context, projectID, state string) ([]models.PathAcceptance, error) {
	query := ps.getDB(ctx).Where("project_id = ?", projectID)
	if state != "" {
		query = query.Where("state = ?", state)
	}
	rows := []models.PathAcceptance{}
	if err := query.Order("created_at DESC").Limit(1000).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list path acceptances: %w", err)
	}
	return rows, nil
}

func (ps PostgresDbStore) TransitionPathAcceptance(ctx context.Context, row *models.PathAcceptance, from ...string) error {
	if len(from) == 0 {
		return errors.New("a transition needs at least one from state")
	}
	row.UpdatedAt = time.Now().UTC()
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.PathAcceptance{}).Where("acceptance_id = ? AND state IN ?", row.AcceptanceID, from).
			Updates(map[string]any{"state": row.State, "accepted_by": row.AcceptedBy, "accepted_at": row.AcceptedAt,
				"revoked_by": row.RevokedBy, "revoked_at": row.RevokedAt, "decision_reason": row.DecisionReason,
				"updated_at": row.UpdatedAt})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return store.ErrConflict
		}
		return nil
	})
	if errors.Is(err, store.ErrConflict) {
		return err
	}
	if err != nil {
		if isUniqueViolation(err) {
			return store.ErrAlreadyExists
		}
		return fmt.Errorf("failed to change path acceptance: %w", err)
	}
	return nil
}

// jsonText encodes a value for a jsonb column.
func reportingJSONText(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("failed to encode a JSON value: %w", err)
	}
	return string(encoded), nil
}
