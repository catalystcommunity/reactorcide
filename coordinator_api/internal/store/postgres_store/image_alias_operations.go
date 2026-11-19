package postgres_store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imageref"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Image aliases (migration 000068). See store.ImageAliasStore.

var (
	_ store.ImageAliasStore  = PostgresDbStore{}
	_ store.WorkerImageStore = PostgresDbStore{}
)

func scopeProjectID(projectID *string) *string {
	if projectID == nil || *projectID == "" {
		return nil
	}
	return projectID
}

// exactScope limits a query to one scope: the organization scope when
// projectID is nil, else the project scope.
func exactScope(db *gorm.DB, orgID string, projectID *string) *gorm.DB {
	db = db.Where("org_id = ?", orgID)
	if projectID = scopeProjectID(projectID); projectID == nil {
		return db.Where("project_id IS NULL")
	}
	return db.Where("project_id = ?", *projectID)
}

// visibleScope limits a query to the aliases that a job of projectID can
// see: the organization aliases and, when projectID is set, the project
// aliases.
func visibleScope(db *gorm.DB, orgID string, projectID *string) *gorm.DB {
	db = db.Where("org_id = ?", orgID)
	if projectID = scopeProjectID(projectID); projectID == nil {
		return db.Where("project_id IS NULL")
	}
	return db.Where("(project_id IS NULL OR project_id = ?)", *projectID)
}

func validScopeIDs(orgID string, projectID *string) bool {
	if !isValidUUID(orgID) {
		return false
	}
	if p := scopeProjectID(projectID); p != nil && !isValidUUID(*p) {
		return false
	}
	return true
}

func (ps PostgresDbStore) CreateImageAlias(ctx context.Context, alias *models.ImageAlias) error {
	alias.ProjectID = scopeProjectID(alias.ProjectID)
	if alias.Characteristics == nil {
		alias.Characteristics = characteristics.Characteristics{}
	}
	if err := alias.Validate(); err != nil {
		return fmt.Errorf("%v: %w", err, store.ErrInvalidInput)
	}
	if !validScopeIDs(alias.OrgID, alias.ProjectID) {
		return fmt.Errorf("image alias: org_id or project_id is not a UUID: %w", store.ErrInvalidInput)
	}
	now := time.Now().UTC()
	alias.CreatedAt, alias.UpdatedAt = now, now
	if err := ps.getDB(ctx).Create(alias).Error; err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("image alias %q already exists in this scope: %w", alias.Name, store.ErrAlreadyExists)
		}
		return fmt.Errorf("failed to create image alias: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) UpdateImageAlias(ctx context.Context, alias *models.ImageAlias) error {
	if !isValidUUID(alias.AliasID) {
		return store.ErrNotFound
	}
	if alias.Characteristics == nil {
		alias.Characteristics = characteristics.Characteristics{}
	}
	if err := alias.Validate(); err != nil {
		return fmt.Errorf("%v: %w", err, store.ErrInvalidInput)
	}
	chars, err := json.Marshal(alias.Characteristics)
	if err != nil {
		return fmt.Errorf("image alias %q: characteristics: %v: %w", alias.Name, err, store.ErrInvalidInput)
	}
	now := time.Now().UTC()
	result := ps.getDB(ctx).Model(&models.ImageAlias{}).Where("alias_id = ?", alias.AliasID).Updates(map[string]any{
		"kind":            alias.Kind,
		"os":              alias.OS,
		"arch":            alias.Arch,
		"characteristics": gorm.Expr("?::jsonb", string(chars)),
		"active_ref":      alias.ActiveRef,
		"active_digest":   alias.ActiveDigest,
		"pending_ref":     alias.PendingRef,
		"pending_digest":  alias.PendingDigest,
		"pending_since":   alias.PendingSince,
		"updated_by":      alias.UpdatedBy,
		"updated_at":      now,
	})
	if result.Error != nil {
		return fmt.Errorf("failed to update image alias: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	alias.UpdatedAt = now
	return nil
}

func (ps PostgresDbStore) GetImageAlias(ctx context.Context, orgID string, projectID *string, name string) (*models.ImageAlias, error) {
	if !validScopeIDs(orgID, projectID) || !imageref.IsAliasName(name) {
		return nil, store.ErrNotFound
	}
	var alias models.ImageAlias
	err := exactScope(ps.getDB(ctx), orgID, projectID).Where("name = ?", name).First(&alias).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get image alias: %w", err)
	}
	return &alias, nil
}

func (ps PostgresDbStore) GetImageAliasByID(ctx context.Context, aliasID string) (*models.ImageAlias, error) {
	if !isValidUUID(aliasID) {
		return nil, store.ErrNotFound
	}
	var alias models.ImageAlias
	err := ps.getDB(ctx).Where("alias_id = ?", aliasID).First(&alias).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get image alias: %w", err)
	}
	return &alias, nil
}

