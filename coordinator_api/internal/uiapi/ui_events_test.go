package uiapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// eventFakeStore adds the CI event store to the shared fake.
type eventFakeStore struct {
	*fakeStore
	events    map[string]*models.CIEvent
	decisions map[string][]models.CIEventWorkflowDecision
}

func (s *eventFakeStore) RecordCIEvent(context.Context, *models.CIEvent) (*models.CIEvent, store.CIEventRecordOutcome, error) {
	return nil, "", store.ErrInvalidInput
}
func (s *eventFakeStore) GetCIEvent(_ context.Context, id string) (*models.CIEvent, error) {
	if event, ok := s.events[id]; ok {
		return event, nil
	}
	return nil, store.ErrNotFound
}
func (s *eventFakeStore) AttachCIEventEvalJob(context.Context, string, string) error { return nil }
func (s *eventFakeStore) SetCIEventStatus(context.Context, string, string) error     { return nil }
func (s *eventFakeStore) SetCIEventSummary(context.Context, string, models.CIEventSummary) error {
	return nil
}
func (s *eventFakeStore) UpsertCIEventWorkflowDecisions(context.Context, []models.CIEventWorkflowDecision) error {
	return nil
}
func (s *eventFakeStore) ListCIEventWorkflowDecisions(_ context.Context, id string) ([]models.CIEventWorkflowDecision, error) {
	return s.decisions[id], nil
}
func (s *eventFakeStore) GetCIEventWorkflowDecisionByWorkflow(_ context.Context, workflowID string) (*models.CIEventWorkflowDecision, error) {
	for _, decisions := range s.decisions {
		for i := range decisions {
			if decisions[i].WorkflowID != nil && *decisions[i].WorkflowID == workflowID {
				return &decisions[i], nil
			}
		}
	}
	return nil, store.ErrNotFound
}

func newEventFixture(t *testing.T) (*eventFakeStore, visibilityFixture, *UiService, context.Context, context.Context) {
	t.Helper()
	deps, st := newTestDeps(t)
	fixture := newVisibilityFixture(t, st)
	events := &eventFakeStore{fakeStore: st, events: map[string]*models.CIEvent{}, decisions: map[string][]models.CIEventWorkflowDecision{}}
	deps.Store = events
	matched := true
	count := 2
	for _, item := range []struct {
		id  string
		job models.Job
		wf  models.WorkflowInstance
	}{{"event-public", fixture.publicJob, fixture.publicWF}, {"event-private", fixture.privateJob, fixture.privateWF}} {
		workflowID := item.wf.WorkflowID
		pr := 7
		events.events[item.id] = &models.CIEvent{EventID: item.id, OrgID: "org-1", ProjectID: item.job.ProjectID,
			Source: models.CIEventSourceWebhook, Provider: "github", EventType: "pull_request_updated", Repo: "org/repo",
			CommitSHA: "abc", PRNumber: &pr, Status: models.CIEventStatusEvaluated, EvalJobID: &item.job.JobID,
			Summary: models.CIEventSummary{ChangedFilesCount: 120, ChangedFilesPreview: []string{"api/a.go"}, Action: "synchronize"}}
		events.decisions[item.id] = []models.CIEventWorkflowDecision{
			{EventID: item.id, WorkflowSecurityID: "ci", WorkflowName: "CI", Decision: models.WorkflowDecisionRun, WorkflowID: &workflowID,
				Reasons: []models.DecisionReason{
					{Kind: "paths", Matched: &matched, Include: []string{"api/**"}, MatchedFiles: []string{"api/a.go"}, MatchedCount: &count},
					{Kind: "policy", CIOrigin: "base", Profile: "standard", Reasons: []string{"no CI definition changed"}},
				}},
			{EventID: item.id, WorkflowSecurityID: "docs", WorkflowName: "Docs", Decision: models.WorkflowDecisionSkipped,
				Reasons: []models.DecisionReason{{Kind: "event", Value: "pull_request_updated", Patterns: []string{"push"}}}},
		}
	}
	ui := NewUiService(deps)
	return events, fixture, ui, mintSessionCtx(t, deps, fixture.owner.UserID), mintSessionCtx(t, deps, fixture.outsider.UserID)
}

func TestGetEventReturnsDecisionsWithReasons(t *testing.T) {
	_, fixture, ui, ownerCtx, _ := newEventFixture(t)
	resp, err := ui.GetEvent(ownerCtx, csilapi.GetEventRequest{EventId: "event-private"})
	requireOK(t, err)
	if resp.Event.EventId != "event-private" || resp.Event.ChangedFilesCount != 120 || *resp.Event.Action != "synchronize" || *resp.Event.PrNumber != 7 {
		t.Fatalf("event = %+v", resp.Event)
	}
	if len(resp.Decisions) != 2 || len(resp.WorkflowIds) != 1 || resp.WorkflowIds[0] != fixture.privateWF.WorkflowID {
		t.Fatalf("decisions = %+v workflows = %v", resp.Decisions, resp.WorkflowIds)
	}
	paths := resp.Decisions[0].Reasons[0]
	if paths.Kind != "paths" || paths.PathsInclude[0] != "api/**" || *paths.MatchedCount != 2 || paths.MatchedFiles[0] != "api/a.go" {
		t.Fatalf("paths reason = %+v", paths)
	}
	if policy := resp.Decisions[0].Reasons[1]; *policy.CiOrigin != "base" || policy.Reasons[0] != "no CI definition changed" {
		t.Fatalf("policy reason = %+v", policy)
	}
}

func TestGetEventHidesPrivateProjectEvents(t *testing.T) {
	_, _, ui, _, outsiderCtx := newEventFixture(t)
	_, err := ui.GetEvent(context.Background(), csilapi.GetEventRequest{EventId: "event-private"})
	requireCode(t, err, "not_found")
	_, err = ui.GetEvent(outsiderCtx, csilapi.GetEventRequest{EventId: "event-private"})
	requireCode(t, err, "not_found")
	// A public project's event is visible like its workflows.
	_, err = ui.GetEvent(outsiderCtx, csilapi.GetEventRequest{EventId: "event-public"})
	requireOK(t, err)
	_, err = ui.GetEvent(outsiderCtx, csilapi.GetEventRequest{EventId: "no-such-event"})
	requireCode(t, err, "not_found")
}

func TestGetWorkflowCarriesWhy(t *testing.T) {
	_, fixture, ui, ownerCtx, _ := newEventFixture(t)
	resp, err := ui.GetWorkflow(ownerCtx, csilapi.GetWorkflowRequest{WorkflowId: fixture.publicWF.WorkflowID})
	requireOK(t, err)
	if resp.Why == nil || resp.Why.WorkflowSecurityId != "ci" || resp.Why.Decision != "run" || len(resp.Why.Reasons) != 2 {
		t.Fatalf("why = %+v", resp.Why)
	}
}
