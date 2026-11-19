package priority

import (
	"context"
	"errors"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuiltinLaneSelection(t *testing.T) {
	lanes := Effective(nil, nil)
	cases := []struct {
		name  string
		facts Facts
		lane  string
		final int
	}{
		{"pr eval", Facts{JobKind: "eval", RefClasses: []string{"pr"}}, "eval-pr", 10},
		{"fork pr eval", Facts{JobKind: "eval", RefClasses: []string{"fork", "pr"}}, "eval-pr", 10},
		{"draft pr eval", Facts{JobKind: "eval", RefClasses: []string{"pr"}, Draft: true}, "eval-pr-draft", 5},
		{"push eval", Facts{JobKind: "eval", RefClasses: []string{"default"}, EventType: "push"}, "eval", 5},
		{"schedule eval", Facts{JobKind: "eval", RefClasses: []string{"schedule"}}, "eval", 5},
		{"manual eval", Facts{JobKind: "eval", RefClasses: []string{"manual"}}, "eval", 5},
		{"release work", Facts{JobKind: "work", RefClasses: []string{"default", "release"}, EventType: "push"}, "release", 100},
		{"production work", Facts{JobKind: "work", RefClasses: []string{"production"}}, "release", 100},
		{"tag work", Facts{JobKind: "work", RefClasses: []string{"tag"}, EventType: "tag_created"}, "release", 100},
		{"default branch work", Facts{JobKind: "work", RefClasses: []string{"default"}, EventType: "push"}, "default-branch", 0},
		{"manual work", Facts{JobKind: "work", RefClasses: []string{"manual"}, EventType: "manual"}, "manual", 0},
		{"pr ready work", Facts{JobKind: "work", RefClasses: []string{"pr"}, EventType: "pull_request_opened"}, "pr-ready", -100},
		{"pr draft work", Facts{JobKind: "work", RefClasses: []string{"pr"}, Draft: true}, "pr-draft", -200},
		{"schedule work", Facts{JobKind: "work", RefClasses: []string{"schedule"}, EventType: "schedule"}, "schedule", -300},
		{"backfill work", Facts{JobKind: "work", RefClasses: []string{"backfill", "manual"}}, "backfill", -400},
		{"feature branch push", Facts{JobKind: "work", EventType: "push"}, "branch", -100},
		{"no facts", Facts{JobKind: "work"}, "standard", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := Compute(lanes, GroupShifts{}, tc.facts)
			assert.Equal(t, tc.lane, decision.Lane)
			assert.Equal(t, tc.final, decision.Final)
			assert.Equal(t, ScopeBuiltin, decision.Scope)
		})
	}
}

// The default order of the plan: release > default branch > PR ready > PR
// draft > schedule > backfill, and every eval ahead of ordinary work.
func TestBuiltinOrder(t *testing.T) {
	lanes := Effective(nil, nil)
	final := func(f Facts) int { return Compute(lanes, GroupShifts{}, f).Final }
	release := final(Facts{RefClasses: []string{"release"}})
	def := final(Facts{RefClasses: []string{"default"}})
	ready := final(Facts{RefClasses: []string{"pr"}})
	draft := final(Facts{RefClasses: []string{"pr"}, Draft: true})
	schedule := final(Facts{RefClasses: []string{"schedule"}})
	backfill := final(Facts{RefClasses: []string{"backfill"}})
	assert.True(t, release > def && def > ready && ready > draft && draft > schedule && schedule > backfill)
	assert.Greater(t, final(Facts{JobKind: "eval"}), def)
}

