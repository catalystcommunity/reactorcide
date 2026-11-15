package evaloutcome

import (
	"context"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// fakeStore is a CIEventStore plus the schedule outcome stores. Only the
// methods Finish uses do anything.
type fakeStore struct {
	store.CIEventStore
	event       models.CIEvent
	statuses    []string
	lastErrors  map[string]string
	failedFires map[string]string
}

func (f *fakeStore) SetCIEventStatus(_ context.Context, eventID, status string) error {
	f.statuses = append(f.statuses, status)
	return nil
}

func (f *fakeStore) GetCIEvent(context.Context, string) (*models.CIEvent, error) {
	copy := f.event
	return &copy, nil
}

func (f *fakeStore) FailStaleScheduleFires(context.Context, time.Duration, string) (int64, error) {
	return 0, nil
}

func (f *fakeStore) SetScheduleLastError(_ context.Context, scheduleID, message string) error {
	f.lastErrors[scheduleID] = message
	return nil
}

func (f *fakeStore) FailScheduleFireForEvent(_ context.Context, eventID, message string) error {
	f.failedFires[eventID] = message
	return nil
}

func newFake(scheduleID string) *fakeStore {
	f := &fakeStore{lastErrors: map[string]string{}, failedFires: map[string]string{}}
	f.event = models.CIEvent{EventID: "event-1"}
	if scheduleID != "" {
		f.event.ScheduleID = &scheduleID
	}
	return f
}

func evalJob() *models.Job {
	event := "event-1"
	return &models.Job{JobID: "job-1", EventID: &event, JobEnvVars: models.JSONB{"REACTORCIDE_JOB_KIND": "eval"}}
}

func TestFinishScheduledEvalFailureRecordsScheduleAndFire(t *testing.T) {
	for status, want := range map[string]string{
		"failed":    "eval job job-1 failed",
		"timeout":   "eval job job-1 timed out",
		"cancelled": "eval job job-1 was cancelled",
	} {
		f := newFake("sched-1")
		Finish(context.Background(), f, evalJob(), status)
		if len(f.statuses) != 1 || f.statuses[0] != models.CIEventStatusFailed {
			t.Fatalf("%s: event statuses = %v, want [failed]", status, f.statuses)
		}
		if got := f.lastErrors["sched-1"]; got != want {
			t.Errorf("%s: schedule last_error = %q, want %q", status, got, want)
		}
		if got := f.failedFires["event-1"]; got != want {
			t.Errorf("%s: fire error = %q, want %q", status, got, want)
		}
	}
}

func TestFinishCompletedOrUnscheduledLeavesSchedulesAlone(t *testing.T) {
	f := newFake("sched-1")
	Finish(context.Background(), f, evalJob(), "completed")
	if len(f.statuses) != 1 || f.statuses[0] != models.CIEventStatusEvaluated {
		t.Fatalf("event statuses = %v, want [evaluated]", f.statuses)
	}
	if len(f.lastErrors) != 0 || len(f.failedFires) != 0 {
		t.Fatalf("a completed eval must not touch the schedule: %v %v", f.lastErrors, f.failedFires)
	}

	f = newFake("")
	Finish(context.Background(), f, evalJob(), "failed")
	if len(f.statuses) != 1 || f.statuses[0] != models.CIEventStatusFailed {
		t.Fatalf("event statuses = %v, want [failed]", f.statuses)
	}
	if len(f.lastErrors) != 0 || len(f.failedFires) != 0 {
		t.Fatalf("a webhook event has no schedule: %v %v", f.lastErrors, f.failedFires)
	}
}

func TestFinishIgnoresNonEvalJobs(t *testing.T) {
	node := "node-1"
	cases := map[string]*models.Job{
		"nil":      nil,
		"no event": {JobID: "j", JobEnvVars: models.JSONB{"REACTORCIDE_JOB_KIND": "eval"}},
		"not eval": func() *models.Job { j := evalJob(); j.JobEnvVars = models.JSONB{}; return j }(),
		"node job": func() *models.Job { j := evalJob(); j.WorkflowNodeID = &node; return j }(),
	}
	for name, job := range cases {
		f := newFake("sched-1")
		Finish(context.Background(), f, job, "failed")
		if len(f.statuses) != 0 || len(f.lastErrors) != 0 {
			t.Errorf("%s: Finish must ignore it, got %v %v", name, f.statuses, f.lastErrors)
		}
	}
}

func TestFinishWithoutEventStoreIsANoop(t *testing.T) {
	Finish(context.Background(), struct{}{}, evalJob(), "failed")
}
