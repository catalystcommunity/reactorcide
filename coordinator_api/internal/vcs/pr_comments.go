package vcs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

const deprecatedJobFlowNotice = "> ⚠️ This repository is using the deprecated Reactorcide job flow. Convert it to workflow-based jobs soon; this path will be removed in the near future."

// prCommentMarkerRolling returns the hidden HTML marker embedded in the
// pre-merge rolling comment for a given commit. New commits get a new
// marker value so each commit naturally gets its own comment.
func prCommentMarkerRolling(commitSHA string) string {
	return fmt.Sprintf("<!-- reactorcide:pr-status:%s -->", commitSHA)
}

// prCommentMarkerPerJob returns the hidden HTML marker for a post-merge
// per-job result comment. Keyed on (commitSHA, key) rather than a specific
// job's JobID so that retrying a post-merge job (jobcontrol.RetryJob clones
// a brand-new JobID) updates the SAME comment in place instead of posting a
// duplicate: key is jobCommentKey(job) (the job's Name, or its JobID as a
// last resort for an unnamed job — see jobCommentKey), which a retry clone
// carries forward unchanged from the original job, and commitSHA scopes it
// so unrelated jobs sharing a name on different commits within the same PR
// still get distinct comments. Mirrors prCommentMarkerRolling's
// commit-scoped, name-keyed approach for the pre-merge rolling comment.
func prCommentMarkerPerJob(commitSHA, key string) string {
	return fmt.Sprintf("<!-- reactorcide:pr-result:%s:%s -->", commitSHA, key)
}

// jobCommentKey returns the identity a PR comment builder dedupes/keys a job
// on: the job's Name, falling back to its JobID when Name is empty so two
// unnamed jobs are never collapsed together. Shared by dedupeJobsByName (the
// pre-merge rolling comment) and prCommentMarkerPerJob (the post-merge
// per-job comment) so both comment flows agree on what makes two job rows
// "the same job, possibly retried" versus "two unrelated jobs".
func jobCommentKey(job *models.Job) string {
	if job.Name != "" {
		return job.Name
	}
	return job.JobID
}

// updatePRCommentForJob posts or updates the appropriate PR comment for a
// job status change. Pre-merge: one rolling comment per (PR, commit) shared
// by all jobs. Post-merge: one comment per job, updated through its lifecycle.
//
// Called from UpdateJobStatus whenever a job has PR metadata and is not an
// eval job. Errors are logged but not propagated — a failed comment update
// should never block a job's status transition.
func (u *JobStatusUpdater) updatePRCommentForJob(ctx context.Context, client Client, job *models.Job, metadata *JobMetadata) {
	if u.store == nil {
		u.logger.Debug("No store configured on JobStatusUpdater; skipping PR comment")
		return
	}

	merged, err := u.store.IsPRMerged(ctx, metadata.Repo, metadata.PRNumber)
	if err != nil {
		u.logger.WithError(err).Warn("Failed to check PR merged state; defaulting to pre-merge flow")
	}

	if merged {
		u.postPerJobComment(ctx, client, job, metadata)
		return
	}
	u.postRollingComment(ctx, client, job, metadata)
}

// postRollingComment regenerates the rolling comment for (repo, PR, commit).
// Wrapped in ForPRCommit so concurrent updates for the same commit serialize
// cleanly via a Postgres advisory lock.
func (u *JobStatusUpdater) postRollingComment(ctx context.Context, client Client, job *models.Job, metadata *JobMetadata) {
	err := u.store.ForPRCommit(ctx, metadata.Repo, metadata.PRNumber, metadata.CommitSHA, func(ctx context.Context) error {
		jobs, err := u.store.ListJobsForPRCommit(ctx, metadata.Repo, metadata.PRNumber, metadata.CommitSHA)
		if err != nil {
			return fmt.Errorf("listing jobs for PR commit: %w", err)
		}

		marker := prCommentMarkerRolling(metadata.CommitSHA)
		body := u.renderRollingCommentBody(jobs, metadata.CommitSHA, marker)

		return client.UpsertPRCommentByMarker(ctx, metadata.Repo, metadata.PRNumber, marker, body)
	})
	if err != nil {
		u.logger.WithError(err).WithFields(map[string]interface{}{
			"repo":      metadata.Repo,
			"pr_number": metadata.PRNumber,
			"sha":       metadata.CommitSHA,
		}).Warn("Failed to update rolling PR comment")
	}
}

// postPerJobComment updates the per-job comment for a merged PR. Keyed by
// (commit, job name) rather than job.JobID (see prCommentMarkerPerJob) so a
// retried job's completion updates the existing comment in place instead of
// posting a new one alongside it — last run wins.
func (u *JobStatusUpdater) postPerJobComment(ctx context.Context, client Client, job *models.Job, metadata *JobMetadata) {
	marker := prCommentMarkerPerJob(metadata.CommitSHA, jobCommentKey(job))
	body := u.renderPerJobCommentBody(job, marker)

	if err := client.UpsertPRCommentByMarker(ctx, metadata.Repo, metadata.PRNumber, marker, body); err != nil {
		u.logger.WithError(err).WithFields(map[string]interface{}{
			"repo":      metadata.Repo,
			"pr_number": metadata.PRNumber,
			"job_id":    job.JobID,
		}).Warn("Failed to update per-job PR comment")
	}
}

