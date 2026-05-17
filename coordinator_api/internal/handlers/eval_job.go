package handlers

import (
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// BuildEvalJob constructs an eval job from a project and webhook event.
// The eval job runs runnerlib eval to determine which CI jobs should be triggered.
//
// Code source vs CI source: the code under test (SourceURL/SourceRef) may live
// on a fork for cross-repository PRs; the CI definitions (CISourceURL/
// CISourceRef) must always come from a trusted location — the project's
// configured DefaultCISource, or the upstream repository's base ref. Fork
// content is NEVER used as a CI source: a malicious PR could otherwise ship
// .reactorcide/jobs/*.yaml that would be executed by the eval job.
func BuildEvalJob(project *models.Project, event *vcs.WebhookEvent) *models.Job {
	sourceType := models.SourceTypeGit

	// Code source URL: for fork PRs, points at the fork; otherwise upstream.
	// Existing job YAMLs that do `git clone $REACTORCIDE_SOURCE_URL` keep
	// working unchanged for fork PRs because SOURCE_URL now resolves to the
	// repo where the branch actually lives.
	upstreamURL := event.Repository.CloneURL
	sourceURL := upstreamURL
	isForkPR := false
	if event.PullRequest != nil && event.PullRequest.HeadRepository != nil {
		sourceURL = event.PullRequest.HeadRepository.CloneURL
		isForkPR = true
	}

	// Determine source ref, branch, and job name based on event type
	var sourceRef, branch, jobName string
	envVars := models.JSONB{
		"REACTORCIDE_CI":         "true",
		"REACTORCIDE_PROVIDER":   string(event.Provider),
		"REACTORCIDE_EVENT_TYPE": string(event.GenericEvent),
		"REACTORCIDE_REPO":       event.Repository.FullName,
		"REACTORCIDE_SOURCE_URL": sourceURL,
	}

	if event.PullRequest != nil {
		pr := event.PullRequest
		sourceRef = pr.HeadSHA
		branch = pr.BaseRef
		jobName = fmt.Sprintf("eval: PR #%d %s on %s", pr.Number, actionLabel(event.GenericEvent), event.Repository.FullName)

		envVars["REACTORCIDE_SHA"] = pr.HeadSHA
		envVars["REACTORCIDE_BRANCH"] = pr.BaseRef
		envVars["REACTORCIDE_PR_NUMBER"] = fmt.Sprintf("%d", pr.Number)
		envVars["REACTORCIDE_PR_REF"] = pr.HeadRef
		envVars["REACTORCIDE_PR_BASE_REF"] = pr.BaseRef
		envVars["REACTORCIDE_DIFF_BASE"] = pr.BaseSHA

		// Explicit head/base URL pair lets job authors set up both remotes
		// (e.g. for `git log base..head`) without overloading SOURCE_URL.
		envVars["REACTORCIDE_HEAD_URL"] = sourceURL
		envVars["REACTORCIDE_HEAD_REF"] = pr.HeadRef
		envVars["REACTORCIDE_BASE_URL"] = upstreamURL
		envVars["REACTORCIDE_BASE_REF"] = pr.BaseRef
		if isForkPR {
			envVars["REACTORCIDE_IS_FORK_PR"] = "true"
		}
	} else if event.Push != nil {
		push := event.Push
		sourceRef = push.After
		branch = extractBranchOrTag(push.Ref)
		jobName = fmt.Sprintf("eval: push to %s (%.7s) on %s", branch, push.After, event.Repository.FullName)

		envVars["REACTORCIDE_SHA"] = push.After
		envVars["REACTORCIDE_BRANCH"] = branch
	}

	// CI source: trusted repo with job definitions
	var ciSourceType *models.SourceType
	var ciSourceURL *string
	var ciSourceRef *string
	if project.DefaultCISourceURL != "" {
		st := project.DefaultCISourceType
		if st == "" {
			st = models.SourceTypeGit
		}
		ciSourceType = &st
		ciSourceURL = &project.DefaultCISourceURL
		if project.DefaultCISourceRef != "" {
			ciSourceRef = &project.DefaultCISourceRef
		}
		envVars["REACTORCIDE_CI_SOURCE_URL"] = project.DefaultCISourceURL
		envVars["REACTORCIDE_CI_SOURCE_REF"] = project.DefaultCISourceRef
	} else {
		// Same-repo mode: use the upstream repo for job definitions. For PR
		// events, anchor at the base branch's SHA (trusted state of the
		// target) rather than the PR head — otherwise a fork PR could ship
		// malicious .reactorcide/jobs/*.yaml that the eval job would execute.
		// For push events, the push has already passed the project's commit
		// gates, so the pushed SHA is trusted.
		st := models.SourceTypeGit
		ciSourceType = &st
		ciSourceURL = &upstreamURL
		var ciRef string
		if event.PullRequest != nil {
			ciRef = event.PullRequest.BaseSHA
		} else {
			ciRef = sourceRef
		}
		ciSourceRef = &ciRef
		envVars["REACTORCIDE_CI_SOURCE_URL"] = upstreamURL
		envVars["REACTORCIDE_CI_SOURCE_REF"] = ciRef
	}

	// Determine job command
	jobCommand := project.DefaultJobCommand
	if jobCommand == "" {
		jobCommand = "runnerlib eval --event-type $REACTORCIDE_EVENT_TYPE --branch $REACTORCIDE_BRANCH"
	}

	// Determine priority: PRs get higher priority
	priority := 5
	if event.PullRequest != nil {
		priority = 10
	}

	job := &models.Job{
		UserID:       config.DefaultUserID,
		ProjectID:    &project.ProjectID,
		Name:         jobName,
		Description:  fmt.Sprintf("Eval job for %s event on %s", event.GenericEvent, event.Repository.FullName),
		SourceURL:    &sourceURL,
		SourceRef:    &sourceRef,
		SourceType:   &sourceType,
		CISourceType: ciSourceType,
		CISourceURL:  ciSourceURL,
		CISourceRef:  ciSourceRef,
		JobCommand:   jobCommand,
		RunnerImage:  project.DefaultRunnerImage,
		JobEnvVars:   envVars,
		Priority:     priority,
		QueueName:    project.DefaultQueueName,
	}

	if project.DefaultTimeoutSeconds > 0 {
		job.TimeoutSeconds = project.DefaultTimeoutSeconds
	}

	return job
}

// actionLabel returns a human-readable label for the generic event type.
func actionLabel(eventType vcs.EventType) string {
	switch eventType {
	case vcs.EventPullRequestOpened:
		return "opened"
	case vcs.EventPullRequestUpdated:
		return "updated"
	case vcs.EventPullRequestMerged:
		return "merged"
	case vcs.EventPullRequestClosed:
		return "closed"
	default:
		return string(eventType)
	}
}

// extractBranchOrTag extracts the branch or tag name from a git ref.
func extractBranchOrTag(ref string) string {
	if strings.HasPrefix(ref, "refs/heads/") {
		return strings.TrimPrefix(ref, "refs/heads/")
	}
	if strings.HasPrefix(ref, "refs/tags/") {
		return strings.TrimPrefix(ref, "refs/tags/")
	}
	return ref
}
