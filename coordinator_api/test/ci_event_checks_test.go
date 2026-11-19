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

// A re-run of an event's workflow keeps the event and supersedes the
// instance it re-runs: the aggregate sees the newest instance of each chain
// once, and a second re-run starts from it.
func TestCIEventChecks_RerunSupersedesItsInstance(t *testing.T) {
	ctx := context.Background()
	events := ciEventStore(t)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	cleanupProjectEvents(t, project.ProjectID)

	event, _, err := events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("delivery"), uniqueName("semantic")))
	require.NoError(t, err)
	eventID := event.EventID
	newInstance := func(name, status string, rerunOf *string) *models.WorkflowInstance {
		wf := &models.WorkflowInstance{UserID: owner.UserID, OrgID: project.OrgID, ProjectID: &project.ProjectID,
			Name: name, WorkflowSecurityID: name, Status: status, QueueName: "reactorcide-jobs", EventID: &eventID,
			RerunOfWorkflowID: rerunOf}
		require.NoError(t, postgres_store.PostgresStore.CreateWorkflowInstance(ctx, wf))
		return wf
	}
	build := newInstance("build", "failed", nil)
	lint := newInstance("lint", "completed", nil)

	// The (event, security ID) identity still refuses a second first instance.
	duplicate := &models.WorkflowInstance{UserID: owner.UserID, OrgID: project.OrgID, ProjectID: &project.ProjectID,
		Name: "build", WorkflowSecurityID: "build", Status: "running", QueueName: "reactorcide-jobs", EventID: &eventID}
	require.Error(t, postgres_store.PostgresStore.CreateWorkflowInstance(ctx, duplicate))

	checks, ok := store.AppStore.(store.CIEventChecksStore)
	require.True(t, ok)
	ids := func() []string {
		roots, err := checks.ListRootWorkflowInstancesForEvent(ctx, eventID)
		require.NoError(t, err)
		result := []string{}
		for _, wf := range roots {
			result = append(result, wf.WorkflowID)
		}
		return result
	}
	require.ElementsMatch(t, []string{build.WorkflowID, lint.WorkflowID}, ids())

	first := newInstance("build", "failed", &build.WorkflowID)
	require.ElementsMatch(t, []string{first.WorkflowID, lint.WorkflowID}, ids(), "a re-run supersedes the first instance")
	second := newInstance("build", "running", &build.WorkflowID)
	require.ElementsMatch(t, []string{second.WorkflowID, lint.WorkflowID}, ids(), "the newest re-run of a chain counts once")

	// The identity lookup still finds the first instance.
	byEvent, err := postgres_store.PostgresStore.GetRootWorkflowInstanceByEvent(ctx, eventID, "build")
	require.NoError(t, err)
	require.Equal(t, build.WorkflowID, byEvent.WorkflowID)

	// A root re-run keeps the event in rerun_event_id, not in event_id: the
	// (event, security ID) index of migration 000041 stays unchanged.
	var stored struct {
		EventID      *string
		RerunEventID *string
	}
	require.NoError(t, postgres_store.PostgresStore.GetDB().Raw(
		`SELECT event_id::text AS event_id, rerun_event_id::text AS rerun_event_id FROM workflow_instances WHERE workflow_id = ?`,
		second.WorkflowID).Scan(&stored).Error)
	require.Nil(t, stored.EventID)
	require.NotNil(t, stored.RerunEventID)
	require.Equal(t, eventID, *stored.RerunEventID)
	require.Equal(t, eventID, *second.CIEventID())
}

// Release v0.12.13 (and the current CreateWorkflowInstanceForTrigger) insert
// a root workflow of an event with this exact ON CONFLICT clause. It must
// still infer workflow_instances_event_security_idx after migration 000051,
// with re-run instances of the same event present. Otherwise old pods fail
// during a rolling deploy and after a rollback.
func TestWorkflowEventIdentityIndex_OldReleaseOnConflict(t *testing.T) {
	ctx := context.Background()
	events := ciEventStore(t)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	cleanupProjectEvents(t, project.ProjectID)
	event, _, err := events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("delivery"), uniqueName("semantic")))
	require.NoError(t, err)
	eventID := event.EventID

	first := &models.WorkflowInstance{UserID: owner.UserID, OrgID: project.OrgID, ProjectID: &project.ProjectID,
		Name: "build", WorkflowSecurityID: "build", Status: "failed", QueueName: "reactorcide-jobs", EventID: &eventID}
	require.NoError(t, postgres_store.PostgresStore.CreateWorkflowInstanceForTrigger(ctx, first))
	rerun := &models.WorkflowInstance{UserID: owner.UserID, OrgID: project.OrgID, ProjectID: &project.ProjectID,
		Name: "build", WorkflowSecurityID: "build", Status: "running", QueueName: "reactorcide-jobs", EventID: &eventID,
		RerunOfWorkflowID: &first.WorkflowID}
	require.NoError(t, postgres_store.PostgresStore.CreateWorkflowInstance(ctx, rerun))

	oldInsert := `INSERT INTO workflow_instances (user_id, org_id, project_id, name, workflow_security_id, status, queue_name, event_id)
VALUES (?, ?, ?, ?, ?, 'evaluating', 'reactorcide-jobs', ?)
ON CONFLICT (event_id, workflow_security_id) WHERE event_id IS NOT NULL AND parent_workflow_id IS NULL DO NOTHING`
	db := postgres_store.PostgresStore.GetDB()
	result := db.Exec(oldInsert, owner.UserID, project.OrgID, project.ProjectID, "build", "build", eventID)
	require.NoError(t, result.Error, "the v0.12.13 ON CONFLICT clause must infer the identity index")
	require.Zero(t, result.RowsAffected, "the first instance is the identity of (event, build)")
	result = db.Exec(oldInsert, owner.UserID, project.OrgID, project.ProjectID, "lint", "lint", eventID)
	require.NoError(t, result.Error)
	require.EqualValues(t, 1, result.RowsAffected)

	var predicate string
	require.NoError(t, db.Raw(`SELECT pg_get_indexdef('workflow_instances_event_security_idx'::regclass)`).Scan(&predicate).Error)
	require.Contains(t, predicate, "WHERE ((event_id IS NOT NULL) AND (parent_workflow_id IS NULL))")
}