// renderRollingCommentBody produces the markdown table summarizing every
// job registered against (repo, PR, commit). Rows are rendered in job
// creation order so the eval job shows first. When the same job name has
// been run multiple times (e.g. a webhook retry), only the most recent run
// is shown — older runs would otherwise mislead readers into thinking a
// long-since-fixed failure is current. Jobs are assumed to arrive in
// ascending CreatedAt order (that's the contract of ListJobsForPRCommit).
func (u *JobStatusUpdater) renderRollingCommentBody(jobs []models.Job, commitSHA, marker string) string {
	deduped := dedupeJobsByName(jobs)

	var b strings.Builder
	shortSHA := commitSHA
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}

	fmt.Fprintf(&b, "## Reactorcide checks — `%s`\n\n", shortSHA)
	fmt.Fprintf(&b, "%s\n\n", deprecatedJobFlowNotice)
	b.WriteString("| Job | Status | Duration | Link |\n")
	b.WriteString("|-----|--------|----------|------|\n")

	for i := range deduped {
		job := &deduped[i]
		statusEmoji, statusText := renderStatus(job)
		duration := renderDuration(job)
		link := u.getJobURL(job.JobID)

		name := job.Name
		if name == "" {
			name = job.JobID
		}

		if link != "" {
			fmt.Fprintf(&b, "| %s | %s %s | %s | [details](%s) |\n", escapeTableCell(name), statusEmoji, statusText, duration, link)
		} else {
			fmt.Fprintf(&b, "| %s | %s %s | %s | — |\n", escapeTableCell(name), statusEmoji, statusText, duration)
		}
	}

	fmt.Fprintf(&b, "\n<sub>Updated %s · %s</sub>\n", time.Now().UTC().Format(time.RFC3339), marker)
	return b.String()
}

// dedupeJobsByName keeps only the most-recent job per jobCommentKey (Name,
// falling back to JobID) from an ASC-by-CreatedAt input. The returned slice
// preserves the original relative order of kept jobs (so an eval registered
// first still renders first; a retried child slots into its first
// appearance position with the newer job's content).
func dedupeJobsByName(jobs []models.Job) []models.Job {
	if len(jobs) <= 1 {
		return jobs
	}
	latestIdx := make(map[string]int, len(jobs))
	for i := range jobs {
		latestIdx[jobCommentKey(&jobs[i])] = i
	}
	keep := make(map[int]bool, len(latestIdx))
	for _, idx := range latestIdx {
		keep[idx] = true
	}
	out := make([]models.Job, 0, len(latestIdx))
	for i := range jobs {
		if keep[i] {
			out = append(out, jobs[i])
		}
	}
	return out
}

// renderPerJobCommentBody produces the body for a post-merge single-job
// comment. Starts as "submitted" and progresses through the lifecycle.
func (u *JobStatusUpdater) renderPerJobCommentBody(job *models.Job, marker string) string {
	statusEmoji, statusText := renderStatus(job)
	name := job.Name
	if name == "" {
		name = job.JobID
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s **%s** — %s", statusEmoji, name, statusText)
	fmt.Fprintf(&b, "\n\n%s", deprecatedJobFlowNotice)

	link := u.getJobURL(job.JobID)
	if link != "" {
		fmt.Fprintf(&b, " — [details](%s)", link)
	}

	if job.ExitCode != nil {
		fmt.Fprintf(&b, "\n\n**Exit code:** %d", *job.ExitCode)
	}
	if job.StartedAt != nil && job.CompletedAt != nil {
		duration := job.CompletedAt.Sub(*job.StartedAt)
		fmt.Fprintf(&b, "\n**Duration:** %.3fs", duration.Seconds())
	}
	if job.LastError != "" && job.Status == "failed" {
		fmt.Fprintf(&b, "\n\n### Error\n```\n%s\n```", job.LastError)
	}

	fmt.Fprintf(&b, "\n\n<sub>%s</sub>\n", marker)
	return b.String()
}

// renderStatus maps a job state to an emoji + human label.
func renderStatus(job *models.Job) (emoji string, text string) {
	switch job.Status {
	case "submitted":
		return "⏳", "submitted"
	case "queued":
		return "⏳", "queued"
	case "running":
		return "🟡", "running"
	case "completed":
		if job.ExitCode != nil && *job.ExitCode == 0 {
			return "✅", "succeeded"
		}
		return "❌", "failed"
	case "failed":
		return "❌", "failed"
	case "cancelled":
		return "⚠️", "cancelled"
	case "timeout":
		return "⏱️", "timed out"
	default:
		return "❓", job.Status
	}
}

// renderDuration returns a 3-decimal-place seconds string for a completed
// job, or an em-dash placeholder for jobs that haven't completed.
func renderDuration(job *models.Job) string {
	if job.StartedAt == nil || job.CompletedAt == nil {
		return "—"
	}
	d := job.CompletedAt.Sub(*job.StartedAt)
	return fmt.Sprintf("%.3fs", d.Seconds())
}

// escapeTableCell escapes pipes in a cell value so they don't break the
// markdown table layout.
func escapeTableCell(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}

// Compile-time check that Store has everything we need. This is a
// documentation aid; remove if tests catch it.
var _ interface {
	IsPRMerged(ctx context.Context, repo string, prNumber int) (bool, error)
	ListJobsForPRCommit(ctx context.Context, repo string, prNumber int, commitSHA string) ([]models.Job, error)
	ForPRCommit(ctx context.Context, repo string, prNumber int, commitSHA string, fn func(ctx context.Context) error) error
} = (store.Store)(nil)
