package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/codeowners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/reporting"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/sirupsen/logrus"
)

// Report modes and the code owners import in the webhook handler
// (plan items A3 and A2, docs/reporting-and-ownership.md).

// vcsStatusesAllowed reports whether the report mode of the project allows
// commit statuses (silent posts none).
func (h *WebhookHandler) vcsStatusesAllowed(ctx context.Context, project *models.Project) bool {
	if project == nil {
		return true
	}
	return reporting.AllowsStatuses(reporting.Mode(ctx, h.store, project.OwnershipOrgID(), project.ProjectID))
}

// vcsStatusesAllowedForJob is vcsStatusesAllowed for the project of a job.
func (h *WebhookHandler) vcsStatusesAllowedForJob(ctx context.Context, job *models.Job) bool {
	if job == nil || job.ProjectID == nil || *job.ProjectID == "" {
		return true
	}
	// The report mode belongs to the owning organization of the project, not
	// to the organization recorded on the job.
	orgID := job.OrgID
	if project, err := h.store.GetProjectByID(ctx, *job.ProjectID); err == nil && project != nil {
		if owner := project.OwnershipOrgID(); owner != "" {
			orgID = owner
		}
	}
	return reporting.AllowsStatuses(reporting.Mode(ctx, h.store, orgID, *job.ProjectID))
}

// codeownersImportOn reports whether the project imports its code owners
// file into required reviews. On a settings read error it reports false:
// then no derived review is posted, and a branch rule that requires
// "Reactorcide Reviews" keeps the last posted state.
func (h *WebhookHandler) codeownersImportOn(ctx context.Context, project *models.Project) bool {
	settings, err := reporting.Effective(ctx, h.store, project.OwnershipOrgID(), project.ProjectID)
	if err != nil {
		h.logger.WithError(err).WithField("project", project.Name).Warn("Could not read the code owners import setting")
		return false
	}
	return settings.CodeownersImport == models.CodeownersImportOn
}

// codeownersReviews reads the code owners file of the trusted base SHA and
// returns one derived review for each owner set of the changed files. When
// the changed files are not known, every owner set of the file applies
// (fail closed). When the file cannot be read, one review that nobody can
// accept applies (fail closed).
func (h *WebhookHandler) codeownersReviews(ctx context.Context, project *models.Project, target *prTarget, changed []string, changedKnown bool) []models.EffectiveRequiredReview {
	fields := logrus.Fields{"project": project.Name, "pr_number": target.PR.Number}
	settings, err := reporting.Effective(ctx, h.store, project.OwnershipOrgID(), project.ProjectID)
	if err != nil {
		return []models.EffectiveRequiredReview{reporting.UnreadableCodeownersReview("the settings could not be read")}
	}
	reader, ok := target.Client.(reporting.FileReader)
	if !ok {
		h.logger.WithFields(fields).Warn("The VCS client cannot read the code owners file")
		return []models.EffectiveRequiredReview{reporting.UnreadableCodeownersReview("the VCS client cannot read repository files")}
	}
	baseSHA := target.PR.BaseSHA
	ttl := time.Duration(settings.CodeownersCacheTTLSeconds) * time.Second
	loaded, err := reporting.DefaultCodeownersCache.Load(ctx, reader, string(target.Provider), target.Repo, baseSHA, ttl)
	if err != nil {
		h.logger.WithError(err).WithFields(fields).Warn("Could not read the code owners file; the code owners review cannot pass")
		return []models.EffectiveRequiredReview{reporting.UnreadableCodeownersReview(err.Error())}
	}
	if loaded.Path == "" {
		return nil
	}
	for _, warning := range loaded.File.Warnings {
		h.logger.WithFields(fields).WithField("file", loaded.Path).Warn("Code owners file: " + warning.String())
	}
	mapper := reporting.SubjectMapper{Provider: string(target.Provider), Repo: target.Repo, Store: h.store,
		Warn: func(owner codeowners.Owner, reason string) {
			h.logger.WithFields(fields).WithField("owner", owner.String()).Warn("Code owners file: " + reason)
		}}
	ownership := reporting.CodeownersMap(ctx, loaded.File, mapper)
	var groups []reporting.OwnerGroup
	if changedKnown {
		groups = reporting.GroupFilesByOwners(ownership, changed)
	} else {
		groups = reporting.AllOwnerGroups(ownership)
	}
	return reporting.DerivedReviews(groups)
}

