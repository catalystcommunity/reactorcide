package postgres_store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"gorm.io/gorm"
)

// ValidateAPIToken validates an API token and returns the token and associated user
func (ps PostgresDbStore) ValidateAPIToken(ctx context.Context, token string) (*models.APIToken, *models.User, error) {
	tokenHash := checkauth.HashAPIToken(token)

	var apiToken models.APIToken
	if err := ps.getDB(ctx).Where("token_hash = ? AND is_active = true AND revoked_at IS NULL", tokenHash).First(&apiToken).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil, store.ErrNotFound
		}
		return nil, nil, fmt.Errorf("failed to validate API token: %w", err)
	}

	// Check if token is expired
	if apiToken.IsExpired() {
		return nil, nil, store.ErrNotFound
	}

	var orgIDs []string
	if err := ps.getDB(ctx).Table("api_token_organizations").Where("token_id = ?", apiToken.TokenID).Pluck("org_id", &orgIDs).Error; err != nil {
		return nil, nil, fmt.Errorf("failed to load API token organizations: %w", err)
	}
	apiToken.OrganizationIDs = orgIDs
	var capabilities []string
	if err := ps.getDB(ctx).Table("api_token_capabilities").Where("token_id = ?", apiToken.TokenID).Pluck("capability", &capabilities).Error; err != nil {
		return nil, nil, fmt.Errorf("failed to load API token capabilities: %w", err)
	}
	apiToken.Capabilities = capabilities

	if apiToken.SubjectType != "user_token" {
		return &apiToken, nil, nil
	}
	if apiToken.UserID == "" {
		return nil, nil, store.ErrNotFound
	}

	// A delegated token always uses the current user state and RBAC rows.
	var user models.User
	if err := ps.getDB(ctx).Where("user_id = ?", apiToken.UserID).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil, store.ErrNotFound
		}
		return nil, nil, fmt.Errorf("failed to load user for API token: %w", err)
	}
	if !user.IsActive() {
		return nil, nil, store.ErrNotFound
	}

	return &apiToken, &user, nil
}

func (ps PostgresDbStore) MintJobToken(ctx context.Context, job *models.Job) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("failed to generate job token: %w", err)
	}
	tokenValue := hex.EncodeToString(raw)
	expiresAt := time.Now().UTC().Add(time.Duration(job.TimeoutSeconds)*time.Second + 15*time.Minute)
	orgID := job.OrgID
	token := &models.APIToken{TokenHash: checkauth.HashAPIToken(tokenValue), Name: "job:" + job.JobID,
		SubjectType: "job_token", OwnerOrgID: &orgID, BoundJobID: &job.JobID,
		ExpiresAt: &expiresAt, IsActive: true, OrganizationIDs: []string{orgID}, Capabilities: []string{tokencaps.JobsSubmit}}
	if err := ps.CreateAPIToken(ctx, token); err != nil {
		return "", err
	}
	return tokenValue, nil
}

func (ps PostgresDbStore) RevokeJobTokens(ctx context.Context, jobID string) error {
	return ps.getDB(ctx).Model(&models.APIToken{}).Where("subject_type = 'job_token' AND bound_job_id = ? AND revoked_at IS NULL", jobID).
		Updates(map[string]any{"is_active": false, "revoked_at": gorm.Expr("timezone('utc', now())")}).Error
}

// CreateAPIToken creates a new API token
func (ps PostgresDbStore) CreateAPIToken(ctx context.Context, apiToken *models.APIToken) error {
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		copyToken := *apiToken
		copyToken.OrganizationIDs = nil
		copyToken.Capabilities = nil
		if err := tx.Create(&copyToken).Error; err != nil {
			return fmt.Errorf("failed to create API token: %w", err)
		}
		apiToken.TokenID = copyToken.TokenID
		for _, orgID := range apiToken.OrganizationIDs {
			if err := tx.Exec("INSERT INTO api_token_organizations (token_id, org_id) VALUES (?, ?)", copyToken.TokenID, orgID).Error; err != nil {
				return fmt.Errorf("failed to add API token organization: %w", err)
			}
		}
		for _, capability := range apiToken.Capabilities {
			if err := tx.Exec("INSERT INTO api_token_capabilities (token_id, capability) VALUES (?, ?)", copyToken.TokenID, capability).Error; err != nil {
				return fmt.Errorf("failed to add API token capability: %w", err)
			}
		}
		return nil
	})
}

// UpdateTokenLastUsed updates the last used timestamp for an API token
func (ps PostgresDbStore) UpdateTokenLastUsed(ctx context.Context, tokenID string, lastUsed time.Time) error {
	result := ps.getDB(ctx).Model(&models.APIToken{}).
		Where("token_id = ?", tokenID).
		Update("last_used_at", lastUsed)

	if result.Error != nil {
		return fmt.Errorf("failed to update token last used: %w", result.Error)
	}

	return nil
}

