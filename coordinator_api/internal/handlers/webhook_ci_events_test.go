package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventWebhookStore adds an in-memory CI event store to the webhook mock. It
// models the outcomes of postgres_store.RecordCIEvent: a known idempotency
// key is a redelivery, and a known semantic key with skipDuplicates is a
// duplicate. The Postgres behavior itself is covered in test/.
type eventWebhookStore struct {
	*WebhookMockStore
	skipDuplicates bool
	events         []*models.CIEvent
	attached       map[string]string
}

func (s *eventWebhookStore) RecordCIEvent(_ context.Context, event *models.CIEvent) (*models.CIEvent, store.CIEventRecordOutcome, error) {
	for _, existing := range s.events {
		if event.IdempotencyKey != nil && existing.IdempotencyKey != nil && *existing.IdempotencyKey == *event.IdempotencyKey {
			return existing, store.CIEventRedelivery, nil
		}
	}
	outcome := store.CIEventCreated
	if s.skipDuplicates {
		for _, existing := range s.events {
			if existing.SemanticKey == event.SemanticKey && existing.Status != models.CIEventStatusDuplicate {
				first := existing.EventID
				event.DuplicateOfEventID = &first
				event.Status = models.CIEventStatusDuplicate
				outcome = store.CIEventDuplicate
				break
			}
		}
	}
	copy := *event
	copy.EventID = "00000000-0000-0000-0000-00000000000" + string(rune('1'+len(s.events)))
	s.events = append(s.events, &copy)
	return &copy, outcome, nil
}
func (s *eventWebhookStore) GetCIEvent(context.Context, string) (*models.CIEvent, error) {
	return nil, store.ErrNotFound
}
func (s *eventWebhookStore) AttachCIEventEvalJob(_ context.Context, eventID, jobID string) error {
	if s.attached == nil {
		s.attached = map[string]string{}
	}
	s.attached[eventID] = jobID
	for _, event := range s.events {
		if event.EventID == eventID {
			id := jobID
			event.EvalJobID = &id
			event.Status = models.CIEventStatusEvaluating
		}
	}
	return nil
}
func (s *eventWebhookStore) SetCIEventStatus(context.Context, string, string) error { return nil }
func (s *eventWebhookStore) SetCIEventSummary(context.Context, string, models.CIEventSummary) error {
	return nil
}
func (s *eventWebhookStore) UpsertCIEventWorkflowDecisions(context.Context, []models.CIEventWorkflowDecision) error {
	return nil
}
func (s *eventWebhookStore) ListCIEventWorkflowDecisions(context.Context, string) ([]models.CIEventWorkflowDecision, error) {
	return nil, nil
}
func (s *eventWebhookStore) GetCIEventWorkflowDecisionByWorkflow(context.Context, string) (*models.CIEventWorkflowDecision, error) {
	return nil, store.ErrNotFound
}

func newEventWebhookHandler(t *testing.T, st *eventWebhookStore, event *vcs.WebhookEvent) *WebhookHandler {
	t.Helper()
	project := webhookTestProject()
	if st.GetProjectByRepoURLFunc == nil {
		st.GetProjectByRepoURLFunc = func(context.Context, string) (*models.Project, error) { return project, nil }
	}
	handler := NewWebhookHandler(st, corndogs.NewMockClient())
	handler.SetTokenResolver(testTokenResolver())
	handler.AddVCSClient(vcs.GitHub, &MockVCSClient{
		ParseWebhookFunc: func(*http.Request) (*vcs.WebhookEvent, error) {
			copy := *event
			return &copy, nil
		},
	})
	return handler
}

