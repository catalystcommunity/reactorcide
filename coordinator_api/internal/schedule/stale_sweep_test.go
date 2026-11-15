package schedule

import (
	"context"
	"testing"
	"time"
)

// sweepingStore adds the maintenance writes to the fake store.
type sweepingStore struct {
	*fakeStore
	sweeps    []time.Duration
	messages  []string
	lastError map[string]string
}

func (s *sweepingStore) FailStaleScheduleFires(_ context.Context, olderThan time.Duration, message string) (int64, error) {
	s.sweeps = append(s.sweeps, olderThan)
	s.messages = append(s.messages, message)
	return 1, nil
}

func (s *sweepingStore) SetScheduleLastError(_ context.Context, id, message string) error {
	if s.lastError == nil {
		s.lastError = map[string]string{}
	}
	s.lastError[id] = message
	return nil
}

func TestSchedulerPassSweepsStaleFires(t *testing.T) {
	st := &sweepingStore{fakeStore: newFakeStore()}
	sched := &Scheduler{Store: st, Firer: &countingFirer{}}
	if _, err := sched.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.sweeps) != 1 || st.sweeps[0] != StaleFireAfter {
		t.Fatalf("sweeps = %v, want one sweep of %s", st.sweeps, StaleFireAfter)
	}
	if st.messages[0] != StaleFireMessage {
		t.Fatalf("message = %q", st.messages[0])
	}
	if StaleFireAfter <= fireTimeout {
		t.Fatalf("StaleFireAfter (%s) must exceed fireTimeout (%s), or a slow live fire is failed", StaleFireAfter, fireTimeout)
	}
}

func TestSchedulerPassWithoutMaintenanceStore(t *testing.T) {
	sched := &Scheduler{Store: newFakeStore(), Firer: &countingFirer{}}
	if _, err := sched.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}
