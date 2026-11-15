package uiapi

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// scheduleFakeStore adds an in-memory store.ScheduleStore to the shared fake,
// without changing the shared fakeStore type.
type scheduleFakeStore struct {
	*fakeStore
	smu       sync.Mutex
	schedules map[string]models.CISchedule
	fires     []models.CIScheduleFire
	nextID    int
	// auditEvents captures audit.RecordUser calls.
	auditEvents []models.AuditEvent
}

func (s *scheduleFakeStore) AppendAuditEvent(_ context.Context, event *models.AuditEvent) error {
	s.smu.Lock()
	defer s.smu.Unlock()
	s.auditEvents = append(s.auditEvents, *event)
	return nil
}

func (s *scheduleFakeStore) CreateSchedule(_ context.Context, sched *models.CISchedule) error {
	s.smu.Lock()
	defer s.smu.Unlock()
	for _, existing := range s.schedules {
		if existing.ProjectID == sched.ProjectID && existing.Name == sched.Name {
			return store.ErrAlreadyExists
		}
	}
	s.nextID++
	sched.ScheduleID = "sched-" + string(rune('a'+s.nextID))
	sched.CreatedAt, sched.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	s.schedules[sched.ScheduleID] = *sched
	return nil
}

func (s *scheduleFakeStore) GetSchedule(_ context.Context, id string) (*models.CISchedule, error) {
	s.smu.Lock()
	defer s.smu.Unlock()
	sched, ok := s.schedules[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &sched, nil
}

func (s *scheduleFakeStore) ListSchedulesByProject(_ context.Context, projectID string) ([]models.CISchedule, error) {
	s.smu.Lock()
	defer s.smu.Unlock()
	out := []models.CISchedule{}
	for _, sched := range s.schedules {
		if sched.ProjectID == projectID {
			out = append(out, sched)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *scheduleFakeStore) UpdateSchedule(_ context.Context, sched *models.CISchedule) error {
	s.smu.Lock()
	defer s.smu.Unlock()
	if _, ok := s.schedules[sched.ScheduleID]; !ok {
		return store.ErrNotFound
	}
	s.schedules[sched.ScheduleID] = *sched
	return nil
}

func (s *scheduleFakeStore) DeleteSchedule(_ context.Context, id string) error {
	s.smu.Lock()
	defer s.smu.Unlock()
	if _, ok := s.schedules[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.schedules, id)
	return nil
}

func (s *scheduleFakeStore) ListScheduleFires(_ context.Context, id string, limit int) ([]models.CIScheduleFire, error) {
	s.smu.Lock()
	defer s.smu.Unlock()
	out := []models.CIScheduleFire{}
	for _, fire := range s.fires {
		if fire.ScheduleID == id && len(out) < limit {
			out = append(out, fire)
		}
	}
	return out, nil
}

func (s *scheduleFakeStore) ClaimDueScheduleFires(context.Context, time.Time, int, store.ScheduleFirePlan) ([]store.ClaimedScheduleFire, error) {
	return nil, nil
}

func (s *scheduleFakeStore) RecordScheduleFireResult(context.Context, string, time.Time, *string, string) error {
	return nil
}

func newScheduleTestDeps(t *testing.T) (*Deps, *scheduleFakeStore) {
	t.Helper()
	st := &scheduleFakeStore{fakeStore: newFakeStore(), schedules: map[string]models.CISchedule{}}
	return NewDeps(st, auth.NewNoneBackend(), nil, nil), st
}

func withScheduleClock(t *testing.T, now time.Time) {
	t.Helper()
	prev := scheduleNow
	scheduleNow = func() time.Time { return now }
	t.Cleanup(func() { scheduleNow = prev })
}

func TestScheduleLifecycle(t *testing.T) {
	deps, st := newScheduleTestDeps(t)
	withScheduleClock(t, time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC))
	owner := st.putUser(models.User{UserID: "owner-1"})
	project := st.putProject(models.Project{OrgID: "org-1", UserID: &owner.UserID, Name: "application"})
	seedProjectOwner(st.fakeStore, owner.UserID, project.ProjectID)
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, owner.UserID)

	created, err := ui.CreateSchedule(ctx, csilapi.CreateScheduleRequest{
		ProjectId: project.ProjectID, Name: "nightly", Branch: "main",
		WorkflowRef: ".reactorcide/workflows/nightly.yaml", Cron: "0 3 * * *", Timezone: strPtr("America/New_York"),
	})
	requireOK(t, err)
	got := created.Schedule
	if !got.Enabled || got.Timezone != "America/New_York" || got.NextFireAt == nil || *got.NextFireAt != "2026-11-15T08:00:00Z" {
		t.Fatalf("created = %+v (next %v)", got, got.NextFireAt)
	}

	_, err = ui.CreateSchedule(ctx, csilapi.CreateScheduleRequest{ProjectId: project.ProjectID, Name: "nightly", Branch: "main", WorkflowRef: "x", Cron: "@daily"})
	requireCode(t, err, "conflict")

	// A name edit keeps the planned time; disabling clears it; re-enabling
	// plans from now.
	renamed, err := ui.UpdateSchedule(ctx, csilapi.UpdateScheduleRequest{ScheduleId: got.ScheduleId, Name: strPtr("nightly build")})
	requireOK(t, err)
	if renamed.Schedule.NextFireAt == nil || *renamed.Schedule.NextFireAt != *got.NextFireAt {
		t.Fatalf("rename moved the next fire: %v", renamed.Schedule.NextFireAt)
	}
	disabled, err := ui.UpdateSchedule(ctx, csilapi.UpdateScheduleRequest{ScheduleId: got.ScheduleId, Enabled: boolPtr(false)})
	requireOK(t, err)
	if disabled.Schedule.Enabled || disabled.Schedule.NextFireAt != nil {
		t.Fatalf("disabled = %+v", disabled.Schedule)
	}
	withScheduleClock(t, time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	enabled, err := ui.UpdateSchedule(ctx, csilapi.UpdateScheduleRequest{ScheduleId: got.ScheduleId, Enabled: boolPtr(true), Cron: strPtr("@hourly"), Timezone: strPtr("")})
	requireOK(t, err)
	if enabled.Schedule.NextFireAt == nil || *enabled.Schedule.NextFireAt != "2026-11-20T13:00:00Z" || enabled.Schedule.Timezone != "UTC" {
		t.Fatalf("re-enabled = %+v (next %v)", enabled.Schedule, enabled.Schedule.NextFireAt)
	}

	list, err := ui.ListSchedules(ctx, csilapi.ListSchedulesRequest{ProjectId: project.ProjectID})
	requireOK(t, err)
	if len(list.Schedules) != 1 {
		t.Fatalf("list = %+v", list.Schedules)
	}

	st.fires = append(st.fires, models.CIScheduleFire{FireID: "f1", ScheduleID: got.ScheduleId, FireTime: time.Now(), Status: models.ScheduleFireFailed, Error: "event creation not wired yet"})
	fires, err := ui.ListScheduleFires(ctx, csilapi.ListScheduleFiresRequest{ScheduleId: got.ScheduleId})
	requireOK(t, err)
	if len(fires.Fires) != 1 || fires.Fires[0].Status != "failed" {
		t.Fatalf("fires = %+v", fires.Fires)
	}
	bad := int64(500)
	_, err = ui.ListScheduleFires(ctx, csilapi.ListScheduleFiresRequest{ScheduleId: got.ScheduleId, Limit: &bad})
	requireCode(t, err, "invalid_argument")

	deleted, err := ui.DeleteSchedule(ctx, csilapi.DeleteScheduleRequest{ScheduleId: got.ScheduleId})
	requireOK(t, err)
	if !deleted.Ok {
		t.Fatal("Ok = false")
	}
	_, err = ui.DeleteSchedule(ctx, csilapi.DeleteScheduleRequest{ScheduleId: got.ScheduleId})
	requireCode(t, err, "not_found")

	actions := map[string]bool{}
	for _, event := range st.auditEvents {
		actions[event.Action] = true
	}
	for _, want := range []string{"schedule.created", "schedule.updated", "schedule.deleted"} {
		if !actions[want] {
			t.Errorf("missing audit event %s (have %v)", want, actions)
		}
	}
}

func TestScheduleValidation(t *testing.T) {
	deps, st := newScheduleTestDeps(t)
	owner := st.putUser(models.User{UserID: "owner-1"})
	project := st.putProject(models.Project{OrgID: "org-1", UserID: &owner.UserID, Name: "application"})
	seedProjectOwner(st.fakeStore, owner.UserID, project.ProjectID)
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, owner.UserID)

	for name, req := range map[string]csilapi.CreateScheduleRequest{
		"bad cron":     {Name: "a", Branch: "main", WorkflowRef: "x", Cron: "every day"},
		"bad timezone": {Name: "a", Branch: "main", WorkflowRef: "x", Cron: "@daily", Timezone: strPtr("Mars/Olympus")},
		"bad ref":      {Name: "a", Branch: "main", WorkflowRef: "../x.yaml", Cron: "@daily"},
		"bad branch":   {Name: "a", Branch: "has space", WorkflowRef: "x", Cron: "@daily"},
		"no name":      {Name: "", Branch: "main", WorkflowRef: "x", Cron: "@daily"},
	} {
		req.ProjectId = project.ProjectID
		_, err := ui.CreateSchedule(ctx, req)
		if code := serviceErrCode(t, err); code != "invalid_argument" {
			t.Errorf("%s: code = %q, want invalid_argument", name, code)
		}
	}
}

func TestScheduleAuthorization(t *testing.T) {
	deps, st := newScheduleTestDeps(t)
	owner := st.putUser(models.User{UserID: "owner-1"})
	member := st.putUser(models.User{UserID: "member-1"})
	orgAdmin := st.putUser(models.User{UserID: "admin-1"})
	stranger := st.putUser(models.User{UserID: "stranger-1"})
	project := st.putProject(models.Project{OrgID: "org-1", UserID: &owner.UserID, Name: "application"})
	seedProjectOwner(st.fakeStore, owner.UserID, project.ProjectID)
	seedProjectMember(st.fakeStore, member.UserID, project.ProjectID)
	seedOrgAdmin(st.fakeStore, orgAdmin.UserID, "org-1")
	ui := NewUiService(deps)

	created, err := ui.CreateSchedule(mintSessionCtx(t, deps, owner.UserID), csilapi.CreateScheduleRequest{
		ProjectId: project.ProjectID, Name: "nightly", Branch: "main", WorkflowRef: "nightly", Cron: "@daily",
	})
	requireOK(t, err)
	id := created.Schedule.ScheduleId

	for _, who := range []string{member.UserID, stranger.UserID} {
		ctx := mintSessionCtx(t, deps, who)
		_, err := ui.ListSchedules(ctx, csilapi.ListSchedulesRequest{ProjectId: project.ProjectID})
		requireCode(t, err, "forbidden")
		_, err = ui.CreateSchedule(ctx, csilapi.CreateScheduleRequest{ProjectId: project.ProjectID, Name: "x", Branch: "main", WorkflowRef: "x", Cron: "@daily"})
		requireCode(t, err, "forbidden")
		_, err = ui.UpdateSchedule(ctx, csilapi.UpdateScheduleRequest{ScheduleId: id, Enabled: boolPtr(false)})
		requireCode(t, err, "forbidden")
		_, err = ui.DeleteSchedule(ctx, csilapi.DeleteScheduleRequest{ScheduleId: id})
		requireCode(t, err, "forbidden")
		_, err = ui.ListScheduleFires(ctx, csilapi.ListScheduleFiresRequest{ScheduleId: id})
		requireCode(t, err, "forbidden")
	}

	// An org admin manages the project's schedules.
	_, err = ui.UpdateSchedule(mintSessionCtx(t, deps, orgAdmin.UserID), csilapi.UpdateScheduleRequest{ScheduleId: id, Enabled: boolPtr(false)})
	requireOK(t, err)

	_, err = ui.ListSchedules(anonCtx(), csilapi.ListSchedulesRequest{ProjectId: project.ProjectID})
	requireCode(t, err, "unauthorized")
}