func TestLaneMatchFacts(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name  string
		lane  models.PriorityLane
		facts Facts
		want  bool
	}{
		{"empty matches all", models.PriorityLane{Enabled: true}, Facts{JobKind: "work"}, true},
		{"disabled", models.PriorityLane{}, Facts{}, false},
		{"classes any", models.PriorityLane{Enabled: true, ClassesAny: []string{"a", "b"}}, Facts{RefClasses: []string{"b"}}, true},
		{"classes any miss", models.PriorityLane{Enabled: true, ClassesAny: []string{"a"}}, Facts{RefClasses: []string{"b"}}, false},
		{"classes none", models.PriorityLane{Enabled: true, ClassesNone: []string{"fork"}}, Facts{RefClasses: []string{"fork", "pr"}}, false},
		{"event type", models.PriorityLane{Enabled: true, EventTypes: []string{"push"}}, Facts{EventType: "push"}, true},
		{"event type miss", models.PriorityLane{Enabled: true, EventTypes: []string{"push"}}, Facts{EventType: "manual"}, false},
		{"draft true", models.PriorityLane{Enabled: true, Draft: &yes}, Facts{Draft: true}, true},
		{"draft false miss", models.PriorityLane{Enabled: true, Draft: &no}, Facts{Draft: true}, false},
		{"eval kind", models.PriorityLane{Enabled: true, JobKind: "eval"}, Facts{JobKind: "work"}, false},
		{"work kind", models.PriorityLane{Enabled: true, JobKind: "work"}, Facts{JobKind: "work"}, true},
		{"work kind eval miss", models.PriorityLane{Enabled: true, JobKind: "work"}, Facts{JobKind: "eval"}, false},
		{"retry lane, not a retry", models.PriorityLane{Enabled: true, Retry: true}, Facts{}, false},
		{"retry lane, retry", models.PriorityLane{Enabled: true, Retry: true}, Facts{Retry: true}, true},
		{"normal lane, retry", models.PriorityLane{Enabled: true}, Facts{Retry: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Matches(tc.lane, tc.facts))
		})
	}
}

func TestEffectiveOverrides(t *testing.T) {
	org := []models.PriorityLane{
		{LaneID: "org-1", Name: "pr-ready", Position: 400, Base: -50, ClassesAny: []string{"pr"}, Enabled: true},
		{LaneID: "org-2", Name: "hotfix", Position: 5, Base: 200, ClassesAny: []string{"hotfix"}, Enabled: true},
	}
	project := []models.PriorityLane{
		{LaneID: "p-1", Name: "pr-ready", Position: 400, Base: -20, ClassesAny: []string{"pr"}, Enabled: true},
		{LaneID: "p-2", Name: "schedule", Position: 600, Base: -300, ClassesAny: []string{"schedule"}, Enabled: false},
	}
	lanes := Effective(org, project)
	assert.Equal(t, "hotfix", lanes[0].Name, "position 5 runs first")
	byName := map[string]Lane{}
	for _, lane := range lanes {
		byName[lane.Name] = lane
	}
	assert.Equal(t, ScopeProject, byName["pr-ready"].Scope)
	assert.Equal(t, ScopeOrg, byName["pr-ready"].Replaces)
	assert.Equal(t, -20, byName["pr-ready"].Base)
	assert.Equal(t, len(Builtins())+1, len(lanes), "a replaced lane is not listed twice")

	d := Compute(lanes, GroupShifts{}, Facts{RefClasses: []string{"pr"}})
	assert.Equal(t, "pr-ready", d.Lane)
	assert.Equal(t, -20, d.Final)
	assert.Equal(t, "p-1", d.RuleID)

	// A disabled project schedule lane: a schedule falls to "standard".
	d = Compute(lanes, GroupShifts{}, Facts{RefClasses: []string{"schedule"}})
	assert.Equal(t, "standard", d.Lane)

	d = Compute(lanes, GroupShifts{}, Facts{RefClasses: []string{"hotfix", "pr"}})
	assert.Equal(t, "hotfix", d.Lane)
	assert.Equal(t, 200, d.Final)
}

func TestFallbackWhenNothingMatches(t *testing.T) {
	d := Compute([]Lane{}, GroupShifts{}, Facts{})
	assert.Equal(t, FallbackLane, d.Lane)
	assert.Equal(t, 0, d.Final)
}

