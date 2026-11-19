// Package priority computes the queue priority of a job from lanes and group
// shifts (plan item D1, owner decision D10; docs/priority.md).
//
// Priority is the only capacity mechanism. Corndogs gives the next task of a
// queue set by priority (higher first), then by age. Reactorcide has no
// per-tenant caps and no fair share.
//
// A lane is a coordinator rule on trusted facts of a job: its ref classes,
// its event type and source, the draft state of its pull request, its kind
// (eval or work), and whether it is a retry. The first lane that matches
// gives the base priority. A group of the actor (and, later, the owner group
// of the workflow) adds a bounded shift. Every submit path calls Assign (or
// AssignRetry), so all paths agree.
package priority

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Bounds.
const (
	// MinBase and MaxBase bound the base priority of a lane.
	MinBase = -500
	MaxBase = 500
	// MinGroupShift and MaxGroupShift bound the shift of a group.
	MinGroupShift = -50
	MaxGroupShift = 50
	// MaxRequested bounds the priority that a job specification or a
	// submitter asks for. The request moves the job inside its lane only:
	// the built-in lanes of work jobs are 100 apart.
	MaxRequested = 50
	// ShedPRShift is the shift of pull request work while the shed level in
	// force has lower_pr_priority. It moves the work one lane down.
	ShedPRShift = -100
	// MinPriority and MaxPriority bound the final priority.
	MinPriority = -1000
	MaxPriority = 1000
)

// Lane scopes.
const (
	ScopeBuiltin = "builtin"
	ScopeOrg     = "org"
	ScopeProject = "project"
)

// Shift sources.
const (
	ShiftSourceGroup      = "group"
	ShiftSourceOwnerGroup = "owner_group"
	ShiftSourceRequested  = "requested"
	ShiftSourceShed       = "shed"
)

// ReasonKind is the decision reason kind of a priority decision.
const ReasonKind = "priority"

// FallbackLane is the lane of a job that no enabled lane matches (only when
// an operator disables the built-in "standard" lane).
const FallbackLane = "none"

// Lane is one effective lane: a built-in lane or a stored lane, with its
// scope.
type Lane struct {
	models.PriorityLane
	// Scope is builtin, org or project.
	Scope string `json:"scope"`
	// RuleID is "builtin:NAME" for a built-in lane, else the lane UUID.
	RuleID string `json:"rule_id"`
	// Replaces is the scope of the lane that this lane replaces (same
	// name), or "".
	Replaces string `json:"replaces,omitempty"`
}

func boolPtr(value bool) *bool { return &value }

func builtinLane(position int, name, description string, base int, apply func(*models.PriorityLane)) Lane {
	lane := models.PriorityLane{Name: name, Position: position, Description: description, Base: base, Enabled: true,
		ClassesAny: []string{}, ClassesNone: []string{}, EventTypes: []string{}}
	if apply != nil {
		apply(&lane)
	}
	return Lane{PriorityLane: lane, Scope: ScopeBuiltin, RuleID: "builtin:" + name}
}

// Builtins returns the built-in lanes in evaluation order. The eval lanes
// keep the eval priorities from before lanes (pull request 10, other 5), so
// evals stay ahead of ordinary work.
func Builtins() []Lane {
	return []Lane{
		builtinLane(10, "eval-pr-draft", "The eval job of a draft pull request.", 5, func(l *models.PriorityLane) {
			l.JobKind, l.ClassesAny, l.Draft = models.PriorityJobKindEval, []string{"pr"}, boolPtr(true)
		}),
		builtinLane(20, "eval-pr", "The eval job of a pull request.", 10, func(l *models.PriorityLane) {
			l.JobKind, l.ClassesAny = models.PriorityJobKindEval, []string{"pr"}
		}),
		builtinLane(30, "eval", "Every other eval job.", 5, func(l *models.PriorityLane) {
			l.JobKind = models.PriorityJobKindEval
		}),
		builtinLane(50, "backfill", "Work with the ref class backfill. Give the class with a ref class rule.", -400, func(l *models.PriorityLane) {
			l.ClassesAny = []string{"backfill"}
		}),
		builtinLane(100, "release", "Release and production work: the ref classes release, production, or tag.", 100, func(l *models.PriorityLane) {
			l.ClassesAny = []string{"release", "production", "tag"}
		}),
		builtinLane(200, "default-branch", "Work of a push to the default branch.", 0, func(l *models.PriorityLane) {
			l.ClassesAny = []string{"default"}
		}),
		builtinLane(300, "manual", "Work of a manual submit or a manual run.", 0, func(l *models.PriorityLane) {
			l.ClassesAny = []string{"manual"}
		}),
		builtinLane(400, "pr-ready", "Work of a pull request that is ready for review.", -100, func(l *models.PriorityLane) {
			l.ClassesAny, l.Draft = []string{"pr"}, boolPtr(false)
		}),
		builtinLane(500, "pr-draft", "Work of a draft pull request.", -200, func(l *models.PriorityLane) {
			l.ClassesAny, l.Draft = []string{"pr"}, boolPtr(true)
		}),
		builtinLane(600, "schedule", "Work of a schedule fire.", -300, func(l *models.PriorityLane) {
			l.ClassesAny = []string{"schedule"}
		}),
		builtinLane(700, "branch", "Work of a push to a branch that is not the default branch.", -100, func(l *models.PriorityLane) {
			l.EventTypes = []string{"push"}
		}),
		builtinLane(1000, "standard", "Every other job.", 0, nil),
	}
}

// BuiltinByName returns the built-in lane with the name, if there is one.
func BuiltinByName(name string) (Lane, bool) {
	for _, lane := range Builtins() {
		if lane.Name == name {
			return lane, true
		}
	}
	return Lane{}, false
}

