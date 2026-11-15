package test

// Postgres-backed coverage for migration 000042, the lease store, the lease
// manager and the schedule store: lease takeover after expiry and no double
// hold, schedule CRUD, and exactly-once fires under concurrent scheduler
// passes.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/lease"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/schedule"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestLeaseStoreAcquireRenewTakeover(t *testing.T) {
	ctx := context.Background()
	ls, ok := store.AppStore.(store.LeaseStore)
	require.True(t, ok, "store.AppStore must implement store.LeaseStore")
	name := uniqueName("lease")

	got, err := ls.TryAcquireLease(ctx, name, "a", 2*time.Second)
	require.NoError(t, err)
	require.True(t, got, "free lease is taken")

	got, err = ls.TryAcquireLease(ctx, name, "b", 2*time.Second)
	require.NoError(t, err)
	require.False(t, got, "held lease is not taken by another holder")

	got, err = ls.TryAcquireLease(ctx, name, "a", 2*time.Second)
	require.NoError(t, err)
	require.True(t, got, "holder renews")

	// Expire it, then b takes over and a can no longer renew.
	require.NoError(t, testDB.Exec("UPDATE coordinator_leases SET expires_at = now() - interval '1 second' WHERE name = ?", name).Error)
	got, err = ls.TryAcquireLease(ctx, name, "b", 2*time.Second)
	require.NoError(t, err)
	require.True(t, got, "expired lease is taken over")
	got, err = ls.TryAcquireLease(ctx, name, "a", 2*time.Second)
	require.NoError(t, err)
	require.False(t, got, "old holder cannot renew after takeover")

	// Release by a non-holder is a no-op; release by the holder frees it.
	require.NoError(t, ls.ReleaseLease(ctx, name, "a"))
	got, err = ls.TryAcquireLease(ctx, name, "c", 2*time.Second)
	require.NoError(t, err)
	require.False(t, got)
	require.NoError(t, ls.ReleaseLease(ctx, name, "b"))
	got, err = ls.TryAcquireLease(ctx, name, "c", 2*time.Second)
	require.NoError(t, err)
	require.True(t, got)
}

func TestLeaseStoreConcurrentAcquireHasOneWinner(t *testing.T) {
	ctx := context.Background()
	ls := store.AppStore.(store.LeaseStore)
	name := uniqueName("lease")
	var winners int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := ls.TryAcquireLease(ctx, name, fmt.Sprintf("h%d", i), 10*time.Second)
			if err == nil && got {
				atomic.AddInt32(&winners, 1)
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 1, winners)
}

func TestLeaseManagerSingleLeaderAndFailover(t *testing.T) {
	ls := store.AppStore.(store.LeaseStore)
	name := uniqueName("lease")
	var active, maxActive int32
	work := func(ctx context.Context) {
		n := atomic.AddInt32(&active, 1)
		for {
			m := atomic.LoadInt32(&maxActive)
			if n <= m || atomic.CompareAndSwapInt32(&maxActive, m, n) {
				break
			}
		}
		<-ctx.Done()
		atomic.AddInt32(&active, -1)
	}
	newManager := func(holder string) *lease.Manager {
		return &lease.Manager{Store: ls, Holder: holder, TTL: 1500 * time.Millisecond, RenewEvery: 200 * time.Millisecond}
	}

	ctxA, stopA := context.WithCancel(context.Background())
	doneA := make(chan struct{})
	go func() { defer close(doneA); newManager("a").Run(ctxA, name, work) }()
	require.Eventually(t, func() bool { return atomic.LoadInt32(&active) == 1 }, 3*time.Second, 20*time.Millisecond)

	ctxB, stopB := context.WithCancel(context.Background())
	defer stopB()
	doneB := make(chan struct{})
	go func() { defer close(doneB); newManager("b").Run(ctxB, name, work) }()
	time.Sleep(time.Second)
	require.EqualValues(t, 1, atomic.LoadInt32(&maxActive), "only one leader while a holds the lease")

	// Clean shutdown of a releases the lease; b takes over.
	stopA()
	<-doneA
	require.Eventually(t, func() bool { return atomic.LoadInt32(&active) == 1 }, 3*time.Second, 20*time.Millisecond)
	var holder string
	require.NoError(t, testDB.Raw("SELECT holder FROM coordinator_leases WHERE name = ?", name).Scan(&holder).Error)
	require.Equal(t, "b", holder)

	// Another holder steals the lease (as after a stall); b stops promptly.
	require.NoError(t, testDB.Exec("UPDATE coordinator_leases SET holder = 'thief', expires_at = now() + interval '1 hour' WHERE name = ?", name).Error)
	require.Eventually(t, func() bool { return atomic.LoadInt32(&active) == 0 }, 3*time.Second, 20*time.Millisecond)
	require.EqualValues(t, 1, atomic.LoadInt32(&maxActive))
	stopB()
	<-doneB
}

