package vcs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// A CI event posts ONE commit status, "Reactorcide Checks". It covers the
// eval job, every root workflow the event created, workflows the coordinator
// did not admit, and CI files the policy refused. Branch protection then needs
// one required name, whatever workflows a repository has.

// eventChecksPublishTimeout bounds the provider call made while the event
// lock is held.
const eventChecksPublishTimeout = 15 * time.Second

// maxStatusDescription is the GitHub limit for a commit status description.
const maxStatusDescription = 140

// EventChecksWorkflow is one root workflow of an event, as the aggregate sees it.
type EventChecksWorkflow struct {
	ID     string
	Name   string
	Status string
}

// EventChecksInput is everything the aggregate reads, loaded from the database.
type EventChecksInput struct {
	EventStatus string
	// EvalJobStatus is empty when the event has no eval job yet.
	EvalJobStatus string
	Workflows     []EventChecksWorkflow
	// NotAdmitted names workflows the coordinator refused that have no
	// workflow row of their own (a refused workflow with a row is counted
	// through its failed status instead).
	NotAdmitted      []string
	PolicyViolations int
}

// EventChecksResult is the computed status. WorkflowID is set when the event
// has exactly one workflow, so the status links to that workflow page.
type EventChecksResult struct {
	State       StatusState
	Description string
	WorkflowID  string
}

// ComputeEventChecks applies the aggregate rules:
//
//   - failure as soon as a failure is certain: the eval failed, a workflow
//     failed or was cancelled, a workflow was not admitted, or the policy
//     refused a CI change. A failure cannot become a success without a new
//     run, so reporting it at once tells the author sooner. The description
//     still says how many workflows are running.
//   - pending while the eval or any workflow runs.
//   - success when every workflow passed or was skipped, or when a
//     successful eval matched no workflow.
func ComputeEventChecks(in EventChecksInput) EventChecksResult {
	result := EventChecksResult{}
	if len(in.Workflows) == 1 && len(in.NotAdmitted) == 0 {
		result.WorkflowID = in.Workflows[0].ID
	}

	switch in.EvalJobStatus {
	case "failed":
		result.State, result.Description = StatusFailure, "Eval failed"
		return result
	case "timeout":
		result.State, result.Description = StatusFailure, "Eval timed out"
		return result
	case "cancelled":
		result.State, result.Description = StatusFailure, "Eval cancelled"
		return result
	}
	if in.EventStatus == models.CIEventStatusFailed {
		result.State, result.Description = StatusFailure, "Eval failed"
		return result
	}
	if in.EventStatus == models.CIEventStatusAwaitingApproval {
		result.State, result.Description = StatusPending, "Waiting for a run approver: /reactorcide approve-ci"
		return result
	}

	total := len(in.Workflows) + len(in.NotAdmitted)
	var passed, skipped, running int
	var failures []string
	for _, wf := range in.Workflows {
		switch wf.Status {
		case "success":
			passed++
		case "skipped":
			skipped++
		case "failed":
			failures = append(failures, workflowLabel(wf.Name))
		case "cancelled":
			failures = append(failures, workflowLabel(wf.Name)+" (cancelled)")
		default:
			// evaluating, running, cancelling, or a status this code does
			// not know yet: not finished.
			running++
		}
	}
	for _, name := range in.NotAdmitted {
		failures = append(failures, workflowLabel(name)+" (not admitted)")
	}

	if len(failures) > 0 || in.PolicyViolations > 0 {
		var parts []string
		if len(failures) > 0 {
			parts = append(parts, fmt.Sprintf("%d of %d %s failed: %s",
				len(failures), total, plural(total, "workflow", "workflows"), strings.Join(failures, ", ")))
		}
		if in.PolicyViolations > 0 {
			parts = append(parts, fmt.Sprintf("policy refused %d CI %s",
				in.PolicyViolations, plural(in.PolicyViolations, "change", "changes")))
		}
		description := capitalize(strings.Join(parts, "; "))
		if running > 0 {
			description = fmt.Sprintf("%s (%d running)", description, running)
		}
		result.State, result.Description = StatusFailure, truncateWorkflowDescription(description, maxStatusDescription)
		return result
	}

	evalDone := in.EvalJobStatus == "completed" ||
		(in.EvalJobStatus == "" && in.EventStatus == models.CIEventStatusEvaluated)
	if !evalDone {
		result.State, result.Description = StatusPending, "Evaluating event"
		return result
	}
	if running > 0 {
		result.State = StatusPending
		result.Description = fmt.Sprintf("%d of %d %s done", total-running, total, plural(total, "workflow", "workflows"))
		return result
	}

	result.State = StatusSuccess
	switch {
	case total == 0:
		result.Description = "No workflows to run"
	case skipped == 0:
		result.Description = fmt.Sprintf("%d %s passed", passed, plural(passed, "workflow", "workflows"))
	case passed == 0:
		result.Description = fmt.Sprintf("%d %s skipped", skipped, plural(skipped, "workflow", "workflows"))
	default:
		result.Description = fmt.Sprintf("%d %s passed, %d skipped", passed, plural(passed, "workflow", "workflows"), skipped)
	}
	return result
}

