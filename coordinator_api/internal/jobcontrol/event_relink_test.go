package jobcontrol

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// relinkStore adds a CI event store to the retry fake.
type relinkStore struct {
	*retryMockStore
	event    models.CIEvent
	relinked string
}

func (s *relinkStore) RecordCIEvent(context.Context, *models.CIEvent) (*models.CIEvent, store.CIEventRecordOutcome, error) {
	return nil, "", nil
}
func (s *relinkStore) GetCIEvent(_ context.Context, id string) (*models.CIEvent, error) {
	if id != s.event.EventID {
		return nil, store.ErrNotFound
	}
	copy := s.event
	return &copy, nil
}
func (s *relinkStore) AttachCIEventEvalJob(context.Context, string, string) error { return nil }
func (s *relinkStore) SetCIEventStatus(context.Context, string, string) error     { return nil }
func (s *relinkStore) SetCIEventSummary(context.Context, string, models.CIEventSummary) error {
	return nil
}
func (s *relinkStore) UpsertCIEventWorkflowDecisions(context.Context, []models.CIEventWorkflowDecision) error {
	return nil
}
func (s *relinkStore) ListCIEventWorkflowDecisions(context.Context, string) ([]models.CIEventWorkflowDecision, error) {
	return nil, nil
}
func (s *relinkStore) GetCIEventWorkflowDecisionByWorkflow(context.Context, string) (*models.CIEventWorkflowDecision, error) {
	return nil, store.ErrNotFound
}
func (s *relinkStore) ClaimCIEventForEval(context.Context, string) (bool, error) { return false, nil }
func (s *relinkStore) ReleaseCIEventClaim(context.Context, string) error         { return nil }
func (s *relinkStore) RelinkCIEventEvalJob(_ context.Context, eventID, jobID string) error {
	s.relinked = jobID
	return nil
}

func TestRetryJob_RelinksEventToRetriedEval(t *testing.T) {
	eventID := "00000000-0000-0000-0000-000000000001"
	st := &relinkStore{retryMockStore: newRetryMockStore()}
	evalJob := st.addJob(&models.Job{JobID: "eval-1", UserID: "u", Status: "failed", JobCommand: "runnerlib eval", EventID: &eventID})
	evalID := evalJob.JobID
	st.event = models.CIEvent{EventID: eventID, EvalJobID: &evalID, Status: models.CIEventStatusFailed}

	retried, err := RetryJob(context.Background(), st, corndogs.NewMockClient(), evalJob)
	if err != nil {
		t.Fatal(err)
	}
	if st.relinked != retried.JobID {
		t.Fatalf("event relinked to %q, want the retry %q", st.relinked, retried.JobID)
	}
}

func TestRetryJob_NodeRetryLeavesEventAlone(t *testing.T) {
	eventID := "00000000-0000-0000-0000-000000000001"
	st := &relinkStore{retryMockStore: newRetryMockStore()}
	evalID := "eval-1"
	st.event = models.CIEvent{EventID: eventID, EvalJobID: &evalID}
	node := st.addJob(&models.Job{JobID: "node-job", UserID: "u", Status: "failed", JobCommand: "make", EventID: &eventID})

	if _, err := RetryJob(context.Background(), st, corndogs.NewMockClient(), node); err != nil {
		t.Fatal(err)
	}
	if st.relinked != "" {
		t.Fatalf("a node retry must not relink the event, got %q", st.relinked)
	}
}