func (ps PostgresDbStore) ListImageAliases(ctx context.Context, orgID string, projectID *string) ([]models.ImageAlias, error) {
	if !validScopeIDs(orgID, projectID) {
		return []models.ImageAlias{}, nil
	}
	aliases := []models.ImageAlias{}
	if err := exactScope(ps.getDB(ctx), orgID, projectID).Order("name").Find(&aliases).Error; err != nil {
		return nil, fmt.Errorf("failed to list image aliases: %w", err)
	}
	return aliases, nil
}

func (ps PostgresDbStore) ListOrgImageAliases(ctx context.Context, orgID string) ([]models.ImageAlias, error) {
	if !isValidUUID(orgID) {
		return []models.ImageAlias{}, nil
	}
	aliases := []models.ImageAlias{}
	if err := ps.getDB(ctx).Where("org_id = ?", orgID).
		Order("project_id NULLS FIRST").Order("name").Find(&aliases).Error; err != nil {
		return nil, fmt.Errorf("failed to list image aliases: %w", err)
	}
	return aliases, nil
}

func (ps PostgresDbStore) DeleteImageAlias(ctx context.Context, orgID string, projectID *string, name string) error {
	if !validScopeIDs(orgID, projectID) || !imageref.IsAliasName(name) {
		return store.ErrNotFound
	}
	result := exactScope(ps.getDB(ctx), orgID, projectID).Where("name = ?", name).Delete(&models.ImageAlias{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete image alias: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) ResolveImageAlias(ctx context.Context, orgID string, projectID *string, name string) (*models.ImageAlias, error) {
	if !validScopeIDs(orgID, projectID) || !imageref.IsAliasName(name) {
		return nil, store.ErrNotFound
	}
	var alias models.ImageAlias
	// A project alias sorts before the organization alias of the same name.
	err := visibleScope(ps.getDB(ctx), orgID, projectID).Where("name = ?", name).
		Order("project_id NULLS LAST").First(&alias).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to resolve image alias: %w", err)
	}
	return &alias, nil
}

func (ps PostgresDbStore) ListImageAliasesWithDigest(ctx context.Context, orgID string, projectID *string, digest string, platform models.ImagePlatform) ([]models.ImageAlias, error) {
	aliases := []models.ImageAlias{}
	if !validScopeIDs(orgID, projectID) || !imageref.ValidDigest(digest) {
		return aliases, nil
	}
	query := visibleScope(ps.getDB(ctx), orgID, projectID).
		Where("(active_digest = ? OR pending_digest = ?)", digest, digest).
		Where("kind = ? AND os = ?", platform.Kind, platform.OS)
	if platform.Arch != "" {
		query = query.Where("arch = ?", platform.Arch)
	}
	if err := query.
		Order("project_id NULLS LAST").Order("name").Find(&aliases).Error; err != nil {
		return nil, fmt.Errorf("failed to list image aliases by digest: %w", err)
	}
	return aliases, nil
}

func (ps PostgresDbStore) SetImageAliasPending(ctx context.Context, aliasID, ref, updatedBy string) (*models.ImageAlias, error) {
	parsed, err := imageref.ParsePinned(ref)
	if err != nil {
		return nil, fmt.Errorf("%v: %w", err, store.ErrInvalidInput)
	}
	if !isValidUUID(aliasID) {
		return nil, store.ErrNotFound
	}
	var alias models.ImageAlias
	result := ps.getDB(ctx).Model(&alias).Clauses(clause.Returning{}).
		Where("alias_id = ?", aliasID).
		Updates(map[string]any{
			"pending_ref":    ref,
			"pending_digest": parsed.Digest,
			"pending_since":  time.Now().UTC(),
			"updated_by":     updatedBy,
			"updated_at":     time.Now().UTC(),
		})
	if result.Error != nil {
		return nil, fmt.Errorf("failed to set image alias pending pin: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, store.ErrNotFound
	}
	return &alias, nil
}

func (ps PostgresDbStore) ClearImageAliasPending(ctx context.Context, aliasID, updatedBy string) (*models.ImageAlias, error) {
	if !isValidUUID(aliasID) {
		return nil, store.ErrNotFound
	}
	var alias models.ImageAlias
	result := ps.getDB(ctx).Model(&alias).Clauses(clause.Returning{}).
		Where("alias_id = ?", aliasID).
		Updates(map[string]any{
			"pending_ref":    gorm.Expr("NULL"),
			"pending_digest": gorm.Expr("NULL"),
			"pending_since":  gorm.Expr("NULL"),
			"updated_by":     updatedBy,
			"updated_at":     time.Now().UTC(),
		})
	if result.Error != nil {
		return nil, fmt.Errorf("failed to clear image alias pending pin: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, store.ErrNotFound
	}
	return &alias, nil
}

func (ps PostgresDbStore) PromoteImageAliasPending(ctx context.Context, aliasID, expectedPendingDigest string) (*models.ImageAlias, bool, error) {
	if !isValidUUID(aliasID) {
		return nil, false, store.ErrNotFound
	}
	var alias models.ImageAlias
	result := ps.getDB(ctx).Model(&alias).Clauses(clause.Returning{}).
		Where("alias_id = ? AND pending_digest = ?", aliasID, expectedPendingDigest).
		Updates(map[string]any{
			"active_ref":     gorm.Expr("pending_ref"),
			"active_digest":  gorm.Expr("pending_digest"),
			"pending_ref":    gorm.Expr("NULL"),
			"pending_digest": gorm.Expr("NULL"),
			"pending_since":  gorm.Expr("NULL"),
			"updated_at":     time.Now().UTC(),
		})
	if result.Error != nil {
		return nil, false, fmt.Errorf("failed to promote image alias pending pin: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return &alias, true, nil
	}
	current, err := ps.GetImageAliasByID(ctx, aliasID)
	if err != nil {
		return nil, false, err
	}
	return current, false, nil
}

// --- worker image state -----------------------------------------------------

func (ps PostgresDbStore) SetWorkerImageSet(ctx context.Context, workerID string, version int64, hash string) error {
	if !isValidUUID(workerID) {
		return store.ErrNotFound
	}
	result := ps.getDB(ctx).Model(&models.Worker{}).Where("worker_id = ?", workerID).Updates(map[string]any{
		"image_set_version": version,
		"image_set_hash":    hash,
	})
	if result.Error != nil {
		return fmt.Errorf("failed to set worker image set: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) UpdateWorkerImageState(ctx context.Context, workerID string, state models.WorkerImageState) error {
	if !isValidUUID(workerID) {
		return store.ErrNotFound
	}
	switch state.State {
	case models.WorkerImageStateUpdating, models.WorkerImageStateUpdated, models.WorkerImageStateFailed:
	default:
		return fmt.Errorf("worker image state %q is not valid: %w", state.State, store.ErrInvalidInput)
	}
	digests := state.ReadyDigests
	if digests == nil {
		digests = []string{}
	}
	digestsJSON, err := json.Marshal(digests)
	if err != nil {
		return fmt.Errorf("worker image state: %w", err)
	}
	var detail any = gorm.Expr("NULL")
	if state.Detail != "" {
		detail = state.Detail
	}
	var failed any = gorm.Expr("NULL")
	if len(state.Failed) > 0 {
		failedJSON, err := json.Marshal(state.Failed)
		if err != nil {
			return fmt.Errorf("worker image state: %w", err)
		}
		failed = gorm.Expr("?::jsonb", string(failedJSON))
	}
	result := ps.getDB(ctx).Model(&models.Worker{}).Where("worker_id = ?", workerID).Updates(map[string]any{
		"image_state":         state.State,
		"image_state_version": state.Version,
		"image_state_detail":  detail,
		"image_ready_digests": gorm.Expr("?::jsonb", string(digestsJSON)),
		"image_failed":        failed,
	})
	if result.Error != nil {
		return fmt.Errorf("failed to update worker image state: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) GetWorkerImageState(ctx context.Context, workerID string) (*models.WorkerImageState, error) {
	worker, err := ps.GetWorkerByID(ctx, workerID)
	if err != nil {
		return nil, err
	}
	return WorkerImageStateOf(worker), nil
}

// WorkerImageStateOf returns the recorded image state of a worker row, or
// nil when the worker sent no report.
func WorkerImageStateOf(worker *models.Worker) *models.WorkerImageState {
	if worker == nil || worker.ImageState == nil {
		return nil
	}
	state := &models.WorkerImageState{State: *worker.ImageState, ReadyDigests: worker.ImageReadyDigests, Failed: worker.ImageFailed}
	if worker.ImageStateVersion != nil {
		state.Version = *worker.ImageStateVersion
	}
	if worker.ImageStateDetail != nil {
		state.Detail = *worker.ImageStateDetail
	}
	if state.ReadyDigests == nil {
		state.ReadyDigests = []string{}
	}
	return state
}
