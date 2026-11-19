package priority

import (
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Facts are the trusted facts of one job that the lanes and shifts read.
// Workflow code cannot set them: the coordinator takes them from its own
// records (the event, the parent job, the workflow, the authenticated
// submit).
type Facts struct {
	OrgID     string  `json:"org_id,omitempty"`
	ProjectID *string `json:"project_id,omitempty"`
	// RefClasses are the ref classes of the job (docs/ref-classes.md).
	RefClasses []string `json:"ref_classes"`
	// EventType is the event type of the CI event of the job. A manual
	// event always has the type "manual", as for ref classes.
	EventType string `json:"event_type,omitempty"`
	// EventSource is webhook, schedule, manual, retry, approval, or rerun.
	EventSource string `json:"event_source,omitempty"`
	// Draft is true for the work of a draft pull request.
	Draft bool `json:"draft"`
	// JobKind is eval or work.
	JobKind string `json:"job_kind"`
	// Retry is true for a retry of a job. Compute uses only retry lanes for
	// a retry; AssignRetry keeps the original lane when none matches.
	Retry bool `json:"retry"`
	// ActorPrincipalID is the principal whose groups shift the priority:
	// the actor of the event, else the actor of the job. Empty or the
	// system principal: no group shift.
	ActorPrincipalID string `json:"actor_principal_id,omitempty"`
	// OwnerGroupIDs are the groups of the trusted owner snapshot of the
	// workflow (plan item B2, WP7; FactsForJob sets them).
	OwnerGroupIDs []string `json:"owner_group_ids,omitempty"`
	// Requested is the priority that the job specification or the
	// submitter asked for. It is ignored for an eval job.
	Requested int `json:"requested,omitempty"`
	// Shed are the actions of the shed level in force.
	Shed models.ShedActions `json:"-"`
}

// Decision is how the priority of a job was computed.
type Decision struct {
	Lane string `json:"lane"`
	// RuleID is "builtin:NAME" or the UUID of the stored lane.
	RuleID string `json:"rule_id"`
	// Scope is builtin, org, or project.
	Scope  string                 `json:"scope"`
	Base   int                    `json:"base"`
	Shifts []models.PriorityShift `json:"shifts"`
	Final  int                    `json:"final"`
	// RetainedFrom is the job whose decision a retry kept.
	RetainedFrom string `json:"retained_from,omitempty"`
}

// ShedAdjustment is the shift that the shed actions give work with these
// facts: ShedPRShift for pull request work (an eval or a work job) while
// lower_pr_priority is on, else 0. WP9 shed levels (plan item D4) use this
// function, so "lower PR priority" moves pull requests with the lane
// mechanism.
func ShedAdjustment(actions models.ShedActions, facts Facts) int {
	if actions.LowerPRPriority && refclass.Has(facts.RefClasses, "pr") {
		return ShedPRShift
	}
	return 0
}

// GroupShifts are the shifts of the groups of the actor and the owner
// groups of the workflow.
type GroupShifts struct {
	Actor []models.GroupPriorityShift
	Owner []models.GroupPriorityShift
}

// Compute selects the lane and combines the shifts. lanes must be the
// effective lanes (Effective). It never fails: when no lane matches, the
// lane is FallbackLane with base 0.
//
// Combine rule: final = clamp(base + group + requested + shed) where group is
// the LARGEST shift of the groups that have a shift (actor groups and owner
// groups together; a group with shift 0 has no shift), requested is the
// request clamped to +-MaxRequested (work jobs only), and shed is
// ShedAdjustment. The final value is clamped to MinPriority..MaxPriority.
func Compute(lanes []Lane, groups GroupShifts, facts Facts) Decision {
	lane, ok := Select(lanes, facts)
	decision := Decision{Lane: FallbackLane, RuleID: "builtin:" + FallbackLane, Scope: ScopeBuiltin}
	if ok {
		decision = Decision{Lane: lane.Name, RuleID: lane.RuleID, Scope: lane.Scope, Base: lane.Base}
	}
	decision.Shifts = combineShifts(groups, facts)
	decision.Final = finalOf(decision.Base, decision.Shifts)
	return decision
}

func combineShifts(groups GroupShifts, facts Facts) []models.PriorityShift {
	shifts := []models.PriorityShift{}
	best := -1
	add := func(source string, group models.GroupPriorityShift) {
		if group.Shift == 0 {
			return
		}
		shift := models.PriorityShift{Source: source, Group: group.Name, Value: clamp(group.Shift, MinGroupShift, MaxGroupShift)}
		shifts = append(shifts, shift)
		if best < 0 || shift.Value > shifts[best].Value {
			best = len(shifts) - 1
		}
	}
	for _, group := range groups.Actor {
		add(ShiftSourceGroup, group)
	}
	for _, group := range groups.Owner {
		add(ShiftSourceOwnerGroup, group)
	}
	if best >= 0 {
		shifts[best].Applied = true
	}
	if facts.JobKind != models.PriorityJobKindEval && facts.Requested != 0 {
		shifts = append(shifts, models.PriorityShift{Source: ShiftSourceRequested,
			Value: clamp(facts.Requested, -MaxRequested, MaxRequested), Applied: true})
	}
	if shed := ShedAdjustment(facts.Shed, facts); shed != 0 {
		shifts = append(shifts, models.PriorityShift{Source: ShiftSourceShed, Value: shed, Applied: true})
	}
	return shifts
}

func finalOf(base int, shifts []models.PriorityShift) int {
	total := base
	for _, shift := range shifts {
		if shift.Applied {
			total += shift.Value
		}
	}
	return clamp(total, MinPriority, MaxPriority)
}

// RequestedOf returns the requested priority of a decision (0 when none).
func RequestedOf(reason *models.DecisionReason) (int, bool) {
	if reason == nil {
		return 0, false
	}
	for _, shift := range reason.Shifts {
		if shift.Source == ShiftSourceRequested {
			return shift.Value, true
		}
	}
	return 0, true
}

// Retain returns the decision that a retry of original keeps. With a retry
// lane in lanes that matches facts (facts.Retry true), the retry gets the
// base of that lane and keeps the group shifts and the request of the
// original; the re-runner's groups never apply. The bool is false when the
// original has no recorded decision (a job made before lanes): then the
// retry keeps the stored priority of the original.
func Retain(original *models.Job, lanes []Lane, facts Facts) (Decision, bool) {
	if original == nil {
		return Decision{}, false
	}
	reason := original.PriorityReason
	if reason == nil || reason.Kind != ReasonKind {
		return Decision{}, false
	}
	decision := DecisionFromReason(*reason)
	decision.RetainedFrom = original.JobID
	facts.Retry = true
	if lane, ok := Select(lanes, facts); ok {
		decision.Lane, decision.RuleID, decision.Scope, decision.Base = lane.Name, lane.RuleID, lane.Scope, lane.Base
		kept := []models.PriorityShift{}
		for _, shift := range decision.Shifts {
			if shift.Source != ShiftSourceShed {
				kept = append(kept, shift)
			}
		}
		if shed := ShedAdjustment(facts.Shed, facts); shed != 0 {
			kept = append(kept, models.PriorityShift{Source: ShiftSourceShed, Value: shed, Applied: true})
		}
		decision.Shifts = kept
		decision.Final = finalOf(decision.Base, decision.Shifts)
	}
	return decision, true
}

// Reason is the structured decision reason {kind: "priority", lane, base,
// shifts, final}.
func (d Decision) Reason() models.DecisionReason {
	base, final := d.Base, d.Final
	shifts := append([]models.PriorityShift{}, d.Shifts...)
	reason := models.DecisionReason{Kind: ReasonKind, Lane: d.Lane, Base: &base, Shifts: shifts, Final: &final,
		RuleID: d.RuleID, Value: d.Scope, Detail: d.Summary()}
	return reason
}

// DecisionFromReason reads a stored reason back.
func DecisionFromReason(reason models.DecisionReason) Decision {
	decision := Decision{Lane: reason.Lane, RuleID: reason.RuleID, Scope: reason.Value,
		Shifts: append([]models.PriorityShift{}, reason.Shifts...)}
	if reason.Base != nil {
		decision.Base = *reason.Base
	}
	if reason.Final != nil {
		decision.Final = *reason.Final
	}
	return decision
}

// Summary is one line, for example
// "priority -90: lane pr-ready (builtin) base -100, group release-team +10".
func (d Decision) Summary() string {
	scope := d.Scope
	if scope == "" {
		scope = "kept"
	}
	parts := []string{fmt.Sprintf("lane %s (%s) base %d", d.Lane, scope, d.Base)}
	for _, shift := range d.Shifts {
		if !shift.Applied {
			continue
		}
		label := shift.Source
		if shift.Group != "" {
			label += " " + shift.Group
		}
		parts = append(parts, fmt.Sprintf("%s %+d", label, shift.Value))
	}
	text := fmt.Sprintf("priority %d: %s", d.Final, strings.Join(parts, ", "))
	if d.RetainedFrom != "" {
		text += " (kept from job " + d.RetainedFrom + ")"
	}
	return text
}

// Apply writes the decision into the job fields: Priority, PriorityLane and
// PriorityReason.
func (d Decision) Apply(job *models.Job) {
	if job == nil {
		return
	}
	reason := d.Reason()
	job.Priority = d.Final
	job.PriorityLane = d.Lane
	job.PriorityReason = &reason
}
