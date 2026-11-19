package approvals

import (
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestGranted_AnyoneSubject(t *testing.T) {
	rows := build(actionSpec{models.CIApprovalKindRun, models.CIApprovalActionApproved, "", "github/stranger",
		[]string{"vcs_user:github/stranger", "principal:p1"}})
	if Granted(rows, models.CIApprovalKindRun, "head", []string{"*"}, base.Add(time.Hour)) == nil {
		t.Fatal("* must grant through any recorded person")
	}
	if Granted(rows, models.CIApprovalKindRun, "head", nil, base.Add(time.Hour)) != nil {
		t.Fatal("an omitted subject list never means anyone")
	}
}

func TestGranted_PrincipalSubject(t *testing.T) {
	rows := build(actionSpec{models.CIApprovalKindRun, models.CIApprovalActionApproved, "", "github/alice",
		[]string{"vcs_user:github/alice", "identity:linkkeys:alice@example.com", "principal:p1"}})
	// A grant to the LinkKeys identity agrees with an approval by the VCS
	// login of the same person.
	if Granted(rows, models.CIApprovalKindRun, "head", []string{"identity:linkkeys:alice@example.com"}, base.Add(time.Hour)) == nil {
		t.Fatal("a linked identity subject must grant")
	}
	if Granted(rows, models.CIApprovalKindRun, "head", []string{"principal:p2"}, base.Add(time.Hour)) != nil {
		t.Fatal("another principal must not grant")
	}
}

// Identity subjects are not roles: a person whose only subjects are their
// own identities cannot satisfy non_author on a public repository.
func TestNonAuthorNeedsARoleNotAnIdentity(t *testing.T) {
	rows := build(accept("github/drive-by", "vcs_user:github/drive-by", "principal:p9", "principal_kind:human",
		"identity:linkkeys:x@example.com"))
	results := evaluate(t, []models.EffectiveRequiredReview{review("peer", 1, models.SubjectNonAuthor)}, rows,
		AuthorFacts{Authors: map[string]bool{"github/author": true}})
	if results[0].Satisfied {
		t.Fatal("principal and identity subjects must not count as a role")
	}
	rows = build(accept("github/member", "vcs_user:github/member", "principal:p9", "reactorcide_group:release"))
	results = evaluate(t, []models.EffectiveRequiredReview{review("peer", 1, models.SubjectNonAuthor)}, rows,
		AuthorFacts{Authors: map[string]bool{"github/author": true}})
	if !results[0].Satisfied {
		t.Fatal("a group is a role")
	}
}

func TestRequiredReviewAnyone(t *testing.T) {
	rows := build(accept("github/a", "vcs_user:github/a"), accept("github/b", "vcs_user:github/b"))
	results := evaluate(t, []models.EffectiveRequiredReview{review("two", 2, "*")}, rows, AuthorFacts{})
	if !results[0].Satisfied || len(results[0].Accepted) != 2 {
		t.Fatalf("results %+v", results[0])
	}
}