// reviewsReportKey is the entry of the required reviews in the shared pull
// request comment.
const reviewsReportKey = "reactorcide-reviews"

// publishReviewsReport writes one line for each required review into the
// shared pull request comment. The reconciler writes the comment; the report
// mode decides if it reaches the VCS.
func (h *WebhookHandler) publishReviewsReport(ctx context.Context, project *models.Project, target *prTarget, reviews *approvals.HeadReviews) {
	reports, ok := h.store.(interface {
		UpsertVCSReportTarget(context.Context, *models.VCSReportTarget) error
		UpsertVCSReportEntry(context.Context, *models.VCSReportEntry) error
	})
	if !ok || reviews == nil || target == nil || target.PR == nil {
		return
	}
	projectID := project.ProjectID
	reportTarget := &models.VCSReportTarget{OrgID: project.OwnershipOrgID(), ProjectID: &projectID, Provider: string(target.Provider),
		Repository: target.Repo, TargetType: "pull_request", ExternalTargetID: fmt.Sprintf("%d", target.PR.Number),
		RootMarker: "<!-- reactorcide:report:v1 -->", CurrentGeneration: 1}
	if err := reports.UpsertVCSReportTarget(ctx, reportTarget); err != nil {
		h.logger.WithError(err).WithField("pr_number", target.PR.Number).Warn("Could not store the reviews report target")
		return
	}
	status := "success"
	if reviews.State != string(vcs.StatusSuccess) {
		status = "pending"
	}
	entry := &models.VCSReportEntry{ReportTargetID: reportTarget.ReportTargetID, EntryKey: reviewsReportKey,
		Generation: reportTarget.CurrentGeneration, Status: status,
		StructuredState: models.JSONB{"title": "Required reviews", "body": RenderReviewLines(reviews), "persistent": true}}
	if err := reports.UpsertVCSReportEntry(ctx, entry); err != nil {
		h.logger.WithError(err).WithField("pr_number", target.PR.Number).Warn("Could not store the reviews report")
	}
}

// RenderReviewLines renders one line for each required review: its state,
// its ID (also the accept label), its description, and who accepted.
func RenderReviewLines(reviews *approvals.HeadReviews) string {
	var out strings.Builder
	out.WriteString(reviews.Summary)
	out.WriteString("\n")
	if len(reviews.Reviews) == 0 {
		return strings.TrimSpace(out.String())
	}
	out.WriteString("\n")
	for _, review := range reviews.Reviews {
		glyph, state := "⏳", "waiting"
		switch {
		case !review.Applies:
			glyph, state = "➖", "does not apply"
		case review.Satisfied:
			glyph, state = "✅", "accepted"
		}
		fmt.Fprintf(&out, "- %s %s: %s", glyph, reviewCode(review.ID), state)
		if review.Applies {
			fmt.Fprintf(&out, " (%d of %d)", len(review.Accepted), review.Required)
		}
		if review.Description != "" {
			fmt.Fprintf(&out, " — %s", reviewText(review.Description))
		}
		if len(review.Accepted) > 0 {
			fmt.Fprintf(&out, ". Accepted by %s", reviewText(strings.Join(review.Accepted, ", ")))
		}
		if review.Applies && !review.Satisfied {
			if len(review.Accept) == 0 {
				out.WriteString(". Nobody can accept this review now")
			} else {
				fmt.Fprintf(&out, ". Can accept: %s; give `/reactorcide accept %s`", reviewText(strings.Join(review.Accept, ", ")), review.ID)
			}
		}
		out.WriteString("\n")
	}
	return strings.TrimSpace(out.String())
}

func reviewCode(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "'") + "`"
}

// reviewText removes characters that change the Markdown of the comment.
func reviewText(value string) string {
	replacer := strings.NewReplacer("\n", " ", "\r", " ", "|", "\\|", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}
