package vcs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestComputeEventChecks(t *testing.T) {
	wf := func(id, name, status string) EventChecksWorkflow {
		return EventChecksWorkflow{ID: id, Name: name, Status: status}
	}
	tests := []struct {
		name        string
		in          EventChecksInput
		state       StatusState
		description string
		workflowID  string
	}{
		{"eval queued", EventChecksInput{EventStatus: "evaluating", EvalJobStatus: "submitted"}, StatusPending, "Evaluating event", ""},
		{"eval running with a workflow already created",
			EventChecksInput{EventStatus: "evaluated", EvalJobStatus: "running", Workflows: []EventChecksWorkflow{wf("w1", "build", "running")}},
			StatusPending, "Evaluating event", "w1"},
		{"eval failed", EventChecksInput{EventStatus: "failed", EvalJobStatus: "failed"}, StatusFailure, "Eval failed", ""},
		{"eval timed out", EventChecksInput{EvalJobStatus: "timeout"}, StatusFailure, "Eval timed out", ""},
		{"eval refused at creation", EventChecksInput{EventStatus: "failed", SkipKind: "image_alias", SkipReason: `eval job not created: image alias "win" is for os "windows"`},
			StatusFailure, `eval job not created: image alias "win" is for os "windows"`, ""},
		{"eval cancelled", EventChecksInput{EvalJobStatus: "cancelled"}, StatusFailure, "Eval cancelled", ""},
		{"event failed without a job status", EventChecksInput{EventStatus: "failed"}, StatusFailure, "Eval failed", ""},
		{"no workflow matched", EventChecksInput{EventStatus: "evaluated", EvalJobStatus: "completed"}, StatusSuccess, "No workflows to run", ""},
		{"evaluated event, eval job not linked", EventChecksInput{EventStatus: "evaluated"}, StatusSuccess, "No workflows to run", ""},
		{"one workflow passed",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{wf("w1", "build", "success")}},
			StatusSuccess, "1 workflow passed", "w1"},
		{"three workflows passed",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{wf("a", "a", "success"), wf("b", "b", "success"), wf("c", "c", "success")}},
			StatusSuccess, "3 workflows passed", ""},
		{"passed and skipped",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{wf("a", "a", "success"), wf("b", "b", "skipped")}},
			StatusSuccess, "1 workflow passed, 1 skipped", ""},
		{"only skipped",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{wf("a", "a", "skipped"), wf("b", "b", "skipped")}},
			StatusSuccess, "2 workflows skipped", ""},
		{"workflows running",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{wf("a", "a", "success"), wf("b", "b", "running"), wf("c", "c", "evaluating")}},
			StatusPending, "1 of 3 workflows done", ""},
		{"cancelling counts as running",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{wf("a", "a", "cancelling")}},
			StatusPending, "0 of 1 workflow done", "a"},
		{"one of three failed",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{wf("a", "lint", "success"), wf("b", "build", "failed"), wf("c", "docs", "success")}},
			StatusFailure, "1 of 3 workflows failed: build", ""},
		{"failure is reported while others still run",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{wf("a", "build", "failed"), wf("b", "test", "running")}},
			StatusFailure, "1 of 2 workflows failed: build (1 running)", ""},
		{"cancelled workflow fails",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{wf("a", "build", "cancelled")}},
			StatusFailure, "1 of 1 workflow failed: build (cancelled)", "a"},
		{"not admitted without a workflow row",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{wf("a", "build", "success")}, NotAdmitted: []string{"deploy"}},
			StatusFailure, "1 of 2 workflows failed: deploy (not admitted)", ""},
		{"policy refusal alone fails",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{wf("a", "build", "success")}, PolicyViolations: 2},
			StatusFailure, "Policy refused 2 CI changes", "a"},
		{"policy refusal with a failed workflow",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{wf("a", "build", "failed")}, PolicyViolations: 1},
			StatusFailure, "1 of 1 workflow failed: build; policy refused 1 CI change", "a"},
		{"policy refusal while the eval runs",
			EventChecksInput{EvalJobStatus: "running", PolicyViolations: 1},
			StatusFailure, "Policy refused 1 CI change", ""},
		{"unnamed workflow",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{wf("a", "", "failed")}},
			StatusFailure, "1 of 1 workflow failed: unnamed workflow", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeEventChecks(tt.in)
			if got.State != tt.state || got.Description != tt.description || got.WorkflowID != tt.workflowID {
				t.Fatalf("ComputeEventChecks = {%s %q %q}, want {%s %q %q}",
					got.State, got.Description, got.WorkflowID, tt.state, tt.description, tt.workflowID)
			}
		})
	}
}

