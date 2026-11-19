package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/owners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/reporting"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// Owner acceptance of path dependencies at admission (plan item A7,
// docs/reporting-and-ownership.md). With path_acceptance warn or enforce, the
// coordinator checks the matched files of each workflow that the eval
// selected by its paths filter. A matched file in an area of another owner
// counts only with an acceptance of that owner for the workflow.
//
//   - enforce: the coordinator drops the files with no acceptance from the
//     match. When no matched file stays, the workflow is skipped with the
//     reason "paths in X are not accepted by owner Y".
//   - warn: the workflow runs; its decision has the same reason as a flag.
//
// The decision reason has kind "path_acceptance".

// pathAcceptanceNotePrefix keys the path acceptance reason of a workflow in
// the notes map of an eval.
const pathAcceptanceNotePrefix = "path-acceptance:"

// DecisionKindPathAcceptance is the kind of the decision reason.
const DecisionKindPathAcceptance = "path_acceptance"

// maxPathAcceptanceFiles caps the files in one reason.
const maxPathAcceptanceFiles = 50

// projectClientSource is the status updater surface that gives the VCS
// client of a project.
type projectClientSource interface {
	ProjectClient(ctx context.Context, projectID string, provider vcs.Provider) vcs.Client
}

// pathOwnershipFor returns the path ownership map of the project: the code
// owners file of the trusted base SHA when the import is on, else the
// coordinator registry. nil means that no owner is known.
func (tp *TriggerProcessor) pathOwnershipFor(ctx context.Context, parentJob *models.Job, event *models.CIEvent,
	settings models.EffectiveReportingSettings) (reporting.OwnershipMap, error) {
	if settings.CodeownersImport != models.CodeownersImportOn {
		if len(settings.PathOwners) == 0 {
			return nil, nil
		}
		return reporting.RegistryMap(settings.PathOwners), nil
	}
	source, ok := tp.statusUpdater.(projectClientSource)
	if !ok || event == nil || event.ProjectID == nil {
		return nil, fmt.Errorf("no VCS client to read the code owners file")
	}
	reader, ok := source.ProjectClient(ctx, *event.ProjectID, vcs.Provider(event.Provider)).(reporting.FileReader)
	if !ok {
		return nil, fmt.Errorf("the VCS client cannot read the code owners file")
	}
	baseSHA := event.BaseSHA
	if baseSHA == "" {
		// A push or a schedule: the trusted CI commit is the base.
		baseSHA = parentJob.CISHA
	}
	ttl := time.Duration(settings.CodeownersCacheTTLSeconds) * time.Second
	loaded, err := reporting.DefaultCodeownersCache.Load(ctx, reader, event.Provider, event.Repo, baseSHA, ttl)
	if err != nil {
		return nil, err
	}
	if loaded.Path == "" {
		return nil, nil
	}
	return reporting.CodeownersMap(ctx, loaded.File, reporting.SubjectMapper{Provider: event.Provider, Repo: event.Repo, Store: tp.store}), nil
}

// matchedFilesOf returns the files that matched the paths filter of a
// workflow, from its decision. complete is false when the decision capped
// the list and the full list cannot be computed again.
func matchedFilesOf(decision *evalWorkflowDecision, tf *triggersFile) (files []string, filtered bool, complete bool) {
	if decision == nil {
		return nil, false, true
	}
	for _, reason := range decision.Reasons {
		if reason.Kind != "paths" || reason.Matched == nil || !*reason.Matched {
			continue
		}
		if reason.MatchedCount == nil || *reason.MatchedCount <= len(reason.MatchedFiles) {
			return append([]string{}, reason.MatchedFiles...), true, true
		}
		// The eval capped the list. Compute it again from the changed
		// files when the event list is complete.
		if tf.ChangedFiles == nil || len(tf.ChangedFiles) < tf.ChangedFilesTotal {
			return append([]string{}, reason.MatchedFiles...), true, false
		}
		out := []string{}
		for _, file := range tf.ChangedFiles {
			if pathSelected(reason.Include, reason.Exclude, file) {
				out = append(out, file)
			}
		}
		return out, true, true
	}
	return nil, false, true
}