func TestCombineAndClamp(t *testing.T) {
	lanes := Effective([]models.PriorityLane{{LaneID: "x", Name: "top", Base: 500, Position: 1, Enabled: true, ClassesAny: []string{"top"}},
		{LaneID: "y", Name: "bottom", Base: -500, Position: 2, Enabled: true, ClassesAny: []string{"bottom"}}}, nil)
	groups := GroupShifts{Actor: []models.GroupPriorityShift{{Name: "bots", Shift: -30}, {Name: "release", Shift: 20}, {Name: "none", Shift: 0}}}

	d := Compute(lanes, groups, Facts{RefClasses: []string{"pr"}})
	assert.Equal(t, -100+20, d.Final, "the largest group shift applies")
	require.Len(t, d.Shifts, 2, "a group with shift 0 has no shift")
	assert.False(t, d.Shifts[0].Applied)
	assert.True(t, d.Shifts[1].Applied)
	assert.Equal(t, "release", d.Shifts[1].Group)

	only := GroupShifts{Actor: []models.GroupPriorityShift{{Name: "bots", Shift: -30}}}
	assert.Equal(t, -130, Compute(lanes, only, Facts{RefClasses: []string{"pr"}}).Final, "a negative shift alone applies")

	owner := GroupShifts{Actor: []models.GroupPriorityShift{{Name: "dev", Shift: 5}}, Owner: []models.GroupPriorityShift{{Name: "platform", Shift: 40}}}
	d = Compute(lanes, owner, Facts{RefClasses: []string{"default"}})
	assert.Equal(t, 40, d.Final, "owner groups and actor groups combine with max")
	assert.Equal(t, ShiftSourceOwnerGroup, d.Shifts[1].Source)

	// The request is clamped to +-MaxRequested and ignored for an eval.
	assert.Equal(t, 50, Compute(lanes, GroupShifts{}, Facts{RefClasses: []string{"default"}, Requested: 900}).Final)
	assert.Equal(t, -50, Compute(lanes, GroupShifts{}, Facts{RefClasses: []string{"default"}, Requested: -900}).Final)
	assert.Equal(t, 10, Compute(lanes, GroupShifts{}, Facts{RefClasses: []string{"default"}, Requested: 10}).Final)
	assert.Equal(t, 10, Compute(lanes, GroupShifts{}, Facts{JobKind: "eval", RefClasses: []string{"pr"}, Requested: 40}).Final)

	// A group shift out of bounds is clamped.
	big := GroupShifts{Actor: []models.GroupPriorityShift{{Name: "x", Shift: 999}}}
	assert.Equal(t, 50, Compute(lanes, big, Facts{RefClasses: []string{"default"}}).Final)

	// The final value is clamped.
	all := GroupShifts{Actor: []models.GroupPriorityShift{{Name: "x", Shift: 50}}}
	assert.Equal(t, 600, Compute(lanes, all, Facts{RefClasses: []string{"top"}, Requested: 50}).Final)
	assert.LessOrEqual(t, Compute(lanes, all, Facts{RefClasses: []string{"top"}, Requested: 50}).Final, MaxPriority)
	low := GroupShifts{Actor: []models.GroupPriorityShift{{Name: "x", Shift: -50}}}
	shed := models.ShedActions{LowerPRPriority: true}
	d = Compute(lanes, low, Facts{RefClasses: []string{"bottom", "pr"}, Requested: -50, Shed: shed})
	assert.Equal(t, -700, d.Final)
	assert.GreaterOrEqual(t, d.Final, MinPriority)
}

func TestShedAdjustment(t *testing.T) {
	on := models.ShedActions{LowerPRPriority: true}
	assert.Equal(t, ShedPRShift, ShedAdjustment(on, Facts{RefClasses: []string{"pr"}}))
	assert.Equal(t, 0, ShedAdjustment(on, Facts{RefClasses: []string{"default"}}))
	assert.Equal(t, 0, ShedAdjustment(models.ShedActions{}, Facts{RefClasses: []string{"pr"}}))

	lanes := Effective(nil, nil)
	d := Compute(lanes, GroupShifts{}, Facts{JobKind: "eval", RefClasses: []string{"pr"}, Shed: on})
	assert.Equal(t, 10+ShedPRShift, d.Final)
	assert.Less(t, d.Final, Compute(lanes, GroupShifts{}, Facts{JobKind: "eval"}).Final, "a PR eval falls below a push eval")
	assert.Less(t, d.Final, Compute(lanes, GroupShifts{}, Facts{RefClasses: []string{"default"}}).Final)
	assert.Equal(t, ShiftSourceShed, d.Shifts[len(d.Shifts)-1].Source)
}

