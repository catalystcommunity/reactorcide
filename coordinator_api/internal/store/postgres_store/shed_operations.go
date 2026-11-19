package postgres_store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ApplyOrganizationDefaultsProfile implements store.DefaultsProfileStore.
func (ps PostgresDbStore) ApplyOrganizationDefaultsProfile(ctx context.Context, orgID, profile, updatedBy string) error {
	if !models.IsValidDefaultsProfile(profile) {
		return fmt.Errorf("defaults profile must be legacy or secure: %w", store.ErrInvalidInput)
	}
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		return applyDefaultsProfile(tx, orgID, profile, updatedBy)
	})
}

// applyDefaultsProfile writes the profile and its settings in tx.
func applyDefaultsProfile(tx *gorm.DB, orgID, profile, updatedBy string) error {
	settings := models.SettingsForDefaultsProfile(profile)
	result := tx.Model(&models.Organization{}).Where("org_id = ?", orgID).Updates(map[string]any{
		"defaults_profile": profile,
		"dynamic_jobs":     settings.DynamicJobs,
		"updated_at":       gorm.Expr("timezone('utc', now())"),
	})
	if result.Error != nil {
		return fmt.Errorf("failed to apply defaults profile: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	// The "standard" execution profile gets the cache write classes of the
	// profile. Other profiles keep their values.
	if err := tx.Model(&models.ExecutionProfile{}).Where("org_id = ? AND name = ?", orgID, "standard").
		Update("cache_write_classes", pq.StringArray(append([]string{}, settings.CacheWriteClasses...))).Error; err != nil {
		return fmt.Errorf("failed to write cache write classes: %w", err)
	}
	// The organization trust row keeps its other values; only run_gate
	// changes. A missing row is created with run_gate only.
	var existing models.CITrustSettings
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("org_id = ? AND project_id IS NULL", orgID).First(&existing).Error
	runGate := pq.StringArray(append([]string{}, settings.RunGate...))
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		row := &models.CITrustSettings{OrgID: orgID, RunGate: runGate, UpdatedAt: time.Now().UTC(), UpdatedBy: updatedBy}
		if err := tx.Create(row).Error; err != nil {
			return fmt.Errorf("failed to write run gate: %w", err)
		}
	case err != nil:
		return fmt.Errorf("failed to read trust settings: %w", err)
	default:
		if err := tx.Model(&models.CITrustSettings{}).Where("settings_id = ?", existing.SettingsID).
			Updates(map[string]any{"run_gate": runGate, "updated_at": time.Now().UTC(), "updated_by": updatedBy}).Error; err != nil {
			return fmt.Errorf("failed to write run gate: %w", err)
		}
	}
	return nil
}

// EffectiveDraftPRs implements store.DraftShedStore.
func (ps PostgresDbStore) EffectiveDraftPRs(ctx context.Context, projectID string) (string, error) {
	if !isValidUUID(projectID) {
		return "", store.ErrNotFound
	}
	var row struct {
		ProjectSetting string
		OrgSetting     *string
	}
	err := ps.getDB(ctx).Raw(`SELECT p.draft_prs AS project_setting, o.draft_prs AS org_setting
		FROM projects p LEFT JOIN organizations o ON o.org_id = p.org_id
		WHERE p.project_id = ?`, projectID).Scan(&row).Error
	if err != nil {
		return "", fmt.Errorf("reading draft_prs setting: %w", err)
	}
	orgSetting := ""
	if row.OrgSetting != nil {
		orgSetting = *row.OrgSetting
	}
	return models.EffectiveDraftPRs(row.ProjectSetting, orgSetting), nil
}

// SetOrganizationDraftPRs implements store.DraftShedStore.
func (ps PostgresDbStore) SetOrganizationDraftPRs(ctx context.Context, orgID, value string) error {
	if !models.IsValidDraftPRsOrgSetting(value) {
		return fmt.Errorf("draft_prs must be run, skip or skip_when_shedding: %w", store.ErrInvalidInput)
	}
	return ps.updateOrganizationColumns(ctx, orgID, map[string]any{"draft_prs": value})
}

func (ps PostgresDbStore) globalShedLevel(ctx context.Context) (string, error) {
	setting, err := ps.GetGlobalSetting(ctx, models.GlobalSettingShedLevel)
	if errors.Is(err, store.ErrNotFound) {
		return models.ShedLevelNormal, nil
	}
	if err != nil {
		return "", err
	}
	var level string
	if err := json.Unmarshal(setting.Value, &level); err != nil || !models.IsValidShedLevel(level) {
		// A value that is not a level must not stop work.
		return models.ShedLevelNormal, nil
	}
	return level, nil
}

// GetShedState implements store.DraftShedStore.
func (ps PostgresDbStore) GetShedState(ctx context.Context, orgID string) (models.ShedState, error) {
	global, err := ps.globalShedLevel(ctx)
	if err != nil {
		return models.ShedState{}, err
	}
	if orgID == "" {
		state := models.EffectiveShedState(global, models.ShedLevelNormal, nil)
		state.OrgLevel = ""
		if global != models.ShedLevelNormal {
			state.Source = "global"
		}
		return state, nil
	}
	if !isValidUUID(orgID) {
		return models.ShedState{}, store.ErrNotFound
	}
	var org models.Organization
	if err := ps.getDB(ctx).Select("org_id", "shed_level", "shed_actions").Where("org_id = ?", orgID).First(&org).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.ShedState{}, store.ErrNotFound
		}
		return models.ShedState{}, fmt.Errorf("reading organization shed level: %w", err)
	}
	return models.EffectiveShedState(global, org.ShedLevel, org.ShedActions), nil
}

// SetGlobalShedLevel implements store.DraftShedStore.
func (ps PostgresDbStore) SetGlobalShedLevel(ctx context.Context, level string) error {
	if !models.IsValidShedLevel(level) {
		return fmt.Errorf("shed level must be normal, degraded or critical: %w", store.ErrInvalidInput)
	}
	raw, _ := json.Marshal(level)
	return ps.SetGlobalSetting(ctx, models.GlobalSettingShedLevel, models.JSONValue(raw))
}

// SetOrganizationShed implements store.DraftShedStore.
func (ps PostgresDbStore) SetOrganizationShed(ctx context.Context, orgID, level string, actions *models.ShedActionsConfig, clearActions bool) error {
	values := map[string]any{}
	if level != "" {
		if !models.IsValidShedLevel(level) {
			return fmt.Errorf("shed level must be normal, degraded or critical: %w", store.ErrInvalidInput)
		}
		values["shed_level"] = level
	}
	switch {
	case clearActions:
		values["shed_actions"] = gorm.Expr("NULL")
	case actions != nil:
		raw, err := json.Marshal(actions)
		if err != nil {
			return fmt.Errorf("encoding shed actions: %w", err)
		}
		values["shed_actions"] = gorm.Expr("?::jsonb", string(raw))
	}
	if len(values) == 0 {
		return nil
	}
	return ps.updateOrganizationColumns(ctx, orgID, values)
}

func (ps PostgresDbStore) updateOrganizationColumns(ctx context.Context, orgID string, values map[string]any) error {
	if !isValidUUID(orgID) {
		return store.ErrNotFound
	}
	values["updated_at"] = gorm.Expr("timezone('utc', now())")
	result := ps.getDB(ctx).Model(&models.Organization{}).Where("org_id = ?", orgID).Updates(values)
	if result.Error != nil {
		return fmt.Errorf("failed to update organization: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}