func deliverWebhook(t *testing.T, handler *WebhookHandler, eventHeader, deliveryID string, body []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", eventHeader)
	if deliveryID != "" {
		req.Header.Set("X-GitHub-Delivery", deliveryID)
	}
	w := httptest.NewRecorder()
	handler.HandleGitHubWebhook(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func prWebhookEvent() *vcs.WebhookEvent {
	return &vcs.WebhookEvent{
		Provider: vcs.GitHub, EventType: "pull_request", GenericEvent: vcs.EventPullRequestUpdated, SenderLogin: "dev",
		Repository: vcs.RepositoryInfo{FullName: "test-org/test-repo", CloneURL: "https://github.com/test-org/test-repo.git"},
		PullRequest: &vcs.PullRequestInfo{Number: 42, Action: "synchronize", HeadSHA: "abc123", HeadRef: "feature",
			BaseRef: "main", BaseSHA: "base123"},
	}
}

func TestWebhookCIEvent_RedeliveryCreatesNoSecondEval(t *testing.T) {
	st := &eventWebhookStore{WebhookMockStore: &WebhookMockStore{}}
	handler := newEventWebhookHandler(t, st, prWebhookEvent())
	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "abc123", "feature", "main", 42)

	deliverWebhook(t, handler, "pull_request", "delivery-1", body)
	deliverWebhook(t, handler, "pull_request", "delivery-1", body)

	require.Len(t, st.CreateJobCalls, 1, "a redelivery must not create a second eval job")
	job := st.CreateJobCalls[0]
	require.Len(t, st.events, 1)
	event := st.events[0]
	assert.Equal(t, event.EventID, *job.EventID)
	assert.Equal(t, event.EventID, job.JobEnvVars["REACTORCIDE_EVENT_ID"])
	assert.Equal(t, job.JobID, st.attached[event.EventID])
	assert.Equal(t, "github:delivery-1", *event.IdempotencyKey)
	assert.Equal(t, "github:test-org/test-repo:pull_request_updated:feature:abc123:42", event.SemanticKey)
	assert.Equal(t, "base123", event.BaseSHA)
	assert.Equal(t, 42, *event.PRNumber)
	assert.Equal(t, "synchronize", event.Summary.Action)
	assert.Equal(t, "Eval event pull_request #42 updated on test-org/test-repo", job.Name)
}

func TestWebhookCIEvent_NewDeliveryOfSameChange(t *testing.T) {
	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "abc123", "feature", "main", 42)

	// Default (owner decision D1): it runs again.
	st := &eventWebhookStore{WebhookMockStore: &WebhookMockStore{}}
	handler := newEventWebhookHandler(t, st, prWebhookEvent())
	deliverWebhook(t, handler, "pull_request", "delivery-1", body)
	deliverWebhook(t, handler, "pull_request", "delivery-2", body)
	require.Len(t, st.CreateJobCalls, 2)

	// With duplicate_events=skip it is recorded and not evaluated.
	st = &eventWebhookStore{WebhookMockStore: &WebhookMockStore{}, skipDuplicates: true}
	handler = newEventWebhookHandler(t, st, prWebhookEvent())
	deliverWebhook(t, handler, "pull_request", "delivery-1", body)
	deliverWebhook(t, handler, "pull_request", "delivery-2", body)
	require.Len(t, st.CreateJobCalls, 1)
	require.Len(t, st.events, 2)
	assert.Equal(t, models.CIEventStatusDuplicate, st.events[1].Status)
	assert.Equal(t, st.events[0].EventID, *st.events[1].DuplicateOfEventID)
}

func TestWebhookCIEvent_FailedFirstAttemptIsEvaluatedOnRedelivery(t *testing.T) {
	st := &eventWebhookStore{WebhookMockStore: &WebhookMockStore{}}
	// The event exists from a first attempt that never created its eval job.
	key := "github:delivery-1"
	st.events = append(st.events, &models.CIEvent{EventID: "00000000-0000-0000-0000-000000000009", IdempotencyKey: &key,
		Status: models.CIEventStatusReceived})
	handler := newEventWebhookHandler(t, st, prWebhookEvent())
	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "abc123", "feature", "main", 42)
	deliverWebhook(t, handler, "pull_request", "delivery-1", body)
	require.Len(t, st.CreateJobCalls, 1)
	assert.Equal(t, "00000000-0000-0000-0000-000000000009", *st.CreateJobCalls[0].EventID)
}