func TestComputeEventChecksTruncatesLongDescriptions(t *testing.T) {
	var workflows []EventChecksWorkflow
	for i := 0; i < 20; i++ {
		workflows = append(workflows, EventChecksWorkflow{ID: "w", Name: strings.Repeat("x", 20), Status: "failed"})
	}
	got := ComputeEventChecks(EventChecksInput{EvalJobStatus: "completed", Workflows: workflows})
	if len(got.Description) > maxStatusDescription || !strings.HasSuffix(got.Description, "...") {
		t.Fatalf("description not truncated to %d: %q", maxStatusDescription, got.Description)
	}
}

// fakeChecksStore implements the parts of store.Store, store.CIEventStore and
// store.CIEventChecksStore the aggregate uses. Other Store methods panic.
type fakeChecksStore struct {
	store.Store
	mu         sync.Mutex
	event      models.CIEvent
	jobs       map[string]*models.Job
	workflows  []models.WorkflowInstance
	decisions  []models.CIEventWorkflowDecision
	lockCalls  int
	saveCalls  int
	violations int
}

func (f *fakeChecksStore) GetJobByID(_ context.Context, id string) (*models.Job, error) {
	if job, ok := f.jobs[id]; ok {
		copied := *job
		return &copied, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeChecksStore) RecordCIEvent(context.Context, *models.CIEvent) (*models.CIEvent, store.CIEventRecordOutcome, error) {
	return nil, "", errors.New("not used")
}
func (f *fakeChecksStore) GetCIEvent(_ context.Context, id string) (*models.CIEvent, error) {
	if id != f.event.EventID {
		return nil, store.ErrNotFound
	}
	copied := f.event
	return &copied, nil
}
func (f *fakeChecksStore) AttachCIEventEvalJob(context.Context, string, string) error { return nil }
func (f *fakeChecksStore) SetCIEventStatus(_ context.Context, _ string, status string) error {
	f.event.Status = status
	return nil
}
func (f *fakeChecksStore) SetCIEventSummary(context.Context, string, models.CIEventSummary) error {
	return nil
}
func (f *fakeChecksStore) UpsertCIEventWorkflowDecisions(context.Context, []models.CIEventWorkflowDecision) error {
	return nil
}
func (f *fakeChecksStore) ListCIEventWorkflowDecisions(context.Context, string) ([]models.CIEventWorkflowDecision, error) {
	return f.decisions, nil
}
func (f *fakeChecksStore) GetCIEventWorkflowDecisionByWorkflow(context.Context, string) (*models.CIEventWorkflowDecision, error) {
	return nil, store.ErrNotFound
}

func (f *fakeChecksStore) WithCIEventChecksLock(ctx context.Context, _ string, fn func(context.Context) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lockCalls++
	return fn(ctx)
}
func (f *fakeChecksStore) ListRootWorkflowInstancesForEvent(context.Context, string) ([]models.WorkflowInstance, error) {
	return f.workflows, nil
}
func (f *fakeChecksStore) SaveCIEventChecks(_ context.Context, _ string, checks models.CIEventChecks) error {
	f.saveCalls++
	f.event.ChecksState, f.event.ChecksDescription, f.event.ChecksTargetURL = checks.State, checks.Description, checks.TargetURL
	return nil
}
func (f *fakeChecksStore) SetCIEventPolicyViolationCount(_ context.Context, _ string, count int) error {
	f.violations = count
	f.event.PolicyViolationCount = count
	return nil
}

// recordingClient records commit statuses. Other Client methods panic.
type recordingClient struct {
	Client
	mu      sync.Mutex
	updates []StatusUpdate
	fail    error
}

func (c *recordingClient) UpdateCommitStatus(_ context.Context, _ string, update StatusUpdate) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail != nil {
		return c.fail
	}
	c.updates = append(c.updates, update)
	return nil
}

