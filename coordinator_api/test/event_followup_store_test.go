package test

// Postgres-backed coverage for postgres_store/event_followup_operations.go
// and migration 000044: the dynamic_jobs / duplicate_events resolution, the
// guarded event claim (concurrent deliveries), the claim release and stale
// reclaim, the retry relink, the stale schedule-fire sweep and the schedule
// last_error write.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/evaloutcome"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func eventFollowupStores(t *testing.T) (store.EventSettingsStore, store.CIEventClaimStore, store.ScheduleMaintenanceStore) {
	t.Helper()
	settings, ok := store.AppStore.(store.EventSettingsStore)
	require.True(t, ok, "store.AppStore must implement store.EventSettingsStore")
	claims, ok := store.AppStore.(store.CIEventClaimStore)
	require.True(t, ok, "store.AppStore must implement store.CIEventClaimStore")
	maintenance, ok := store.AppStore.(store.ScheduleMaintenanceStore)
	require.True(t, ok, "store.AppStore must implement store.ScheduleMaintenanceStore")
	return settings, claims, maintenance
}

func TestEventSettings_DynamicJobsResolution(t *testing.T) {
	ctx := context.Background()
	settings, _, _ := eventFollowupStores(t)
	org := &models.Organization{Name: uniqueName("org"), DisplayName: "Org"}
	require.NoError(t, testDB.Create(org).Error)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	require.NoError(t, testDB.Model(&models.Project{}).Where("project_id = ?", project.ProjectID).Update("org_id", org.OrgID).Error)

	cases := []struct {
		org, project, want string
	}{
		{"allow", "inherit", "allow"},
		{"deny", "inherit", "deny"},
		{"deny", "allow", "allow"},
		{"allow", "deny", "deny"},
	}
	for _, tc := range cases {
		require.NoError(t, testDB.Model(&models.Organization{}).Where("org_id = ?", org.OrgID).Update("dynamic_jobs", tc.org).Error)
		require.NoError(t, testDB.Model(&models.Project{}).Where("project_id = ?", project.ProjectID).Update("dynamic_jobs", tc.project).Error)
		got, err := settings.EffectiveDynamicJobs(ctx, org.OrgID, &project.ProjectID)
		require.NoError(t, err)
		require.Equal(t, tc.want, got, "org=%s project=%s", tc.org, tc.project)
	}
	// A job with no project follows the organization.
	got, err := settings.EffectiveDynamicJobs(ctx, org.OrgID, nil)
	require.NoError(t, err)
	require.Equal(t, "allow", got)

	// The defaults are allow / inherit.
	fresh := &models.Organization{Name: uniqueName("org"), DisplayName: "Org"}
	require.NoError(t, testDB.Create(fresh).Error)
	got, err = settings.EffectiveDynamicJobs(ctx, fresh.OrgID, nil)
	require.NoError(t, err)
	require.Equal(t, "allow", got)

	dup, err := settings.EffectiveDuplicateEvents(ctx, project.ProjectID)
	require.NoError(t, err)
	require.Equal(t, "run", dup)
}

func TestEventSettings_UpdateOrganizationPersistsSettings(t *testing.T) {
	ctx := context.Background()
	org := &models.Organization{Name: uniqueName("org"), DisplayName: "Org", Status: models.OrganizationStatusActive}
	require.NoError(t, testDB.Create(org).Error)
	org.DynamicJobs = models.DynamicJobsDeny
	org.DuplicateEvents = models.DuplicateEventsSkip
	orgs, ok := store.AppStore.(interface {
		UpdateOrganization(context.Context, *models.Organization) error
	})
	require.True(t, ok)
	require.NoError(t, orgs.UpdateOrganization(ctx, org))
	var stored models.Organization
	require.NoError(t, testDB.Where("org_id = ?", org.OrgID).First(&stored).Error)
	require.Equal(t, models.DynamicJobsDeny, stored.DynamicJobs)
	require.Equal(t, models.DuplicateEventsSkip, stored.DuplicateEvents)

	// A struct without the settings (empty values) keeps the stored ones.
	stored.DynamicJobs, stored.DuplicateEvents = "", ""
	stored.DisplayName = "Renamed"
	require.NoError(t, orgs.UpdateOrganization(ctx, &stored))
	var again models.Organization
	require.NoError(t, testDB.Where("org_id = ?", org.OrgID).First(&again).Error)
	require.Equal(t, models.DynamicJobsDeny, again.DynamicJobs)
	require.Equal(t, models.DuplicateEventsSkip, again.DuplicateEvents)
}