// Effective merges the built-in lanes, the organization lanes and the
// project lanes. A lane replaces the lane with the same name of a lower
// scope (project > org > builtin). The result is sorted by position, then
// name.
func Effective(orgLanes, projectLanes []models.PriorityLane) []Lane {
	byName := map[string]Lane{}
	order := []string{}
	put := func(lane Lane) {
		if existing, ok := byName[lane.Name]; ok {
			lane.Replaces = existing.Scope
		} else {
			order = append(order, lane.Name)
		}
		byName[lane.Name] = lane
	}
	for _, lane := range Builtins() {
		put(lane)
	}
	for _, lane := range orgLanes {
		put(Lane{PriorityLane: lane, Scope: ScopeOrg, RuleID: lane.LaneID})
	}
	for _, lane := range projectLanes {
		put(Lane{PriorityLane: lane, Scope: ScopeProject, RuleID: lane.LaneID})
	}
	lanes := make([]Lane, 0, len(order))
	for _, name := range order {
		lanes = append(lanes, byName[name])
	}
	SortLanes(lanes)
	return lanes
}

// SortLanes sorts lanes by position, then name.
func SortLanes(lanes []Lane) {
	sort.SliceStable(lanes, func(i, j int) bool {
		if lanes[i].Position != lanes[j].Position {
			return lanes[i].Position < lanes[j].Position
		}
		return lanes[i].Name < lanes[j].Name
	})
}

// Matches reports whether the lane matches the facts. A disabled lane never
// matches. A retry lane matches only a retry, and a lane that is not a retry
// lane matches only a job that is not a retry (a retry keeps the lane of its
// original job; see AssignRetry).
func Matches(lane models.PriorityLane, facts Facts) bool {
	if !lane.Enabled || lane.Retry != facts.Retry {
		return false
	}
	switch lane.JobKind {
	case models.PriorityJobKindEval:
		if facts.JobKind != models.PriorityJobKindEval {
			return false
		}
	case models.PriorityJobKindWork:
		if facts.JobKind == models.PriorityJobKindEval {
			return false
		}
	}
	if lane.Draft != nil && *lane.Draft != facts.Draft {
		return false
	}
	if len(lane.EventTypes) > 0 && !contains(lane.EventTypes, facts.EventType) {
		return false
	}
	return refclass.Satisfies(facts.RefClasses, lane.ClassesAny, lane.ClassesNone)
}

// Select returns the first lane that matches, or false.
func Select(lanes []Lane, facts Facts) (Lane, bool) {
	for _, lane := range lanes {
		if Matches(lane.PriorityLane, facts) {
			return lane, true
		}
	}
	return Lane{}, false
}

var laneNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidLaneName reports whether name is a valid lane name.
func ValidLaneName(name string) bool { return laneNamePattern.MatchString(name) }

// KnownEventTypes are the event types that a lane can match.
func KnownEventTypes() []string { return refclass.KnownEventTypes() }

// ValidateLane checks a stored lane.
func ValidateLane(lane models.PriorityLane) error {
	if !ValidLaneName(lane.Name) {
		return fmt.Errorf("lane name %q must have lowercase letters, digits and hyphens, and at most 63 characters", lane.Name)
	}
	if lane.Name == FallbackLane {
		return fmt.Errorf("lane name %q is reserved", FallbackLane)
	}
	if lane.Base < MinBase || lane.Base > MaxBase {
		return fmt.Errorf("base %d must be from %d to %d", lane.Base, MinBase, MaxBase)
	}
	if lane.Position < -100000 || lane.Position > 100000 {
		return fmt.Errorf("position %d must be from -100000 to 100000", lane.Position)
	}
	switch lane.JobKind {
	case models.PriorityJobKindAny, models.PriorityJobKindEval, models.PriorityJobKindWork:
	default:
		return fmt.Errorf("job kind %q must be eval, work, or empty", lane.JobKind)
	}
	if err := refclass.ValidateClasses(lane.ClassesAny); err != nil {
		return err
	}
	if err := refclass.ValidateClasses(lane.ClassesNone); err != nil {
		return err
	}
	known := KnownEventTypes()
	for _, eventType := range lane.EventTypes {
		if !contains(known, eventType) {
			return fmt.Errorf("unknown event type %q", eventType)
		}
	}
	return nil
}

// ValidateGroupShift checks a group shift.
func ValidateGroupShift(shift int) error {
	if shift < MinGroupShift || shift > MaxGroupShift {
		return fmt.Errorf("priority shift %d must be from %d to %d", shift, MinGroupShift, MaxGroupShift)
	}
	return nil
}

// MatchText is the match facts of a lane as one short text.
func MatchText(lane models.PriorityLane) string {
	parts := []string{}
	switch lane.JobKind {
	case models.PriorityJobKindEval:
		parts = append(parts, "eval jobs")
	case models.PriorityJobKindWork:
		parts = append(parts, "work jobs")
	}
	if len(lane.ClassesAny) > 0 {
		parts = append(parts, "classes any of "+strings.Join(lane.ClassesAny, ", "))
	}
	if len(lane.ClassesNone) > 0 {
		parts = append(parts, "classes none of "+strings.Join(lane.ClassesNone, ", "))
	}
	if len(lane.EventTypes) > 0 {
		parts = append(parts, "events "+strings.Join(lane.EventTypes, ", "))
	}
	if lane.Draft != nil {
		if *lane.Draft {
			parts = append(parts, "draft")
		} else {
			parts = append(parts, "not a draft")
		}
	}
	if lane.Retry {
		parts = append(parts, "retries only")
	}
	if len(parts) == 0 {
		return "every job"
	}
	return strings.Join(parts, "; ")
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
