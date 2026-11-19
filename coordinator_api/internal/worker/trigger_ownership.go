package worker

import (
	"context"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/owners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// Workflow ownership (docs/workflow-design.md "Owners and contact").
//
// The coordinator reads the owner of a workflow from the TRUSTED CI
// definition only:
//
//   - Policy candidates (a base and a head CI checkout): the base definition
//     gives the owner, also when policy grants head CI. A workflow that only
//     the head has gets "no owner". A pull request cannot change the owner
//     of the workflow it runs.
//   - One CI checkout at a trusted revision (the base SHA, a push, a tag, a
//     merged pull request, a separate CI repository): that definition.
//   - One CI checkout at the pull request head (a project with no stored CI
//     policy runs the event's own CI): this content is not trusted for
//     ownership, so the coordinator copies the latest earlier trusted
//     snapshot of the same workflow (source "prior"), or records unknown.
//
// The REF of the CI definition must also be trusted (ownerRefTrust): the
// default branch (ref class default), a pull request whose base is the
// default branch, a tag (ref class tag), or the separate CI repository of
// the project. A push to another branch is pusher-controlled: with no
// policy its CI is the pushed commit, and with a policy the base is
// push.before, the pusher's own earlier commit. Such a run behaves like a
// pull request head: it copies the prior trusted snapshot or records
// unknown. Only a trusted run writes source "ci".

// setOwnershipFromSpec sets the snapshot of spec from a trusted owners and
// contact block. An invalid block records unknown.
func (tp *TriggerProcessor) setOwnershipFromSpec(parentJob *models.Job, spec *triggerWorkflowSpec, owner *owners.Spec, contact *owners.ContactSpec) {
	snapshot, err := owners.FromSpec(owner, contact, parentJob.CISHA)
	if err != nil {
		logging.Log.WithError(err).WithField("workflow_security_id", workflowSecurityIDOf(*spec)).
			Warn("The trusted workflow definition has an invalid owners or contact block; the owner is unknown")
		spec.Ownership, spec.OwnershipSource = nil, ""
		return
	}
	spec.Ownership, spec.OwnershipSource = &snapshot, models.OwnerSourceCI
}

// assignTrustedOwnership sets the owner snapshot of every eval batch. A
// snapshot that selectPolicyCandidates read from the base definition stays
// only when the ref is trusted; otherwise the batch gets the prior trusted
// snapshot.
func (tp *TriggerProcessor) assignTrustedOwnership(ctx context.Context, parentJob *models.Job, batches []triggerWorkflowSpec) {
	eventContent := evalCIIsEventContent(parentJob)
	trusted, classes := tp.ownerRefTrust(ctx, parentJob)
	for i := range batches {
		if batches[i].Ownership != nil && batches[i].OwnershipSource == models.OwnerSourceCI && trusted {
			batches[i].Ownership.Trusted, batches[i].Ownership.RefClasses = true, classes
			continue
		}
		if batches[i].Ownership == nil && !eventContent && trusted {
			tp.setOwnershipFromSpec(parentJob, &batches[i], batches[i].Owners, batches[i].Contact)
			if batches[i].Ownership != nil {
				batches[i].Ownership.Trusted, batches[i].Ownership.RefClasses = true, classes
			}
			continue
		}
		batches[i].Ownership, batches[i].OwnershipSource = nil, ""
		if parentJob.ProjectID == nil {
			continue
		}
		prior, err := owners.ForWorkflow(ctx, tp.store, *parentJob.ProjectID, workflowSecurityIDOf(batches[i]))
		if err != nil {
			logging.Log.WithError(err).Warn("Could not read the earlier trusted owner of the workflow")
			continue
		}
		if prior.Known() && prior.Trusted {
			batches[i].Ownership, batches[i].OwnershipSource = &prior, models.OwnerSourcePrior
		}
	}
}

// ownerRefTrust reports whether the CI definition of the eval came from a
// trusted ref, and the ref classes of the run. The facts come from the
// coordinator only: the ref classes of the job (a database trigger sets
// them), the stored facts of the event, and the project settings.
func (tp *TriggerProcessor) ownerRefTrust(ctx context.Context, parentJob *models.Job) (bool, []string) {
	classes := refclass.ForJob(parentJob)
	var event *models.CIEvent
	if eventID := stringValue(parentJob.EventID); eventID != "" {
		if events, ok := tp.store.(store.CIEventStore); ok {
			if loaded, err := events.GetCIEvent(ctx, eventID); err == nil && loaded != nil {
				event = loaded
				if eventClasses := refclass.ForEvent(loaded); len(eventClasses) > 0 {
					classes = eventClasses
				}
			}
		}
	}
	if containsString(classes, refclass.ClassDefault) || containsString(classes, refclass.ClassTag) {
		return true, classes
	}
	// A pull request whose base is the default branch: the base SHA (and the
	// merge SHA of a merged pull request) is default-branch content.
	if event != nil {
		facts := refclass.CompleteFacts(refclass.FactsOfEvent(event))
		if facts.IsPR && facts.IsDefaultBranch && facts.BaseBranch != "" {
			return true, classes
		}
	}
	// The separate CI repository of the project: an admin chose it and its ref.
	if parentJob.ProjectID != nil && parentJob.CIRepository != "" && tp.store != nil {
		if project, err := tp.store.GetProjectByID(ctx, *parentJob.ProjectID); err == nil && project != nil &&
			project.DefaultCISourceURL != "" &&
			vcs.NormalizeRepoURL(project.DefaultCISourceURL) == vcs.NormalizeRepoURL(parentJob.CIRepository) {
			return true, classes
		}
	}
	return false, classes
}

// evalCIIsEventContent reports whether the eval read its CI definition from
// the pull request head itself (no stored CI policy, same-repository pull
// request). Then the definition is not trusted for ownership.
func evalCIIsEventContent(parentJob *models.Job) bool {
	head := stringValue(parentJob.SourceRef)
	if head == "" || parentJob.CISHA == "" || parentJob.CISHA != head {
		return false
	}
	if envString(parentJob, "REACTORCIDE_PR_NUMBER") == "" {
		return false
	}
	return envString(parentJob, "REACTORCIDE_EVENT_TYPE") != string(vcs.EventPullRequestMerged)
}

// applyOwnership writes the chosen snapshot onto a new workflow instance.
func applyOwnership(wf *models.WorkflowInstance, spec *triggerWorkflowSpec) {
	if spec == nil || spec.Ownership == nil {
		return
	}
	wf.SetOwnership(*spec.Ownership, spec.OwnershipSource)
}
