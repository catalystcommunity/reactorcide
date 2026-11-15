package test

// Postgres-backed coverage for the "Reactorcide Checks" aggregate status
// (vcs.PublishEventChecks, postgres_store/ci_event_checks_operations.go,
// migration 000045): concurrent recomputes from several "replicas" post one
// status per state, and the stored state follows the database.

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// countingStatusClient records commit statuses; other Client methods panic.
type countingStatusClient struct {
	vcs.Client
	mu      sync.Mutex
	updates []vcs.StatusUpdate
}

func (c *countingStatusClient) UpdateCommitStatus(_ context.Context, _ string, update vcs.StatusUpdate) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updates = append(c.updates, update)
	return nil
}

func (c *countingStatusClient) snapshot() []vcs.StatusUpdate {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]vcs.StatusUpdate(nil), c.updates...)
}

func TestCIEventChecks_ConcurrentReplicasPublishOncePerState(t *testing.T) {
	ctx := context.Background()
	events := ciEventStore(t)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	cleanupProjectEvents(t, project.ProjectID)

	event, _, err := events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("delivery"), uniqueName("semantic")))
	require.NoError(t, err)
	require.NoError(t, events.SetCIEventStatus(ctx, event.EventID, models.CIEventStatusEvaluated))

	eventID := event.EventID
	wf := &models.WorkflowInstance{UserID: owner.UserID, OrgID: project.OrgID, ProjectID: &project.ProjectID,
		Name: "build", Status: "success", QueueName: "reactorcide-jobs", EventID: &eventID}
	require.NoError(t, postgres_store.PostgresStore.CreateWorkflowInstance(ctx, wf))

	client := &countingStatusClient{}
	// Several updaters on one database stand for several coordinator replicas.
	replicas := make([]*vcs.JobStatusUpdater, 4)
	for i := range replicas {
		replicas[i] = vcs.NewJobStatusUpdater()
		replicas[i].SetStore(store.AppStore)
		replicas[i].SetBaseURL("https://ci.example")
		replicas[i].AddVCSClient(vcs.GitHub, client)
	}
	recompute := func() {
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(updater *vcs.JobStatusUpdater) {
				defer wg.Done()
				require.NoError(t, updater.PublishEventChecks(ctx, eventID))
			}(replicas[i%len(replicas)])
		}
		wg.Wait()
	}

	recompute()
	updates := client.snapshot()
	require.Len(t, updates, 1, "one state must be posted once across replicas")
	require.Equal(t, "Reactorcide Checks", updates[0].Context)
	require.Equal(t, vcs.StatusSuccess, updates[0].State)
	require.Equal(t, "1 workflow passed", updates[0].Description)
	require.Equal(t, "https://ci.example/app/workflows/"+wf.WorkflowID, updates[0].TargetURL)

	stored, err := events.GetCIEvent(ctx, eventID)
	require.NoError(t, err)
	require.Equal(t, "success", stored.ChecksState)
	require.Equal(t, "1 workflow passed", stored.ChecksDescription)
	require.NotNil(t, stored.ChecksPublishedAt)

	// A new state is posted once more, read from the database.
	checks, ok := store.AppStore.(store.CIEventChecksStore)
	require.True(t, ok)
	require.NoError(t, checks.SetCIEventPolicyViolationCount(ctx, eventID, 2))
	recompute()
	updates = client.snapshot()
	require.Len(t, updates, 2)
	require.Equal(t, vcs.StatusFailure, updates[1].State)
	require.Equal(t, "Policy refused 2 CI changes", updates[1].Description)

	roots, err := checks.ListRootWorkflowInstancesForEvent(ctx, eventID)
	require.NoError(t, err)
	require.Len(t, roots, 1)
}
