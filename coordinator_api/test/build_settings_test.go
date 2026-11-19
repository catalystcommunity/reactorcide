package test

// Postgres-backed coverage for migration 000060 (cache namespace classes,
// job links, environment defaults, image policies) and
// postgres_store/build_settings_operations.go (WP14).

import (
	"context"
	"testing"

	"github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestCacheNamespacesAndDefaultsMigration(t *testing.T) {
	db := newScratchDBAt(t, "wp14_migrate", 59)

	var orgID string
	require.NoError(t, db.QueryRow(`INSERT INTO organizations (name, display_name) VALUES ('wp14-org', 'WP14') RETURNING org_id`).Scan(&orgID))
	_, err := db.Exec(`INSERT INTO execution_profiles (org_id, name) VALUES ($1, 'old-profile')`, orgID)
	require.NoError(t, err)

	require.NoError(t, goose.UpTo(db, "migrations", 60))

	var split, write pq.StringArray
	var trusted bool
	require.NoError(t, db.QueryRow(`SELECT cache_namespace_classes, cache_write_classes, trusted_cache_writes FROM execution_profiles WHERE name = 'old-profile'`).
		Scan(&split, &write, &trusted))
	require.Empty(t, split, "existing profiles keep one namespace")
	require.Empty(t, write, "existing profiles keep their write mode")
	require.True(t, trusted)

	_, err = db.Exec(`INSERT INTO environment_defaults (org_id, scope, key, value) VALUES ($1, 'org', 'GOPROXY', 'https://proxy.example')`, orgID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO environment_defaults (org_id, scope, key, value) VALUES ($1, 'org', 'GOPROXY', 'again')`, orgID)
	require.Error(t, err, "one key per scope")
	for _, key := range []string{"REACTORCIDE_SHA", "reactorcide_x", "RC_WF_VARS_JSON", "1BAD"} {
		_, err = db.Exec(`INSERT INTO environment_defaults (org_id, scope, key, value) VALUES ($1, 'org', $2, 'v')`, orgID, key)
		require.Error(t, err, "key %s must be refused", key)
	}
	_, err = db.Exec(`INSERT INTO environment_defaults (org_id, scope, key, value) VALUES ($1, 'project', 'X', 'v')`, orgID)
	require.Error(t, err, "a project scope needs a project")

	var jobID string
	require.NoError(t, db.QueryRow(`INSERT INTO jobs (org_id, name, job_command, status) VALUES ($1, 'j', 'true', 'completed') RETURNING job_id`, orgID).Scan(&jobID))
	_, err = db.Exec(`INSERT INTO job_links (job_id, position, name, url) VALUES ($1, 0, 'Results', 'https://builds.example/1')`, jobID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO job_links (job_id, position, name, url) VALUES ($1, 1, 'Bad', 'javascript:alert(1)')`, jobID)
	require.Error(t, err, "a link must be http or https")

	var enabled bool
	_, err = db.Exec(`INSERT INTO organization_image_policies (org_id) VALUES ($1)`, orgID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT enabled FROM organization_image_policies WHERE org_id = $1`, orgID).Scan(&enabled))
	require.False(t, enabled, "the image policy is off by default")

	require.NoError(t, goose.DownTo(db, "migrations", 59))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name IN ('job_links', 'environment_defaults', 'organization_image_policies')`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'execution_profiles' AND column_name IN ('cache_namespace_classes', 'cache_write_classes')`).Scan(&count))
	require.Zero(t, count)
}

func TestBuildSettingsStore(t *testing.T) {
	// Run in a rolled-back transaction: a committed public job here is a
	// job in every later job list (TestJobsAPI* count them).
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		testBuildSettingsStore(t, ctx, tx)
	})
}

func testBuildSettingsStore(t *testing.T, ctx context.Context, tx *gorm.DB) {
	defaults, ok := store.AppStore.(store.EnvDefaultsStore)
	require.True(t, ok)
	policies, ok := store.AppStore.(store.ImagePolicyStore)
	require.True(t, ok)
	links, ok := store.AppStore.(store.JobLinkStore)
	require.True(t, ok)

	org := &models.Organization{Name: uniqueName("wp14org"), DisplayName: "Org"}
	require.NoError(t, tx.Create(org).Error)
	owner := createTestUserIn(t, tx)
	project := createTestProjectIn(t, tx, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	require.NoError(t, tx.Model(&models.Project{}).Where("project_id = ?", project.ProjectID).Update("org_id", org.OrgID).Error)
	class := &models.WorkerClass{OrgID: org.OrgID, Name: "fast"}
	require.NoError(t, tx.Create(class).Error)

	_, err := defaults.SetEnvDefault(ctx, org.OrgID, models.EnvDefaultScopeOrg, "", "MIRROR", "org")
	require.NoError(t, err)
	_, err = defaults.SetEnvDefault(ctx, org.OrgID, models.EnvDefaultScopeOrg, "", "ONLY_ORG", "org")
	require.NoError(t, err)
	_, err = defaults.SetEnvDefault(ctx, org.OrgID, models.EnvDefaultScopeWorkerClass, "fast", "MIRROR", "class")
	require.NoError(t, err)
	row, err := defaults.SetEnvDefault(ctx, org.OrgID, models.EnvDefaultScopeProject, project.ProjectID, "MIRROR", "project")
	require.NoError(t, err)
	require.Equal(t, "project", row.Value)
	row, err = defaults.SetEnvDefault(ctx, org.OrgID, models.EnvDefaultScopeOrg, "", "MIRROR", "org2")
	require.NoError(t, err, "a second set updates the value")
	require.Equal(t, "org2", row.Value)
	_, err = defaults.SetEnvDefault(ctx, org.OrgID, models.EnvDefaultScopeOrg, "", "REACTORCIDE_SHA", "x")
	require.ErrorIs(t, err, store.ErrInvalidInput)
	_, err = defaults.SetEnvDefault(ctx, org.OrgID, models.EnvDefaultScopeWorkerClass, "missing", "A", "x")
	require.ErrorIs(t, err, store.ErrNotFound)

	orgLayer, classLayer, projectLayer, err := defaults.EnvDefaultLayers(ctx, org.OrgID, "fast", &project.ProjectID)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"MIRROR": "org2", "ONLY_ORG": "org"}, orgLayer)
	require.Equal(t, map[string]string{"MIRROR": "class"}, classLayer)
	require.Equal(t, map[string]string{"MIRROR": "project"}, projectLayer)
	_, classLayer, projectLayer, err = defaults.EnvDefaultLayers(ctx, org.OrgID, "", nil)
	require.NoError(t, err)
	require.Empty(t, classLayer)
	require.Empty(t, projectLayer)

	rows, err := defaults.ListEnvDefaults(ctx, org.OrgID, models.EnvDefaultScopeWorkerClass, "fast")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "fast", rows[0].WorkerClass)
	require.NoError(t, defaults.DeleteEnvDefault(ctx, org.OrgID, models.EnvDefaultScopeWorkerClass, "fast", "MIRROR"))
	require.ErrorIs(t, defaults.DeleteEnvDefault(ctx, org.OrgID, models.EnvDefaultScopeWorkerClass, "fast", "MIRROR"), store.ErrNotFound)

	policy, err := policies.GetImagePolicy(ctx, org.OrgID)
	require.NoError(t, err)
	require.False(t, policy.Enabled, "no row: off")
	require.NoError(t, policies.SetImagePolicy(ctx, &models.ImagePolicy{OrgID: org.OrgID, Enabled: true, AllowedPrefixes: pq.StringArray{"ghcr.io/acme/"}, RequireDigest: true}))
	policy, err = policies.GetImagePolicy(ctx, org.OrgID)
	require.NoError(t, err)
	require.True(t, policy.Enabled)
	require.True(t, policy.RequireDigest)
	require.Equal(t, []string{"ghcr.io/acme/"}, []string(policy.AllowedPrefixes))

	job := createTestJobIn(t, tx, owner.UserID, "completed")
	require.NoError(t, links.ReplaceJobLinks(ctx, job.JobID, []models.JobLink{{Name: "A", URL: "https://a.example"}, {Name: "B", URL: "https://b.example"}}))
	require.NoError(t, links.ReplaceJobLinks(ctx, job.JobID, []models.JobLink{{Name: "C", URL: "https://c.example"}}))
	got, err := links.ListJobLinks(ctx, job.JobID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "C", got[0].Name)
	byJob, err := links.ListJobLinksForJobs(ctx, []string{job.JobID, "not-a-uuid"})
	require.NoError(t, err)
	require.Len(t, byJob[job.JobID], 1)

	profile := &models.ExecutionProfile{OrgID: org.OrgID, Name: "split", SecretPathAllowlist: pq.StringArray{}, RuntimeCapabilities: pq.StringArray{},
		AllowedWorkerClasses: pq.StringArray{}, ResourceCeilings: models.JSONB{}, TrustedCacheWrites: true,
		CacheNamespaceClasses: pq.StringArray{"release", "default"}}
	require.NoError(t, store.AppStore.(interface {
		CreateExecutionProfile(context.Context, *models.ExecutionProfile) error
	}).CreateExecutionProfile(ctx, profile))
	saved, err := store.AppStore.(interface {
		GetExecutionProfile(context.Context, string, string) (*models.ExecutionProfile, error)
	}).GetExecutionProfile(ctx, org.OrgID, "split")
	require.NoError(t, err)
	require.Equal(t, []string{"release", "default"}, []string(saved.CacheNamespaceClasses), "order stays")
	require.Empty(t, saved.CacheWriteClasses)
}