func TestWebhookCIEvent_PushCarriesPolicyFactsAndTrustedBase(t *testing.T) {
	parsedPolicy, err := cipolicy.ParseDocument([]byte(testCIPolicyDocument))
	require.NoError(t, err)
	canonical, err := cipolicy.CanonicalDocument(parsedPolicy)
	require.NoError(t, err)
	var document models.JSONB
	require.NoError(t, json.Unmarshal(canonical, &document))
	st := &eventWebhookStore{WebhookMockStore: &WebhookMockStore{
		GetCIPolicyFunc: func(_ context.Context, projectID string) (*models.CIPolicy, error) {
			return &models.CIPolicy{ProjectID: projectID, Document: document, Revision: parsedPolicy.Revision}, nil
		},
	}}
	push := &vcs.WebhookEvent{
		Provider: vcs.GitHub, EventType: "push", GenericEvent: vcs.EventPush, SenderLogin: "pusher",
		Repository: vcs.RepositoryInfo{FullName: "test-org/test-repo", CloneURL: "https://github.com/test-org/test-repo.git", DefaultBranch: "trunk"},
		// Not the default branch: a push to the default branch is trusted.
		Push: &vcs.PushInfo{Ref: "refs/heads/main", Before: "before-sha", After: "after-sha"},
	}
	handler := newEventWebhookHandler(t, st, push)
	deliverWebhook(t, handler, "push", "delivery-p", makePushWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "after-sha", "refs/heads/main"))

	require.Len(t, st.CreateJobCalls, 1)
	job := st.CreateJobCalls[0]
	assert.Equal(t, "Eval event push main (after-s) on test-org/test-repo", job.Name)
	assert.Equal(t, "before-sha", job.JobEnvVars["REACTORCIDE_CI_SOURCE_REF"], "the trusted CI base is the commit before the push")
	assert.Equal(t, "before-sha", job.CISHA)
	assert.Equal(t, "before-sha", job.JobEnvVars["REACTORCIDE_DIFF_BASE"])
	assert.Equal(t, "true", job.JobEnvVars["REACTORCIDE_EVAL_DUAL_CI"])
	assert.Equal(t, "after-sha", job.JobEnvVars["REACTORCIDE_SHA"])
	assert.Equal(t, `["vcs_user:github/pusher"]`, job.JobEnvVars["REACTORCIDE_ACTOR_SUBJECTS"])
	assert.Equal(t, parsedPolicy.Revision, job.JobEnvVars["REACTORCIDE_CI_POLICY_REVISION"])
	require.Len(t, st.events, 1)
	assert.Equal(t, "before-sha", st.events[0].BaseSHA)
	assert.Equal(t, "github:test-org/test-repo:push:refs/heads/main:after-sha:0", st.events[0].SemanticKey)
}

func TestPushCIBaseRef(t *testing.T) {
	newBranch := &vcs.WebhookEvent{Repository: vcs.RepositoryInfo{DefaultBranch: "main"},
		Push: &vcs.PushInfo{Before: "0000000000000000000000000000000000000000", After: "new"}}
	assert.Equal(t, "main", pushCIBaseRef(newBranch, "new"), "a new branch trusts the default branch")
	newBranch.Repository.DefaultBranch = ""
	assert.Equal(t, "new", pushCIBaseRef(newBranch, "new"), "with no other revision the pushed commit stays the CI source")
	normal := &vcs.WebhookEvent{Push: &vcs.PushInfo{Before: "old", After: "new"}}
	assert.Equal(t, "old", pushCIBaseRef(normal, "new"))

	// The default branch and tags are trusted: they run their own CI.
	toMain := &vcs.WebhookEvent{Repository: vcs.RepositoryInfo{DefaultBranch: "main"},
		Push: &vcs.PushInfo{Ref: "refs/heads/main", Before: "old", After: "new"}}
	assert.Equal(t, "new", pushCIBaseRef(toMain, "new"), "a push to the default branch uses the pushed commit")
	toBranch := &vcs.WebhookEvent{Repository: vcs.RepositoryInfo{DefaultBranch: "main"},
		Push: &vcs.PushInfo{Ref: "refs/heads/dev", Before: "old", After: "new"}}
	assert.Equal(t, "old", pushCIBaseRef(toBranch, "new"), "a push to another branch stays policy-checked")
	tag := &vcs.WebhookEvent{GenericEvent: vcs.EventTagCreated, Repository: vcs.RepositoryInfo{DefaultBranch: "main"},
		Push: &vcs.PushInfo{Ref: "refs/tags/v1.0.0", Before: "0000000000000000000000000000000000000000", After: "new"}}
	assert.Equal(t, "new", pushCIBaseRef(tag, "new"), "a tag uses the tagged commit")
}
