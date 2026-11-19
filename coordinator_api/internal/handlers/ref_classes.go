package handlers

import (
	"context"
	"strings"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// classifyRefs computes the ref classes for trusted facts with the rules of
// the organization and the project (docs/ref-classes.md). When the rules
// cannot be read, only the built-in rules apply: operator classes are then
// missing, so a consumer that needs an operator class refuses (fail closed).
func classifyRefs(ctx context.Context, st interface{}, orgID string, projectID *string, facts models.RefClassFacts) refclass.Result {
	var rules []models.RefClassRule
	if rulesStore, ok := st.(store.RefClassStore); ok && orgID != "" {
		loaded, err := rulesStore.ListEffectiveRefClassRules(ctx, orgID, projectID)
		if err != nil {
			logging.Log.WithError(err).WithField("org_id", orgID).Warn("Failed to load ref class rules; only built-in rules apply")
		} else {
			rules = loaded
		}
	}
	return refclass.Classify(facts, rules)
}

// applyRefClassesToEvent classifies an event before the coordinator stores
// it. The event is the authority for its workflows and jobs.
func applyRefClassesToEvent(ctx context.Context, st interface{}, ciEvent *models.CIEvent, facts models.RefClassFacts) {
	if ciEvent == nil {
		return
	}
	facts.Source = ciEvent.Source
	facts = refclass.CompleteFacts(facts)
	result := classifyRefs(ctx, st, ciEvent.OrgID, ciEvent.ProjectID, facts)
	ciEvent.RefClasses = result.Classes
	ciEvent.RefClassFacts = &facts
	ciEvent.RefClassReasons = result.Reasons
}

// webhookRefClassFacts returns the facts of a signed webhook delivery, or of
// a coordinator re-evaluation of a pull request head (approval, rerun). The
// default branch and the fork relation come from the signed payload.
func webhookRefClassFacts(event *vcs.WebhookEvent) models.RefClassFacts {
	facts := models.RefClassFacts{EventType: string(event.GenericEvent), DefaultBranch: event.Repository.DefaultBranch}
	switch {
	case event.PullRequest != nil:
		facts.IsPR = true
		facts.HeadBranch = event.PullRequest.HeadRef
		facts.BaseBranch = event.PullRequest.BaseRef
		facts.IsFork = event.PullRequest.HeadRepository != nil
	case event.Push != nil:
		ref := event.Push.Ref
		switch {
		case strings.HasPrefix(ref, "refs/tags/"):
			facts.Tag = strings.TrimPrefix(ref, "refs/tags/")
		case event.GenericEvent == vcs.EventTagCreated:
			facts.Tag = extractBranchOrTag(ref)
		default:
			facts.Branch = extractBranchOrTag(ref)
		}
	}
	return facts
}

// applyManualRefClasses classifies a job that an API client submitted with
// no CI event and no parent job. The event type is always "manual". The
// classes are "manual" plus the classes of operator rules that match a
// manual event: a rule that names the event type "manual", or a rule with
// no event types and no ref condition. The branch is the one that the
// submitter states in REACTORCIDE_BRANCH, so a ref condition (branch_glob,
// tag_glob, is_default_branch) applies only in a rule that names "manual"
// (refclass.RuleMatches).
func applyManualRefClasses(ctx context.Context, st interface{}, job *models.Job) {
	if job == nil || job.EventID != nil || job.ParentJobID != nil || job.WorkflowID != nil {
		return
	}
	facts := models.RefClassFacts{Source: refclass.SourceManual, Branch: jobEnvString(job, "REACTORCIDE_BRANCH")}
	job.RefClasses = classifyRefs(ctx, st, job.OrgID, job.ProjectID, facts).Classes
}
