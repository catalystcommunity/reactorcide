package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/egress"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------- shared

// memoryEvents is an in-memory CI event store with the guarded claim of
// postgres_store (received → evaluating only while no eval job is linked).
type memoryEvents struct {
	events   []*models.CIEvent
	released []string
	relinked map[string]string
}

func (m *memoryEvents) find(id string) *models.CIEvent {
	for _, event := range m.events {
		if event.EventID == id {
			return event
		}
	}
	return nil
}

func (m *memoryEvents) RecordCIEvent(_ context.Context, event *models.CIEvent) (*models.CIEvent, store.CIEventRecordOutcome, error) {
	for _, existing := range m.events {
		if event.IdempotencyKey != nil && existing.IdempotencyKey != nil && *existing.IdempotencyKey == *event.IdempotencyKey {
			return existing, store.CIEventRedelivery, nil
		}
	}
	copy := *event
	copy.EventID = fmt.Sprintf("00000000-0000-0000-0000-%012d", len(m.events)+1)
	if copy.Status == "" {
		copy.Status = models.CIEventStatusReceived
	}
	m.events = append(m.events, &copy)
	return &copy, store.CIEventCreated, nil
}
func (m *memoryEvents) GetCIEvent(_ context.Context, id string) (*models.CIEvent, error) {
	if event := m.find(id); event != nil {
		return event, nil
	}
	return nil, store.ErrNotFound
}
func (m *memoryEvents) AttachCIEventEvalJob(_ context.Context, eventID, jobID string) error {
	if event := m.find(eventID); event != nil {
		id := jobID
		event.EvalJobID = &id
		event.Status = models.CIEventStatusEvaluating
	}
	return nil
}
func (m *memoryEvents) SetCIEventStatus(_ context.Context, id, status string) error {
	if event := m.find(id); event != nil {
		event.Status = status
	}
	return nil
}
func (m *memoryEvents) SetCIEventSummary(context.Context, string, models.CIEventSummary) error {
	return nil
}
func (m *memoryEvents) UpsertCIEventWorkflowDecisions(context.Context, []models.CIEventWorkflowDecision) error {
	return nil
}
func (m *memoryEvents) ListCIEventWorkflowDecisions(context.Context, string) ([]models.CIEventWorkflowDecision, error) {
	return nil, nil
}
func (m *memoryEvents) GetCIEventWorkflowDecisionByWorkflow(context.Context, string) (*models.CIEventWorkflowDecision, error) {
	return nil, store.ErrNotFound
}
func (m *memoryEvents) ClaimCIEventForEval(_ context.Context, id string) (bool, error) {
	event := m.find(id)
	if event == nil || event.EvalJobID != nil || event.Status != models.CIEventStatusReceived {
		return false, nil
	}
	event.Status = models.CIEventStatusEvaluating
	return true, nil
}
func (m *memoryEvents) ReleaseCIEventClaim(_ context.Context, id string) error {
	m.released = append(m.released, id)
	if event := m.find(id); event != nil && event.EvalJobID == nil && event.Status == models.CIEventStatusEvaluating {
		event.Status = models.CIEventStatusReceived
	}
	return nil
}
func (m *memoryEvents) RelinkCIEventEvalJob(_ context.Context, eventID, jobID string) error {
	if m.relinked == nil {
		m.relinked = map[string]string{}
	}
	m.relinked[eventID] = jobID
	return m.AttachCIEventEvalJob(context.Background(), eventID, jobID)
}

// ------------------------------------------------------- dynamic jobs gate

type dynamicJobsStore struct {
	*MockStore
	effective string
}

func (s *dynamicJobsStore) EffectiveDynamicJobs(context.Context, string, *string) (string, error) {
	return s.effective, nil
}
func (s *dynamicJobsStore) EffectiveDuplicateEvents(context.Context, string) (string, error) {
	return models.DuplicateEventsRun, nil
}

