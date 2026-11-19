package test

// Postgres-backed coverage for postgres_store/infra_retry_operations.go,
// the failure_class column of migration 000047 and Job.BeforeSave.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

func TestInfraRetriesResolution(t *testing.T) {
	ctx := context.Background()
	settings, ok := store.AppStore.(store.InfraRetryStore)
	require.True(t, ok, "store.AppStore must implement store.InfraRetryStore")

	org := &models.Organization{Name: uniqueName("org"), DisplayName: "Org"}
	require.NoError(t, testDB.Create(org).Error)
	require.NotNil(t, org.InfraRetries)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	require.NoError(t, testDB.Model(&models.Project{}).Where("project_id = ?", project.ProjectID).Update("org_id", org.OrgID).Error)

	got, err := settings.EffectiveInfraRetries(ctx, org.OrgID, &project.ProjectID)
	require.NoError(t, err)
	require.Equal(t, models.DefaultInfraRetries, got, "a new organization and an inheriting project use the default")

	require.NoError(t, settings.SetOrganizationInfraRetries(ctx, org.OrgID, 3))
	got, err = settings.EffectiveInfraRetries(ctx, org.OrgID, &project.ProjectID)
	require.NoError(t, err)
	require.Equal(t, 3, got)
	require.Error(t, settings.SetOrganizationInfraRetries(ctx, org.OrgID, 6))

	require.NoError(t, testDB.Model(&models.Project{}).Where("project_id = ?", project.ProjectID).Update("infra_retries", 0).Error)
	got, err = settings.EffectiveInfraRetries(ctx, org.OrgID, &project.ProjectID)
	require.NoError(t, err)
	require.Equal(t, 0, got, "the project override wins")
	got, err = settings.EffectiveInfraRetries(ctx, org.OrgID, nil)
	require.NoError(t, err)
	require.Equal(t, 3, got, "a job with no project follows the organization")

	// A project saved through the REST path keeps its override.
	loaded, err := store.AppStore.GetProjectByID(ctx, project.ProjectID)
	require.NoError(t, err)
	require.NotNil(t, loaded.InfraRetries)
	loaded.InfraRetries = nil
	require.NoError(t, store.AppStore.UpdateProject(ctx, loaded))
	got, err = settings.EffectiveInfraRetries(ctx, org.OrgID, &project.ProjectID)
	require.NoError(t, err)
	require.Equal(t, 3, got, "nil inherits again")
}

func TestJobFailureClassPersists(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		jobFailureClassPersists(t, ctx, tx)
	})
}

func jobFailureClassPersists(t *testing.T, ctx context.Context, tx *gorm.DB) {
	du := &DataUtils{db: tx}
	owner, err := du.CreateUser(DataSetup{})
	require.NoError(t, err)
	job := &models.Job{UserID: owner.UserID, Name: "fc", JobCommand: "true", Status: "submitted"}
	require.NoError(t, store.AppStore.CreateJob(ctx, job))

	exit := 137
	updated, matched, err := postgres_store.PostgresStore.UpdateJobStatusGuarded(ctx, job.JobID, []string{"submitted"}, func(j *models.Job) {
		j.Status = "failed"
		j.ExitCode = &exit
		j.SetFailure(models.FailureClassOOM, "OOMKilled")
	})
	require.NoError(t, err)
	require.True(t, matched)
	require.Equal(t, models.FailureClassOOM, updated.FailureClassValue())

	loaded, err := store.AppStore.GetJobByID(ctx, job.JobID)
	require.NoError(t, err)
	require.Equal(t, models.FailureClassOOM, loaded.FailureClassValue())
	require.Equal(t, "OOMKilled", loaded.FailureDetail)

	// Back in the queue: the class is cleared on save.
	_, _, err = postgres_store.PostgresStore.UpdateJobStatusGuarded(ctx, job.JobID, []string{"failed"}, func(j *models.Job) { j.Status = "submitted" })
	require.NoError(t, err)
	loaded, err = store.AppStore.GetJobByID(ctx, job.JobID)
	require.NoError(t, err)
	require.Nil(t, loaded.FailureClass)
}