func TestReasonRoundTrip(t *testing.T) {
	lanes := Effective(nil, nil)
	groups := GroupShifts{Actor: []models.GroupPriorityShift{{Name: "release-team", Shift: 10}}}
	d := Compute(lanes, groups, Facts{RefClasses: []string{"pr"}, Requested: 3})
	reason := d.Reason()
	assert.Equal(t, "priority", reason.Kind)
	assert.Equal(t, "pr-ready", reason.Lane)
	require.NotNil(t, reason.Base)
	require.NotNil(t, reason.Final)
	assert.Equal(t, -100, *reason.Base)
	assert.Equal(t, -87, *reason.Final)
	assert.Equal(t, "builtin:pr-ready", reason.RuleID)
	assert.Equal(t, "builtin", reason.Value)
	assert.Equal(t, "priority -87: lane pr-ready (builtin) base -100, group release-team +10, requested +3", reason.Detail)
	assert.Equal(t, d, DecisionFromReason(reason))
	requested, ok := RequestedOf(&reason)
	assert.True(t, ok)
	assert.Equal(t, 3, requested)

	job := &models.Job{}
	d.Apply(job)
	assert.Equal(t, -87, job.Priority)
	assert.Equal(t, "pr-ready", job.PriorityLane)
	assert.Equal(t, reason, *job.PriorityReason)
}

func TestRetainKeepsOriginalLane(t *testing.T) {
	lanes := Effective(nil, nil)
	original := &models.Job{JobID: "orig"}
	Compute(lanes, GroupShifts{Actor: []models.GroupPriorityShift{{Name: "release", Shift: 30}}}, Facts{RefClasses: []string{"default"}, Requested: 5}).Apply(original)
	require.Equal(t, 35, original.Priority)

	// The rules changed since: a new org lane would now give default work
	// 200. A retry still keeps the original lane and shifts.
	changed := Effective([]models.PriorityLane{{LaneID: "x", Name: "default-branch", Position: 200, Base: 200, ClassesAny: []string{"default"}, Enabled: true}}, nil)
	d, ok := Retain(original, changed, Facts{RefClasses: []string{"default"}})
	require.True(t, ok)
	assert.Equal(t, "default-branch", d.Lane)
	assert.Equal(t, 35, d.Final)
	assert.Equal(t, "orig", d.RetainedFrom)

	// A retry lane changes the lane but keeps the original shifts.
	retryLanes := Effective([]models.PriorityLane{{LaneID: "r", Name: "retries", Position: 1, Base: -50, Retry: true, Enabled: true}}, nil)
	d, ok = Retain(original, retryLanes, Facts{RefClasses: []string{"default"}})
	require.True(t, ok)
	assert.Equal(t, "retries", d.Lane)
	assert.Equal(t, -50+30+5, d.Final)

	_, ok = Retain(&models.Job{Priority: 7}, lanes, Facts{})
	assert.False(t, ok, "a job made before lanes has no decision to keep")
}

func TestValidateLane(t *testing.T) {
	good := models.PriorityLane{Name: "hotfix", Base: 10, ClassesAny: []string{"hotfix"}, EventTypes: []string{"push"}, JobKind: "work"}
	require.NoError(t, ValidateLane(good))
	bad := []models.PriorityLane{
		{Name: "Bad Name"},
		{Name: FallbackLane},
		{Name: "x", Base: 501},
		{Name: "x", Base: -501},
		{Name: "x", JobKind: "deploy"},
		{Name: "x", ClassesAny: []string{"Not Valid"}},
		{Name: "x", EventTypes: []string{"comment"}},
		{Name: "x", Position: 100001},
	}
	for _, lane := range bad {
		assert.Error(t, ValidateLane(lane), lane.Name)
	}
	assert.NoError(t, ValidateGroupShift(-50))
	assert.NoError(t, ValidateGroupShift(50))
	assert.Error(t, ValidateGroupShift(51))
	assert.Error(t, ValidateGroupShift(-51))
}