func TestCIEventClaim_OneOfConcurrentClaimsWins(t *testing.T) {
	ctx := context.Background()
	events := ciEventStore(t)
	_, claims, _ := eventFollowupStores(t)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	cleanupProjectEvents(t, project.ProjectID)

	event, _, err := events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("delivery"), uniqueName("semantic")))
	require.NoError(t, err)

	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := claims.ClaimCIEventForEval(ctx, event.EventID)
			require.NoError(t, err)
			if claimed {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, wins)

	// A release lets the next delivery claim it again.
	require.NoError(t, claims.ReleaseCIEventClaim(ctx, event.EventID))
	claimed, err := claims.ClaimCIEventForEval(ctx, event.EventID)
	require.NoError(t, err)
	require.True(t, claimed)

	// A claim older than the stale window can be taken again.
	require.NoError(t, testDB.Exec("UPDATE ci_events SET updated_at = timezone('utc', now()) - interval '10 minutes' WHERE event_id = ?", event.EventID).Error)
	claimed, err = claims.ClaimCIEventForEval(ctx, event.EventID)
	require.NoError(t, err)
	require.True(t, claimed)

	// Once the eval job is linked, nothing can claim the event.
	job := createTestJob(t, owner.UserID, "submitted")
	require.NoError(t, testDB.Model(&models.Job{}).Where("job_id = ?", job.JobID).Update("project_id", project.ProjectID).Error)
	require.NoError(t, events.AttachCIEventEvalJob(ctx, event.EventID, job.JobID))
	require.NoError(t, testDB.Exec("UPDATE ci_events SET updated_at = timezone('utc', now()) - interval '10 minutes' WHERE event_id = ?", event.EventID).Error)
	claimed, err = claims.ClaimCIEventForEval(ctx, event.EventID)
	require.NoError(t, err)
	require.False(t, claimed)
	// A release after the link is a no-op.
	require.NoError(t, claims.ReleaseCIEventClaim(ctx, event.EventID))
	got, err := events.GetCIEvent(ctx, event.EventID)
	require.NoError(t, err)
	require.Equal(t, models.CIEventStatusEvaluating, got.Status)
}

func TestCIEventClaim_RelinkPointsAtRetry(t *testing.T) {
	ctx := context.Background()
	events := ciEventStore(t)
	_, claims, _ := eventFollowupStores(t)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	cleanupProjectEvents(t, project.ProjectID)

	event, _, err := events.RecordCIEvent(ctx, newTestCIEvent(project, uniqueName("delivery"), uniqueName("semantic")))
	require.NoError(t, err)
	first := createTestJob(t, owner.UserID, "failed")
	retry := createTestJob(t, owner.UserID, "submitted")
	for _, id := range []string{first.JobID, retry.JobID} {
		require.NoError(t, testDB.Model(&models.Job{}).Where("job_id = ?", id).Update("project_id", project.ProjectID).Error)
	}
	require.NoError(t, events.AttachCIEventEvalJob(ctx, event.EventID, first.JobID))
	require.NoError(t, events.SetCIEventStatus(ctx, event.EventID, models.CIEventStatusFailed))

	require.NoError(t, claims.RelinkCIEventEvalJob(ctx, event.EventID, retry.JobID))
	got, err := events.GetCIEvent(ctx, event.EventID)
	require.NoError(t, err)
	require.Equal(t, retry.JobID, *got.EvalJobID)
	require.Equal(t, models.CIEventStatusEvaluating, got.Status)
}

