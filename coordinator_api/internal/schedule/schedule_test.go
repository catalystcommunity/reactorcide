package schedule

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestNextFireAfterUsesTimezone(t *testing.T) {
	// 03:00 in New York is 08:00 UTC in winter (EST, -5).
	next, err := NextFireAfter("0 3 * * *", "America/New_York", mustTime(t, "2026-01-10T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if want := mustTime(t, "2026-01-11T08:00:00Z"); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
	// In summer (EDT, -4) the same schedule is 07:00 UTC.
	next, err = NextFireAfter("0 3 * * *", "America/New_York", mustTime(t, "2026-07-10T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if want := mustTime(t, "2026-07-11T07:00:00Z"); !next.Equal(want) {
		t.Fatalf("summer next = %s, want %s", next, want)
	}
}

func TestNextFireAfterAcrossSpringForward(t *testing.T) {
	// 2026-03-08 02:30 does not exist in New York. The fire after midnight
	// that day must land on a real instant after the gap, not be skipped
	// into the next day.
	next, err := NextFireAfter("30 2 * * *", "America/New_York", mustTime(t, "2026-03-08T05:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if next.Before(mustTime(t, "2026-03-08T07:00:00Z")) || next.After(mustTime(t, "2026-03-09T07:00:00Z")) {
		t.Fatalf("next across DST gap = %s", next)
	}
}

func TestPlanFireFiresNewestMissedOnly(t *testing.T) {
	// Hourly schedule, due at 01:00, scheduler back at 05:30: fire 05:00
	// once, next at 06:00. Not 01:00, 02:00, 03:00, 04:00, 05:00.
	fire, next, err := PlanFire("0 * * * *", "UTC", mustTime(t, "2026-05-01T01:00:00Z"), mustTime(t, "2026-05-01T05:30:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if want := mustTime(t, "2026-05-01T05:00:00Z"); !fire.Equal(want) {
		t.Fatalf("fire = %s, want %s", fire, want)
	}
	if want := mustTime(t, "2026-05-01T06:00:00Z"); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestPlanFireOnTime(t *testing.T) {
	due := mustTime(t, "2026-05-01T03:00:00Z")
	fire, next, err := PlanFire("@daily", "UTC", due, due.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !fire.Equal(due) {
		t.Fatalf("fire = %s, want %s", fire, due)
	}
	if want := mustTime(t, "2026-05-02T00:00:00Z"); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestParseSpecRejects(t *testing.T) {
	for name, tc := range map[string][2]string{
		"empty":         {"", "UTC"},
		"seconds field": {"0 0 3 * * *", "UTC"},
		"bad tz":        {"0 3 * * *", "Mars/Olympus"},
		"inline tz":     {"CRON_TZ=UTC 0 3 * * *", "UTC"},
		"garbage":       {"every day", "UTC"},
	} {
		if err := Validate(tc[0], tc[1]); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if err := Validate("*/15 * * * *", ""); err != nil {
		t.Errorf("empty timezone should mean UTC: %v", err)
	}
}

func TestValidateDefinition(t *testing.T) {
	ok := [][3]string{
		{"nightly", "main", "nightly-build"},
		{"Nightly build", "release/1.2", ".reactorcide/workflows/nightly.yaml"},
		{"n", "main", ".reactorcide/workflows/n.yml"},
	}
	for _, c := range ok {
		if err := ValidateDefinition(c[0], c[1], c[2]); err != nil {
			t.Errorf("%v: %v", c, err)
		}
	}
	bad := [][3]string{
		{"", "main", "x"},
		{"n", "", "x"},
		{"n", "has space", "x"},
		{"n", "a/../b", "x"},
		{"n", "main", ""},
		{"n", "main", "../etc/passwd"},
		{"n", "main", ".reactorcide/workflows/../../x.yaml"},
		{"n", "main", ".reactorcide/workflows/sub/x.yaml"},
		{"n", "main", ".reactorcide/jobs/x.yaml"},
		{"n", "main", ".reactorcide/workflows/x.txt"},
	}
	for _, c := range bad {
		if err := ValidateDefinition(c[0], c[1], c[2]); err == nil {
			t.Errorf("%v: expected an error", c)
		}
	}
}

// fakeStore is an in-memory ScheduleStore with the same exactly-once rule as
// the Postgres store: a fire row for (schedule, time) is created once.
type fakeStore struct {
	mu        sync.Mutex
	schedules map[string]*models.CISchedule
	fires     map[string]*models.CIScheduleFire
}

func newFakeStore(schedules ...models.CISchedule) *fakeStore {
	s := &fakeStore{schedules: map[string]*models.CISchedule{}, fires: map[string]*models.CIScheduleFire{}}
	for i := range schedules {
		sched := schedules[i]
		s.schedules[sched.ScheduleID] = &sched
	}
	return s
}

func fireKey(id string, at time.Time) string { return id + "|" + at.UTC().Format(time.RFC3339Nano) }

func (s *fakeStore) CreateSchedule(context.Context, *models.CISchedule) error { return nil }
func (s *fakeStore) GetSchedule(_ context.Context, id string) (*models.CISchedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sched, ok := s.schedules[id]; ok {
		copy := *sched
		return &copy, nil
	}
	return nil, store.ErrNotFound
}
func (s *fakeStore) ListSchedulesByProject(context.Context, string) ([]models.CISchedule, error) {
	return nil, nil
}
func (s *fakeStore) UpdateSchedule(context.Context, *models.CISchedule) error { return nil }
func (s *fakeStore) DeleteSchedule(context.Context, string) error             { return nil }
func (s *fakeStore) ListScheduleFires(context.Context, string, int) ([]models.CIScheduleFire, error) {
	return nil, nil
}

func (s *fakeStore) ClaimDueScheduleFires(_ context.Context, now time.Time, limit int, plan store.ScheduleFirePlan) ([]store.ClaimedScheduleFire, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.ClaimedScheduleFire
	for _, sched := range s.schedules {
		if len(out) >= limit {
			break
		}
		if !sched.Enabled || sched.NextFireAt == nil || sched.NextFireAt.After(now) {
			continue
		}
		fireTime, next, err := plan(*sched, now)
		if err != nil {
			sched.LastError = err.Error()
			retry := now.Add(time.Hour)
			sched.NextFireAt = &retry
			continue
		}
		sched.NextFireAt = &next
		key := fireKey(sched.ScheduleID, fireTime)
		if _, exists := s.fires[key]; exists {
			continue
		}
		s.fires[key] = &models.CIScheduleFire{ScheduleID: sched.ScheduleID, FireTime: fireTime, Status: models.ScheduleFireFiring}
		out = append(out, store.ClaimedScheduleFire{Schedule: *sched, FireTime: fireTime})
	}
	return out, nil
}

func (s *fakeStore) RecordScheduleFireResult(_ context.Context, id string, at time.Time, eventID *string, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fire := s.fires[fireKey(id, at)]
	if fire == nil {
		return errors.New("no fire row")
	}
	fire.Status = models.ScheduleFireFired
	if msg != "" {
		fire.Status = models.ScheduleFireFailed
	}
	fire.EventID, fire.Error = eventID, msg
	sched := s.schedules[id]
	sched.LastFireAt, sched.LastError = &at, msg
	if eventID != nil {
		sched.LastEventID = eventID
	}
	return nil
}

type countingFirer struct {
	mu    sync.Mutex
	calls []time.Time
	err   error
}

func (f *countingFirer) Fire(_ context.Context, _ models.CISchedule, at time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, at)
	if f.err != nil {
		return "", f.err
	}
	return "01EVENT", nil
}

func TestSchedulerFiresNewestMissedOnceAndRecords(t *testing.T) {
	due := mustTime(t, "2026-05-01T01:00:00Z")
	st := newFakeStore(models.CISchedule{ScheduleID: "s1", Cron: "0 * * * *", Timezone: "UTC", Enabled: true, NextFireAt: &due})
	firer := &countingFirer{}
	now := mustTime(t, "2026-05-01T05:30:00Z")
	sched := &Scheduler{Store: st, Firer: firer, Now: func() time.Time { return now }}

	n, err := sched.RunOnce(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("RunOnce = %d, %v; want 1 fire", n, err)
	}
	if len(firer.calls) != 1 || !firer.calls[0].Equal(mustTime(t, "2026-05-01T05:00:00Z")) {
		t.Fatalf("fires = %v, want one at 05:00", firer.calls)
	}
	got, _ := st.GetSchedule(context.Background(), "s1")
	if got.LastEventID == nil || *got.LastEventID != "01EVENT" || got.LastError != "" {
		t.Fatalf("schedule result not recorded: %+v", got)
	}
	if !got.NextFireAt.Equal(mustTime(t, "2026-05-01T06:00:00Z")) {
		t.Fatalf("next = %s", got.NextFireAt)
	}

	// A second pass at the same instant fires nothing.
	if n, _ := sched.RunOnce(context.Background()); n != 0 {
		t.Fatalf("second pass fired %d", n)
	}
}

func TestSchedulerConcurrentPassesFireOnce(t *testing.T) {
	due := mustTime(t, "2026-05-01T03:00:00Z")
	st := newFakeStore(models.CISchedule{ScheduleID: "s1", Cron: "0 3 * * *", Timezone: "UTC", Enabled: true, NextFireAt: &due})
	firer := &countingFirer{}
	now := due.Add(time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &Scheduler{Store: st, Firer: firer, Now: func() time.Time { return now }}
			_, _ = s.RunOnce(context.Background())
		}()
	}
	wg.Wait()
	if len(firer.calls) != 1 {
		t.Fatalf("fired %d times, want exactly once", len(firer.calls))
	}
}

func TestSchedulerRecordsFirerErrorWithoutDeleting(t *testing.T) {
	due := mustTime(t, "2026-05-01T03:00:00Z")
	st := newFakeStore(models.CISchedule{ScheduleID: "s1", Cron: "0 3 * * *", Timezone: "UTC", Enabled: true, NextFireAt: &due})
	sched := &Scheduler{Store: st, Firer: &countingFirer{err: errors.New("workflow not found")}, Now: func() time.Time { return due }}
	if _, err := sched.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSchedule(context.Background(), "s1")
	if err != nil {
		t.Fatal("schedule must not be deleted by a failed fire")
	}
	if got.LastError != "workflow not found" || !got.Enabled {
		t.Fatalf("schedule after failed fire: %+v", got)
	}
	fire := st.fires[fireKey("s1", due)]
	if fire.Status != models.ScheduleFireFailed || fire.Error != "workflow not found" {
		t.Fatalf("fire row: %+v", fire)
	}
}

func TestUnwiredFirerDefault(t *testing.T) {
	due := mustTime(t, "2026-05-01T03:00:00Z")
	st := newFakeStore(models.CISchedule{ScheduleID: "s1", Cron: "0 3 * * *", Timezone: "UTC", Enabled: true, NextFireAt: &due})
	if _, err := (&Scheduler{Store: st, Now: func() time.Time { return due }}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetSchedule(context.Background(), "s1")
	if got.LastError != ErrFirerNotWired.Error() {
		t.Fatalf("last error = %q", got.LastError)
	}
}

func TestSchedulerSkipsDisabledAndFuture(t *testing.T) {
	now := mustTime(t, "2026-05-01T03:00:00Z")
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	st := newFakeStore(
		models.CISchedule{ScheduleID: "off", Cron: "0 * * * *", Timezone: "UTC", Enabled: false, NextFireAt: &past},
		models.CISchedule{ScheduleID: "later", Cron: "0 * * * *", Timezone: "UTC", Enabled: true, NextFireAt: &future},
	)
	firer := &countingFirer{}
	if n, _ := (&Scheduler{Store: st, Firer: firer, Now: func() time.Time { return now }}).RunOnce(context.Background()); n != 0 {
		t.Fatalf("fired %d", n)
	}
}