func TestBuiltinsAreValid(t *testing.T) {
	names := map[string]bool{}
	for _, lane := range Builtins() {
		require.NoError(t, ValidateLane(lane.PriorityLane), lane.Name)
		assert.False(t, names[lane.Name], "duplicate %s", lane.Name)
		names[lane.Name] = true
	}
}

// fakeStore implements the optional store surfaces that Assign reads.
type fakeStore struct {
	events    map[string]*models.CIEvent
	jobs      map[string]*models.Job
	workflows map[string]*models.WorkflowInstance
	orgLanes  []models.PriorityLane
	projLanes []models.PriorityLane
	shifts    map[string][]models.GroupPriorityShift
	owner     []models.GroupPriorityShift
	shed      models.ShedState
	lanesErr  error
}

func (f *fakeStore) GetCIEvent(_ context.Context, id string) (*models.CIEvent, error) {
	if event, ok := f.events[id]; ok {
		return event, nil
	}
	return nil, store.ErrNotFound
}
func (f *fakeStore) RecordCIEvent(context.Context, *models.CIEvent) (*models.CIEvent, store.CIEventRecordOutcome, error) {
	return nil, "", nil
}
func (f *fakeStore) AttachCIEventEvalJob(context.Context, string, string) error { return nil }
func (f *fakeStore) SetCIEventStatus(context.Context, string, string) error     { return nil }
func (f *fakeStore) SetCIEventSummary(context.Context, string, models.CIEventSummary) error {
	return nil
}
func (f *fakeStore) UpsertCIEventWorkflowDecisions(context.Context, []models.CIEventWorkflowDecision) error {
	return nil
}
func (f *fakeStore) ListCIEventWorkflowDecisions(context.Context, string) ([]models.CIEventWorkflowDecision, error) {
	return nil, nil
}
func (f *fakeStore) GetCIEventWorkflowDecisionByWorkflow(context.Context, string) (*models.CIEventWorkflowDecision, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) GetJobByID(_ context.Context, id string) (*models.Job, error) {
	if job, ok := f.jobs[id]; ok {
		return job, nil
	}
	return nil, store.ErrNotFound
}
func (f *fakeStore) GetWorkflowInstance(_ context.Context, id string) (*models.WorkflowInstance, error) {
	if wf, ok := f.workflows[id]; ok {
		return wf, nil
	}
	return nil, store.ErrNotFound
}
func (f *fakeStore) ListPriorityLanes(_ context.Context, _ string, projectID *string) ([]models.PriorityLane, error) {
	if f.lanesErr != nil {
		return nil, f.lanesErr
	}
	if projectID != nil {
		return f.projLanes, nil
	}
	return f.orgLanes, nil
}
func (f *fakeStore) GetPriorityLane(context.Context, string, *string, string) (*models.PriorityLane, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) UpsertPriorityLane(context.Context, *models.PriorityLane) error    { return nil }
func (f *fakeStore) DeletePriorityLane(context.Context, string, *string, string) error { return nil }
func (f *fakeStore) ListPrincipalGroupShifts(_ context.Context, _ string, principalID string) ([]models.GroupPriorityShift, error) {
	return f.shifts[principalID], nil
}
func (f *fakeStore) ListGroupShifts(context.Context, string, []string) ([]models.GroupPriorityShift, error) {
	return f.owner, nil
}
func (f *fakeStore) SetGroupPriorityShift(context.Context, string, int) error      { return nil }
func (f *fakeStore) EffectiveDraftPRs(context.Context, string) (string, error)     { return "run", nil }
func (f *fakeStore) SetOrganizationDraftPRs(context.Context, string, string) error { return nil }
func (f *fakeStore) GetShedState(context.Context, string) (models.ShedState, error) {
	return f.shed, nil
}
func (f *fakeStore) SetGlobalShedLevel(context.Context, string) error { return nil }
func (f *fakeStore) SetOrganizationShed(context.Context, string, string, *models.ShedActionsConfig, bool) error {
	return nil
}

