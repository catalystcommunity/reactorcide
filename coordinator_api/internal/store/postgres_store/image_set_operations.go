package postgres_store

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Worker image set inputs (docs/image-aliases.md). See store.ImageSetStore.

var (
	_ store.ImageSetStore    = PostgresDbStore{}
	_ store.ImageAliasLocker = PostgresDbStore{}
	_ store.ImageAliasUsage  = PostgresDbStore{}
)

// drainingJobStatuses are the job statuses that are not terminal. They must
// agree with the partial index jobs_image_alias_active_idx (migration
// 000069).
var drainingJobStatuses = []string{"submitted", "queued", "scheduling", "running", "cancelling"}

func (ps PostgresDbStore) ListPoolClassGrants(ctx context.Context, poolIDs []string) ([]models.PoolClassGrant, error) {
	grants := []models.PoolClassGrant{}
	query := ps.getDB(ctx).Table("worker_class_pools wcp").
		Joins("JOIN worker_classes wc ON wc.class_id = wcp.class_id").
		Select("wcp.pool_id::text AS pool_id, wc.org_id::text AS org_id, wc.name AS class_name")
	if poolIDs != nil {
		ids := validUUIDs(poolIDs)
		if len(ids) == 0 {
			return grants, nil
		}
		query = query.Where("wcp.pool_id IN ?", ids)
	}
	if err := query.Order("wcp.pool_id").Order("wc.org_id").Order("wc.name").Scan(&grants).Error; err != nil {
		return nil, fmt.Errorf("failed to list pool class grants: %w", err)
	}
	return grants, nil
}

func (ps PostgresDbStore) ListImageAliasesForOrgs(ctx context.Context, orgIDs []string) ([]models.ImageAlias, error) {
	aliases := []models.ImageAlias{}
	ids := validUUIDs(orgIDs)
	if len(ids) == 0 {
		return aliases, nil
	}
	if err := ps.getDB(ctx).Where("org_id IN ?", ids).
		Order("org_id").Order("project_id NULLS FIRST").Order("name").Find(&aliases).Error; err != nil {
		return nil, fmt.Errorf("failed to list image aliases: %w", err)
	}
	return aliases, nil
}

func (ps PostgresDbStore) ListPendingImageAliases(ctx context.Context) ([]models.ImageAlias, error) {
	aliases := []models.ImageAlias{}
	if err := ps.getDB(ctx).Where("pending_digest IS NOT NULL").
		Order("pending_since").Order("alias_id").Find(&aliases).Error; err != nil {
		return nil, fmt.Errorf("failed to list pending image aliases: %w", err)
	}
	return aliases, nil
}

func (ps PostgresDbStore) ListDrainingImages(ctx context.Context, orgIDs []string) ([]models.DrainingImage, error) {
	images := []models.DrainingImage{}
	ids := validUUIDs(orgIDs)
	if len(ids) == 0 {
		return images, nil
	}
	// The digest is the image_digest characteristic of the job
	// (internal/imagealias). Characteristics use the tagged JSON wire form
	// {"image_digest": {"type": "string", "string": "sha256:..."}}.
	err := ps.getDB(ctx).Table("jobs").
		Select("DISTINCT org_id::text AS org_id, project_id::text AS project_id, image_alias, characteristics, "+
			"COALESCE(NULLIF(container_image, ''), runner_image) AS ref, "+
			"characteristics->'image_digest'->>'string' AS digest").
		Where("org_id IN ? AND image_alias IS NOT NULL AND status IN ?", ids, drainingJobStatuses).
		Where("characteristics->'image_digest'->>'string' IS NOT NULL").
		Order("org_id").Order("image_alias").Order("digest").
		Scan(&images).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list draining images: %w", err)
	}

	// Jobs with a direct pinned VM reference (no alias). Only a VM job
	// with a pinned reference gets image_digest without an alias
	// (imagealias.ResolveJob). The predicate matches the partial index
	// jobs_image_digest_direct_active_idx (migration 000070). The worker
	// class and the characteristics select the workers that could run the
	// job (imageset.Target.ServesDirect).
	direct := []models.DrainingImage{}
	err = ps.getDB(ctx).Table("jobs").
		Select("DISTINCT org_id::text AS org_id, '' AS image_alias, worker_class, characteristics, "+
			"COALESCE(NULLIF(container_image, ''), runner_image) AS ref, "+
			"characteristics->'image_digest'->>'string' AS digest").
		Where("org_id IN ? AND image_alias IS NULL AND status IN ?", ids, drainingJobStatuses).
		Where("(characteristics->'image_digest') IS NOT NULL").
		Where("characteristics->'image_digest'->>'string' IS NOT NULL").
		Order("org_id").Order("digest").Order("worker_class").
		Scan(&direct).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list draining direct images: %w", err)
	}
	return append(images, direct...), nil
}

func (ps PostgresDbStore) LockWorkerImageSet(ctx context.Context, workerID string) (*models.Worker, error) {
	if !isValidUUID(workerID) {
		return nil, store.ErrNotFound
	}
	var worker models.Worker
	err := ps.getDB(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("worker_id = ?", workerID).First(&worker).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock worker: %w", err)
	}
	return &worker, nil
}

func (ps PostgresDbStore) LockImageAlias(ctx context.Context, aliasID string) (*models.ImageAlias, error) {
	if !isValidUUID(aliasID) {
		return nil, store.ErrNotFound
	}
	var alias models.ImageAlias
	err := ps.getDB(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("alias_id = ?", aliasID).First(&alias).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock image alias: %w", err)
	}
	return &alias, nil
}

func (ps PostgresDbStore) CountActiveImageAliasJobs(ctx context.Context, alias *models.ImageAlias) (int64, error) {
	if alias == nil || !isValidUUID(alias.OrgID) {
		return 0, nil
	}
	digests := []string{alias.ActiveDigest}
	if alias.PendingDigest != nil && *alias.PendingDigest != "" {
		digests = append(digests, *alias.PendingDigest)
	}
	db := ps.getDB(ctx)
	named := db.Where("jobs.image_alias = ?", alias.Name)
	direct := db.Where("jobs.image_alias IS NULL AND (jobs.characteristics->'image_digest') IS NOT NULL").
		Where("jobs.characteristics->'image_digest'->>'string' IN ?", digests)
	if alias.ProjectID != nil && *alias.ProjectID != "" {
		named = named.Where("jobs.project_id = ?", *alias.ProjectID)
		direct = direct.Where("jobs.project_id = ?", *alias.ProjectID)
	} else {
		// A project alias with the same name replaces the organization
		// alias for the jobs of that project.
		named = named.Where("NOT EXISTS (SELECT 1 FROM image_aliases pa WHERE pa.org_id = jobs.org_id " +
			"AND pa.project_id = jobs.project_id AND pa.name = jobs.image_alias)")
	}
	var count int64
	err := db.Table("jobs").
		Where("jobs.org_id = ? AND jobs.status IN ?", alias.OrgID, drainingJobStatuses).
		Where(db.Where(named).Or(direct)).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("failed to count the jobs of an image alias: %w", err)
	}
	return count, nil
}
