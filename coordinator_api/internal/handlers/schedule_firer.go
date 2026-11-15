package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/schedule"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/sirupsen/logrus"
)

// ScheduleFirer starts the work of a schedule fire: it resolves the branch
// head, records a `schedule` CI event, and submits an eval job that runs only
// the schedule's workflow_ref on that commit. It reuses the webhook path's VCS
// credentials, queue resolution and Corndogs submission.
type ScheduleFirer struct {
	h *WebhookHandler
}

var _ schedule.Firer = ScheduleFirer{}

// ScheduleFirer returns the schedule firer backed by this handler.
func (h *WebhookHandler) ScheduleFirer() ScheduleFirer {
	return ScheduleFirer{h: h}
}

// Env vars the eval reads for a scheduled run. runnerlib evaluates only the
// workflow named by REACTORCIDE_WORKFLOW_REF and records a `schedule` reason
// built from the others.
const (
	envWorkflowRef       = "REACTORCIDE_WORKFLOW_REF"
	envScheduleID        = "REACTORCIDE_SCHEDULE_ID"
	envScheduleName      = "REACTORCIDE_SCHEDULE_NAME"
	envScheduleCron      = "REACTORCIDE_SCHEDULE_CRON"
	envScheduleFireTime  = "REACTORCIDE_SCHEDULE_FIRE_TIME"
	scheduleStatusPrefix = "schedule:"
)

// ScheduleIdempotencyKey is the event key of one fire. A fire is
// exactly-once already (the unique fire row); the key also makes a repeated
// Fire call for the same fire return the first event.
func ScheduleIdempotencyKey(scheduleID string, fireTime time.Time) string {
	return fmt.Sprintf("%s%s:%s", scheduleStatusPrefix, scheduleID, fireTime.UTC().Format(time.RFC3339))
}

// projectRepository derives the provider, the "owner/repo" name and the
// clone URL from a project's canonical repo_url (host/owner/repo).
func (h *WebhookHandler) projectRepository(project *models.Project) (vcs.Provider, string, string, error) {
	canonical := strings.TrimSuffix(strings.TrimSpace(project.RepoURL), "/")
	host, path, ok := strings.Cut(canonical, "/")
	if !ok || path == "" {
		return "", "", "", fmt.Errorf("project repo_url %q has no repository path", project.RepoURL)
	}
	var provider vcs.Provider
	switch {
	case host == "github.com" || strings.Contains(host, "github"):
		provider = vcs.GitHub
	case strings.Contains(host, "gitlab"):
		provider = vcs.GitLab
	default:
		// A self-hosted host with an unrevealing name: use the one provider
		// the project has credentials for, else the one client configured.
		if len(project.VCSCredentialSecrets) == 1 {
			for name := range project.VCSCredentialSecrets {
				provider = vcs.Provider(name)
			}
		} else if len(h.vcsClients) == 1 {
			for name := range h.vcsClients {
				provider = name
			}
		}
		if provider == "" {
			return "", "", "", fmt.Errorf("cannot tell the VCS provider of %s", project.RepoURL)
		}
	}
	return provider, path, "https://" + canonical + ".git", nil
}

// Fire implements schedule.Firer.
func (f ScheduleFirer) Fire(ctx context.Context, sched models.CISchedule, fireTime time.Time) (string, error) {
	h := f.h
	project, err := h.store.GetProjectByID(ctx, sched.ProjectID)
	if err != nil {
		return "", fmt.Errorf("loading project: %w", err)
	}
	if !project.Enabled {
		return "", fmt.Errorf("project %s is disabled", project.Name)
	}
	events, ok := h.store.(store.CIEventStore)
	if !ok {
		return "", errors.New("the store does not support CI events")
	}
	provider, repo, cloneURL, err := h.projectRepository(project)
	if err != nil {
		return "", err
	}
	client := h.getStatusClient(ctx, project, provider, h.vcsClients[provider])
	resolver, ok := client.(vcs.BranchHeadResolver)
	if client == nil || !ok {
		return "", fmt.Errorf("no %s client can resolve branches for %s", provider, repo)
	}
	sha, err := resolver.GetBranchHead(ctx, repo, sched.Branch)
	if err != nil {
		return "", fmt.Errorf("resolving head of %s: %w", sched.Branch, err)
	}

	key := ScheduleIdempotencyKey(sched.ScheduleID, fireTime)
	scheduleID := sched.ScheduleID
	projectID := project.ProjectID
	recorded, outcome, err := events.RecordCIEvent(ctx, &models.CIEvent{
		OrgID:          project.OwnershipOrgID(),
		ProjectID:      &projectID,
		Source:         models.CIEventSourceSchedule,
		Provider:       string(provider),
		EventType:      string(vcs.EventSchedule),
		Repo:           repo,
		Ref:            "refs/heads/" + sched.Branch,
		Branch:         sched.Branch,
		CommitSHA:      sha,
		ScheduleID:     &scheduleID,
		Status:         models.CIEventStatusReceived,
		IdempotencyKey: &key,
	})
	if err != nil {
		return "", fmt.Errorf("recording schedule event: %w", err)
	}
	if outcome == store.CIEventRedelivery && recorded.EvalJobID != nil {
		return recorded.EventID, nil
	}
	if claimer, ok := h.store.(store.CIEventClaimStore); ok {
		claimed, err := claimer.ClaimCIEventForEval(ctx, recorded.EventID)
		if err != nil {
			return "", fmt.Errorf("claiming schedule event: %w", err)
		}
		if !claimed {
			return recorded.EventID, nil
		}
	}
	linked := false
	defer func() {
		if !linked {
			h.releaseCIEventClaim(context.Background(), recorded)
		}
	}()

	job := BuildScheduleEvalJob(project, sched, fireTime, provider, repo, cloneURL, sha)
	applyCIEventToEvalJob(job, recorded)
	// The policy snapshot gives the eval result the project's default
	// profile, as for any other event. A schedule has no actor, so there are
	// no actor subjects or approvals.
	h.attachCIPolicy(ctx, job, project)
	metadata := vcs.JobMetadata{
		VCSProvider: string(provider),
		Repo:        repo,
		Branch:      sched.Branch,
		CommitSHA:   sha,
		// A schedule fire always has a CI event, so its status is the event's
		// "Reactorcide Checks" (vcs.JobStatusUpdater routes event-scoped eval
		// jobs there). The context here only matters for a store without CI
		// events, and even then it must not be the legacy "reactorcide/eval".
		StatusContext: models.CIEventChecksContext,
		IsEval:        true,
	}
	if err := metadata.ApplyToJob(job); err != nil {
		return "", fmt.Errorf("applying VCS metadata: %w", err)
	}
	if err := h.resolveJobQueue(ctx, job); err != nil {
		return "", err
	}
	if err := h.store.CreateJob(ctx, job); err != nil {
		return "", fmt.Errorf("creating eval job: %w", err)
	}
	h.attachEvalJobToCIEvent(ctx, recorded, job)
	linked = true
	h.submitJobToCorndogs(job)
	// Pending "Reactorcide Checks" on the branch head, as a webhook eval does.
	h.postScheduleEvalPending(ctx, job, client, repo, sha)
	h.logger.WithFields(logrus.Fields{
		"event_id": recorded.EventID, "job_id": job.JobID, "schedule_id": sched.ScheduleID,
		"branch": sched.Branch, "sha": sha,
	}).Info("Schedule fired")
	return recorded.EventID, nil
}

