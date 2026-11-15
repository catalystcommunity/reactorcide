package approvals

import (
	"fmt"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

var base = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

type actionSpec struct {
	kind, action, label, person string
	subjects                    []string
}

func build(specs ...actionSpec) []models.CIApproval {
	rows := make([]models.CIApproval, 0, len(specs))
	for i, spec := range specs {
		rows = append(rows, models.CIApproval{ApprovalID: fmt.Sprintf("a%02d", i), Kind: spec.kind, Action: spec.action,
			Label: spec.label, Person: spec.person, Subjects: spec.subjects, HeadSHA: "head",
			CreatedAt: base.Add(time.Duration(i) * time.Minute)})
	}
	return rows
}

func accept(person string, subjects ...string) actionSpec {
	return actionSpec{models.CIApprovalKindAccept, models.CIApprovalActionApproved, "", person, subjects}
}

func revoke(person string, subjects ...string) actionSpec {
	return actionSpec{models.CIApprovalKindAccept, models.CIApprovalActionRevoked, "", person, subjects}
}

func review(id string, count int, subjects ...string) models.EffectiveRequiredReview {
	return models.EffectiveRequiredReview{RequiredReview: models.RequiredReview{ID: id, Accept: subjects, Count: count}, Scope: "project"}
}

func evaluate(t *testing.T, reviews []models.EffectiveRequiredReview, rows []models.CIApproval, authors AuthorFacts) []ReviewResult {
	t.Helper()
	return EvaluateReviews(reviews, rows, "head", []string{"src/main.go"}, authors, base.Add(time.Hour))
}

func TestGranted_LastActionForRoleWins(t *testing.T) {
	approve := func(person string, subjects ...string) actionSpec {
		return actionSpec{models.CIApprovalKindRun, models.CIApprovalActionApproved, "", person, subjects}
	}
	cancel := func(person string, subjects ...string) actionSpec {
		return actionSpec{models.CIApprovalKindRun, models.CIApprovalActionRevoked, "", person, subjects}
	}
	rows := build(approve("github/alice", "repository_write"), cancel("github/bob", "repository_write"))
	if got := Granted(rows, models.CIApprovalKindRun, "head", []string{"repository_write"}, base.Add(time.Hour)); got != nil {
		t.Fatalf("a revoke by another person with the role must cancel the approval, got %+v", got)
	}
	rows = build(approve("github/alice", "repository_write", "vcs_team:o/a"), cancel("github/bob", "repository_write"))
	got := Granted(rows, models.CIApprovalKindRun, "head", []string{"repository_write", "vcs_team:o/a"}, base.Add(time.Hour))
	if got == nil || got.Person != "github/alice" {
		t.Fatalf("an approval through a role the revoker does not have must stay, got %+v", got)
	}
	if Granted(rows, models.CIApprovalKindHeadCI, "head", []string{"vcs_team:o/a"}, base.Add(time.Hour)) != nil {
		t.Fatal("a run approval must not grant head CI")
	}
	if Granted(rows, models.CIApprovalKindRun, "other-head", []string{"vcs_team:o/a"}, base.Add(time.Hour)) != nil {
		t.Fatal("an approval binds to its head SHA")
	}
}

func TestEvaluateReviews_TeamRevokeCancelsTeam(t *testing.T) {
	team := "reactorcide_group:maintainers"
	reviews := []models.EffectiveRequiredReview{review("trust", 1, team)}
	results := evaluate(t, reviews, build(accept("github/alice", team), revoke("github/bob", team)), AuthorFacts{})
	if results[0].Satisfied {
		t.Fatalf("the team revoke must win, got %+v", results[0])
	}
	results = evaluate(t, reviews, build(accept("github/alice", team), revoke("github/bob", team), accept("github/carol", team)), AuthorFacts{})
	if !results[0].Satisfied || len(results[0].Accepted) != 1 || results[0].Accepted[0] != "github/carol" {
		t.Fatalf("a new accept after the revoke must count, got %+v", results[0])
	}
}

func TestEvaluateReviews_RevokeKeepsOtherTeams(t *testing.T) {
	security, platform := "vcs_team:o/security", "vcs_team:o/platform"
	reviews := []models.EffectiveRequiredReview{review("sec", 1, security), review("plat", 1, platform)}
	// Bob is in platform only. His revoke must not touch the security review.
	results := evaluate(t, reviews, build(accept("github/alice", security, platform), revoke("github/bob", platform)), AuthorFacts{})
	if !results[0].Satisfied || results[1].Satisfied {
		t.Fatalf("security must stay accepted and platform must be revoked, got %+v", results)
	}
}

func TestEvaluateReviews_CountIsDistinctPeopleAfterRevoke(t *testing.T) {
	team := "vcs_team:o/a"
	reviews := []models.EffectiveRequiredReview{review("two", 2, team)}
	rows := build(accept("github/a", team), accept("github/a", team), accept("github/b", team))
	if results := evaluate(t, reviews, rows, AuthorFacts{}); !results[0].Satisfied {
		t.Fatalf("two different people accepted, got %+v", results[0])
	}
	rows = build(accept("github/a", team), revoke("github/c", team), accept("github/b", team))
	if results := evaluate(t, reviews, rows, AuthorFacts{}); results[0].Satisfied {
		t.Fatalf("only one person accepted after the revoke, got %+v", results[0])
	}
}

func TestEvaluateReviews_NonAuthor(t *testing.T) {
	reviews := []models.EffectiveRequiredReview{review("compliance", 1, models.SubjectNonAuthor)}
	authors := AuthorFacts{Authors: map[string]bool{"github/author": true}, LastPusher: "github/pusher"}
	for _, person := range []string{"github/author", "github/pusher"} {
		if results := evaluate(t, reviews, build(accept(person, "vcs_user:"+person, "repository_write")), authors); results[0].Satisfied {
			t.Fatalf("%s wrote the change and cannot accept it", person)
		}
	}
	if results := evaluate(t, reviews, build(accept("github/stranger", "vcs_user:github/stranger")), authors); results[0].Satisfied {
		t.Fatal("an account with no role must not satisfy non_author")
	}
	// For non_author the role is the person: another person's revoke does
	// not cancel this accept.
	rows := build(accept("github/reviewer", "vcs_user:github/reviewer", "repository_write"), revoke("github/other", "vcs_user:github/other", "repository_write"))
	if results := evaluate(t, reviews, rows, authors); !results[0].Satisfied {
		t.Fatalf("another person's revoke must not cancel a non-author accept, got %+v", results[0])
	}
	if results := evaluate(t, reviews, rows, AuthorFacts{Unverified: true}); results[0].Satisfied {
		t.Fatal("unverified authors must fail closed")
	}
	rows = build(accept("github/reviewer", "vcs_user:github/reviewer", "repository_write"), revoke("github/reviewer", "vcs_user:github/reviewer"))
	if results := evaluate(t, reviews, rows, authors); results[0].Satisfied {
		t.Fatal("the reviewer's own revoke must cancel the accept")
	}
}

func TestEvaluateReviews_LabelsAndPaths(t *testing.T) {
	team := "vcs_team:o/a"
	trust := review("trust", 1, team)
	trust.Paths = []string{"crypto/**"}
	docs := review("docs", 1, team)
	labelled := accept("github/a", team)
	labelled.label = "docs"
	results := EvaluateReviews([]models.EffectiveRequiredReview{trust, docs}, build(labelled), "head",
		[]string{"crypto/keys.rs"}, AuthorFacts{}, base.Add(time.Hour))
	if results[0].Satisfied || !results[1].Satisfied {
		t.Fatalf("a labelled accept counts only for its review, got %+v", results)
	}
	results = EvaluateReviews([]models.EffectiveRequiredReview{trust}, nil, "head", []string{"README.md"}, AuthorFacts{}, base.Add(time.Hour))
	if results[0].Applies || !results[0].Satisfied || !AllSatisfied(results) {
		t.Fatalf("a review whose paths do not change does not apply, got %+v", results)
	}
}
