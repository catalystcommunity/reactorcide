package test

// Postgres-backed coverage for postgres_store/ci_event_operations.go and
// migration 000041: delivery idempotency, the duplicate_events setting
// (default run, project skip, organization skip inherited), concurrent
// deliveries, workflow decisions, and the one-root-workflow-per-event index.

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func newTestCIEvent(project *models.Project, deliveryID, semanticKey string) *models.CIEvent {
	event := &models.CIEvent{
		OrgID: project.OrgID, ProjectID: &project.ProjectID, Source: models.CIEventSourceWebhook,
		Provider: "github", DeliveryID: deliveryID, EventType: "push", Repo: "org/repo",
		Ref: "refs/heads/main", Branch: "main", CommitSHA: "abc123", SemanticKey: semanticKey,
	}
	if deliveryID != "" {
		key := "github:" + deliveryID
		event.IdempotencyKey = &key
	}
	return event
}

func ciEventStore(t *testing.T) store.CIEventStore {
	t.Helper()
	events, ok := store.AppStore.(store.CIEventStore)
	require.True(t, ok, "store.AppStore must implement store.CIEventStore")
	return events
}

// cleanupProjectEvents removes the committed rows a test made for one
// project. These tests commit (the store opens its own transactions), and the
// transaction-scoped tests that list every job must not see their jobs.
func cleanupProjectEvents(t *testing.T, projectID string) {
	t.Helper()
	t.Cleanup(func() {
		testDB.Exec("DELETE FROM ci_event_workflow_decisions WHERE event_id IN (SELECT event_id FROM ci_events WHERE project_id = ?)", projectID)
		testDB.Exec("DELETE FROM workflow_instances WHERE project_id = ?", projectID)
		testDB.Exec("UPDATE ci_events SET eval_job_id = NULL, duplicate_of_event_id = NULL WHERE project_id = ?", projectID)
		testDB.Exec("DELETE FROM jobs WHERE project_id = ?", projectID)
		testDB.Exec("DELETE FROM ci_events WHERE project_id = ?", projectID)
	})
}

func TestCIEventStore_RedeliveryReturnsFirstEvent(t *testing.T) {
	ctx := context.Background()
	events := ciEventStore(t)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	cleanupProjectEvents(t, project.ProjectID)
	delivery, semantic := uniqueName("delivery"), uniqueName("semantic")

	first, outcome, err := events.RecordCIEvent(ctx, newTestCIEvent(project, delivery, semantic))
	require.NoError(t, err)
	require.Equal(t, store.CIEventCreated, outcome)
	require.NotEmpty(t, first.EventID)

	again, outcome, err := events.RecordCIEvent(ctx, newTestCIEvent(project, delivery, semantic))
	require.NoError(t, err)
	require.Equal(t, store.CIEventRedelivery, outcome)
	require.Equal(t, first.EventID, again.EventID)

	var count int64
	require.NoError(t, testDB.Model(&models.CIEvent{}).Where("project_id = ?", project.ProjectID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestCIEventStore_SemanticDuplicateRunsByDefault(t *testing.T) {
	ctx := context.Background()
	events := ciEventStore(t)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	cleanupProjectEvents(t, project.ProjectID)
	semantic := uniqueName("semantic")

	first, outcome, err := events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("d"), semantic))
	require.NoError(t, err)
	require.Equal(t, store.CIEventCreated, outcome)
	second, outcome, err := events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("d"), semantic))
	require.NoError(t, err)
	require.Equal(t, store.CIEventCreated, outcome, "owner decision D1: semantic duplicates run again by default")
	require.NotEqual(t, first.EventID, second.EventID)
	require.Nil(t, second.DuplicateOfEventID)
}