// BuildScheduleEvalJob builds the eval job of a schedule fire. It starts from
// BuildEvalJob with a synthetic push to the branch head, then makes the
// branch head itself the trusted CI source: a schedule is not a change, so
// there is no head candidate to put through policy and no diff.
func BuildScheduleEvalJob(project *models.Project, sched models.CISchedule, fireTime time.Time,
	provider vcs.Provider, repo, cloneURL, sha string) *models.Job {
	event := &vcs.WebhookEvent{
		Provider:     provider,
		GenericEvent: vcs.EventSchedule,
		Repository:   vcs.RepositoryInfo{FullName: repo, CloneURL: cloneURL, DefaultBranch: sched.Branch},
		Push:         &vcs.PushInfo{Ref: "refs/heads/" + sched.Branch, After: sha},
	}
	job := BuildEvalJob(project, event)
	job.Name = fmt.Sprintf("Eval event schedule %s on %s (%.7s) on %s", sched.Name, sched.Branch, sha, repo)
	job.Description = fmt.Sprintf("Eval job for schedule %s on %s", sched.Name, repo)
	env := job.JobEnvVars
	delete(env, "REACTORCIDE_EVAL_DUAL_CI")
	delete(env, "REACTORCIDE_HEAD_URL")
	delete(env, "REACTORCIDE_HEAD_REPOSITORY")
	delete(env, "REACTORCIDE_DIFF_BASE")
	if project.DefaultCISourceURL == "" {
		ref := sha
		job.CISourceRef = &ref
		job.CISHA = sha
		env["REACTORCIDE_CI_SOURCE_REF"] = sha
	}
	env["REACTORCIDE_EVENT_TYPE"] = string(vcs.EventSchedule)
	env[envWorkflowRef] = sched.WorkflowRef
	env[envScheduleID] = sched.ScheduleID
	env[envScheduleName] = sched.Name
	env[envScheduleCron] = sched.Cron
	env[envScheduleFireTime] = fireTime.UTC().Format(time.RFC3339)
	return job
}

// postScheduleEvalPending posts the pending status of a scheduled eval. It is
// the event's "Reactorcide Checks" status. Unlike postEvalPendingStatus it has
// no legacy "reactorcide/eval" fallback: a schedule fire always has an event.
// Without an event-checks publisher it posts the same context directly.
func (h *WebhookHandler) postScheduleEvalPending(ctx context.Context, job *models.Job, client vcs.Client, repo, sha string) {
	if job.EventID != nil && *job.EventID != "" {
		if publisher, ok := h.statusUpdater.(eventChecksPublisher); ok && publisher.SupportsEventChecks() {
			if err := publisher.PublishEventChecks(ctx, *job.EventID); err != nil {
				h.logger.WithError(err).WithField("event_id", *job.EventID).Warn("Failed to update event checks status")
			}
			return
		}
	}
	if client == nil {
		return
	}
	if err := client.UpdateCommitStatus(ctx, repo, vcs.StatusUpdate{
		SHA:         sha,
		State:       vcs.StatusPending,
		TargetURL:   h.getJobURL(job.JobID),
		Description: "Evaluating event",
		Context:     models.CIEventChecksContext,
	}); err != nil {
		h.logger.WithError(err).Warn("Failed to post pending status of scheduled eval")
	}
}
