package test

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobcontrol"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A retry with shifts stores the shifts and the shifted resources, keeps the
// authority columns of the original job, and records the re-runner.
func TestRetryWithShifts_StoresShiftsAndKeepsAuthority(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		rerunner, err := du.CreateUser(DataSetup{"Username": "shift-rerunner", "Email": "s@example.com"})
		require.NoError(t, err)
		original, err := du.CreateJob(DataSetup{"Status": "failed"})
		require.NoError(t, err)
		require.NoError(t, tx.Model(&models.Job{}).Where("job_id = ?", original.JobID).Updates(map[string]any{
			"ci_origin": "base", "ci_sha": "base-sha", "execution_profile": "pr-untrusted",
			"policy_revision": "rev-9", "policy_rule_id": "rule-2", "resource_storage_limit": "10Gi",
		}).Error)
		original, err = postgres_store.PostgresStore.GetJobByID(ctx, original.JobID)
		require.NoError(t, err)

		actor := principals.Actor{PrincipalID: rerunner.UserID, Identity: "user:" + rerunner.UserID}
		shifts := models.RunShifts{MemoryLimit: "8Gi", StorageLimit: "20Gi", TimeoutSeconds: 5400}
		retried, err := jobcontrol.RetryJobWithShifts(principals.WithActor(ctx, actor), postgres_store.PostgresStore, nil, original, shifts)
		require.NoError(t, err)

		stored, err := postgres_store.PostgresStore.GetJobByID(ctx, retried.JobID)
		require.NoError(t, err)
		require.NotNil(t, stored.Shifts)
		assert.Equal(t, shifts, *stored.Shifts)
		assert.Equal(t, "8Gi", stored.ResourceMemoryLimit)
		assert.Equal(t, "20Gi", stored.ResourceStorageLimit)
		assert.Equal(t, 5400, stored.TimeoutSeconds)
		assert.Equal(t, original.ResourceCPULimit, stored.ResourceCPULimit)
		assert.Equal(t, "base", stored.CIOrigin)
		assert.Equal(t, "base-sha", stored.CISHA)
		assert.Equal(t, "pr-untrusted", stored.ExecutionProfile)
		assert.Equal(t, "rev-9", stored.PolicyRevision)
		assert.Equal(t, "rule-2", stored.PolicyRuleID)
		assert.Equal(t, original.JobCommand, stored.JobCommand)
		assert.Equal(t, original.RunnerImage, stored.RunnerImage)
		require.NotNil(t, stored.ActorPrincipalID)
		assert.Equal(t, rerunner.UserID, *stored.ActorPrincipalID)

		// A plain job has no shifts (NULL), and the column survives an update.
		plain, err := postgres_store.PostgresStore.GetJobByID(ctx, original.JobID)
		require.NoError(t, err)
		assert.Nil(t, plain.Shifts)
		require.NoError(t, postgres_store.PostgresStore.UpdateJob(ctx, stored))
		again, err := postgres_store.PostgresStore.GetJobByID(ctx, stored.JobID)
		require.NoError(t, err)
		require.NotNil(t, again.Shifts)
	})
}

// The rerun_shift_limits setting round-trips, a project value overrides the
// organization value key by key, and the retry path refuses a shift over it.
func TestRerunShiftLimits_SettingBoundsShifts(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org := newPrincipalOrg(t, ctx, "shift-limits")
		project := &models.Project{Name: "shift-limits-app", RepoURL: "github.com/shift/limits", OrgID: org.OrgID}
		require.NoError(t, ps.CreateProject(ctx, project))
		require.NoError(t, ps.PutCITrustSettings(ctx, &models.CITrustSettings{OrgID: org.OrgID,
			RerunShiftLimits: &models.RerunShiftLimits{MaxMemory: "8Gi", MaxTimeoutSeconds: 3600}}))
		require.NoError(t, ps.PutCITrustSettings(ctx, &models.CITrustSettings{OrgID: org.OrgID, ProjectID: &project.ProjectID,
			RerunShiftLimits: &models.RerunShiftLimits{MaxMemory: "16Gi"}}))
		effective, err := ps.EffectiveCITrust(ctx, org.OrgID, project.ProjectID)
		require.NoError(t, err)
		assert.Equal(t, models.RerunShiftLimits{MaxMemory: "16Gi", MaxTimeoutSeconds: 3600}, effective.RerunShiftLimits)

		require.ErrorIs(t, ps.PutCITrustSettings(ctx, &models.CITrustSettings{OrgID: org.OrgID,
			RerunShiftLimits: &models.RerunShiftLimits{MaxMemory: "lots"}}), store.ErrInvalidInput)

		job := &models.Job{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "build", Status: "failed",
			JobCommand: "make", RunnerImage: "runner:1", QueueName: "reactorcide-jobs"}
		require.NoError(t, ps.CreateJob(ctx, job))
		_, err = jobcontrol.RetryJobWithShifts(ctx, ps, nil, job, models.RunShifts{TimeoutSeconds: 7200})
		require.ErrorIs(t, err, runshifts.ErrOverLimit)
		retried, err := jobcontrol.RetryJobWithShifts(ctx, ps, nil, job, models.RunShifts{MemoryLimit: "16Gi"})
		require.NoError(t, err)
		assert.Equal(t, "16Gi", retried.ResourceMemoryLimit)
	})
}