func workflowLabel(name string) string {
	if strings.TrimSpace(name) == "" {
		return "unnamed workflow"
	}
	return strings.TrimSpace(name)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func capitalize(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

// eventChecksStores returns the stores the aggregate needs, or false when the
// configured store predates CI events.
func (u *JobStatusUpdater) eventChecksStores() (store.CIEventChecksStore, store.CIEventStore, bool) {
	if u.store == nil {
		return nil, nil, false
	}
	checks, ok := u.store.(store.CIEventChecksStore)
	if !ok {
		return nil, nil, false
	}
	events, ok := u.store.(store.CIEventStore)
	if !ok {
		return nil, nil, false
	}
	return checks, events, true
}

// SupportsEventChecks reports whether event-scoped statuses can be published.
// Callers keep the legacy per-job and per-workflow statuses when it is false.
func (u *JobStatusUpdater) SupportsEventChecks() bool {
	_, _, ok := u.eventChecksStores()
	return ok
}

// PublishEventChecks recomputes the event's "Reactorcide Checks" status from
// the database and posts it when it differs from the last published state.
// It runs under an advisory lock on the event, and the provider call happens
// inside that lock, so two replicas cannot post an older state after a newer
// one. Call it after anything that can change the aggregate: eval start and
// finish, a root workflow status change, a policy refusal, a cancel.
func (u *JobStatusUpdater) PublishEventChecks(ctx context.Context, eventID string) error {
	checksStore, events, ok := u.eventChecksStores()
	if !ok || eventID == "" {
		return nil
	}
	// One commit has one "Reactorcide Checks" status. When a newer event of
	// the same commit exists (a re-evaluation or a rerun), it owns the status.
	// It can carry this event's workflows, so it is recomputed instead.
	if newest, ok := u.store.(store.CIEventSupersedeStore); ok {
		newestID, err := newest.NewestCIEventForCommit(ctx, eventID)
		if err != nil {
			return err
		}
		if newestID != "" && newestID != eventID {
			eventID = newestID
		}
	}
	return checksStore.WithCIEventChecksLock(ctx, eventID, func(ctx context.Context) error {
		event, err := events.GetCIEvent(ctx, eventID)
		if err != nil {
			return fmt.Errorf("loading CI event: %w", err)
		}
		if event.Status == models.CIEventStatusDuplicate || event.Provider == "" || event.Repo == "" || event.CommitSHA == "" {
			return nil
		}
		input := EventChecksInput{EventStatus: event.Status, PolicyViolations: event.PolicyViolationCount}
		if event.EvalJobID != nil && *event.EvalJobID != "" {
			job, jobErr := u.store.GetJobByID(ctx, *event.EvalJobID)
			if jobErr != nil {
				return fmt.Errorf("loading eval job: %w", jobErr)
			}
			input.EvalJobStatus = job.Status
		}
		workflows, err := checksStore.ListRootWorkflowInstancesForEvent(ctx, eventID)
		if err != nil {
			return err
		}
		known := make(map[string]bool, len(workflows))
		for _, wf := range workflows {
			known[wf.WorkflowID] = true
			input.Workflows = append(input.Workflows, EventChecksWorkflow{ID: wf.WorkflowID, Name: wf.Name, Status: wf.Status})
		}
		decisions, err := events.ListCIEventWorkflowDecisions(ctx, eventID)
		if err != nil {
			return err
		}
		for _, decision := range decisions {
			if decision.Decision != models.WorkflowDecisionNotAdmitted {
				continue
			}
			if decision.WorkflowID != nil && known[*decision.WorkflowID] {
				continue
			}
			name := decision.WorkflowName
			if name == "" {
				name = decision.WorkflowSecurityID
			}
			input.NotAdmitted = append(input.NotAdmitted, name)
		}

		result := ComputeEventChecks(input)
		target := u.getEventURL(eventID)
		if result.WorkflowID != "" {
			target = u.getWorkflowURL(result.WorkflowID)
		}
		next := models.CIEventChecks{State: string(result.State), Description: result.Description, TargetURL: target}
		if event.ChecksState == next.State && event.ChecksDescription == next.Description && event.ChecksTargetURL == next.TargetURL {
			return nil
		}

		provider := Provider(event.Provider)
		client := u.getProjectClient(ctx, event.ProjectID, provider)
		if client == nil {
			client = u.vcsClients[provider]
		}
		if client == nil {
			u.logger.WithField("provider", provider).Debug("No VCS client available for event checks")
			return nil
		}
		publishCtx, cancel := context.WithTimeout(ctx, eventChecksPublishTimeout)
		defer cancel()
		if err := client.UpdateCommitStatus(publishCtx, event.Repo, StatusUpdate{
			SHA:         event.CommitSHA,
			State:       result.State,
			TargetURL:   target,
			Description: result.Description,
			Context:     models.CIEventChecksContext,
		}); err != nil {
			// Nothing is saved, so the next recompute posts again.
			return fmt.Errorf("updating event checks status: %w", err)
		}
		return checksStore.SaveCIEventChecks(ctx, eventID, next)
	})
}

func (u *JobStatusUpdater) getEventURL(eventID string) string {
	if u.baseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/app/events/%s", u.baseURL, eventID)
}

// eventScoped reports whether a job's statuses belong to its CI event's
// aggregate instead of a context of their own.
func (u *JobStatusUpdater) eventScoped(eventID *string) bool {
	return eventID != nil && *eventID != "" && u.SupportsEventChecks()
}