func strPtr(value string) *string { return &value }

func TestAssignReadsEventFacts(t *testing.T) {
	author, rerunner := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	st := &fakeStore{
		events: map[string]*models.CIEvent{
			"ev-pr":    {EventID: "ev-pr", Source: "webhook", EventType: "pull_request_opened", RefClasses: []string{"pr"}, Summary: models.CIEventSummary{Draft: true}, ActorPrincipalID: &author},
			"ev-rerun": {EventID: "ev-rerun", Source: models.CIEventSourceRerun, EventType: "pull_request_updated", RefClasses: []string{"pr"}, ActorPrincipalID: &rerunner},
			"ev-man":   {EventID: "ev-man", Source: models.CIEventSourceManual, EventType: "push", RefClasses: []string{"manual"}, ActorPrincipalID: &author},
		},
		shifts: map[string][]models.GroupPriorityShift{
			author:   {{Name: "authors", Shift: 10}},
			rerunner: {{Name: "admins", Shift: 50}},
		},
	}
	org := "33333333-3333-3333-3333-333333333333"

	// A draft PR work job: lane pr-draft, the author's group applies, and the
	// job's own priority is the request.
	job := &models.Job{OrgID: org, EventID: strPtr("ev-pr"), Priority: 4}
	d := Assign(context.Background(), st, job)
	assert.Equal(t, "pr-draft", d.Lane)
	assert.Equal(t, -200+10+4, job.Priority)
	assert.Equal(t, "pr-draft", job.PriorityLane)
	require.NotNil(t, job.PriorityReason)
	assert.Equal(t, "priority", job.PriorityReason.Kind)

	// Assign again (for example a second submit attempt): the request stays 4.
	Assign(context.Background(), st, job)
	assert.Equal(t, -186, job.Priority)

	// A rerun event: the re-runner's groups do not apply.
	rerun := &models.Job{OrgID: org, EventID: strPtr("ev-rerun")}
	Assign(context.Background(), st, rerun)
	assert.Equal(t, -100, rerun.Priority)

	// A manual event has event type manual whatever the submitter stated.
	manual := &models.Job{OrgID: org, EventID: strPtr("ev-man")}
	facts := FactsForJob(context.Background(), st, manual)
	assert.Equal(t, "manual", facts.EventType)
	assert.Equal(t, author, facts.ActorPrincipalID)
}

func TestAssignEvalAndFallbacks(t *testing.T) {
	org := "33333333-3333-3333-3333-333333333333"
	st := &fakeStore{
		jobs:      map[string]*models.Job{"parent": {JobID: "parent", RefClasses: []string{"default"}}},
		workflows: map[string]*models.WorkflowInstance{"wf": {WorkflowID: "wf", RefClasses: []string{"schedule"}}},
	}
	// An eval job: the BuildEvalJob priority is not a request.
	eval := &models.Job{OrgID: org, Priority: 10, JobEnvVars: models.JSONB{"REACTORCIDE_JOB_KIND": "eval"}, RefClasses: []string{"pr"}}
	Assign(context.Background(), st, eval)
	assert.Equal(t, 10, eval.Priority)
	assert.Equal(t, "eval-pr", eval.PriorityLane)

	// Classes from the workflow first, then the parent.
	node := &models.Job{OrgID: org, WorkflowID: strPtr("wf"), ParentJobID: strPtr("parent")}
	Assign(context.Background(), st, node)
	assert.Equal(t, "schedule", node.PriorityLane)
	child := &models.Job{OrgID: org, ParentJobID: strPtr("parent")}
	Assign(context.Background(), st, child)
	assert.Equal(t, "default-branch", child.PriorityLane)

	// A store with no events: a pull request from the VCS metadata.
	pr := 7
	legacy := &models.Job{OrgID: org, PRNumber: &pr, JobEnvVars: models.JSONB{"REACTORCIDE_JOB_KIND": "eval"}}
	Assign(context.Background(), struct{}{}, legacy)
	assert.Equal(t, "eval-pr", legacy.PriorityLane)
	assert.Equal(t, 10, legacy.Priority)

	// A lane read failure falls back to the built-in lanes.
	st.lanesErr = errors.New("down")
	st.orgLanes = []models.PriorityLane{{Name: "x", Base: 400, Enabled: true, Position: 1}}
	fallback := &models.Job{OrgID: org, RefClasses: []string{"default"}}
	Assign(context.Background(), st, fallback)
	assert.Equal(t, "default-branch", fallback.PriorityLane)
}