func (c *recordingClient) contexts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.updates))
	for _, update := range c.updates {
		out = append(out, update.Context)
	}
	return out
}

func (c *recordingClient) last() StatusUpdate {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.updates[len(c.updates)-1]
}

const testEventID = "01HZZZZZZZZZZZZZZZZZZZZZZZ"

func newChecksFixture() (*JobStatusUpdater, *fakeChecksStore, *recordingClient) {
	evalID := "eval-job"
	fake := &fakeChecksStore{
		event: models.CIEvent{EventID: testEventID, Provider: "github", Repo: "org/repo", CommitSHA: "abc123",
			Status: models.CIEventStatusEvaluating, EvalJobID: &evalID},
		jobs: map[string]*models.Job{evalID: {JobID: evalID, Status: "submitted"}},
	}
	client := &recordingClient{}
	updater := NewJobStatusUpdater()
	updater.SetStore(fake)
	updater.SetBaseURL("https://ci.example")
	updater.AddVCSClient(GitHub, client)
	return updater, fake, client
}

func TestPublishEventChecksPostsOnlyChanges(t *testing.T) {
	ctx := context.Background()
	updater, fake, client := newChecksFixture()

	if err := updater.PublishEventChecks(ctx, testEventID); err != nil {
		t.Fatal(err)
	}
	if err := updater.PublishEventChecks(ctx, testEventID); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 1 {
		t.Fatalf("same state published %d times, want 1", len(client.updates))
	}
	first := client.last()
	if first.Context != "Reactorcide Checks" || first.State != StatusPending || first.Description != "Evaluating event" {
		t.Fatalf("first status = %+v", first)
	}
	if first.TargetURL != "https://ci.example/app/events/"+testEventID {
		t.Fatalf("target = %q, want the event page", first.TargetURL)
	}

	fake.jobs["eval-job"].Status = "completed"
	fake.workflows = []models.WorkflowInstance{{WorkflowID: "wf-1", Name: "build", Status: "success"}}
	if err := updater.PublishEventChecks(ctx, testEventID); err != nil {
		t.Fatal(err)
	}
	got := client.last()
	if len(client.updates) != 2 || got.State != StatusSuccess || got.Description != "1 workflow passed" {
		t.Fatalf("after success: %d updates, last %+v", len(client.updates), got)
	}
	if got.TargetURL != "https://ci.example/app/workflows/wf-1" {
		t.Fatalf("target = %q, want the single workflow page", got.TargetURL)
	}
	if fake.saveCalls != 2 {
		t.Fatalf("saved %d states, want 2", fake.saveCalls)
	}
}

func TestPublishEventChecksRetriesAfterProviderFailure(t *testing.T) {
	ctx := context.Background()
	updater, fake, client := newChecksFixture()
	client.fail = errors.New("provider down")
	if err := updater.PublishEventChecks(ctx, testEventID); err == nil {
		t.Fatal("expected the provider error")
	}
	if fake.saveCalls != 0 {
		t.Fatal("a state that was not posted must not be saved")
	}
	client.fail = nil
	if err := updater.PublishEventChecks(ctx, testEventID); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 1 || fake.saveCalls != 1 {
		t.Fatalf("retry: %d updates, %d saves", len(client.updates), fake.saveCalls)
	}
}

