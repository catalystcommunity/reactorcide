package jobcontrol

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// shiftStore adds the rerun_shift_limits setting and execution profiles to
// the retry fake.
type shiftStore struct {
	*retryMockStore
	org, project *models.CITrustSettings
	profiles     map[string]*models.ExecutionProfile
}

func (s *shiftStore) GetCITrustSettings(_ context.Context, _ string, projectID *string) (*models.CITrustSettings, error) {
	row := s.org
	if projectID != nil {
		row = s.project
	}
	if row == nil {
		return nil, store.ErrNotFound
	}
	return row, nil
}

func (s *shiftStore) PutCITrustSettings(context.Context, *models.CITrustSettings) error { return nil }

func (s *shiftStore) EffectiveCITrust(context.Context, string, string) (models.EffectiveTrustSettings, error) {
	return models.EffectiveTrust(s.org, s.project), nil
}

func (s *shiftStore) GetExecutionProfile(_ context.Context, _ string, name string) (*models.ExecutionProfile, error) {
	profile, ok := s.profiles[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *profile
	return &cp, nil
}

var _ store.CITrustSettingsStore = (*shiftStore)(nil)

// authorityJob is a failed job with every authority field set.
func authorityJob() *models.Job {
	return &models.Job{
		JobID: "job-orig", UserID: "user-1", OrgID: "org-1", ProjectID: strPtr("proj-1"), Status: "failed",
		Name: "build", JobCommand: "make build", RunnerImage: "runner:1", ContainerImage: strPtr("img:1"),
		JobEnvVars: models.JSONB{"A": "1"}, SourceRef: strPtr("head-sha"), SourceURL: strPtr("https://example.test/fork.git"),
		CISourceURL: strPtr("https://example.test/upstream.git"), CISourceRef: strPtr("base-sha"),
		CIOrigin: "base", CIRepository: "https://example.test/upstream.git", CISHA: "base-sha",
		ExecutionProfile: "standard", PolicyRevision: "rev-7", PolicyRuleID: "rule-1", ApprovalID: strPtr("approval-1"),
		EventID: strPtr("event-1"), WorkerClass: "default", Capabilities: []string{"docker"},
		ResourceCPURequest: "1", ResourceCPULimit: "2", ResourceMemoryLimit: "4Gi", TimeoutSeconds: 600,
		RefClasses: []string{"pr"},
	}
}

func newShiftStore() *shiftStore {
	return &shiftStore{retryMockStore: newRetryMockStore(), profiles: map[string]*models.ExecutionProfile{
		"standard": {OrgID: "org-1", Name: "standard", ResourceCeilings: models.JSONB{"memory_limit": "16Gi"}},
	}}
}

func TestRetryJobWithShifts_KeepsAuthority(t *testing.T) {
	st := newShiftStore()
	original := st.addJob(authorityJob())
	rerunner := principals.Actor{PrincipalID: "principal-rerunner", Identity: "user:rerunner"}
	ctx := principals.WithActor(context.Background(), rerunner)

	shifts := models.RunShifts{MemoryLimit: "8Gi", CPULimit: "4", StorageLimit: "20Gi", TimeoutSeconds: 7200}
	newJob, err := RetryJobWithShifts(ctx, st, corndogs.NewMockClient(), original, shifts)
	if err != nil {
		t.Fatal(err)
	}
	if newJob.ResourceMemoryLimit != "8Gi" || newJob.ResourceCPULimit != "4" || newJob.ResourceStorageLimit != "20Gi" ||
		newJob.TimeoutSeconds != 7200 || newJob.ResourceCPURequest != "1" {
		t.Fatalf("shifts not applied: %+v", newJob)
	}
	if newJob.Shifts == nil || *newJob.Shifts != shifts {
		t.Fatalf("shifts not recorded: %+v", newJob.Shifts)
	}
	// Authority and spec come from the original job.
	checks := map[string][2]any{
		"execution_profile": {newJob.ExecutionProfile, original.ExecutionProfile},
		"ci_sha":            {newJob.CISHA, original.CISHA},
		"ci_origin":         {newJob.CIOrigin, original.CIOrigin},
		"ci_source_ref":     {*newJob.CISourceRef, *original.CISourceRef},
		"source_ref":        {*newJob.SourceRef, *original.SourceRef},
		"policy_revision":   {newJob.PolicyRevision, original.PolicyRevision},
		"policy_rule_id":    {newJob.PolicyRuleID, original.PolicyRuleID},
		"approval_id":       {*newJob.ApprovalID, *original.ApprovalID},
		"event_id":          {*newJob.EventID, *original.EventID},
		"job_command":       {newJob.JobCommand, original.JobCommand},
		"runner_image":      {newJob.RunnerImage, original.RunnerImage},
		"container_image":   {*newJob.ContainerImage, *original.ContainerImage},
		"worker_class":      {newJob.WorkerClass, original.WorkerClass},
	}
	for name, pair := range checks {
		if pair[0] != pair[1] {
			t.Errorf("%s changed: %v, want %v", name, pair[0], pair[1])
		}
	}
	if !reflect.DeepEqual(newJob.JobEnvVars, original.JobEnvVars) || !reflect.DeepEqual([]string(newJob.Capabilities), []string(original.Capabilities)) {
		t.Errorf("environment or capabilities changed: %+v", newJob)
	}
	// The re-runner is the actor of the new run (WP2).
	if newJob.ActorPrincipalID == nil || *newJob.ActorPrincipalID != rerunner.PrincipalID || newJob.ActorIdentity != rerunner.Identity {
		t.Errorf("actor = %v %q, want the re-runner", newJob.ActorPrincipalID, newJob.ActorIdentity)
	}
}

// Ref classes are part of the authority (WP3 copies them on retries).
func TestRetryJobWithShifts_KeepsRefClasses(t *testing.T) {
	st := newShiftStore()
	original := st.addJob(authorityJob())
	newJob, err := RetryJobWithShifts(context.Background(), st, nil, original, models.RunShifts{MemoryLimit: "8Gi"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual([]string(newJob.RefClasses), []string(original.RefClasses)) {
		t.Fatalf("ref classes = %v, want %v", newJob.RefClasses, original.RefClasses)
	}
}

func TestRetryJobWithShifts_RetryOfShiftedJobKeepsShifts(t *testing.T) {
	st := newShiftStore()
	original := authorityJob()
	original.ResourceMemoryLimit = "8Gi"
	original.Shifts = &models.RunShifts{MemoryLimit: "8Gi"}
	original = st.addJob(original)
	newJob, err := RetryJobWithShifts(context.Background(), st, nil, original, models.RunShifts{TimeoutSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	want := models.RunShifts{MemoryLimit: "8Gi", TimeoutSeconds: 60}
	if newJob.ResourceMemoryLimit != "8Gi" || newJob.Shifts == nil || *newJob.Shifts != want {
		t.Fatalf("shifts = %+v, memory %q", newJob.Shifts, newJob.ResourceMemoryLimit)
	}
	plain, err := RetryJob(context.Background(), st, nil, original)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Shifts == nil || *plain.Shifts != *original.Shifts {
		t.Fatalf("plain retry lost the earlier shifts: %+v", plain.Shifts)
	}
}

func TestRetryJobWithShifts_Limits(t *testing.T) {
	st := newShiftStore()
	st.org = &models.CITrustSettings{RerunShiftLimits: &models.RerunShiftLimits{MaxMemory: "8Gi", MaxTimeoutSeconds: 3600}}
	original := st.addJob(authorityJob())

	if _, err := RetryJobWithShifts(context.Background(), st, nil, original, models.RunShifts{MemoryLimit: "12Gi"}); !errors.Is(err, runshifts.ErrOverLimit) {
		t.Fatalf("org limit: got %v", err)
	}
	// The project override raises the memory limit and inherits the timeout.
	st.project = &models.CITrustSettings{RerunShiftLimits: &models.RerunShiftLimits{MaxMemory: "12Gi"}}
	if _, err := RetryJobWithShifts(context.Background(), st, nil, original, models.RunShifts{MemoryLimit: "12Gi"}); err != nil {
		t.Fatalf("project override: %v", err)
	}
	if _, err := RetryJobWithShifts(context.Background(), st, nil, original, models.RunShifts{TimeoutSeconds: 7200}); !errors.Is(err, runshifts.ErrOverLimit) {
		t.Fatalf("inherited timeout limit: got %v", err)
	}
	// The execution profile ceiling still applies.
	st.project.RerunShiftLimits.MaxMemory = "64Gi"
	if _, err := RetryJobWithShifts(context.Background(), st, nil, original, models.RunShifts{MemoryLimit: "32Gi"}); !errors.Is(err, runshifts.ErrOverLimit) {
		t.Fatalf("profile ceiling: got %v", err)
	}
	if len(st.jobs) != 2 {
		t.Fatalf("refused retries created jobs: %d jobs", len(st.jobs))
	}
}

func TestRerunWorkflow_AppliesShiftsToEveryJob(t *testing.T) {
	base, old := newRetryWorkflowFixture(t)
	old.Status = "completed"
	old.ExecutionProfile = ""
	old = base.addWorkflow(old)
	st := &shiftStore{retryMockStore: base}

	if _, err := RetryWorkflowWithShifts(context.Background(), st, nil, old.WorkflowID, models.RunShifts{}); !errors.Is(err, ErrWorkflowNotRetryable) {
		t.Fatalf("retry of a completed workflow: got %v", err)
	}
	shifts := models.RunShifts{MemoryLimit: "8Gi", TimeoutSeconds: 1200}
	newWf, err := RerunWorkflow(context.Background(), st, corndogs.NewMockClient(), old.WorkflowID, shifts)
	if err != nil {
		t.Fatal(err)
	}
	if newWf.Shifts == nil || *newWf.Shifts != shifts {
		t.Fatalf("instance shifts = %+v", newWf.Shifts)
	}
	nodes, _ := st.ListWorkflowNodes(context.Background(), newWf.WorkflowID)
	if len(nodes) != 1 || nodes[0].JobID == nil {
		t.Fatalf("nodes = %+v", nodes)
	}
	job, _ := st.GetJobByID(context.Background(), *nodes[0].JobID)
	if job.ResourceMemoryLimit != "8Gi" || job.TimeoutSeconds != 1200 || job.Shifts == nil || *job.Shifts != shifts {
		t.Fatalf("node job did not get the shifts: %+v", job)
	}
	if job.JobCommand != "make build" {
		t.Fatalf("node job spec changed: %q", job.JobCommand)
	}
}

func TestRerunWorkflow_RefusesRunningWorkflow(t *testing.T) {
	st, old := newRetryWorkflowFixture(t)
	old.Status = "running"
	st.addWorkflow(old)
	if _, err := RerunWorkflow(context.Background(), st, nil, old.WorkflowID, models.RunShifts{}); !errors.Is(err, ErrWorkflowNotRerunnable) {
		t.Fatalf("got %v", err)
	}
}

func TestRetryUnsuccessfulJobsWithShifts_RefusesOverLimitBeforeRetrying(t *testing.T) {
	base, old := newRetryWorkflowFixture(t)
	old.OrgID = "org-1"
	base.addWorkflow(old)
	st := &shiftStore{retryMockStore: base,
		project: &models.CITrustSettings{RerunShiftLimits: &models.RerunShiftLimits{MaxCPU: "2"}}}
	jobsBefore := len(base.jobs)
	retried, err := RetryUnsuccessfulJobsWithShifts(context.Background(), st, nil, old.WorkflowID, models.RunShifts{CPULimit: "3"})
	if !errors.Is(err, runshifts.ErrOverLimit) || len(retried) != 0 || len(base.jobs) != jobsBefore {
		t.Fatalf("got %v, %d retried, %d jobs", err, len(retried), len(base.jobs))
	}
	retried, err = RetryUnsuccessfulJobsWithShifts(context.Background(), st, nil, old.WorkflowID, models.RunShifts{CPULimit: "2"})
	if err != nil || len(retried) != 1 || retried[0].ResourceCPULimit != "2" {
		t.Fatalf("got %v, %+v", err, retried)
	}
}

// A re-run keeps the CI event of the original (in rerun_event_id), so its
// jobs keep the event and the event checks count it. Every instance of a chain points at the
// first instance.
func TestRerunWorkflow_KeepsEventAndChain(t *testing.T) {
	base, old := newRetryWorkflowFixture(t)
	old.Status = "completed"
	old.ExecutionProfile = ""
	old.EventID = strPtr("event-1")
	old = base.addWorkflow(old)
	st := &shiftStore{retryMockStore: base}

	first, err := RerunWorkflow(context.Background(), st, corndogs.NewMockClient(), old.WorkflowID, models.RunShifts{})
	if err != nil {
		t.Fatal(err)
	}
	if ev := first.CIEventID(); ev == nil || *ev != "event-1" {
		t.Fatalf("re-run event = %v, want event-1", ev)
	}
	// A root re-run keeps the event in rerun_event_id: event_id stays the
	// identity of the first instance (migration 000051).
	if first.EventID != nil || first.RerunEventID == nil || *first.RerunEventID != "event-1" {
		t.Fatalf("root re-run event_id = %v, rerun_event_id = %v; want nil and event-1", first.EventID, first.RerunEventID)
	}
	if first.RerunOfWorkflowID == nil || *first.RerunOfWorkflowID != old.WorkflowID {
		t.Fatalf("re-run chain = %v, want %s", first.RerunOfWorkflowID, old.WorkflowID)
	}
	nodes, _ := st.ListWorkflowNodes(context.Background(), first.WorkflowID)
	job, _ := st.GetJobByID(context.Background(), *nodes[0].JobID)
	if job.EventID == nil || *job.EventID != "event-1" {
		t.Fatalf("re-run job event = %v, want event-1", job.EventID)
	}

	stored := base.workflows[first.WorkflowID]
	stored.Status = "failed"
	second, err := RerunWorkflow(context.Background(), st, corndogs.NewMockClient(), first.WorkflowID, models.RunShifts{})
	if err != nil {
		t.Fatal(err)
	}
	if second.RerunOfWorkflowID == nil || *second.RerunOfWorkflowID != old.WorkflowID {
		t.Fatalf("a re-run of a re-run points at the first instance, got %v", second.RerunOfWorkflowID)
	}
}