func TestCIEventStore_SemanticDuplicateSkippedByProjectSetting(t *testing.T) {
	ctx := context.Background()
	events := ciEventStore(t)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	cleanupProjectEvents(t, project.ProjectID)
	require.NoError(t, testDB.Model(&models.Project{}).Where("project_id = ?", project.ProjectID).Update("duplicate_events", "skip").Error)
	semantic := uniqueName("semantic")

	first, _, err := events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("d"), semantic))
	require.NoError(t, err)
	dup, outcome, err := events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("d"), semantic))
	require.NoError(t, err)
	require.Equal(t, store.CIEventDuplicate, outcome)
	require.Equal(t, models.CIEventStatusDuplicate, dup.Status)
	require.NotNil(t, dup.DuplicateOfEventID)
	require.Equal(t, first.EventID, *dup.DuplicateOfEventID)

	// A third delivery still points at the first real event, not the duplicate.
	third, outcome, err := events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("d"), semantic))
	require.NoError(t, err)
	require.Equal(t, store.CIEventDuplicate, outcome)
	require.Equal(t, first.EventID, *third.DuplicateOfEventID)

	// A different change is not a duplicate.
	_, outcome, err = events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("d"), uniqueName("other")))
	require.NoError(t, err)
	require.Equal(t, store.CIEventCreated, outcome)
}

func TestCIEventStore_ProjectInheritsOrganizationSkip(t *testing.T) {
	ctx := context.Background()
	events := ciEventStore(t)
	org := &models.Organization{Name: uniqueName("org"), DisplayName: "Org", DuplicateEvents: models.DuplicateEventsSkip}
	require.NoError(t, testDB.Create(org).Error)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	cleanupProjectEvents(t, project.ProjectID)
	require.NoError(t, testDB.Model(&models.Project{}).Where("project_id = ?", project.ProjectID).Update("org_id", org.OrgID).Error)
	semantic := uniqueName("semantic")

	_, _, err := events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("d"), semantic))
	require.NoError(t, err)
	_, outcome, err := events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("d"), semantic))
	require.NoError(t, err)
	require.Equal(t, store.CIEventDuplicate, outcome)

	// A project override wins over the organization.
	require.NoError(t, testDB.Model(&models.Project{}).Where("project_id = ?", project.ProjectID).Update("duplicate_events", "run").Error)
	_, outcome, err = events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("d"), semantic))
	require.NoError(t, err)
	require.Equal(t, store.CIEventCreated, outcome)
}

func TestCIEventStore_ConcurrentSameDeliveryCreatesOneEvent(t *testing.T) {
	ctx := context.Background()
	events := ciEventStore(t)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	cleanupProjectEvents(t, project.ProjectID)
	delivery, semantic := uniqueName("delivery"), uniqueName("semantic")

	const workers = 8
	var wg sync.WaitGroup
	outcomes := make([]store.CIEventRecordOutcome, workers)
	ids := make([]string, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			event, outcome, err := events.RecordCIEvent(ctx, newTestCIEvent(project, delivery, semantic))
			errs[i], outcomes[i] = err, outcome
			if event != nil {
				ids[i] = event.EventID
			}
		}(i)
	}
	wg.Wait()
	created := 0
	for i := 0; i < workers; i++ {
		require.NoError(t, errs[i])
		require.Equal(t, ids[0], ids[i], "every delivery resolves to one event")
		if outcomes[i] == store.CIEventCreated {
			created++
		}
	}
	require.Equal(t, 1, created)
}

