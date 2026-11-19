package postgres_store

import (
	"context"
	"errors"
	"fmt"

	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/envdefaults"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

var (
	_ store.EnvDefaultsStore = PostgresDbStore{}
	_ store.ImagePolicyStore = PostgresDbStore{}
	_ store.JobLinkStore     = PostgresDbStore{}
)

// envDefaultScopeQuery limits a query to one scope. It resolves a worker
// class name to its ID.
func (ps PostgresDbStore) envDefaultScopeQuery(ctx context.Context, orgID, scope, scopeRef string) (*gorm.DB, *string, *string, error) {
	if !isValidUUID(orgID) || !envdefaults.ValidScope(scope) {
		return nil, nil, nil, store.ErrInvalidInput
	}
	db := ps.getDB(ctx).Model(&models.EnvDefault{}).Where("org_id = ? AND scope = ?", orgID, scope)
	switch scope {
	case models.EnvDefaultScopeOrg:
		if scopeRef != "" {
			return nil, nil, nil, store.ErrInvalidInput
		}
		return db.Session(&gorm.Session{}), nil, nil, nil
	case models.EnvDefaultScopeWorkerClass:
		var class models.WorkerClass
		if err := ps.getDB(ctx).Where("org_id = ? AND name = ?", orgID, scopeRef).First(&class).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil, nil, store.ErrNotFound
			}
			return nil, nil, nil, fmt.Errorf("read worker class: %w", err)
		}
		return db.Where("worker_class_id = ?", class.ClassID).Session(&gorm.Session{}), &class.ClassID, nil, nil
	default:
		if !isValidUUID(scopeRef) {
			return nil, nil, nil, store.ErrNotFound
		}
		var count int64
		if err := ps.getDB(ctx).Model(&models.Project{}).Where("project_id = ? AND org_id = ?", scopeRef, orgID).Count(&count).Error; err != nil {
			return nil, nil, nil, fmt.Errorf("read project: %w", err)
		}
		if count == 0 {
			return nil, nil, nil, store.ErrNotFound
		}
		ref := scopeRef
		return db.Where("project_id = ?", scopeRef).Session(&gorm.Session{}), nil, &ref, nil
	}
}

func (ps PostgresDbStore) ListEnvDefaults(ctx context.Context, orgID, scope, scopeRef string) ([]models.EnvDefault, error) {
	db, _, _, err := ps.envDefaultScopeQuery(ctx, orgID, scope, scopeRef)
	if err != nil {
		return nil, err
	}
	var rows []models.EnvDefault
	if err := db.Order("key").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list environment defaults: %w", err)
	}
	for i := range rows {
		if scope == models.EnvDefaultScopeWorkerClass {
			rows[i].WorkerClass = scopeRef
		}
	}
	return rows, nil
}