// GetAPITokensByUser retrieves all API tokens for a user
func (ps PostgresDbStore) GetAPITokensByUser(ctx context.Context, userID string) ([]models.APIToken, error) {
	var tokens []models.APIToken

	if err := ps.getDB(ctx).Where("user_id = ?", userID).
		Order("created_at DESC").
		Find(&tokens).Error; err != nil {
		return nil, fmt.Errorf("failed to get API tokens for user %s: %w", userID, err)
	}

	return tokens, nil
}

// DeleteAPIToken deletes an API token by its ID
func (ps PostgresDbStore) DeleteAPIToken(ctx context.Context, tokenID string) error {
	if !isValidUUID(tokenID) {
		return store.ErrNotFound
	}

	result := ps.getDB(ctx).Where("token_id = ?", tokenID).Delete(&models.APIToken{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete API token %s: %w", tokenID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) GetAPITokenByID(ctx context.Context, tokenID string) (*models.APIToken, error) {
	if !isValidUUID(tokenID) {
		return nil, store.ErrNotFound
	}
	var token models.APIToken
	if err := ps.getDB(ctx).Where("token_id = ?", tokenID).First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get API token: %w", err)
	}
	if err := ps.loadTokenScopes(ctx, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

// ListAPITokens returns one page of API tokens, newest first, and the number
// of tokens that match the filter. The scopes of the page are loaded in two
// queries, not two queries per token.
func (ps PostgresDbStore) ListAPITokens(ctx context.Context, filter store.APITokenListFilter) ([]models.APIToken, int64, error) {
	query := ps.getDB(ctx).Model(&models.APIToken{})
	if !filter.AllOrganizations {
		if len(filter.OrgIDs) == 0 {
			return []models.APIToken{}, 0, nil
		}
		query = query.Where("owner_org_id IN ? OR token_id IN (SELECT token_id FROM api_token_organizations WHERE org_id IN ?)",
			filter.OrgIDs, filter.OrgIDs)
	}
	if filter.ExcludeExpired {
		query = query.Where("expires_at IS NULL OR expires_at > timezone('utc', now())")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count API tokens: %w", err)
	}
	var tokens []models.APIToken
	page := query.Order("created_at DESC").Order("token_id DESC").Offset(filter.Offset)
	if filter.Limit > 0 {
		page = page.Limit(filter.Limit)
	}
	if err := page.Find(&tokens).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list API tokens: %w", err)
	}
	if err := ps.loadTokenScopesBatch(ctx, tokens); err != nil {
		return nil, 0, err
	}
	return tokens, total, nil
}

// loadTokenScopesBatch fills OrganizationIDs and Capabilities of each token.
func (ps PostgresDbStore) loadTokenScopesBatch(ctx context.Context, tokens []models.APIToken) error {
	if len(tokens) == 0 {
		return nil
	}
	ids := make([]string, len(tokens))
	index := make(map[string]int, len(tokens))
	for i := range tokens {
		ids[i] = tokens[i].TokenID
		index[tokens[i].TokenID] = i
	}
	type scopeRow struct {
		TokenID string
		Value   string
	}
	var orgRows []scopeRow
	if err := ps.getDB(ctx).Table("api_token_organizations").Select("token_id, org_id AS value").
		Where("token_id IN ?", ids).Order("org_id").Scan(&orgRows).Error; err != nil {
		return fmt.Errorf("failed to load token organizations: %w", err)
	}
	for _, row := range orgRows {
		if i, ok := index[row.TokenID]; ok {
			tokens[i].OrganizationIDs = append(tokens[i].OrganizationIDs, row.Value)
		}
	}
	var capRows []scopeRow
	if err := ps.getDB(ctx).Table("api_token_capabilities").Select("token_id, capability AS value").
		Where("token_id IN ?", ids).Order("capability").Scan(&capRows).Error; err != nil {
		return fmt.Errorf("failed to load token capabilities: %w", err)
	}
	for _, row := range capRows {
		if i, ok := index[row.TokenID]; ok {
			tokens[i].Capabilities = append(tokens[i].Capabilities, row.Value)
		}
	}
	return nil
}

func (ps PostgresDbStore) loadTokenScopes(ctx context.Context, token *models.APIToken) error {
	if err := ps.getDB(ctx).Table("api_token_organizations").Where("token_id = ?", token.TokenID).Pluck("org_id", &token.OrganizationIDs).Error; err != nil {
		return fmt.Errorf("failed to load token organizations: %w", err)
	}
	if err := ps.getDB(ctx).Table("api_token_capabilities").Where("token_id = ?", token.TokenID).Pluck("capability", &token.Capabilities).Error; err != nil {
		return fmt.Errorf("failed to load token capabilities: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) RevokeAPIToken(ctx context.Context, tokenID string) error {
	if !isValidUUID(tokenID) {
		return store.ErrNotFound
	}
	result := ps.getDB(ctx).Model(&models.APIToken{}).Where("token_id = ?", tokenID).Updates(map[string]any{"is_active": false, "revoked_at": gorm.Expr("timezone('utc', now())")})
	if result.Error != nil {
		return fmt.Errorf("failed to revoke API token: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}