func TestCIEventStore_DecisionsAndEventWorkflowIdentity(t *testing.T) {
	ctx := context.Background()
	events := ciEventStore(t)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	cleanupProjectEvents(t, project.ProjectID)
	event, _, err := events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("d"), uniqueName("s")))
	require.NoError(t, err)

	evalJob := createTestJobInProject(t, owner.UserID, project.ProjectID, "running")
	require.NoError(t, events.AttachCIEventEvalJob(ctx, event.EventID, evalJob.JobID))
	loaded, err := events.GetCIEvent(ctx, event.EventID)
	require.NoError(t, err)
	require.Equal(t, models.CIEventStatusEvaluating, loaded.Status)
	require.Equal(t, evalJob.JobID, *loaded.EvalJobID)

	// Two evals of one event (first and retry) end with one root workflow.
	wfStore, ok := store.AppStore.(interface {
		CreateWorkflowInstanceForTrigger(context.Context, *models.WorkflowInstance) error
		GetRootWorkflowInstanceByEvent(context.Context, string, string) (*models.WorkflowInstance, error)
	})
	require.True(t, ok)
	retryJob := createTestJobInProject(t, owner.UserID, project.ProjectID, "running")
	var workflowIDs []string
	for _, parent := range []string{evalJob.JobID, retryJob.JobID} {
		parentID := parent
		wf := &models.WorkflowInstance{OrgID: project.OrgID, UserID: owner.UserID, ProjectID: &project.ProjectID,
			ParentJobID: &parentID, Name: "CI", WorkflowSecurityID: "ci", TriggerOperationID: event.EventID,
			TriggerType: "runnerlib_eval", EventID: &event.EventID, Status: "evaluating"}
		require.NoError(t, wfStore.CreateWorkflowInstanceForTrigger(ctx, wf))
		workflowIDs = append(workflowIDs, wf.WorkflowID)
	}
	require.Equal(t, workflowIDs[0], workflowIDs[1])
	found, err := wfStore.GetRootWorkflowInstanceByEvent(ctx, event.EventID, "ci")
	require.NoError(t, err)
	require.Equal(t, workflowIDs[0], found.WorkflowID)

	matched := true
	count := 3
	decisions := []models.CIEventWorkflowDecision{
		{EventID: event.EventID, WorkflowSecurityID: "ci", WorkflowName: "CI", Decision: models.WorkflowDecisionRun,
			Reasons:    []models.DecisionReason{{Kind: "paths", Matched: &matched, Include: []string{"api/**"}, MatchedFiles: []string{"api/a.go"}, MatchedCount: &count}},
			WorkflowID: &workflowIDs[0]},
		{EventID: event.EventID, WorkflowSecurityID: "docs", WorkflowName: "Docs", Decision: models.WorkflowDecisionSkipped},
		{EventID: event.EventID, WorkflowSecurityID: "file:.reactorcide/workflows/bad.yaml", WorkflowName: "bad.yaml",
			Decision: models.WorkflowDecisionInvalid, Reasons: []models.DecisionReason{{Kind: "invalid", Detail: "jobs must be a mapping"}}},
	}
	require.NoError(t, events.UpsertCIEventWorkflowDecisions(ctx, decisions))
	// A later write without a workflow keeps the earlier link and replaces the decision.
	require.NoError(t, events.UpsertCIEventWorkflowDecisions(ctx, []models.CIEventWorkflowDecision{
		{EventID: event.EventID, WorkflowSecurityID: "ci", WorkflowName: "CI", Decision: models.WorkflowDecisionNotAdmitted,
			Reasons: []models.DecisionReason{{Kind: "admission", Detail: "profile denies root"}}},
	}))
	listed, err := events.ListCIEventWorkflowDecisions(ctx, event.EventID)
	require.NoError(t, err)
	require.Len(t, listed, 3)
	byID := map[string]models.CIEventWorkflowDecision{}
	for _, decision := range listed {
		byID[decision.WorkflowSecurityID] = decision
	}
	require.Equal(t, models.WorkflowDecisionNotAdmitted, byID["ci"].Decision)
	require.Equal(t, workflowIDs[0], *byID["ci"].WorkflowID)
	require.Equal(t, "admission", byID["ci"].Reasons[0].Kind)
	require.Equal(t, models.WorkflowDecisionInvalid, byID["file:.reactorcide/workflows/bad.yaml"].Decision)

	why, err := events.GetCIEventWorkflowDecisionByWorkflow(ctx, workflowIDs[0])
	require.NoError(t, err)
	require.Equal(t, "ci", why.WorkflowSecurityID)

	summary := models.CIEventSummary{ChangedFilesCount: 120, ChangedFilesPreview: []string{"a", "b"}}
	require.NoError(t, events.SetCIEventSummary(ctx, event.EventID, summary))
	require.NoError(t, events.SetCIEventStatus(ctx, event.EventID, models.CIEventStatusEvaluated))
	loaded, err = events.GetCIEvent(ctx, event.EventID)
	require.NoError(t, err)
	require.Equal(t, 120, loaded.Summary.ChangedFilesCount)
	require.Equal(t, []string{"a", "b"}, loaded.Summary.ChangedFilesPreview)
	require.Equal(t, models.CIEventStatusEvaluated, loaded.Status)

	_, err = events.GetCIEvent(ctx, "not-a-uuid")
	require.ErrorIs(t, err, store.ErrNotFound)
}
