package postgres_store

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

func refClassScope(db *gorm.DB, orgID string, projectID *string) *gorm.DB {
	db = db.Where("org_id = ?", orgID)
	if projectID == nil || *projectID == "" {
		return db.Where("project_id IS NULL")
	}
	return db.Where("project_id = ?", *projectID)
}

func (ps PostgresDbStore) ListRefClassRules(ctx context.Context, orgID string, projectID *string) ([]models.RefClassRule, error) {
	var rules []models.RefClassRule
	if err := refClassScope(ps.getDB(ctx), orgID, projectID).Order("position, name").Find(&rules).Error; err != nil {
		return nil, fmt.Errorf("failed to list ref class rules: %w", err)
	}
	return rules, nil
}

func (ps PostgresDbStore) ListEffectiveRefClassRules(ctx context.Context, orgID string, projectID *string) ([]models.RefClassRule, error) {
	var rules []models.RefClassRule
	query := ps.getDB(ctx).Where("org_id = ?", orgID)
	if projectID == nil || *projectID == "" {
		query = query.Where("project_id IS NULL")
	} else {
		query = query.Where("project_id IS NULL OR project_id = ?", *projectID)
	}
	if err := query.Find(&rules).Error; err != nil {
		return nil, fmt.Errorf("failed to list ref class rules: %w", err)
	}
	refclass.SortRules(rules)
	return rules, nil
}

func (ps PostgresDbStore) GetRefClassRule(ctx context.Context, orgID string, projectID *string, name string) (*models.RefClassRule, error) {
	var rule models.RefClassRule
	if err := refClassScope(ps.getDB(ctx), orgID, projectID).Where("name = ?", name).First(&rule).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get ref class rule: %w", err)
	}
	return &rule, nil
}

func (ps PostgresDbStore) UpsertRefClassRule(ctx context.Context, rule *models.RefClassRule) error {
	if rule.ProjectID != nil && *rule.ProjectID == "" {
		rule.ProjectID = nil
	}
	rule.Classes = refclass.Normalize(rule.Classes)
	if rule.EventTypes == nil {
		rule.EventTypes = []string{}
	}
	if err := refclass.ValidateRule(*rule); err != nil {
		return fmt.Errorf("%w: %v", store.ErrInvalidInput, err)
	}
	existing, err := ps.GetRefClassRule(ctx, rule.OrgID, rule.ProjectID, rule.Name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	db := ps.getDB(ctx)
	if existing == nil {
		if err := db.Create(rule).Error; err != nil {
			if isUniqueViolation(err) {
				return store.ErrAlreadyExists
			}
			return fmt.Errorf("failed to create ref class rule: %w", err)
		}
		return db.Where("rule_id = ?", rule.RuleID).First(rule).Error
	}
	rule.RuleID = existing.RuleID
	rule.CreatedAt = existing.CreatedAt
	result := db.Model(&models.RefClassRule{}).Where("rule_id = ?", existing.RuleID).Updates(map[string]any{
		"position": rule.Position, "description": rule.Description, "event_types": rule.EventTypes,
		"branch_glob": rule.BranchGlob, "tag_glob": rule.TagGlob, "base_branch_glob": rule.BaseBranchGlob,
		"is_pr": rule.IsPR, "is_fork": rule.IsFork, "is_default_branch": rule.IsDefaultBranch,
		"classes": rule.Classes, "enabled": rule.Enabled, "updated_at": gorm.Expr("timezone('utc', now())"),
	})
	if result.Error != nil {
		return fmt.Errorf("failed to update ref class rule: %w", result.Error)
	}
	return db.Where("rule_id = ?", existing.RuleID).First(rule).Error
}

func (ps PostgresDbStore) DeleteRefClassRule(ctx context.Context, orgID string, projectID *string, name string) error {
	result := refClassScope(ps.getDB(ctx), orgID, projectID).Where("name = ?", name).Delete(&models.RefClassRule{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete ref class rule: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// refreshRefClasses reads the classes that the database trigger gave a new
// row (inherited from its parent job, workflow, or event), so the caller's
// struct holds them.
func refreshRefClasses(db *gorm.DB, table, keyColumn, key string, classes *pq.StringArray) {
	if len(*classes) > 0 || key == "" {
		return
	}
	var value pq.StringArray
	if err := db.Raw("SELECT ref_classes FROM "+table+" WHERE "+keyColumn+" = ?", key).Row().Scan(&value); err == nil {
		*classes = value
	}
}
