package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/ctxkey"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// Inbound federation (docs/federation.md). An org admin registers trusted
// issuers and claim mappings. The token exchange reads them.

func (ps PostgresDbStore) CreateFederationIssuer(ctx context.Context, issuer *models.FederationIssuer) error {
	if !isValidUUID(issuer.OrgID) {
		return store.ErrInvalidInput
	}
	if err := ps.getDB(ctx).Create(issuer).Error; err != nil {
		if isUniqueViolation(err) {
			return store.ErrAlreadyExists
		}
		return fmt.Errorf("failed to create federation issuer: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) UpdateFederationIssuer(ctx context.Context, issuer *models.FederationIssuer) error {
	result := ps.getDB(ctx).Model(&models.FederationIssuer{}).
		Where("issuer_id = ? AND org_id = ?", issuer.IssuerID, issuer.OrgID).
		Updates(map[string]any{
			"issuer_url": issuer.IssuerURL, "jwks_url": issuer.JWKSURL, "audiences": issuer.Audiences,
			"algorithms": issuer.Algorithms, "clock_skew_seconds": issuer.ClockSkewSeconds, "status": issuer.Status,
			"updated_at": gorm.Expr("timezone('utc', now())"),
		})
	if result.Error != nil {
		if isUniqueViolation(result.Error) {
			return store.ErrAlreadyExists
		}
		return fmt.Errorf("failed to update federation issuer: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) ListFederationIssuers(ctx context.Context, orgID string) ([]models.FederationIssuer, error) {
	var rows []models.FederationIssuer
	if !isValidUUID(orgID) {
		return rows, nil
	}
	if err := ps.getDB(ctx).Where("org_id = ?", orgID).Order("name").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list federation issuers: %w", err)
	}
	return rows, nil
}

// GetFederationIssuer returns an issuer of the org by name or by UUID.
func (ps PostgresDbStore) GetFederationIssuer(ctx context.Context, orgID, nameOrID string) (*models.FederationIssuer, error) {
	if !isValidUUID(orgID) {
		return nil, store.ErrNotFound
	}
	var issuer models.FederationIssuer
	query := ps.getDB(ctx).Where("org_id = ?", orgID)
	if isValidUUID(nameOrID) {
		query = query.Where("issuer_id = ? OR name = ?", nameOrID, nameOrID)
	} else {
		query = query.Where("name = ?", nameOrID)
	}
	if err := query.First(&issuer).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get federation issuer: %w", err)
	}
	return &issuer, nil
}

// GetFederationIssuerByURL returns the issuer of the org with the exact
// issuer URL.
func (ps PostgresDbStore) GetFederationIssuerByURL(ctx context.Context, orgID, issuerURL string) (*models.FederationIssuer, error) {
	if !isValidUUID(orgID) {
		return nil, store.ErrNotFound
	}
	var issuer models.FederationIssuer
	if err := ps.getDB(ctx).Where("org_id = ? AND issuer_url = ?", orgID, issuerURL).First(&issuer).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get federation issuer: %w", err)
	}
	return &issuer, nil
}

func (ps PostgresDbStore) DeleteFederationIssuer(ctx context.Context, orgID, issuerID string) error {
	result := ps.getDB(ctx).Where("org_id = ? AND issuer_id = ?", orgID, issuerID).Delete(&models.FederationIssuer{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete federation issuer: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) CreateFederationMapping(ctx context.Context, mapping *models.FederationMapping) error {
	if err := ps.getDB(ctx).Create(mapping).Error; err != nil {
		if isUniqueViolation(err) {
			return store.ErrAlreadyExists
		}
		return fmt.Errorf("failed to create federation mapping: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) UpdateFederationMapping(ctx context.Context, mapping *models.FederationMapping) error {
	mapping.UpdatedAt = time.Now().UTC()
	result := ps.getDB(ctx).Model(&models.FederationMapping{}).
		Where("mapping_id = ? AND org_id = ?", mapping.MappingID, mapping.OrgID).
		Select("principal_id", "conditions", "capabilities", "token_ttl_seconds", "status", "updated_at").
		Updates(mapping)
	if result.Error != nil {
		return fmt.Errorf("failed to update federation mapping: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ListFederationMappings returns the mappings of the org. An issuerID limits
// the list to one issuer.
func (ps PostgresDbStore) ListFederationMappings(ctx context.Context, orgID, issuerID string) ([]models.FederationMapping, error) {
	var rows []models.FederationMapping
	if !isValidUUID(orgID) {
		return rows, nil
	}
	query := ps.getDB(ctx).Where("org_id = ?", orgID)
	if issuerID != "" {
		if !isValidUUID(issuerID) {
			return rows, nil
		}
		query = query.Where("issuer_id = ?", issuerID)
	}
	if err := query.Order("name").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list federation mappings: %w", err)
	}
	return rows, nil
}

// GetFederationMapping returns a mapping of the issuer by name or UUID.
func (ps PostgresDbStore) GetFederationMapping(ctx context.Context, orgID, issuerID, nameOrID string) (*models.FederationMapping, error) {
	if !isValidUUID(orgID) || !isValidUUID(issuerID) {
		return nil, store.ErrNotFound
	}
	var mapping models.FederationMapping
	query := ps.getDB(ctx).Where("org_id = ? AND issuer_id = ?", orgID, issuerID)
	if isValidUUID(nameOrID) {
		query = query.Where("mapping_id = ? OR name = ?", nameOrID, nameOrID)
	} else {
		query = query.Where("name = ?", nameOrID)
	}
	if err := query.First(&mapping).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get federation mapping: %w", err)
	}
	return &mapping, nil
}

func (ps PostgresDbStore) DeleteFederationMapping(ctx context.Context, orgID, mappingID string) error {
	result := ps.getDB(ctx).Where("org_id = ? AND mapping_id = ?", orgID, mappingID).Delete(&models.FederationMapping{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete federation mapping: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// CreateFederatedToken creates the token of a token exchange, its record,
// and the federated identity of the subject, in one transaction.
//
// The identity issuer|subject goes to the mapped principal. An identity that
// federation made for another service principal of the same organization
// moves (the mapping changed). An identity of a principal of another
// organization, or one that federation did not make, stays where it is: the
// token is bound to the principal through api_tokens either way.
func (ps PostgresDbStore) CreateFederatedToken(ctx context.Context, token *models.APIToken, record *models.FederationIssuedToken, identityValue string) error {
	if token.PrincipalID == nil || token.OwnerOrgID == nil {
		return store.ErrInvalidInput
	}
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := context.WithValue(ctx, ctxkey.TxKey(), tx)
		if err := ps.CreateAPIToken(txCtx, token); err != nil {
			return err
		}
		record.TokenID = token.TokenID
		if err := tx.Create(record).Error; err != nil {
			return fmt.Errorf("failed to record federated token: %w", err)
		}
		if identityValue == "" {
			return nil
		}
		var existing models.PrincipalIdentity
		err := tx.Where("identity_type = ? AND identity_value = ?", models.IdentityTypeFederated, identityValue).First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return tx.Create(&models.PrincipalIdentity{IdentityType: models.IdentityTypeFederated, IdentityValue: identityValue,
				PrincipalID: *token.PrincipalID, VerifiedBy: models.IdentityVerifiedByFederation}).Error
		case err != nil:
			return err
		case existing.PrincipalID == *token.PrincipalID || existing.VerifiedBy != models.IdentityVerifiedByFederation:
			return nil
		}
		var owner models.Principal
		if err := tx.Where("principal_id = ?", existing.PrincipalID).First(&owner).Error; err != nil {
			return nil
		}
		if owner.Kind == models.PrincipalKindService && owner.OrgID != nil && *owner.OrgID == *token.OwnerOrgID {
			return tx.Model(&models.PrincipalIdentity{}).
				Where("identity_type = ? AND identity_value = ?", models.IdentityTypeFederated, identityValue).
				Update("principal_id", *token.PrincipalID).Error
		}
		return nil
	})
}

// DeleteExpiredFederatedTokens removes exchange tokens that expired before
// the given time, and their token identities. It returns the count.
func (ps PostgresDbStore) DeleteExpiredFederatedTokens(ctx context.Context, before time.Time) (int64, error) {
	var count int64
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []string
		if err := tx.Model(&models.FederationIssuedToken{}).Where("expires_at < ?", before.UTC()).
			Limit(500).Pluck("token_id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Where("identity_type = ? AND identity_value IN ?", models.IdentityTypeToken, ids).
			Delete(&models.PrincipalIdentity{}).Error; err != nil {
			return err
		}
		result := tx.Where("token_id IN ?", ids).Delete(&models.APIToken{})
		count = result.RowsAffected
		return result.Error
	})
	return count, err
}

// CountActiveFederatedTokens returns the exchange tokens of a principal that
// have not expired. The UI shows it.
func (ps PostgresDbStore) CountActiveFederatedTokens(ctx context.Context, principalID string) (int64, error) {
	var count int64
	err := ps.getDB(ctx).Table("federation_issued_tokens f").
		Joins("JOIN api_tokens t ON t.token_id = f.token_id").
		Where("t.principal_id = ? AND f.expires_at > timezone('utc', now()) AND t.revoked_at IS NULL", principalID).
		Count(&count).Error
	return count, err
}