func TestPublishEventChecksSkipsDuplicateEvents(t *testing.T) {
	updater, fake, client := newChecksFixture()
	fake.event.Status = models.CIEventStatusDuplicate
	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 0 {
		t.Fatalf("a duplicate event posted %v", client.contexts())
	}
}

func TestPublishEventChecksCountsNotAdmittedDecisions(t *testing.T) {
	updater, fake, client := newChecksFixture()
	fake.jobs["eval-job"].Status = "completed"
	admittedID := "wf-1"
	fake.workflows = []models.WorkflowInstance{{WorkflowID: admittedID, Name: "build", Status: "failed"}}
	fake.decisions = []models.CIEventWorkflowDecision{
		// Has a workflow row: counted once, through the row's failed status.
		{WorkflowSecurityID: "build", WorkflowName: "build", Decision: models.WorkflowDecisionNotAdmitted, WorkflowID: &admittedID},
		// No row: counted from the decision.
		{WorkflowSecurityID: "deploy", WorkflowName: "deploy", Decision: models.WorkflowDecisionNotAdmitted},
		{WorkflowSecurityID: "docs", WorkflowName: "docs", Decision: models.WorkflowDecisionSkipped},
	}
	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	got := client.last()
	if got.State != StatusFailure || got.Description != "2 of 2 workflows failed: build, deploy (not admitted)" {
		t.Fatalf("status = %+v", got)
	}
}

// A required workflow that the trusted base definition matched, but that did
// not run (the head renamed or removed it), fails the checks.
func TestPublishEventChecksFailsRequiredBaseWorkflowThatDidNotRun(t *testing.T) {
	updater, fake, client := newChecksFixture()
	fake.jobs["eval-job"].Status = "completed"
	fake.workflows = []models.WorkflowInstance{{WorkflowID: "wf-1", Name: "lint", WorkflowSecurityID: "lint", Status: "success"}}
	base := models.DecisionReason{Kind: models.DecisionReasonBaseDefinition}
	fake.decisions = []models.CIEventWorkflowDecision{
		{WorkflowSecurityID: "lint", WorkflowName: "lint", Decision: models.WorkflowDecisionRun, Reasons: []models.DecisionReason{base}},
		{WorkflowSecurityID: "security-scan", WorkflowName: "security scan", Decision: models.WorkflowDecisionSkipped,
			Reasons: []models.DecisionReason{base, {Kind: "admission", Detail: "not selected for this event"}}},
		// Path acceptance skips on purpose; a carried workflow ran earlier.
		{WorkflowSecurityID: "docs", WorkflowName: "docs", Decision: models.WorkflowDecisionSkipped,
			Reasons: []models.DecisionReason{base, {Kind: "path_acceptance"}}},
		{WorkflowSecurityID: "e2e", WorkflowName: "e2e", Decision: models.WorkflowDecisionCarried, Reasons: []models.DecisionReason{base}},
		// No base marker (a push, or a head view): not counted.
		{WorkflowSecurityID: "bench", WorkflowName: "bench", Decision: models.WorkflowDecisionSkipped},
	}
	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	got := client.last()
	if got.State != StatusFailure || got.Description != "1 of 2 workflows failed: required workflow security scan did not run" {
		t.Fatalf("status = %+v", got)
	}
}

func TestPublishEventChecksSerializesConcurrentRecomputes(t *testing.T) {
	updater, fake, client := newChecksFixture()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = updater.PublishEventChecks(context.Background(), testEventID)
		}()
	}
	wg.Wait()
	if len(client.updates) != 1 {
		t.Fatalf("16 concurrent recomputes of one state posted %d times", len(client.updates))
	}
	if fake.lockCalls != 16 {
		t.Fatalf("lock taken %d times, want 16", fake.lockCalls)
	}
}

func eventJob(id string, notes JobMetadata, eventID string, nodeID *string) *models.Job {
	job := &models.Job{JobID: id, Status: "running", EventID: &eventID, WorkflowNodeID: nodeID}
	_ = notes.ApplyToJob(job)
	return job
}

