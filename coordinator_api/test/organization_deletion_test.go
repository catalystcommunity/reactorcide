package test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

func TestDeleteOrganizationReplacesDefaultAndCascadesOwnedResources(t *testing.T) {
	database := newOrganizationMigrationDB(t)
	require.NoError(t, goose.Up(database, "migrations"))
	ctx := organizationStoreContext(t, database)

	source := &models.Organization{Name: "default", DisplayName: "Default"}
	replacement := &models.Organization{Name: "linked", DisplayName: "Linked"}
	require.NoError(t, postgres_store.PostgresStore.CreateOrganization(ctx, source))
	require.NoError(t, postgres_store.PostgresStore.CreateOrganization(ctx, replacement))

	var projectID string
	require.NoError(t, database.QueryRow(
		`INSERT INTO projects (org_id, name, repo_url)
		 VALUES ($1, 'legacy-project', 'github.com/example/legacy-project')
		 RETURNING project_id`,
		source.OrgID,
	).Scan(&projectID))

	var jobID string
	require.NoError(t, database.QueryRow(
		`INSERT INTO jobs (org_id, project_id, name, job_command, status)
		 VALUES ($1, $2, 'legacy-job', 'true', 'completed')
		 RETURNING job_id`,
		source.OrgID, projectID,
	).Scan(&jobID))

	var poolID string
	require.NoError(t, database.QueryRow(
		`INSERT INTO worker_pools (org_id, name) VALUES ($1, 'legacy-pool') RETURNING pool_id`,
		source.OrgID,
	).Scan(&poolID))
	var workerID string
	require.NoError(t, database.QueryRow(
		`INSERT INTO workers (pool_id, worker_key, os, arch)
		 VALUES ($1, 'legacy-worker', 'linux', 'amd64') RETURNING worker_id`,
		poolID,
	).Scan(&workerID))
	_, err := database.Exec(
		`INSERT INTO worker_leases (worker_id, job_id) VALUES ($1, $2)`,
		workerID, jobID,
	)
	require.NoError(t, err)

	var tokenID string
	require.NoError(t, database.QueryRow(
		`INSERT INTO api_tokens (token_hash, name, subject_type, owner_org_id)
		 VALUES ($1, 'legacy-token', 'service_token', $2) RETURNING token_id`,
		[]byte("legacy-token-hash"), source.OrgID,
	).Scan(&tokenID))

	require.NoError(t, postgres_store.PostgresStore.DeleteOrganization(ctx, source.OrgID, replacement.OrgID))

	assertRowCount(t, database, `SELECT count(*) FROM organizations WHERE org_id = $1`, source.OrgID, 0)
	assertRowCount(t, database, `SELECT count(*) FROM projects WHERE project_id = $1`, projectID, 0)
	assertRowCount(t, database, `SELECT count(*) FROM jobs WHERE job_id = $1`, jobID, 0)
	assertRowCount(t, database, `SELECT count(*) FROM workers WHERE worker_id = $1`, workerID, 0)
	assertRowCount(t, database, `SELECT count(*) FROM api_tokens WHERE token_id = $1`, tokenID, 0)

	var defaultOrganizationID string
	require.NoError(t, database.QueryRow(
		`SELECT trim(both '"' from value::text) FROM global_settings WHERE key = 'default_org_id'`,
	).Scan(&defaultOrganizationID))
	require.Equal(t, replacement.OrgID, defaultOrganizationID)
	assertRowCount(t, database, `SELECT count(*) FROM organizations WHERE org_id = $1`, replacement.OrgID, 1)
}

func TestDeleteOrganizationRefusesActiveWork(t *testing.T) {
	database := newOrganizationMigrationDB(t)
	require.NoError(t, goose.Up(database, "migrations"))
	ctx := organizationStoreContext(t, database)

	source := &models.Organization{Name: "default", DisplayName: "Default"}
	replacement := &models.Organization{Name: "linked", DisplayName: "Linked"}
	require.NoError(t, postgres_store.PostgresStore.CreateOrganization(ctx, source))
	require.NoError(t, postgres_store.PostgresStore.CreateOrganization(ctx, replacement))
	var jobID string
	require.NoError(t, database.QueryRow(
		`INSERT INTO jobs (org_id, name, job_command, status)
		 VALUES ($1, 'active-job', 'true', 'running') RETURNING job_id`,
		source.OrgID,
	).Scan(&jobID))

	err := postgres_store.PostgresStore.DeleteOrganization(ctx, source.OrgID, replacement.OrgID)
	require.ErrorIs(t, err, store.ErrConflict)
	assertRowCount(t, database, `SELECT count(*) FROM organizations WHERE org_id = $1`, source.OrgID, 1)

	defaultOrganization, err := postgres_store.PostgresStore.GetDefaultOrganization(ctx)
	require.NoError(t, err)
	require.Equal(t, source.OrgID, defaultOrganization.OrgID)

	_, err = database.Exec(`UPDATE jobs SET status = 'completed' WHERE job_id = $1`, jobID)
	require.NoError(t, err)
	var workflowID string
	require.NoError(t, database.QueryRow(
		`INSERT INTO workflow_instances (org_id, name, workflow_security_id, status)
		 VALUES ($1, 'active-workflow', 'active-workflow', 'running') RETURNING workflow_id`,
		source.OrgID,
	).Scan(&workflowID))

	err = postgres_store.PostgresStore.DeleteOrganization(ctx, source.OrgID, replacement.OrgID)
	require.ErrorIs(t, err, store.ErrConflict)
	assertRowCount(t, database, `SELECT count(*) FROM organizations WHERE org_id = $1`, source.OrgID, 1)
}

func organizationStoreContext(t *testing.T, database *sql.DB) context.Context {
	t.Helper()
	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: database}), &gorm.Config{})
	require.NoError(t, err)
	return context.WithValue(context.Background(), postgres_store.GetTxContextKey(), gormDB)
}

func assertRowCount(t *testing.T, database *sql.DB, query string, argument any, expected int) {
	t.Helper()
	var count int
	require.NoError(t, database.QueryRow(query, argument).Scan(&count))
	require.Equal(t, expected, count)
}