func TestAssignAppliesShedLevel(t *testing.T) {
	org := "33333333-3333-3333-3333-333333333333"
	st := &fakeStore{shed: models.EffectiveShedState(models.ShedLevelNormal, models.ShedLevelDegraded, nil)}
	job := &models.Job{OrgID: org, RefClasses: []string{"pr"}, JobEnvVars: models.JSONB{"REACTORCIDE_JOB_KIND": "eval"}}
	Assign(context.Background(), st, job)
	assert.Equal(t, 10+ShedPRShift, job.Priority)
	push := &models.Job{OrgID: org, RefClasses: []string{"default"}, JobEnvVars: models.JSONB{"REACTORCIDE_JOB_KIND": "eval"}}
	Assign(context.Background(), st, push)
	assert.Equal(t, 5, push.Priority, "the shed level lowers only pull requests")
}

func TestAssignRetry(t *testing.T) {
	org := "33333333-3333-3333-3333-333333333333"
	rerunner := "22222222-2222-2222-2222-222222222222"
	st := &fakeStore{shifts: map[string][]models.GroupPriorityShift{rerunner: {{Name: "admins", Shift: 50}}}}
	original := &models.Job{JobID: "orig", OrgID: org, RefClasses: []string{"pr"}}
	Assign(context.Background(), st, original)
	require.Equal(t, -100, original.Priority)

	retry := &models.Job{OrgID: org, RefClasses: []string{"pr"}, ActorPrincipalID: &rerunner, Priority: original.Priority}
	d := AssignRetry(context.Background(), st, original, retry)
	assert.Equal(t, -100, retry.Priority, "the re-runner's groups do not apply to a retry")
	assert.Equal(t, "pr-ready", retry.PriorityLane)
	assert.Equal(t, "orig", d.RetainedFrom)
	assert.Contains(t, retry.PriorityReason.Detail, "kept from job orig")

	// A job made before lanes keeps its stored priority.
	old := &models.Job{JobID: "old", OrgID: org, Priority: 7}
	again := &models.Job{OrgID: org, Priority: 99}
	AssignRetry(context.Background(), st, old, again)
	assert.Equal(t, 7, again.Priority)
	assert.Empty(t, again.PriorityLane)
	assert.Nil(t, again.PriorityReason)
}

func TestExplainFacts(t *testing.T) {
	st := &fakeStore{orgLanes: []models.PriorityLane{{LaneID: "l", Name: "hotfix", Position: 1, Base: 300, ClassesAny: []string{"hotfix"}, Enabled: true}}}
	org := "33333333-3333-3333-3333-333333333333"
	e := ExplainFacts(context.Background(), st, Facts{OrgID: org, RefClasses: []string{"HOTFIX"}})
	assert.Equal(t, "hotfix", e.Decision.Lane)
	assert.Equal(t, "work", e.Facts.JobKind)
	require.NotEmpty(t, e.Lanes)
	assert.True(t, e.Lanes[0].Matched)
	assert.Equal(t, ScopeOrg, e.Lanes[0].Scope)

	job := &models.Job{JobID: "j", OrgID: org, RefClasses: []string{"default"}}
	Assign(context.Background(), st, job)
	je := ExplainJob(context.Background(), st, job)
	require.NotNil(t, je.Stored)
	assert.Equal(t, "default-branch", je.Stored.Lane)
	assert.Equal(t, "j", je.JobID)
}

func TestMatchText(t *testing.T) {
	yes := true
	assert.Equal(t, "every job", MatchText(models.PriorityLane{}))
	assert.Equal(t, "eval jobs; classes any of pr; draft", MatchText(models.PriorityLane{JobKind: "eval", ClassesAny: []string{"pr"}, Draft: &yes}))
}