func TestUpdateJobStatusRoutesEventEvalToChecks(t *testing.T) {
	updater, _, client := newChecksFixture()
	job := eventJob("eval-job", JobMetadata{VCSProvider: "github", Repo: "org/repo", CommitSHA: "abc123",
		StatusContext: "reactorcide/eval", IsEval: true}, testEventID, nil)
	if err := updater.UpdateJobStatus(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if got := client.contexts(); len(got) != 1 || got[0] != "Reactorcide Checks" {
		t.Fatalf("contexts = %v, want only Reactorcide Checks", got)
	}
}

func TestUpdateJobStatusPostsNothingForEventNodeJobs(t *testing.T) {
	updater, _, client := newChecksFixture()
	node := "node-1"
	job := eventJob("node-job", JobMetadata{VCSProvider: "github", Repo: "org/repo", CommitSHA: "abc123",
		StatusContext: "build"}, testEventID, &node)
	if err := updater.UpdateJobStatus(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 0 {
		t.Fatalf("node job of an event posted %v", client.contexts())
	}
}

// A non-eval, non-node job of an event (a dynamic job outside a workflow)
// posts nothing; its old per-job context must not reappear.
func TestUpdateJobStatusPostsNothingForOtherEventJobs(t *testing.T) {
	updater, _, client := newChecksFixture()
	job := eventJob("child-job", JobMetadata{VCSProvider: "github", Repo: "org/repo", CommitSHA: "abc123",
		StatusContext: "child"}, testEventID, nil)
	if err := updater.UpdateJobStatus(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 0 {
		t.Fatalf("non-eval job of an event posted %v", client.contexts())
	}
}

func TestUpdateJobStatusLegacyEvalKeepsItsContext(t *testing.T) {
	client := &recordingClient{}
	updater := NewJobStatusUpdater()
	updater.AddVCSClient(GitHub, client)
	job := &models.Job{JobID: "legacy", Status: "running"}
	_ = (&JobMetadata{VCSProvider: "github", Repo: "org/repo", CommitSHA: "abc123", StatusContext: "reactorcide/eval", IsEval: true}).ApplyToJob(job)
	if err := updater.UpdateJobStatus(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if got := client.contexts(); len(got) != 1 || got[0] != "reactorcide/eval" {
		t.Fatalf("legacy eval contexts = %v", got)
	}
}

func eventWorkflow(status, statusContext string) *models.WorkflowInstance {
	eventID := testEventID
	return &models.WorkflowInstance{WorkflowID: "wf-1", Name: "build", Status: status, VCSProvider: "github",
		VCSRepo: "org/repo", CommitSHA: "abc123", StatusContext: statusContext, EventID: &eventID}
}

func TestUpdateWorkflowStatusEventWorkflowPostsOnlyChecks(t *testing.T) {
	updater, fake, client := newChecksFixture()
	fake.jobs["eval-job"].Status = "completed"
	fake.workflows = []models.WorkflowInstance{{WorkflowID: "wf-1", Name: "build", Status: "success"}}
	if err := updater.UpdateWorkflowStatus(context.Background(), eventWorkflow("success", ""), nil); err != nil {
		t.Fatal(err)
	}
	if got := client.contexts(); len(got) != 1 || got[0] != "Reactorcide Checks" {
		t.Fatalf("contexts = %v, want only Reactorcide Checks", got)
	}
}

func TestUpdateWorkflowStatusEventWorkflowWithExtraContext(t *testing.T) {
	updater, fake, client := newChecksFixture()
	fake.jobs["eval-job"].Status = "completed"
	fake.workflows = []models.WorkflowInstance{{WorkflowID: "wf-1", Name: "build", Status: "failed"}}
	if err := updater.UpdateWorkflowStatus(context.Background(), eventWorkflow("failed", "lint"), nil); err != nil {
		t.Fatal(err)
	}
	got := client.contexts()
	if len(got) != 2 || got[0] != "lint" || got[1] != "Reactorcide Checks" {
		t.Fatalf("contexts = %v, want [lint Reactorcide Checks]", got)
	}
	if client.updates[0].State != StatusFailure || client.updates[1].State != StatusFailure {
		t.Fatalf("states = %s, %s", client.updates[0].State, client.updates[1].State)
	}
}

func TestUpdateWorkflowStatusNeverPostsChecksContextAsExtra(t *testing.T) {
	updater, fake, client := newChecksFixture()
	fake.jobs["eval-job"].Status = "completed"
	fake.workflows = []models.WorkflowInstance{{WorkflowID: "wf-1", Name: "build", Status: "success"}}
	if err := updater.UpdateWorkflowStatus(context.Background(), eventWorkflow("success", "Reactorcide Checks"), nil); err != nil {
		t.Fatal(err)
	}
	if got := client.contexts(); len(got) != 1 {
		t.Fatalf("contexts = %v, want the aggregate only", got)
	}
}

func TestUpdateWorkflowStatusLegacyCancelledIsFailure(t *testing.T) {
	client := &recordingClient{}
	updater := NewJobStatusUpdater()
	updater.AddVCSClient(GitHub, client)
	wf := &models.WorkflowInstance{WorkflowID: "wf", Name: "build", Status: "cancelled", VCSProvider: "github",
		VCSRepo: "org/repo", CommitSHA: "abc123", StatusContext: "build"}
	if err := updater.UpdateWorkflowStatus(context.Background(), wf, []models.WorkflowNode{{Name: "build", Status: "cancelled"}}); err != nil {
		t.Fatal(err)
	}
	got := client.last()
	if got.Context != "build" || got.State != StatusFailure || got.Description != "Workflow cancelled" {
		t.Fatalf("legacy cancelled status = %+v", got)
	}
}

func TestUpdateCIPolicyStatusEventScopedFailsChecks(t *testing.T) {
	updater, fake, client := newChecksFixture()
	job := eventJob("eval-job", JobMetadata{VCSProvider: "github", Repo: "org/repo", CommitSHA: "abc123",
		StatusContext: "reactorcide/eval", IsEval: true}, testEventID, nil)
	if err := updater.UpdateCIPolicyStatus(context.Background(), job, []CIPolicyViolation{{Path: ".reactorcide/x.yaml"}}); err != nil {
		t.Fatal(err)
	}
	if fake.violations != 1 {
		t.Fatalf("violation count = %d", fake.violations)
	}
	got := client.contexts()
	if len(got) != 1 || got[0] != "Reactorcide Checks" {
		t.Fatalf("contexts = %v, want only Reactorcide Checks", got)
	}
	if last := client.last(); last.State != StatusFailure || last.Description != "Policy refused 1 CI change" {
		t.Fatalf("status = %+v", last)
	}
}

func TestUpdateCIPolicyStatusLegacyKeepsPolicyContext(t *testing.T) {
	client := &recordingClient{}
	updater := NewJobStatusUpdater()
	updater.AddVCSClient(GitHub, client)
	job := &models.Job{JobID: "legacy"}
	_ = (&JobMetadata{VCSProvider: "github", Repo: "org/repo", CommitSHA: "abc123", IsEval: true}).ApplyToJob(job)
	if err := updater.UpdateCIPolicyStatus(context.Background(), job, nil); err != nil {
		t.Fatal(err)
	}
	if got := client.contexts(); len(got) != 1 || got[0] != "Reactorcide CI Policy" {
		t.Fatalf("legacy contexts = %v", got)
	}
}

func TestComputeEventChecks_AwaitingApprovalIsPending(t *testing.T) {
	result := ComputeEventChecks(EventChecksInput{EventStatus: models.CIEventStatusAwaitingApproval})
	if result.State != StatusPending || !strings.Contains(result.Description, "approve-ci") {
		t.Fatalf("a held event must be pending and name approve-ci: %+v", result)
	}
}
