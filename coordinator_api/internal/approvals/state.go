// Package approvals calculates the state of pull request approvals from the
// append-only approval actions.
//
// Each action is one person approving or revoking one kind for one head. The
// rules are:
//
//   - For each role (approver subject), the last action that names the role
//     wins. A revoke by a person cancels every role that person has, also the
//     approval of another person with the same role.
//   - An approval through a role that the revoker does not have stays.
//   - For the non_author subject of a required review, the role is the person:
//     a revoke cancels only that person's approval. The person must also have
//     a role (not only their own vcs_user subject) and must not have written
//     the change.
package approvals

import (
	"sort"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/subjects"
)

// ordered returns the active actions for the head, oldest first.
func ordered(rows []models.CIApproval, headSHA string, now time.Time) []models.CIApproval {
	result := make([]models.CIApproval, 0, len(rows))
	for _, row := range rows {
		if row.HeadSHA == headSHA && row.IsActive(now) {
			result = append(result, row)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return result[i].ApprovalID < result[j].ApprovalID
	})
	return result
}

// Granted returns the approval that grants kind for the head through one of
// the allowed subjects, or nil. When more than one role grants, the newest
// approval is returned.
func Granted(rows []models.CIApproval, kind, headSHA string, allowed []string, now time.Time) *models.CIApproval {
	actions := ordered(rows, headSHA, now)
	var granted *models.CIApproval
	for _, subject := range allowed {
		var last *models.CIApproval
		for i := range actions {
			if actions[i].Kind == kind && actions[i].HasSubject(subject) {
				last = &actions[i]
			}
		}
		if last != nil && last.Action == models.CIApprovalActionApproved {
			if granted == nil || last.CreatedAt.After(granted.CreatedAt) {
				copied := *last
				granted = &copied
			}
		}
	}
	return granted
}

// AuthorFacts are the people who wrote the change. Values are person keys
// (models.VCSPerson).
type AuthorFacts struct {
	Authors map[string]bool
	// LastPusher is the person who made the current head. That person cannot
	// approve the head as a non-author.
	LastPusher string
	// Unverified is true when the authors could not be listed. No accept
	// counts through non_author then, so the review fails closed.
	Unverified bool
}

// ReviewResult is the state of one required review for a head.
type ReviewResult struct {
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Scope       string   `json:"scope"`
	Applies     bool     `json:"applies"`
	Required    int      `json:"required"`
	Accepted    []string `json:"accepted"`
	Satisfied   bool     `json:"satisfied"`
	Accept      []string `json:"accept"`
}

// labelMatches reports whether an accept action counts for the review. An
// accept with no label counts for every review; a labelled accept counts only
// for the review with that ID. A revoke has no label and counts for every
// review.
func labelMatches(action models.CIApproval, reviewID string) bool {
	return action.Label == "" || action.Label == reviewID
}

// EvaluateReviews calculates each required review for the head.
func EvaluateReviews(reviews []models.EffectiveRequiredReview, rows []models.CIApproval, headSHA string,
	changedPaths []string, authors AuthorFacts, now time.Time) []ReviewResult {
	actions := ordered(rows, headSHA, now)
	accepts := make([]models.CIApproval, 0, len(actions))
	for _, action := range actions {
		if action.Kind == models.CIApprovalKindAccept {
			accepts = append(accepts, action)
		}
	}
	results := make([]ReviewResult, 0, len(reviews))
	for _, review := range reviews {
		result := ReviewResult{ID: review.ID, Description: review.Description, Scope: review.Scope,
			Required: review.RequiredCount(), Accept: append([]string{}, review.Accept...), Accepted: []string{}}
		result.Applies = reviewApplies(review.RequiredReview, changedPaths)
		if !result.Applies {
			result.Satisfied = true
			results = append(results, result)
			continue
		}
		people := map[string]bool{}
		for _, subject := range review.Accept {
			for person := range acceptedThrough(accepts, review.ID, subject, authors) {
				people[person] = true
			}
		}
		for person := range people {
			result.Accepted = append(result.Accepted, person)
		}
		sort.Strings(result.Accepted)
		result.Satisfied = len(result.Accepted) >= result.Required
		results = append(results, result)
	}
	return results
}

func reviewApplies(review models.RequiredReview, changedPaths []string) bool {
	if len(review.Paths) == 0 {
		return true
	}
	for _, changed := range changedPaths {
		for _, pattern := range review.Paths {
			if cipolicy.GlobMatch(pattern, changed) {
				return true
			}
		}
	}
	return false
}

// acceptedThrough returns the people whose accept counts through one subject.
func acceptedThrough(accepts []models.CIApproval, reviewID, subject string, authors AuthorFacts) map[string]bool {
	result := map[string]bool{}
	if subject == models.SubjectNonAuthor {
		if authors.Unverified {
			return result
		}
		// The role is the person: only that person's own last action counts.
		last := map[string]models.CIApproval{}
		for _, action := range accepts {
			if labelMatches(action, reviewID) {
				last[action.Person] = action
			}
		}
		for person, action := range last {
			if action.Action == models.CIApprovalActionApproved && hasRole(action) && !authors.Authors[person] && person != authors.LastPusher {
				result[person] = true
			}
		}
		return result
	}
	// The role is shared: the last revoke by anyone with the role cancels
	// every approval through it that came before.
	cut := -1
	for i, action := range accepts {
		if action.Action == models.CIApprovalActionRevoked && action.HasSubject(subject) {
			cut = i
		}
	}
	last := map[string]models.CIApproval{}
	for i, action := range accepts {
		if i <= cut || !labelMatches(action, reviewID) {
			continue
		}
		last[action.Person] = action
	}
	for person, action := range last {
		if action.Action == models.CIApprovalActionApproved && action.HasSubject(subject) {
			result[person] = true
		}
	}
	return result
}

// hasRole reports whether the person had a subject other than their own
// vcs_user identity. Any account can comment on a public repository, so
// non_author counts only a person with a role: repository write, a team, a
// group, or project owner.
func hasRole(action models.CIApproval) bool {
	for _, subject := range action.Subjects {
		if subjects.IsRole(subject) {
			return true
		}
	}
	return false
}

// AllSatisfied reports whether every review that applies is satisfied.
func AllSatisfied(results []ReviewResult) bool {
	for _, result := range results {
		if result.Applies && !result.Satisfied {
			return false
		}
	}
	return true
}