func pathSelected(include, exclude []string, file string) bool {
	if len(include) > 0 {
		found := false
		for _, pattern := range include {
			if cipolicy.GlobMatch(pattern, file) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, pattern := range exclude {
		if cipolicy.GlobMatch(pattern, file) {
			return false
		}
	}
	return true
}

func capFiles(files []string) []string {
	if len(files) <= maxPathAcceptanceFiles {
		return append([]string{}, files...)
	}
	return append([]string{}, files[:maxPathAcceptanceFiles]...)
}

// applyPathAcceptance checks the path dependencies of the selected
// workflows. It never fails the eval: when the ownership map or the
// acceptances cannot be read, every workflow runs and the log says why.
func (tp *TriggerProcessor) applyPathAcceptance(ctx context.Context, parentJob *models.Job, tf *triggersFile,
	batches []triggerWorkflowSpec, notes map[string]models.DecisionReason) []triggerWorkflowSpec {
	if len(batches) == 0 || parentJob == nil || parentJob.ProjectID == nil || *parentJob.ProjectID == "" ||
		stringValue(parentJob.EventID) == "" || notes == nil || tf == nil {
		return batches
	}
	projectID := *parentJob.ProjectID
	settings, err := reporting.Effective(ctx, tp.store, parentJob.OrgID, projectID)
	if err != nil || settings.PathAcceptance == models.PathAcceptanceOff || settings.PathAcceptance == "" {
		return batches
	}
	log := logging.Log.WithField("event_id", stringValue(parentJob.EventID)).WithField("path_acceptance", settings.PathAcceptance)
	var event *models.CIEvent
	if events, ok := tp.store.(store.CIEventStore); ok {
		event, _ = events.GetCIEvent(ctx, stringValue(parentJob.EventID))
	}
	ownership, err := tp.pathOwnershipFor(ctx, parentJob, event, settings)
	if err != nil {
		log.WithError(err).Warn("Could not read the path ownership map; path acceptance does not apply to this event")
		return batches
	}
	if ownership == nil {
		return batches
	}
	var index *reporting.AcceptanceIndex
	if reports, ok := tp.store.(store.ReportingStore); ok {
		rows, err := reports.ListPathAcceptances(ctx, projectID, models.PathAcceptanceAccepted)
		if err != nil {
			log.WithError(err).Warn("Could not read the path acceptances; path acceptance does not apply to this event")
			return batches
		}
		index = reporting.NewAcceptanceIndex(rows)
	}
	decisions := map[string]*evalWorkflowDecision{}
	for i := range tf.Decisions {
		decisions[tf.Decisions[i].ID] = &tf.Decisions[i]
	}
	kept := batches[:0:0]
	for _, batch := range batches {
		id := workflowSecurityIDOf(batch)
		files, filtered, complete := matchedFilesOf(decisions[id], tf)
		if !filtered || len(files) == 0 {
			kept = append(kept, batch)
			continue
		}
		var ownerSubjects []string
		if batch.Ownership != nil {
			ownerSubjects = owners.Subjects(*batch.Ownership)
		}
		decision := reporting.CheckPaths(files, id, ownerSubjects, ownership, index)
		if len(decision.Unaccepted) == 0 {
			kept = append(kept, batch)
			continue
		}
		reason := reporting.ForeignReason(decision.Foreign)
		note := models.DecisionReason{Kind: DecisionKindPathAcceptance, Value: settings.PathAcceptance,
			MatchedFiles: capFiles(decision.Unaccepted), Detail: reason}
		count := len(decision.Unaccepted)
		note.MatchedCount = &count
		action := "path_acceptance.flag"
		switch {
		case settings.PathAcceptance == models.PathAcceptanceWarn:
			note.Detail = "warning: " + reason
			kept = append(kept, batch)
		case !complete:
			// The full match is not known: other matched files can be in
			// accepted areas. The workflow runs (fail open) with a flag.
			note.Detail = "warning (the matched file list is incomplete): " + reason
			kept = append(kept, batch)
		case len(decision.Kept) == 0:
			action = "path_acceptance.skip"
			matched := false
			note.Matched = &matched
		default:
			action = "path_acceptance.drop"
			note.Detail = fmt.Sprintf("%d matched files are not counted: %s", count, reason)
			kept = append(kept, batch)
		}
		notes[pathAcceptanceNotePrefix+id] = note
		log.WithField("workflow_security_id", id).WithField("unaccepted", count).Info("Path acceptance: " + note.Detail)
		audit.Record(ctx, tp.store, parentJob.OrgID, action, "job", parentJob.JobID, models.JSONB{
			"project_id": projectID, "workflow_security_id": id, "mode": settings.PathAcceptance,
			"unaccepted_files": capFiles(decision.Unaccepted), "unaccepted_count": count, "kept_count": len(decision.Kept),
			"areas": decision.Foreign, "reason": reason,
		})
	}
	return kept
}