// Test schedules use 2099 dates and an injected clock, so a scheduler loop the
// router may start in this test process (real clock) never finds them due.
func newTestSchedule(t *testing.T, project *models.Project, name string, next time.Time) *models.CISchedule {
	t.Helper()
	ss := store.AppStore.(store.ScheduleStore)
	sched := &models.CISchedule{
		OrgID: project.OwnershipOrgID(), ProjectID: project.ProjectID, Name: name,
		Branch: "main", WorkflowRef: "nightly", Cron: "0 3 * * *", Timezone: "UTC",
		Enabled: true, NextFireAt: &next,
	}
	require.NoError(t, ss.CreateSchedule(context.Background(), sched))
	require.NotEmpty(t, sched.ScheduleID)
	return sched
}

func TestScheduleStoreCRUD(t *testing.T) {
	ctx := context.Background()
	ss, ok := store.AppStore.(store.ScheduleStore)
	require.True(t, ok, "store.AppStore must implement store.ScheduleStore")
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	next := time.Date(2099, 11, 15, 3, 0, 0, 0, time.UTC)

	sched := newTestSchedule(t, project, "nightly", next)
	dup := &models.CISchedule{OrgID: project.OwnershipOrgID(), ProjectID: project.ProjectID, Name: "nightly", Branch: "main", WorkflowRef: "x", Cron: "@daily", Timezone: "UTC"}
	require.ErrorIs(t, ss.CreateSchedule(ctx, dup), store.ErrAlreadyExists)

	got, err := ss.GetSchedule(ctx, sched.ScheduleID)
	require.NoError(t, err)
	require.Equal(t, "nightly", got.WorkflowRef)
	require.True(t, got.NextFireAt.Equal(next))

	got.Enabled = false
	got.NextFireAt = nil
	got.Cron = "0 4 * * *"
	require.NoError(t, ss.UpdateSchedule(ctx, got))
	again, err := ss.GetSchedule(ctx, sched.ScheduleID)
	require.NoError(t, err)
	require.False(t, again.Enabled)
	require.Nil(t, again.NextFireAt)
	require.Equal(t, "0 4 * * *", again.Cron)

	list, err := ss.ListSchedulesByProject(ctx, project.ProjectID)
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.NoError(t, ss.DeleteSchedule(ctx, sched.ScheduleID))
	_, err = ss.GetSchedule(ctx, sched.ScheduleID)
	require.ErrorIs(t, err, store.ErrNotFound)
	require.ErrorIs(t, ss.DeleteSchedule(ctx, sched.ScheduleID), store.ErrNotFound)
}

type recordingFirer struct {
	mu    sync.Mutex
	calls map[string]int
	err   error
}

func (f *recordingFirer) Fire(_ context.Context, sched models.CISchedule, at time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[sched.ScheduleID+"|"+at.UTC().Format(time.RFC3339)]++
	if f.err != nil {
		return "", f.err
	}
	return "0193b0c4-0000-7000-8000-000000000001", nil
}

func TestSchedulerExactlyOnceAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	ss := store.AppStore.(store.ScheduleStore)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	due := time.Date(2099, 11, 10, 3, 0, 0, 0, time.UTC)
	sched := newTestSchedule(t, project, "nightly", due)
	// Three days later: one fire for the newest missed time (Nov 13 03:00).
	now := time.Date(2099, 11, 13, 9, 0, 0, 0, time.UTC)

	firer := &recordingFirer{calls: map[string]int{}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &schedule.Scheduler{Store: ss, Firer: firer, Now: func() time.Time { return now }}
			_, _ = s.RunOnce(ctx)
		}()
	}
	wg.Wait()
	require.Len(t, firer.calls, 1)
	for key, n := range firer.calls {
		require.Equal(t, 1, n, key)
		require.Contains(t, key, "2099-11-13T03:00:00Z")
	}

	got, err := ss.GetSchedule(ctx, sched.ScheduleID)
	require.NoError(t, err)
	require.True(t, got.NextFireAt.Equal(time.Date(2099, 11, 14, 3, 0, 0, 0, time.UTC)), "next = %v", got.NextFireAt)
	require.NotNil(t, got.LastEventID)
	require.Equal(t, "", got.LastError)

	fires, err := ss.ListScheduleFires(ctx, sched.ScheduleID, 10)
	require.NoError(t, err)
	require.Len(t, fires, 1)
	require.Equal(t, models.ScheduleFireFired, fires[0].Status)
	require.NotNil(t, fires[0].CompletedAt)

	// A pass that re-plans the same fire time (stale next_fire_at) inserts
	// nothing new.
	stale := time.Date(2099, 11, 13, 3, 0, 0, 0, time.UTC)
	got.NextFireAt = &stale
	require.NoError(t, ss.UpdateSchedule(ctx, got))
	n, err := (&schedule.Scheduler{Store: ss, Firer: firer, Now: func() time.Time { return now }}).RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestSchedulerFailedFireKeepsSchedule(t *testing.T) {
	ctx := context.Background()
	ss := store.AppStore.(store.ScheduleStore)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	due := time.Date(2099, 11, 10, 3, 0, 0, 0, time.UTC)
	sched := newTestSchedule(t, project, "nightly", due)

	firer := &recordingFirer{calls: map[string]int{}, err: errors.New("workflow not found on branch main")}
	_, err := (&schedule.Scheduler{Store: ss, Firer: firer, Now: func() time.Time { return due }}).RunOnce(ctx)
	require.NoError(t, err)
	got, err := ss.GetSchedule(ctx, sched.ScheduleID)
	require.NoError(t, err, "a failed fire must not delete the schedule")
	require.True(t, got.Enabled)
	require.Equal(t, "workflow not found on branch main", got.LastError)
	require.Nil(t, got.LastEventID)
	fires, err := ss.ListScheduleFires(ctx, sched.ScheduleID, 10)
	require.NoError(t, err)
	require.Len(t, fires, 1)
	require.Equal(t, models.ScheduleFireFailed, fires[0].Status)
}

func TestSchedulerBrokenCronRetriesLater(t *testing.T) {
	ctx := context.Background()
	ss := store.AppStore.(store.ScheduleStore)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	due := time.Date(2099, 11, 10, 3, 0, 0, 0, time.UTC)
	sched := newTestSchedule(t, project, "broken", due)
	require.NoError(t, testDB.Exec("UPDATE ci_schedules SET timezone = 'Mars/Olympus' WHERE schedule_id = ?", sched.ScheduleID).Error)

	firer := &recordingFirer{calls: map[string]int{}}
	n, err := (&schedule.Scheduler{Store: ss, Firer: firer, Now: func() time.Time { return due }}).RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n)
	got, err := ss.GetSchedule(ctx, sched.ScheduleID)
	require.NoError(t, err)
	require.Contains(t, got.LastError, "timezone")
	require.True(t, got.NextFireAt.After(due), "a broken schedule is pushed back, not retried in a hot loop")
}
