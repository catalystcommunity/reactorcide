package uiapi

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/priority"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestPriorityWireConversions(t *testing.T) {
	builtin, ok := priority.BuiltinByName("pr-ready")
	if !ok {
		t.Fatal("pr-ready is a built-in lane")
	}
	wire := laneToWire(builtin)
	if wire.Name != "pr-ready" || wire.Base != -100 || *wire.Scope != "builtin" || *wire.RuleId != "builtin:pr-ready" || wire.LaneId != nil {
		t.Fatalf("lane wire = %+v", wire)
	}
	if wire.Match.Draft == nil || *wire.Match.Draft || len(wire.Match.ClassesAny) != 1 || wire.Match.Retry != nil {
		t.Fatalf("match wire = %+v", wire.Match)
	}
	if wire.MatchText != "classes any of pr; not a draft" {
		t.Fatalf("match text = %q", wire.MatchText)
	}

	job := &models.Job{JobID: "j1", Name: "build", Priority: 0}
	decision := priority.Compute(priority.Effective(nil, nil),
		priority.GroupShifts{Actor: []models.GroupPriorityShift{{Name: "release", Shift: 20}}},
		priority.Facts{RefClasses: []string{"pr"}})
	decision.Apply(job)
	summary := jobToSummary(job)
	if summary.PriorityLane == nil || *summary.PriorityLane != "pr-ready" || summary.Priority != -80 {
		t.Fatalf("job summary priority = %v %v", summary.PriorityLane, summary.Priority)
	}
	reason := summary.PriorityReason
	if reason == nil || reason.Kind != "priority" || *reason.Lane != "pr-ready" || *reason.Base != -100 || *reason.Final != -80 ||
		len(reason.Shifts) != 1 || reason.Shifts[0].Value != 20 || !reason.Shifts[0].Applied || *reason.Shifts[0].Group != "release" {
		t.Fatalf("job priority reason = %+v", reason)
	}
	if old := jobToSummary(&models.Job{JobID: "j0", Priority: 5}); old.PriorityLane != nil || old.PriorityReason != nil {
		t.Fatalf("a job made before lanes has no lane: %+v", old)
	}

	group := groupToSummary(&models.Group{GroupID: "g", OrgID: "o", Name: "release", PriorityShift: -15})
	if group.PriorityShift == nil || *group.PriorityShift != -15 {
		t.Fatalf("group shift = %v", group.PriorityShift)
	}
	if d := priorityDecisionToWire(decision); d.Final != -80 || d.Summary == "" || d.Scope != "builtin" {
		t.Fatalf("decision wire = %+v", d)
	}
}
