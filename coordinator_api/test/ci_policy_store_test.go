package test

import (
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

func TestCIPolicyStoreRevisionChecksAndProjectCascade(t *testing.T) {
	database := newOrganizationMigrationDB(t)
	require.NoError(t, goose.Up(database, "migrations"))
	ctx := organizationStoreContext(t, database)

	organization := &models.Organization{Name: "policy-org", DisplayName: "Policy Org"}
	require.NoError(t, postgres_store.PostgresStore.CreateOrganization(ctx, organization))

	var projectID string
	require.NoError(t, database.QueryRow(
		`INSERT INTO projects (org_id, name, repo_url)
		 VALUES ($1, 'policy-project', 'github.com/example/policy-project')
		 RETURNING project_id`,
		organization.OrgID,
	).Scan(&projectID))

	policy := &models.CIPolicy{
		OrgID: organization.OrgID, ProjectID: projectID,
		Document: models.JSONB{"version": float64(1)}, Revision: "revision-1",
	}
	require.NoError(t, postgres_store.PostgresStore.UpsertCIPolicy(ctx, policy, nil))

	stored, err := postgres_store.PostgresStore.GetCIPolicyByProject(ctx, projectID)
	require.NoError(t, err)
	require.Equal(t, "revision-1", stored.Revision)
	require.Equal(t, float64(1), stored.Document["version"])

	staleRevision := "stale-revision"
	policy.Document = models.JSONB{"version": float64(2)}
	policy.Revision = "revision-2"
	require.ErrorIs(t, postgres_store.PostgresStore.UpsertCIPolicy(ctx, policy, &staleRevision), store.ErrConflict)

	expectedRevision := "revision-1"
	require.NoError(t, postgres_store.PostgresStore.UpsertCIPolicy(ctx, policy, &expectedRevision))
	stored, err = postgres_store.PostgresStore.GetCIPolicyByProject(ctx, projectID)
	require.NoError(t, err)
	require.Equal(t, "revision-2", stored.Revision)
	require.Equal(t, float64(2), stored.Document["version"])

	require.ErrorIs(t, postgres_store.PostgresStore.DeleteCIPolicy(ctx, projectID, &expectedRevision), store.ErrConflict)
	expectedRevision = "revision-2"
	require.NoError(t, postgres_store.PostgresStore.DeleteCIPolicy(ctx, projectID, &expectedRevision))
	_, err = postgres_store.PostgresStore.GetCIPolicyByProject(ctx, projectID)
	require.ErrorIs(t, err, store.ErrNotFound)

	policy.PolicyID = ""
	require.NoError(t, postgres_store.PostgresStore.UpsertCIPolicy(ctx, policy, nil))
	require.NoError(t, database.QueryRow(
		`DELETE FROM projects WHERE project_id = $1 RETURNING project_id`, projectID,
	).Scan(&projectID))
	_, err = postgres_store.PostgresStore.GetCIPolicyByProject(ctx, projectID)
	require.ErrorIs(t, err, store.ErrNotFound)
}
