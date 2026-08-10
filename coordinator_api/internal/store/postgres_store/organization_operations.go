package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (ps PostgresDbStore) CreateOrganization(ctx context.Context, organization *models.Organization) error {
	name, err := models.NormalizeOrganizationName(organization.Name)
	if err != nil {
		return err
	}
	organization.Name = name
	if organization.Status == "" {
		organization.Status = models.OrganizationStatusActive
	}
	if err := models.ValidateOrganizationStatus(organization.Status); err != nil {
		return err
	}

	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize organization creation so two first creates cannot select
		// different defaults.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(191924761, 1)").Error; err != nil {
			return fmt.Errorf("failed to lock organization creation: %w", err)
		}
		if err := tx.Create(organization).Error; err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("organization %q already exists: %w", name, store.ErrAlreadyExists)
			}
			return fmt.Errorf("failed to create organization: %w", err)
		}
		if err := seedBuiltInProfiles(tx, organization.OrgID); err != nil {
			return err
		}
		setting := &models.GlobalSetting{
			Key:       models.GlobalSettingDefaultOrgID,
			Value:     models.JSONValue([]byte(fmt.Sprintf("%q", organization.OrgID))),
			UpdatedAt: time.Now().UTC(),
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(setting).Error; err != nil {
			return fmt.Errorf("failed to set first organization as default: %w", err)
		}
		return nil
	})
}

func seedBuiltInProfiles(tx *gorm.DB, orgID string) error {
	standard := &models.ExecutionProfile{Name: "standard", OrgID: orgID, MayRunAsRoot: true, TrustedCacheWrites: true,
		SecretPathAllowlist: []string{}, ResourceCeilings: models.JSONB{}}
	untrusted := &models.ExecutionProfile{Name: "pr-untrusted", OrgID: orgID, DenySecrets: true, MayRunAsRoot: false,
		SecretPathAllowlist: []string{}, RuntimeCapabilities: []string{"gpu"}, ResourceCeilings: models.JSONB{}, TrustedCacheWrites: false}
	if err := tx.Create(standard).Error; err != nil {
		return fmt.Errorf("failed to seed standard execution profile: %w", err)
	}
	if err := tx.Create(untrusted).Error; err != nil {
		return fmt.Errorf("failed to seed pr-untrusted execution profile: %w", err)
	}
	class := &models.WorkerClass{OrgID: orgID, Name: "default"}
	if err := tx.Create(class).Error; err != nil {
		return fmt.Errorf("failed to seed default worker class: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error) {
	normalized, err := models.NormalizeOrganizationName(name)
	if err != nil {
		return nil, err
	}
	var organization models.Organization
	if err := ps.getDB(ctx).Where("name = ?", normalized).First(&organization).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get organization %q: %w", normalized, err)
	}
	return &organization, nil
}

func (ps PostgresDbStore) GetOrganizationByID(ctx context.Context, orgID string) (*models.Organization, error) {
	if !isValidUUID(orgID) {
		return nil, store.ErrNotFound
	}
	var organization models.Organization
	if err := ps.getDB(ctx).Where("org_id = ?", orgID).First(&organization).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get organization: %w", err)
	}
	return &organization, nil
}

func (ps PostgresDbStore) ListOrganizations(ctx context.Context, limit, offset int) ([]models.Organization, error) {
	var organizations []models.Organization
	query := ps.getDB(ctx).Order("name")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&organizations).Error; err != nil {
		return nil, fmt.Errorf("failed to list organizations: %w", err)
	}
	return organizations, nil
}

func (ps PostgresDbStore) UpdateOrganization(ctx context.Context, organization *models.Organization) error {
	if err := models.ValidateOrganizationStatus(organization.Status); err != nil {
		return err
	}
	result := ps.getDB(ctx).Model(&models.Organization{}).Where("org_id = ?", organization.OrgID).Updates(map[string]any{
		"display_name": organization.DisplayName, "is_private": organization.IsPrivate,
		"status": organization.Status, "updated_at": gorm.Expr("timezone('utc', now())"),
	})
	if result.Error != nil {
		return fmt.Errorf("failed to update organization: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) SetDefaultOrganization(ctx context.Context, orgID string) error {
	if _, err := ps.GetOrganizationByID(ctx, orgID); err != nil {
		return err
	}
	return ps.SetGlobalSetting(ctx, models.GlobalSettingDefaultOrgID, models.JSONValue([]byte(fmt.Sprintf("%q", orgID))))
}

func (ps PostgresDbStore) GetDefaultOrganization(ctx context.Context) (*models.Organization, error) {
	var organization models.Organization
	err := ps.getDB(ctx).Raw(`SELECT o.* FROM organizations o JOIN global_settings s
		ON s.key = ? AND trim(both '"' from s.value::text) = o.org_id::text`, models.GlobalSettingDefaultOrgID).Scan(&organization).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get default organization: %w", err)
	}
	if organization.OrgID == "" {
		return nil, store.ErrNotFound
	}
	return &organization, nil
}

func (ps PostgresDbStore) EnsureDefaultOrganization(ctx context.Context) error {
	if _, err := ps.GetDefaultOrganization(ctx); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if existing, err := ps.GetOrganizationByName(ctx, config.DefaultOrgName); err == nil {
		return ps.SetDefaultOrganization(ctx, existing.OrgID)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return ps.CreateOrganization(ctx, &models.Organization{Name: config.DefaultOrgName, DisplayName: "Default", Status: models.OrganizationStatusActive})
}