func TestScheduleMaintenance_StaleFireSweepAndLastError(t *testing.T) {
	ctx := context.Background()
	_, _, maintenance := eventFollowupStores(t)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	sched := newTestSchedule(t, project, uniqueName("nightly"), time.Date(2099, 1, 1, 3, 0, 0, 0, time.UTC))
	t.Cleanup(func() {
		testDB.Exec("DELETE FROM ci_schedule_fires WHERE schedule_id = ?", sched.ScheduleID)
		testDB.Exec("DELETE FROM ci_schedules WHERE schedule_id = ?", sched.ScheduleID)
	})

	oldFire := time.Date(2098, 1, 1, 3, 0, 0, 0, time.UTC)
	newFire := time.Date(2098, 1, 2, 3, 0, 0, 0, time.UTC)
	require.NoError(t, testDB.Exec(`INSERT INTO ci_schedule_fires (schedule_id, fire_time, status, created_at)
VALUES (?, ?, 'firing', now() - interval '20 minutes'), (?, ?, 'firing', now())`,
		sched.ScheduleID, oldFire, sched.ScheduleID, newFire).Error)

	changed, err := maintenance.FailStaleScheduleFires(ctx, 10*time.Minute, "coordinator stopped during fire")
	require.NoError(t, err)
	require.GreaterOrEqual(t, changed, int64(1))

	var fires []models.CIScheduleFire
	require.NoError(t, testDB.Where("schedule_id = ?", sched.ScheduleID).Order("fire_time").Find(&fires).Error)
	require.Len(t, fires, 2)
	require.Equal(t, models.ScheduleFireFailed, fires[0].Status)
	require.Equal(t, "coordinator stopped during fire", fires[0].Error)
	require.NotNil(t, fires[0].CompletedAt)
	require.Equal(t, models.ScheduleFireFiring, fires[1].Status, "a recent fire is still in progress")

	var stored models.CISchedule
	require.NoError(t, testDB.Where("schedule_id = ?", sched.ScheduleID).First(&stored).Error)
	require.Equal(t, "coordinator stopped during fire", stored.LastError)
	require.True(t, stored.Enabled, "a failed fire never disables or deletes the schedule")

	require.NoError(t, maintenance.SetScheduleLastError(ctx, sched.ScheduleID, "workflow not found: nightly"))
	require.NoError(t, testDB.Where("schedule_id = ?", sched.ScheduleID).First(&stored).Error)
	require.Equal(t, "workflow not found: nightly", stored.LastError)
	require.NoError(t, maintenance.SetScheduleLastError(ctx, sched.ScheduleID, ""))
	require.NoError(t, testDB.Where("schedule_id = ?", sched.ScheduleID).First(&stored).Error)
	require.Empty(t, stored.LastError)
}

// A scheduled eval job that fails closes its event, records the failure on
// the schedule and on the fire linked to the event. A fire of another event
// is left alone.
func TestScheduleEvalFailure_RecordedOnScheduleAndFire(t *testing.T) {
	ctx := context.Background()
	events := ciEventStore(t)
	owner := createTestUser(t)
	project := createTestProject(t, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
	cleanupProjectEvents(t, project.ProjectID)
	sched := newTestSchedule(t, project, uniqueName("nightly"), time.Date(2099, 1, 1, 3, 0, 0, 0, time.UTC))
	t.Cleanup(func() {
		testDB.Exec("DELETE FROM ci_schedule_fires WHERE schedule_id = ?", sched.ScheduleID)
		testDB.Exec("DELETE FROM ci_schedules WHERE schedule_id = ?", sched.ScheduleID)
	})

	newScheduleEvent := func() *models.CIEvent {
		input := newTestCIEvent(project, uniqueName("delivery"), uniqueName("semantic"))
		scheduleID := sched.ScheduleID
		input.ScheduleID = &scheduleID
		input.Source = models.CIEventSourceSchedule
		event, _, err := events.RecordCIEvent(ctx, input)
		require.NoError(t, err)
		return event
	}
	failed, other := newScheduleEvent(), newScheduleEvent()
	require.NoError(t, testDB.Exec(`INSERT INTO ci_schedule_fires (schedule_id, fire_time, status, event_id, completed_at)
VALUES (?, ?, 'fired', ?, now()), (?, ?, 'fired', ?, now())`,
		sched.ScheduleID, time.Date(2098, 1, 1, 3, 0, 0, 0, time.UTC), failed.EventID,
		sched.ScheduleID, time.Date(2098, 1, 2, 3, 0, 0, 0, time.UTC), other.EventID).Error)

	eventID := failed.EventID
	job := &models.Job{JobID: "00000000-0000-0000-0000-0000000000e1", EventID: &eventID,
		JobEnvVars: models.JSONB{"REACTORCIDE_JOB_KIND": "eval"}}
	evaloutcome.Finish(ctx, store.AppStore, job, "timeout")

	got, err := events.GetCIEvent(ctx, failed.EventID)
	require.NoError(t, err)
	require.Equal(t, models.CIEventStatusFailed, got.Status)

	var stored models.CISchedule
	require.NoError(t, testDB.Where("schedule_id = ?", sched.ScheduleID).First(&stored).Error)
	require.Equal(t, "eval job "+job.JobID+" timed out", stored.LastError)
	require.True(t, stored.Enabled)

	var fires []models.CIScheduleFire
	require.NoError(t, testDB.Where("schedule_id = ?", sched.ScheduleID).Order("fire_time").Find(&fires).Error)
	require.Len(t, fires, 2)
	require.Equal(t, models.ScheduleFireFailed, fires[0].Status)
	require.Equal(t, "eval job "+job.JobID+" timed out", fires[0].Error)
	require.Equal(t, models.ScheduleFireFired, fires[1].Status, "the other event's fire is untouched")
}