// refVCSClient is a VCS client that resolves refs for a manual run.
type refVCSClient struct {
	vcs.Client
	branches map[string]string
}

func (c *refVCSClient) GetProvider() vcs.Provider { return vcs.GitHub }
func (c *refVCSClient) GetBranchHead(_ context.Context, _ string, branch string) (string, error) {
	if sha, ok := c.branches[branch]; ok {
		return sha, nil
	}
	return "", vcs.ErrBranchNotFound
}
func (c *refVCSClient) GetTagCommit(context.Context, string, string) (string, error) {
	return "", vcs.ErrTagNotFound
}
func (c *refVCSClient) GetDefaultBranch(context.Context, string) (string, error) { return "main", nil }
func (c *refVCSClient) UpdateCommitStatus(context.Context, string, vcs.StatusUpdate) error {
	return nil
}

// A manual run on a ref records a full manual CI event with the starter as
// actor, and an eval job linked to it, in the real database.
func TestManualRunOnRef_EndToEnd(t *testing.T) {
	ctx := context.Background()
	owner := createTestUser(t)
	repo := "github.com/manual-run/" + uniqueName("repo")
	project := createTestProject(t, owner.UserID, repo, false)
	cleanupProjectEvents(t, project.ProjectID)

	handler := handlers.NewWebhookHandler(postgres_store.PostgresStore, corndogs.NewMockClient())
	handler.AddVCSClient(vcs.GitHub, &refVCSClient{branches: map[string]string{"main": "0123456789abcdef0123456789abcdef01234567"}})
	actor := principals.Actor{PrincipalID: owner.UserID, Identity: "user:" + owner.UserID}

	result, err := handler.StartManualRun(principals.WithActor(ctx, actor), project,
		handlers.ManualRunRequest{Ref: "main", Workflow: "build"}, actor)
	require.NoError(t, err)
	require.NotEmpty(t, result.EventID)
	require.NotEmpty(t, result.EvalJobID)

	var event models.CIEvent
	require.NoError(t, testDB.Where("event_id = ?", result.EventID).First(&event).Error)
	assert.Equal(t, models.CIEventSourceManual, event.Source)
	assert.Equal(t, "refs/heads/main", event.Ref)
	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", event.CommitSHA)
	require.NotNil(t, event.ActorPrincipalID)
	assert.Equal(t, owner.UserID, *event.ActorPrincipalID)
	require.NotNil(t, event.EvalJobID)
	assert.Equal(t, result.EvalJobID, *event.EvalJobID)

	job, err := postgres_store.PostgresStore.GetJobByID(ctx, result.EvalJobID)
	require.NoError(t, err)
	require.NotNil(t, job.EventID)
	assert.Equal(t, event.EventID, *job.EventID)
	assert.Equal(t, "build", job.JobEnvVars["REACTORCIDE_WORKFLOW_REF"])
	assert.Equal(t, "manual", job.JobEnvVars["REACTORCIDE_EVENT_SOURCE"])
	require.NotNil(t, job.ActorPrincipalID)
	assert.Equal(t, owner.UserID, *job.ActorPrincipalID, "the eval job inherits the event actor")

	// A second run of the same ref is a new event, never a duplicate.
	second, err := handler.StartManualRun(ctx, project, handlers.ManualRunRequest{Ref: "main"}, actor)
	require.NoError(t, err)
	assert.NotEqual(t, result.EventID, second.EventID)

	// A caller without a credential may not start a run.
	assert.False(t, handlers.CanStartManualRun(ctx, postgres_store.PostgresStore, project))
}