func submitTriggers(t *testing.T, st store.Store, parent *models.Job) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewJobHandler(st, nil)
	body := `{"type":"trigger_job","jobs":[{"job_name":"test","job_command":"make test","container_image":"alpine:latest"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+parent.JobID+"/triggers", bytes.NewReader([]byte(body)))
	caps, err := tokencaps.New(tokencaps.JobsSubmit)
	require.NoError(t, err)
	ctx := checkauth.SetPrincipalContext(req.Context(), &checkauth.Principal{CredentialType: "job_token",
		BoundJobID: parent.JobID, OrganizationIDs: []string{parent.OrgID}, Capabilities: caps})
	ctx = context.WithValue(ctx, GetContextKey("job_id"), parent.JobID)
	rr := httptest.NewRecorder()
	handler.SubmitTriggers(rr, req.WithContext(ctx))
	return rr
}

func triggerParent(t *testing.T, eval bool) *models.Job {
	t.Helper()
	parent := &models.Job{JobID: "parent-job", UserID: "user-1", OrgID: "org-1", Status: "running", QueueName: "q", RunnerImage: "img", TimeoutSeconds: 60}
	if eval {
		require.NoError(t, (&vcs.JobMetadata{VCSProvider: "github", Repo: "o/r", CommitSHA: "abc", IsEval: true}).ApplyToJob(parent))
	}
	return parent
}

func TestDynamicJobs_DenyRefusesRuntimeTriggers(t *testing.T) {
	parent := triggerParent(t, false)
	st := &dynamicJobsStore{MockStore: &MockStore{}, effective: models.DynamicJobsDeny}
	st.GetJobByIDFunc = func(context.Context, string) (*models.Job, error) { return parent, nil }

	rr := submitTriggers(t, st, parent)
	require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	var resp ErrorResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "dynamic_jobs_denied", resp.Error)
	assert.Contains(t, resp.Message, "dynamic_jobs")
	assert.Empty(t, st.CreateJobCalls, "a refused trigger creates no job")
}

func TestDynamicJobs_AllowAndUnsupportedStoreAccept(t *testing.T) {
	parent := triggerParent(t, false)
	st := &dynamicJobsStore{MockStore: &MockStore{}, effective: models.DynamicJobsAllow}
	st.GetJobByIDFunc = func(context.Context, string) (*models.Job, error) { return parent, nil }
	rr := submitTriggers(t, st, parent)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	plain := &MockStore{GetJobByIDFunc: func(context.Context, string) (*models.Job, error) { return parent, nil }}
	rr = submitTriggers(t, plain, parent)
	require.Equal(t, http.StatusCreated, rr.Code, "a store without the setting keeps the old behavior")
}

func TestDynamicJobs_EvalResultIsNotADynamicJob(t *testing.T) {
	parent := triggerParent(t, true)
	st := &dynamicJobsStore{MockStore: &MockStore{}, effective: models.DynamicJobsDeny}
	st.GetJobByIDFunc = func(context.Context, string) (*models.Job, error) { return parent, nil }
	rr := submitTriggers(t, st, parent)
	assert.NotEqual(t, http.StatusForbidden, rr.Code, "the eval result must pass the dynamic_jobs gate: %s", rr.Body.String())
}

func TestEffectiveDynamicJobs(t *testing.T) {
	cases := []struct{ project, org, want string }{
		{"inherit", "allow", "allow"},
		{"inherit", "deny", "deny"},
		{"", "deny", "deny"},
		{"allow", "deny", "allow"},
		{"deny", "allow", "deny"},
		{"", "", "allow"},
		{"bogus", "bogus", "allow"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, models.EffectiveDynamicJobs(tc.project, tc.org), "project=%q org=%q", tc.project, tc.org)
	}
}

// ----------------------------------------------------------- manual events

type manualEventStore struct {
	*MockStore
	memoryEvents
	jobs map[string]*models.Job
}

func newManualEventStore() *manualEventStore {
	st := &manualEventStore{MockStore: &MockStore{}, jobs: map[string]*models.Job{}}
	st.CreateJobFunc = func(_ context.Context, job *models.Job) error {
		job.JobID = fmt.Sprintf("job-%d", len(st.jobs)+1)
		st.jobs[job.JobID] = job
		return nil
	}
	st.GetJobByIDFunc = func(_ context.Context, id string) (*models.Job, error) {
		if job, ok := st.jobs[id]; ok {
			return job, nil
		}
		return nil, store.ErrNotFound
	}
	return st
}

func postManualEval(t *testing.T, st store.Store, key string, env map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewJobHandler(st, nil)
	body, _ := json.Marshal(CreateJobRequest{Name: "manual eval", JobCommand: "runnerlib eval", SourceType: "copy", SourcePath: "./", JobEnvVars: env})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", bytes.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	req = req.WithContext(checkauth.SetUserContext(req.Context(), &models.User{UserID: "user-1"}))
	rr := httptest.NewRecorder()
	handler.CreateJob(rr, req)
	return rr
}

func TestManualEvent_EvalSubmitIsIdempotent(t *testing.T) {
	st := newManualEventStore()
	env := map[string]string{"REACTORCIDE_JOB_KIND": "eval", "REACTORCIDE_EVENT_TYPE": "push", "REACTORCIDE_REPO": "o/r", "REACTORCIDE_SHA": "abc"}

	first := postManualEval(t, st, "run-42", env)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	again := postManualEval(t, st, "run-42", env)
	require.Equal(t, http.StatusOK, again.Code, again.Body.String())

	require.Len(t, st.jobs, 1, "the repeat returns the first job")
	require.Len(t, st.events, 1)
	event := st.events[0]
	assert.Equal(t, models.CIEventSourceManual, event.Source)
	assert.Equal(t, "manual", event.EventType, "the submitter cannot state the event type of a manual event")
	assert.Equal(t, "o/r", event.Repo)
	assert.Equal(t, "abc", event.CommitSHA)
	assert.Equal(t, "user-1", event.Actor)
	assert.Equal(t, "manual:user-1:run-42", *event.IdempotencyKey, "the key is scoped to the organization")
	job := st.jobs["job-1"]
	assert.Equal(t, event.EventID, *job.EventID)
	assert.Equal(t, event.EventID, job.JobEnvVars["REACTORCIDE_EVENT_ID"])
	assert.Equal(t, "job-1", *event.EvalJobID)

	var firstResp, againResp JobResponse
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstResp))
	require.NoError(t, json.Unmarshal(again.Body.Bytes(), &againResp))
	assert.Equal(t, firstResp.JobID, againResp.JobID)
}

func TestManualEvent_WithoutKeyAndNonEvalJobs(t *testing.T) {
	st := newManualEventStore()
	env := map[string]string{"REACTORCIDE_JOB_KIND": "eval"}
	require.Equal(t, http.StatusCreated, postManualEval(t, st, "", env).Code)
	require.Equal(t, http.StatusCreated, postManualEval(t, st, "", env).Code)
	assert.Len(t, st.events, 2, "without a key every submit is its own event")
	assert.Equal(t, "manual", st.events[0].EventType)

	plain := newManualEventStore()
	require.Equal(t, http.StatusCreated, postManualEval(t, plain, "k", map[string]string{"FOO": "bar"}).Code)
	assert.Empty(t, plain.events, "a job that is not an eval gets no event")
}

func TestManualEvent_InvalidKeyAndInProgress(t *testing.T) {
	st := newManualEventStore()
	env := map[string]string{"REACTORCIDE_JOB_KIND": "eval"}
	assert.Equal(t, http.StatusBadRequest, postManualEval(t, st, "has space", env).Code)

	// A request with the same key is still creating its job.
	key := "manual:user-1:busy"
	st.events = append(st.events, &models.CIEvent{EventID: "00000000-0000-0000-0000-000000000099", IdempotencyKey: &key,
		Status: models.CIEventStatusEvaluating})
	rr := postManualEval(t, st, "busy", env)
	assert.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	assert.Empty(t, st.jobs)
}

func TestManualEvent_FailedCreateReleasesClaim(t *testing.T) {
	st := newManualEventStore()
	st.CreateJobFunc = func(context.Context, *models.Job) error { return errors.New("db down") }
	rr := postManualEval(t, st, "k1", map[string]string{"REACTORCIDE_JOB_KIND": "eval"})
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.Len(t, st.events, 1)
	assert.Equal(t, []string{st.events[0].EventID}, st.released)
	assert.Equal(t, models.CIEventStatusReceived, st.events[0].Status, "a later request with the key can evaluate it")
}

// ---------------------------------------------------- webhook claim/release

type claimingWebhookStore struct {
	*WebhookMockStore
	memoryEvents
}

func TestWebhookCIEvent_ClaimLostCreatesNoEval(t *testing.T) {
	st := &claimingWebhookStore{WebhookMockStore: &WebhookMockStore{}}
	key := "github:delivery-1"
	// A concurrent delivery already claimed this event and is creating the job.
	st.events = append(st.events, &models.CIEvent{EventID: "00000000-0000-0000-0000-000000000009", IdempotencyKey: &key,
		Status: models.CIEventStatusEvaluating})
	handler := newClaimingWebhookHandler(t, st)
	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "abc123", "feature", "main", 42)
	deliverWebhook(t, handler, "pull_request", "delivery-1", body)
	assert.Empty(t, st.CreateJobCalls, "only the delivery that holds the claim creates the eval job")
}

func TestWebhookCIEvent_FailedJobCreateReleasesClaim(t *testing.T) {
	st := &claimingWebhookStore{WebhookMockStore: &WebhookMockStore{}}
	calls := 0
	st.CreateJobFunc = func(context.Context, *models.Job) error {
		calls++
		if calls == 1 {
			return errors.New("db down")
		}
		return nil
	}
	handler := newClaimingWebhookHandler(t, st)
	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "abc123", "feature", "main", 42)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	handler.HandleGitHubWebhook(httptest.NewRecorder(), req)
	require.Len(t, st.events, 1)
	assert.Equal(t, models.CIEventStatusReceived, st.events[0].Status, "the failed attempt gave the claim back")

	deliverWebhook(t, handler, "pull_request", "delivery-1", body)
	require.Len(t, st.CreateJobCalls, 2)
	require.NotNil(t, st.events[0].EvalJobID, "the redelivery evaluated the event")
}

func newClaimingWebhookHandler(t *testing.T, st *claimingWebhookStore) *WebhookHandler {
	t.Helper()
	project := webhookTestProject()
	st.GetProjectByRepoURLFunc = func(context.Context, string) (*models.Project, error) { return project, nil }
	handler := NewWebhookHandler(st, corndogs.NewMockClient())
	handler.SetTokenResolver(testTokenResolver())
	event := prWebhookEvent()
	handler.AddVCSClient(vcs.GitHub, &MockVCSClient{
		ParseWebhookFunc: func(*http.Request) (*vcs.WebhookEvent, error) {
			copy := *event
			return &copy, nil
		},
	})
	return handler
}

// ------------------------------------------------------------ schedule fire

type branchHeadClient struct {
	*MockVCSClient
	sha string
	err error
}

func (c *branchHeadClient) GetBranchHead(_ context.Context, repo, branch string) (string, error) {
	if c.err != nil {
		return "", c.err
	}
	return c.sha, nil
}

type firerStore struct {
	*WebhookMockStore
	memoryEvents
	project *models.Project
}

func (s *firerStore) GetProjectByID(context.Context, string) (*models.Project, error) {
	return s.project, nil
}

func newFirer(t *testing.T, client vcs.Client) (*firerStore, ScheduleFirer) {
	t.Helper()
	project := webhookTestProject()
	st := &firerStore{WebhookMockStore: &WebhookMockStore{}, project: project}
	handler := NewWebhookHandler(st, corndogs.NewMockClient())
	if client != nil {
		handler.AddVCSClient(vcs.GitHub, client)
	}
	return st, handler.ScheduleFirer()
}

func testSchedule(project *models.Project) models.CISchedule {
	return models.CISchedule{ScheduleID: "11111111-1111-1111-1111-111111111111", ProjectID: project.ProjectID,
		Name: "nightly", Branch: "release", WorkflowRef: ".reactorcide/workflows/nightly.yaml", Cron: "0 3 * * *"}
}

func TestScheduleFirer_CreatesEventAndEvalJob(t *testing.T) {
	st, firer := newFirer(t, &branchHeadClient{MockVCSClient: &MockVCSClient{}, sha: "feedface1234567"})
	sched := testSchedule(st.project)
	fireTime := time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)

	eventID, err := firer.Fire(context.Background(), sched, fireTime)
	require.NoError(t, err)
	require.Len(t, st.events, 1)
	event := st.events[0]
	assert.Equal(t, eventID, event.EventID)
	assert.Equal(t, models.CIEventSourceSchedule, event.Source)
	assert.Equal(t, "schedule", event.EventType)
	assert.Equal(t, "test-org/test-repo", event.Repo)
	assert.Equal(t, "release", event.Branch)
	assert.Equal(t, "feedface1234567", event.CommitSHA)
	assert.Equal(t, sched.ScheduleID, *event.ScheduleID)
	assert.Equal(t, "schedule:"+sched.ScheduleID+":2026-11-15T03:00:00Z", *event.IdempotencyKey)

	require.Len(t, st.CreateJobCalls, 1)
	job := st.CreateJobCalls[0]
	assert.Equal(t, "Eval event schedule nightly on release (feedfac) on test-org/test-repo", job.Name)
	assert.Equal(t, event.EventID, *job.EventID)
	assert.Equal(t, *event.EvalJobID, job.JobID)
	env := job.JobEnvVars
	assert.Equal(t, "schedule", env["REACTORCIDE_EVENT_TYPE"])
	assert.Equal(t, sched.WorkflowRef, env["REACTORCIDE_WORKFLOW_REF"])
	assert.Equal(t, sched.ScheduleID, env["REACTORCIDE_SCHEDULE_ID"])
	assert.Equal(t, "0 3 * * *", env["REACTORCIDE_SCHEDULE_CRON"])
	assert.Equal(t, "2026-11-15T03:00:00Z", env["REACTORCIDE_SCHEDULE_FIRE_TIME"])
	assert.Equal(t, "feedface1234567", env["REACTORCIDE_SHA"])
	// The branch head itself is the trusted CI source: no head candidate,
	// no dual checkout, no diff.
	assert.Equal(t, "feedface1234567", *job.CISourceRef)
	assert.Equal(t, "feedface1234567", env["REACTORCIDE_CI_SOURCE_REF"])
	assert.NotContains(t, env, "REACTORCIDE_EVAL_DUAL_CI")
	assert.NotContains(t, env, "REACTORCIDE_DIFF_BASE")
	metadata, err := vcs.MetadataFromJob(job)
	require.NoError(t, err)
	assert.True(t, metadata.IsEval, "the eval result must be accepted from this job")
	assert.Equal(t, "feedface1234567", metadata.CommitSHA)

	// A repeated Fire for the same fire returns the same event, no new job.
	again, err := firer.Fire(context.Background(), sched, fireTime)
	require.NoError(t, err)
	assert.Equal(t, eventID, again)
	assert.Len(t, st.CreateJobCalls, 1)
}

// egressFirerStore is a firerStore with egress control on for the project.
type egressFirerStore struct {
	*firerStore
	on bool
}

func (s *egressFirerStore) EffectiveEgressControl(context.Context, string, *string, string) (bool, error) {
	return s.on, nil
}

// The eval job of a schedule fire is routed like a webhook eval job: with
// egress control on, it requires a worker with an egress proxy
// (docs/egress-proxy.md "Automatic routing").
func TestScheduleFirer_RoutesEgressControlledEvalJob(t *testing.T) {
	for _, on := range []bool{true, false} {
		project := webhookTestProject()
		st := &egressFirerStore{firerStore: &firerStore{WebhookMockStore: &WebhookMockStore{}, project: project}, on: on}
		handler := NewWebhookHandler(st, corndogs.NewMockClient())
		handler.AddVCSClient(vcs.GitHub, &branchHeadClient{MockVCSClient: &MockVCSClient{}, sha: "feedface1234567"})
		_, err := handler.ScheduleFirer().Fire(context.Background(), testSchedule(project), time.Now())
		require.NoError(t, err)
		require.Len(t, st.CreateJobCalls, 1)
		value, routed := st.CreateJobCalls[0].Characteristics[egress.WorkerCharacteristic]
		if on {
			require.True(t, routed, "an egress-controlled schedule eval job must require an egress worker")
			assert.Equal(t, characteristics.BoolValue(true), value)
		} else {
			assert.False(t, routed, "egress control off: no egress characteristic")
		}
	}
}

func TestScheduleFirer_ErrorsAreReturned(t *testing.T) {
	st, firer := newFirer(t, &branchHeadClient{MockVCSClient: &MockVCSClient{}, err: vcs.ErrBranchNotFound})
	_, err := firer.Fire(context.Background(), testSchedule(st.project), time.Now())
	require.ErrorIs(t, err, vcs.ErrBranchNotFound)
	assert.Empty(t, st.events, "nothing is recorded before the commit is known")

	st, firer = newFirer(t, &MockVCSClient{})
	_, err = firer.Fire(context.Background(), testSchedule(st.project), time.Now())
	require.Error(t, err, "a client that cannot resolve branches is an error")

	st, firer = newFirer(t, &branchHeadClient{MockVCSClient: &MockVCSClient{}, sha: "abc"})
	st.project.Enabled = false
	_, err = firer.Fire(context.Background(), testSchedule(st.project), time.Now())
	require.Error(t, err)

	st, firer = newFirer(t, &branchHeadClient{MockVCSClient: &MockVCSClient{}, sha: "abc"})
	st.CreateJobFunc = func(context.Context, *models.Job) error { return errors.New("db down") }
	_, err = firer.Fire(context.Background(), testSchedule(st.project), time.Now())
	require.Error(t, err)
	require.Len(t, st.events, 1)
	assert.Equal(t, models.CIEventStatusReceived, st.events[0].Status, "the claim is given back")
}

func TestProjectRepository(t *testing.T) {
	h := NewWebhookHandler(&WebhookMockStore{}, nil)
	provider, repo, clone, err := h.projectRepository(&models.Project{RepoURL: "github.com/o/r"})
	require.NoError(t, err)
	assert.Equal(t, vcs.GitHub, provider)
	assert.Equal(t, "o/r", repo)
	assert.Equal(t, "https://github.com/o/r.git", clone)

	provider, repo, _, err = h.projectRepository(&models.Project{RepoURL: "gitlab.example.com/group/sub/r"})
	require.NoError(t, err)
	assert.Equal(t, vcs.GitLab, provider)
	assert.Equal(t, "group/sub/r", repo)

	_, _, _, err = h.projectRepository(&models.Project{RepoURL: "git.example.com/o/r"})
	require.Error(t, err, "an unknown host with no hint is an error")
	provider, _, _, err = h.projectRepository(&models.Project{RepoURL: "git.example.com/o/r",
		VCSCredentialSecrets: models.JSONB{"gitlab": "path:key"}})
	require.NoError(t, err)
	assert.Equal(t, vcs.GitLab, provider)
}

func TestScheduleFirer_PostsPendingReactorcideChecks(t *testing.T) {
	client := &branchHeadClient{MockVCSClient: &MockVCSClient{}, sha: "feedface1234567"}
	var posted []vcs.StatusUpdate
	client.UpdateCommitStatusFunc = func(_ context.Context, _ string, update vcs.StatusUpdate) error {
		posted = append(posted, update)
		return nil
	}
	fireTime := time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)

	t.Run("event checks publisher", func(t *testing.T) {
		posted = nil
		st, firer := newFirer(t, client)
		updater := &recordingJobStatusUpdater{supportsEvent: true}
		firer.h.SetStatusUpdater(updater)
		eventID, err := firer.Fire(context.Background(), testSchedule(st.project), fireTime)
		require.NoError(t, err)
		assert.Equal(t, []string{eventID}, updater.events, "pending comes from the event's Reactorcide Checks")
		assert.Empty(t, posted, "no direct legacy status")

		metadata, err := vcs.MetadataFromJob(st.CreateJobCalls[0])
		require.NoError(t, err)
		assert.Equal(t, models.CIEventChecksContext, metadata.StatusContext)
		assert.NotEqual(t, "reactorcide/eval", metadata.StatusContext)
	})

	t.Run("no event checks publisher", func(t *testing.T) {
		posted = nil
		st, firer := newFirer(t, client)
		firer.h.SetStatusUpdater(&recordingJobStatusUpdater{supportsEvent: false})
		_, err := firer.Fire(context.Background(), testSchedule(st.project), fireTime)
		require.NoError(t, err)
		require.Len(t, posted, 1)
		assert.Equal(t, models.CIEventChecksContext, posted[0].Context, "never the legacy reactorcide/eval")
		assert.Equal(t, vcs.StatusPending, posted[0].State)
		assert.Equal(t, "feedface1234567", posted[0].SHA, "status goes on the branch head")
	})
}
