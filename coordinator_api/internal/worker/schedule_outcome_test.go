package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// scheduleEventStore is the event-recording fake for an event a schedule
// fired, with the schedule maintenance writes.
type scheduleEventStore struct {
	*eventRecordingStore
	scheduleID string
	lastErrors []string
}

func (s *scheduleEventStore) GetCIEvent(_ context.Context, eventID string) (*models.CIEvent, error) {
	id := s.scheduleID
	return &models.CIEvent{EventID: eventID, Source: models.CIEventSourceSchedule, ScheduleID: &id}, nil
}
func (s *scheduleEventStore) FailStaleScheduleFires(context.Context, time.Duration, string) (int64, error) {
	return 0, nil
}
func (s *scheduleEventStore) SetScheduleLastError(_ context.Context, scheduleID, message string) error {
	if scheduleID == s.scheduleID {
		s.lastErrors = append(s.lastErrors, message)
	}
	return nil
}

// process runs the eval result with this outer store, so the processor sees
// the schedule methods (the embedded helper would pass the inner fake).
func (s *scheduleEventStore) process(t *testing.T, parent *models.Job, data []byte) {
	t.Helper()
	s.base.GetJobByIDFunc = func(context.Context, string) (*models.Job, error) {
		copy := *parent
		return &copy, nil
	}
	if _, err := NewTriggerProcessor(s, nil).ProcessTriggersFromDataWithOutcomes(context.Background(), data, "", parent); err != nil {
		t.Fatal(err)
	}
}

func scheduledEvalPayload(t *testing.T, parent *models.Job, decisions []map[string]any, withWorkflow bool) []byte {
	t.Helper()
	workflows := []map[string]any{}
	if withWorkflow {
		workflows = append(workflows, map[string]any{
			"id": "nightly", "name": "Nightly", "source_file": ".reactorcide/workflows/nightly.yaml",
			"ci_origin": "base", "ci_repository": parent.CIRepository, "ci_sha": parent.CISHA,
			"execution_profile": "standard", "worker_class": "default", "event_matched": true,
			"jobs": []map[string]any{{"job_name": "soak", "job_command": "make soak"}},
		})
	}
	data, err := json.Marshal(map[string]any{
		"type": "trigger_job", "trigger_type": "runnerlib_eval", "workflows": workflows, "decisions": decisions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestScheduledEval_MissingWorkflowSetsScheduleError(t *testing.T) {
	st := &scheduleEventStore{eventRecordingStore: newEventRecordingStore(), scheduleID: "11111111-1111-1111-1111-111111111111"}
	parent := eventEvalParent(t, "eval-job", "00000000-0000-0000-0000-000000000001")
	st.process(t, parent, scheduledEvalPayload(t, parent, []map[string]any{{
		"id": "ref:.reactorcide/workflows/nightly.yaml", "name": ".reactorcide/workflows/nightly.yaml",
		"source_file": ".reactorcide/workflows/nightly.yaml", "decision": "invalid",
		"reasons": []map[string]any{{"kind": "invalid", "detail": "workflow not found"}},
	}}, false))

	if len(st.lastErrors) != 1 || st.lastErrors[0] != "workflow not found: .reactorcide/workflows/nightly.yaml" {
		t.Fatalf("schedule last_error writes = %q", st.lastErrors)
	}
	decision, ok := st.decisions["ref:.reactorcide/workflows/nightly.yaml"]
	if !ok || decision.Decision != models.WorkflowDecisionInvalid {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestScheduledEval_FoundWorkflowClearsScheduleError(t *testing.T) {
	st := &scheduleEventStore{eventRecordingStore: newEventRecordingStore(), scheduleID: "11111111-1111-1111-1111-111111111111"}
	parent := eventEvalParent(t, "eval-job", "00000000-0000-0000-0000-000000000001")
	st.process(t, parent, scheduledEvalPayload(t, parent, []map[string]any{{
		"id": "nightly", "name": "Nightly", "source_file": ".reactorcide/workflows/nightly.yaml", "decision": "run",
		"reasons": []map[string]any{{"kind": "schedule", "matched": true, "value": "0 3 * * *",
			"schedule_id": "11111111-1111-1111-1111-111111111111", "fire_time": "2026-11-15T03:00:00Z",
			"detail": "schedule 'nightly' fired at 2026-11-15T03:00:00Z"}},
	}}, true))

	if len(st.lastErrors) != 1 || st.lastErrors[0] != "" {
		t.Fatalf("schedule last_error writes = %q, want one clear", st.lastErrors)
	}
	reasons := st.decisions["nightly"].Reasons
	if len(reasons) == 0 || reasons[0].Kind != "schedule" || reasons[0].ScheduleID == "" || reasons[0].FireTime != "2026-11-15T03:00:00Z" {
		t.Fatalf("schedule reason not kept: %+v", reasons)
	}
}

func TestScheduledEval_NonScheduleEventLeavesSchedulesAlone(t *testing.T) {
	// The plain event fake has no schedule ID.
	st := newEventRecordingStore()
	parent := eventEvalParent(t, "eval-job", "00000000-0000-0000-0000-000000000001")
	st.process(t, parent, evalPayload(t, parent, true))
	// No ScheduleMaintenanceStore on this fake: nothing to assert beyond no panic.
}