func (ps PostgresDbStore) SetEnvDefault(ctx context.Context, orgID, scope, scopeRef, key, value string) (*models.EnvDefault, error) {
	if err := envdefaults.Validate(key, value); err != nil {
		return nil, fmt.Errorf("%w: %s", store.ErrInvalidInput, err.Error())
	}
	db, classID, projectID, err := ps.envDefaultScopeQuery(ctx, orgID, scope, scopeRef)
	if err != nil {
		return nil, err
	}
	var existing models.EnvDefault
	err = db.Where("key = ?", key).First(&existing).Error
	switch {
	case err == nil:
		if err := ps.getDB(ctx).Model(&models.EnvDefault{}).Where("default_id = ?", existing.DefaultID).
			Updates(map[string]any{"value": value, "updated_at": gorm.Expr("timezone('utc', now())")}).Error; err != nil {
			return nil, fmt.Errorf("update environment default: %w", err)
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
		var count int64
		if err := db.Count(&count).Error; err != nil {
			return nil, fmt.Errorf("count environment defaults: %w", err)
		}
		if count >= envdefaults.MaxPerScope {
			return nil, fmt.Errorf("%w: a scope has at most %d environment defaults", store.ErrInvalidInput, envdefaults.MaxPerScope)
		}
		row := &models.EnvDefault{OrgID: orgID, Scope: scope, WorkerClassID: classID, ProjectID: projectID, Key: key, Value: value}
		if err := ps.getDB(ctx).Create(row).Error; err != nil {
			return nil, fmt.Errorf("create environment default: %w", err)
		}
	default:
		return nil, fmt.Errorf("read environment default: %w", err)
	}
	db, _, _, _ = ps.envDefaultScopeQuery(ctx, orgID, scope, scopeRef)
	var row models.EnvDefault
	if err := db.Where("key = ?", key).First(&row).Error; err != nil {
		return nil, fmt.Errorf("read environment default: %w", err)
	}
	if scope == models.EnvDefaultScopeWorkerClass {
		row.WorkerClass = scopeRef
	}
	return &row, nil
}

func (ps PostgresDbStore) DeleteEnvDefault(ctx context.Context, orgID, scope, scopeRef, key string) error {
	db, _, _, err := ps.envDefaultScopeQuery(ctx, orgID, scope, scopeRef)
	if err != nil {
		return err
	}
	result := db.Where("key = ?", key).Delete(&models.EnvDefault{})
	if result.Error != nil {
		return fmt.Errorf("delete environment default: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) EnvDefaultLayers(ctx context.Context, orgID, workerClass string, projectID *string) (map[string]string, map[string]string, map[string]string, error) {
	org, class, project := map[string]string{}, map[string]string{}, map[string]string{}
	if !isValidUUID(orgID) {
		return org, class, project, nil
	}
	type row struct {
		Scope string
		Key   string
		Value string
	}
	var rows []row
	projectRef := ""
	if projectID != nil && isValidUUID(*projectID) {
		projectRef = *projectID
	}
	err := ps.getDB(ctx).Raw(`SELECT d.scope, d.key, d.value FROM environment_defaults d
		LEFT JOIN worker_classes wc ON wc.class_id = d.worker_class_id
		WHERE d.org_id = ? AND (d.scope = 'org'
		   OR (d.scope = 'worker_class' AND wc.name = ? AND ? <> '')
		   OR (d.scope = 'project' AND d.project_id::text = ? AND ? <> ''))`,
		orgID, workerClass, workerClass, projectRef, projectRef).Scan(&rows).Error
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read environment defaults: %w", err)
	}
	for _, r := range rows {
		switch r.Scope {
		case models.EnvDefaultScopeOrg:
			org[r.Key] = r.Value
		case models.EnvDefaultScopeWorkerClass:
			class[r.Key] = r.Value
		case models.EnvDefaultScopeProject:
			project[r.Key] = r.Value
		}
	}
	return org, class, project, nil
}

func (ps PostgresDbStore) GetImagePolicy(ctx context.Context, orgID string) (*models.ImagePolicy, error) {
	policy := &models.ImagePolicy{OrgID: orgID, AllowedPrefixes: pq.StringArray{}}
	if !isValidUUID(orgID) {
		return policy, nil
	}
	err := ps.getDB(ctx).Where("org_id = ?", orgID).First(policy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &models.ImagePolicy{OrgID: orgID, AllowedPrefixes: pq.StringArray{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read image policy: %w", err)
	}
	if policy.AllowedPrefixes == nil {
		policy.AllowedPrefixes = pq.StringArray{}
	}
	return policy, nil
}

func (ps PostgresDbStore) SetImagePolicy(ctx context.Context, policy *models.ImagePolicy) error {
	if policy == nil || !isValidUUID(policy.OrgID) {
		return store.ErrInvalidInput
	}
	prefixes := policy.AllowedPrefixes
	if prefixes == nil {
		prefixes = pq.StringArray{}
	}
	return ps.getDB(ctx).Exec(`INSERT INTO organization_image_policies (org_id, enabled, allowed_prefixes, require_digest, updated_at)
		VALUES (?, ?, ?, ?, timezone('utc', now()))
		ON CONFLICT (org_id) DO UPDATE SET enabled = EXCLUDED.enabled, allowed_prefixes = EXCLUDED.allowed_prefixes,
		    require_digest = EXCLUDED.require_digest, updated_at = EXCLUDED.updated_at`,
		policy.OrgID, policy.Enabled, prefixes, policy.RequireDigest).Error
}

func (ps PostgresDbStore) ReplaceJobLinks(ctx context.Context, jobID string, links []models.JobLink) error {
	if !isValidUUID(jobID) {
		return store.ErrNotFound
	}
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("job_id = ?", jobID).Delete(&models.JobLink{}).Error; err != nil {
			return fmt.Errorf("delete job links: %w", err)
		}
		if len(links) == 0 {
			return nil
		}
		rows := make([]models.JobLink, len(links))
		for i, link := range links {
			rows[i] = models.JobLink{JobID: jobID, Position: i, Name: link.Name, URL: link.URL}
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
			return fmt.Errorf("create job links: %w", err)
		}
		return nil
	})
}

func (ps PostgresDbStore) ListJobLinks(ctx context.Context, jobID string) ([]models.JobLink, error) {
	links := []models.JobLink{}
	if !isValidUUID(jobID) {
		return links, nil
	}
	if err := ps.getDB(ctx).Where("job_id = ?", jobID).Order("position").Find(&links).Error; err != nil {
		return nil, fmt.Errorf("list job links: %w", err)
	}
	return links, nil
}

func (ps PostgresDbStore) ListJobLinksForJobs(ctx context.Context, jobIDs []string) (map[string][]models.JobLink, error) {
	out := map[string][]models.JobLink{}
	valid := make([]string, 0, len(jobIDs))
	for _, id := range jobIDs {
		if isValidUUID(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return out, nil
	}
	var links []models.JobLink
	if err := ps.getDB(ctx).Where("job_id IN ?", valid).Order("job_id, position").Find(&links).Error; err != nil {
		return nil, fmt.Errorf("list job links: %w", err)
	}
	for _, link := range links {
		out[link.JobID] = append(out[link.JobID], link)
	}
	return out, nil
}
