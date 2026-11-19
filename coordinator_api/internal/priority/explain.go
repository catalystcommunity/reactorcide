package priority

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/shedding"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// LaneCheck tells whether one effective lane matched.
type LaneCheck struct {
	Name    string `json:"name"`
	RuleID  string `json:"rule_id"`
	Scope   string `json:"scope"`
	Base    int    `json:"base"`
	Enabled bool   `json:"enabled"`
	Matched bool   `json:"matched"`
	Match   string `json:"match"`
}

// Explanation is the output of explain: the facts, the decision, and each
// lane in evaluation order. For a stored job, Stored is its recorded
// decision.
type Explanation struct {
	Facts    Facts       `json:"facts"`
	Decision Decision    `json:"decision"`
	Lanes    []LaneCheck `json:"lanes"`
	JobID    string      `json:"job_id,omitempty"`
	Stored   *Decision   `json:"stored,omitempty"`
}

// ExplainFacts computes the decision for facts that the caller gives, with
// the current lanes and group shifts. The actor is the principal in
// facts.ActorPrincipalID. The shed level in force applies.
func ExplainFacts(ctx context.Context, st any, facts Facts) Explanation {
	facts.RefClasses = refclass.Normalize(facts.RefClasses)
	facts.Shed = shedding.State(ctx, st, facts.OrgID).Actions
	if facts.JobKind == "" {
		facts.JobKind = models.PriorityJobKindWork
	}
	lanes := LoadLanes(ctx, st, facts.OrgID, facts.ProjectID)
	decision := Compute(lanes, LoadGroupShifts(ctx, st, facts), facts)
	return Explanation{Facts: facts, Decision: decision, Lanes: checks(lanes, facts)}
}

// ExplainJob explains a stored job: its facts now, the decision that the
// current lanes give, and the decision that the job recorded.
func ExplainJob(ctx context.Context, st any, job *models.Job) Explanation {
	facts := FactsForJob(ctx, st, job)
	explanation := ExplainFacts(ctx, st, facts)
	explanation.JobID = job.JobID
	if job.PriorityReason != nil && job.PriorityReason.Kind == ReasonKind {
		stored := DecisionFromReason(*job.PriorityReason)
		explanation.Stored = &stored
	}
	return explanation
}

func checks(lanes []Lane, facts Facts) []LaneCheck {
	out := make([]LaneCheck, 0, len(lanes))
	for _, lane := range lanes {
		out = append(out, LaneCheck{Name: lane.Name, RuleID: lane.RuleID, Scope: lane.Scope, Base: lane.Base,
			Enabled: lane.Enabled, Matched: Matches(lane.PriorityLane, facts), Match: MatchText(lane.PriorityLane)})
	}
	return out
}
