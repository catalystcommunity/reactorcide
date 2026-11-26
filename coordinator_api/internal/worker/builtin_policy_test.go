package worker

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// The secure defaults built-in policy (plan item A5): a head that changes CI
// runs the base definition and the change is reported as refused, until
// override-ci; then the head's CI runs with the override profile.
func TestBuiltInPolicy_CIChangeNeedsOverride(t *testing.T) {
	parent, policy := mixedTrustEvalParent(t, []byte(cipolicy.BuiltInDocument))
	builtIn, _, err := cipolicy.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	if policy.Revision != builtIn.Revision {
		t.Fatalf("revision of the attached document %s, want %s", policy.Revision, builtIn.Revision)
	}
	projectID := "project"
	parent.ProjectID = &projectID
	parent.JobEnvVars["REACTORCIDE_IS_FORK_PR"] = "false"
	if err := (&vcs.JobMetadata{VCSProvider: "github", Repo: "org/repo", PRNumber: 7, CommitSHA: *parent.SourceRef, IsEval: true}).ApplyToJob(parent); err != nil {
		t.Fatal(err)
	}
	changed := []string{".reactorcide/workflows/pr.yaml"}
	tp := newMixedTrustProcessor()

	selected, violations, _, err := tp.selectPolicyCandidatesRefused(context.Background(), parent, mixedTrustCandidates(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].CIOrigin == "head" {
		t.Fatalf("the built-in policy must run base CI: %+v", selected)
	}
	if len(violations) == 0 {
		t.Fatal("a refused CI change must be reported")
	}

	encoded, err := approvals.RunAuthority{Override: &approvals.RunOverride{ApprovalID: "a1", Person: "github/maintainer", Profile: "pr-untrusted"}}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	parent.JobEnvVars[approvals.RunAuthorityEnv] = encoded
	selected, violations, _, err = tp.selectPolicyCandidatesRefused(context.Background(), parent, mixedTrustCandidates(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].CIOrigin != "head" || selected[0].ExecutionProfile != "pr-untrusted" {
		t.Fatalf("override-ci must run the head's CI with the override profile: %+v", selected)
	}
	if len(violations) != 0 {
		t.Fatalf("override-ci must not report a refusal: %+v", violations)
	}
}
