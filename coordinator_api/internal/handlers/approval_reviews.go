package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/sirupsen/logrus"
)

// ReviewsStatusContext is the commit status of the required reviews of a pull
// request head. It is separate from "Reactorcide Checks": a review is about the
// change, not about CI. A ruleset that needs the reviews requires this name.
const ReviewsStatusContext = "Reactorcide Reviews"

// evaluateHeadReviews calculates the required reviews of the current head of
// a pull request. It returns nil when the project needs no review.
func (h *WebhookHandler) evaluateHeadReviews(ctx context.Context, project *models.Project, target *prTarget) (*approvals.HeadReviews, error) {
	trust, err := h.effectiveTrust(ctx, project)
	if err != nil {
		return nil, err
	}
	// The code owners import (docs/reporting-and-ownership.md) adds derived
	// reviews. With the import on, the status is posted also when no review
	// applies, so a branch rule that requires it can pass.
	importCodeowners := h.codeownersImportOn(ctx, project)
	if len(trust.RequiredReviews) == 0 && !importCodeowners {
		return nil, nil
	}
	pr := target.PR
	rows := []models.CIApproval{}
	if actions, ok := h.store.(store.CIApprovalStore); ok {
		if rows, err = actions.ListCIApprovalsForHead(ctx, project.ProjectID, pr.Number, pr.HeadSHA); err != nil {
			return nil, fmt.Errorf("loading approvals: %w", err)
		}
	}
	reviews := trust.RequiredReviews
	needsPaths := importCodeowners
	for _, review := range reviews {
		if len(review.Paths) > 0 {
			needsPaths = true
		}
	}
	var changed []string
	changedKnown := true
	if needsPaths {
		lister, ok := target.Client.(vcs.PRFileLister)
		var listErr error
		if ok {
			changed, listErr = lister.ListPRFiles(ctx, target.Repo, pr.Number)
		}
		if !ok || listErr != nil {
			changedKnown = false
			// Fail closed: every review applies when the changed paths are
			// not known.
			h.logger.WithError(listErr).WithField("pr_number", pr.Number).Warn("Could not list pull request files; every required review applies")
			widened := make([]models.EffectiveRequiredReview, len(reviews))
			for i, review := range reviews {
				widened[i] = review
				widened[i].Paths = nil
			}
			reviews = widened
		}
	}
	if importCodeowners {
		reviews = append(reviews, h.codeownersReviews(ctx, project, target, changed, changedKnown)...)
	}
	authors := h.prAuthors(ctx, project, target)
	results := approvals.EvaluateReviews(reviews, rows, pr.HeadSHA, changed, authors, time.Now().UTC())
	state, summary := reviewsStatus(results)
	return &approvals.HeadReviews{HeadSHA: pr.HeadSHA, Reviews: results, State: string(state), Summary: summary}, nil
}

// prAuthors lists the people who wrote the change: the pull request author,
// the actor of each recorded head update, and the accounts of the commit
// authors and committers.
func (h *WebhookHandler) prAuthors(ctx context.Context, project *models.Project, target *prTarget) approvals.AuthorFacts {
	pr := target.PR
	provider := string(target.Provider)
	facts := approvals.AuthorFacts{Authors: map[string]bool{}}
	add := func(login string) {
		if person := models.VCSPerson(provider, login); person != "" {
			facts.Authors[person] = true
		}
	}
	add(pr.AuthorLogin)
	if heads, ok := h.store.(store.PRHeadStore); ok {
		updates, err := heads.ListPRHeadUpdateActors(ctx, project.ProjectID, pr.Number)
		if err != nil {
			facts.Unverified = true
		}
		for _, update := range updates {
			add(update.Actor)
			if facts.LastPusher == "" && update.HeadSHA == pr.HeadSHA {
				facts.LastPusher = models.VCSPerson(provider, update.Actor)
			}
		}
	}
	if facts.LastPusher == "" {
		facts.LastPusher = models.VCSPerson(provider, pr.AuthorLogin)
	}
	lister, ok := target.Client.(vcs.PRCommitIdentityLister)
	if !ok {
		facts.Unverified = true
		return facts
	}
	logins, err := lister.ListPRCommitLogins(ctx, target.Repo, pr.Number)
	if err != nil {
		h.logger.WithError(err).WithField("pr_number", pr.Number).Warn("Could not list commit authors; non-author reviews fail closed")
		facts.Unverified = true
		return facts
	}
	for _, login := range logins {
		add(login)
	}
	return facts
}

func reviewsStatus(results []approvals.ReviewResult) (vcs.StatusState, string) {
	var missing []string
	applying := 0
	for _, result := range results {
		if !result.Applies {
			continue
		}
		applying++
		if !result.Satisfied {
			missing = append(missing, fmt.Sprintf("%s (%d of %d)", result.ID, len(result.Accepted), result.Required))
		}
	}
	if len(missing) == 0 {
		if applying == 0 {
			return vcs.StatusSuccess, "No required review applies to this change"
		}
		return vcs.StatusSuccess, fmt.Sprintf("%d required %s accepted", applying, plural(applying, "review", "reviews"))
	}
	description := "Needs review: " + strings.Join(missing, ", ")
	if len(description) > 140 {
		description = description[:137] + "..."
	}
	return vcs.StatusPending, description
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// publishReviewsStatus posts the "Reactorcide Reviews" status of the current
// head. A project with no required review posts nothing.
func (h *WebhookHandler) publishReviewsStatus(ctx context.Context, project *models.Project, target *prTarget) {
	reviews, err := h.evaluateHeadReviews(ctx, project, target)
	fields := logrus.Fields{"project": project.Name, "pr_number": target.PR.Number}
	if err != nil {
		h.logger.WithError(err).WithFields(fields).Warn("Could not evaluate required reviews")
		return
	}
	if reviews == nil {
		return
	}
	// One line for each required review in the pull request comment.
	h.publishReviewsReport(ctx, project, target, reviews)
	if !h.vcsStatusesAllowed(ctx, project) {
		return
	}
	if err := target.Client.UpdateCommitStatus(ctx, target.Repo, vcs.StatusUpdate{SHA: reviews.HeadSHA, State: vcs.StatusState(reviews.State),
		Description: reviews.Summary, Context: ReviewsStatusContext}); err != nil {
		h.logger.WithError(err).WithFields(fields).Warn("Could not post the required reviews status")
	}
}

// HeadReviews returns the required-review state of the current head, for the
// API and the UI. Reviews is nil when the project needs no review.
func (h *WebhookHandler) HeadReviews(ctx context.Context, project *models.Project, prNumber int) (*approvals.HeadReviews, error) {
	target, err := h.loadPRTarget(ctx, project, prNumber)
	if err != nil {
		return nil, err
	}
	reviews, err := h.evaluateHeadReviews(ctx, project, target)
	if err != nil {
		return nil, err
	}
	if reviews == nil {
		return &approvals.HeadReviews{HeadSHA: target.PR.HeadSHA}, nil
	}
	return reviews, nil
}
