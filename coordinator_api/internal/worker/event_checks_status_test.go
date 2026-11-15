package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// withStatusContext sets status_context on the first workflow of an eval payload.
func withStatusContext(t *testing.T, data []byte, statusContext string) []byte {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	payload["workflows"].([]any)[0].(map[string]any)["status_context"] = statusContext
	out, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func onlyWorkflow(t *testing.T, st *eventRecordingStore) *models.WorkflowInstance {
	t.Helper()
	if len(st.base.workflows) != 1 {
		t.Fatalf("got %d workflows, want 1", len(st.base.workflows))
	}
	for _, wf := range st.base.workflows {
		return wf
	}
	return nil
}

func TestEventWorkflowStatusContext(t *testing.T) {
	tests := []struct {
		name     string
		declared string
		want     string
	}{
		{"no declared context posts nothing of its own", "", ""},
		{"declared context is kept", "lint", "lint"},
		{"declared context is trimmed", "  lint  ", "lint"},
		{"the aggregate name cannot be claimed", "Reactorcide Checks", ""},
		{"an over-long context is dropped", string(make([]byte, 101)), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newEventRecordingStore()
			parent := eventEvalParent(t, "eval-1", "event-1")
			data := evalPayload(t, parent, true)
			if tt.declared != "" {
				data = withStatusContext(t, data, tt.declared)
			}
			st.process(t, parent, data)
			if got := onlyWorkflow(t, st).StatusContext; got != tt.want {
				t.Fatalf("StatusContext = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLegacyWorkflowKeepsNameAsStatusContext(t *testing.T) {
	st := newEventRecordingStore()
	parent := eventEvalParent(t, "eval-1", "event-1")
	parent.EventID = nil
	st.process(t, parent, evalPayload(t, parent, true))
	if got := onlyWorkflow(t, st).StatusContext; got != "Build" {
		t.Fatalf("legacy StatusContext = %q, want the workflow name", got)
	}
}

type checksRecordingUpdater struct {
	published []string
}

func (u *checksRecordingUpdater) UpdateJobStatus(context.Context, *models.Job) error { return nil }
func (u *checksRecordingUpdater) PublishEventChecks(_ context.Context, eventID string) error {
	u.published = append(u.published, eventID)
	return nil
}

func TestEvalDecisionsRecomputeEventChecks(t *testing.T) {
	st := newEventRecordingStore()
	parent := eventEvalParent(t, "eval-1", "event-1")
	st.base.GetJobByIDFunc = func(_ context.Context, _ string) (*models.Job, error) {
		copied := *parent
		return &copied, nil
	}
	processor := NewTriggerProcessor(st, nil)
	updater := &checksRecordingUpdater{}
	processor.SetStatusUpdater(updater)
	if _, err := processor.ProcessTriggersFromDataWithOutcomes(context.Background(), evalPayload(t, parent, true), "", parent); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range updater.published {
		if id == "event-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("event checks not recomputed after decisions; published %v", updater.published)
	}
}
