package worker

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// selectPolicyCandidatesRefused is selectPolicyCandidates with only the
// refused path decisions, for tests that check for no refusal.
func (tp *TriggerProcessor) selectPolicyCandidatesRefused(ctx context.Context, parentJob *models.Job, candidates *policyCandidateSet, changedCIPaths []string) ([]triggerWorkflowSpec, []ciPathDecision, map[string]models.DecisionReason, error) {
	selected, decisions, notes, err := tp.selectPolicyCandidates(ctx, parentJob, candidates, changedCIPaths)
	return selected, refusedPaths(decisions), notes, err
}

func refusedPaths(decisions []ciPathDecision) []ciPathDecision {
	out := []ciPathDecision(nil)
	for _, decision := range decisions {
		if decision.Authority == nil {
			out = append(out, decision)
		}
	}
	return out
}
