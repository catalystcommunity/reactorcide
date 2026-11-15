package jobcontrol

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type closingEventStore struct {
	store.CIEventStore
	statuses []string
	order    *[]string
}

func (s *closingEventStore) SetCIEventStatus(_ context.Context, _ string, status string) error {
	s.statuses = append(s.statuses, status)
	*s.order = append(*s.order, "close:"+status)
	return nil
}

func (s *closingEventStore) GetCIEvent(context.Context, string) (*models.CIEvent, error) {
	return &models.CIEvent{EventID: "event-1"}, nil
}

// A queued eval cancelled straight to "cancelled" gets no worker result, so
// the cancel path closes its event, and does so BEFORE the status recompute.
// A running eval only reaches "cancelling"; the worker result closes it.
func TestPublishJobEventChecksClosesCancelledEvalEventFirst(t *testing.T) {
	var order []string
	previous := eventChecksPublisher
	t.Cleanup(func() { eventChecksPublisher = previous })
	SetEventChecksPublisher(func(_ context.Context, eventID string) error {
		order = append(order, "publish:"+eventID)
		return nil
	})
	event := "event-1"
	job := func(status string) *models.Job {
		return &models.Job{JobID: "eval-1", Status: status, EventID: &event,
			JobEnvVars: models.JSONB{"REACTORCIDE_JOB_KIND": "eval"}}
	}

	st := &closingEventStore{order: &order}
	publishJobEventChecks(context.Background(), st, job("cancelled"))
	if len(order) != 2 || order[0] != "close:failed" || order[1] != "publish:event-1" {
		t.Fatalf("order = %v, want event closed as failed, then published", order)
	}

	order = nil
	st = &closingEventStore{order: &order}
	publishJobEventChecks(context.Background(), st, job("cancelling"))
	if len(st.statuses) != 0 {
		t.Fatalf("a cancelling eval must not close its event yet: %v", st.statuses)
	}
	if len(order) != 1 || order[0] != "publish:event-1" {
		t.Fatalf("order = %v, want only the publish", order)
	}
}
