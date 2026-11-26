package worker

import (
	"context"
	"strings"
	"testing"

	approvalstate "github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/owners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// The policy of the Firepit incident (2026-11-25): head CI only for the pr
// workflow, for two named users.
const firepitPolicy = `version: 1
defaults: {ci_source: base, profile: standard}
head_ci:
- id: ci-dev
  actors: {any: [vcs_user:github/todpunk, vcs_user:github/junipuff]}
  workflows: [pr]
  paths: ['.reactorcide/**']
  events: [pull_request_opened, pull_request_updated]
  base_branches: [main]
  head_repository: same
  use:
    ci_source: head
    profile: pr-untrusted
    workers: default`

const releaseControlJob = ".reactorcide/jobs/release-control.yaml"

func firepitParent(t *testing.T) *models.Job {
	t.Helper()
	parent := prEvalParent(t, []byte(firepitPolicy))
	parent.JobEnvVars["REACTORCIDE_ACTOR_SUBJECTS"] = `["vcs_user:github/todpunk"]`
	parent.JobEnvVars["REACTORCIDE_IS_FORK_PR"] = "false"
	return parent
}

// A pull request workflow and a release workflow. Only the release
// workflow uses the release-control job, and it does not match a PR event.
func firepitCandidates() *policyCandidateSet {
	workflow := func(id string, matched bool, deps ...string) triggerWorkflowSpec {
		return triggerWorkflowSpec{ID: id, Name: id, EventMatched: matched, ExplicitID: true, DependencyPaths: deps,
			Jobs: []triggerJobSpec{{JobName: id + "-job", JobCommand: "make " + id}}}
	}
	set := &policyCandidateSet{}
	for _, side := range []*[]triggerWorkflowSpec{&set.Base, &set.Head} {
		*side = []triggerWorkflowSpec{
			workflow("pr", true, ".reactorcide/workflows/pr.yaml", ".reactorcide/jobs/test.yaml"),
			workflow("release", false, ".reactorcide/workflows/release.yaml", releaseControlJob),
			workflow("nightly", false, ".reactorcide/workflows/nightly.yaml", ".reactorcide/jobs/nightly.yaml"),
		}
	}
	set.Base[1].Owners = &owners.Spec{Group: "release-team"}
	set.Base[2].Owners = &owners.Spec{Group: "nightly-team"}
	return set
}

func headCIApproval(id, headSHA string, subjects ...string) models.CIApproval {
	return models.CIApproval{ApprovalID: id, Kind: models.CIApprovalKindHeadCI, Action: models.CIApprovalActionApproved,
		OrgID: "org", ProjectID: "project", PRNumber: 7, HeadRepository: "fork/repo", HeadSHA: headSHA,
		Person: "github/carol", Subjects: append([]string{"vcs_user:github/carol"}, subjects...), Channel: models.CIApprovalChannelComment}
}

func selectFirepit(t *testing.T, parent *models.Job, approvals []models.CIApproval, changed ...string) ([]triggerWorkflowSpec, []ciPathDecision) {
	t.Helper()
	st := &multiProfileStore{workflowRuntimeStore: newWorkflowRuntimeStore(), profiles: mixedTrustProfiles(), approvals: approvals}
	selected, decisions, _, err := NewTriggerProcessor(st, nil).selectPolicyCandidates(context.Background(), parent, firepitCandidates(), changed)
	if err != nil {
		t.Fatal(err)
	}
	return selected, decisions
}

func assertOnlyBasePR(t *testing.T, selected []triggerWorkflowSpec) {
	t.Helper()
	if len(selected) != 1 || selected[0].ID != "pr" || selected[0].CIOrigin != "base" {
		t.Fatalf("expected only the pr workflow, on base CI (no release jobs): %+v", selected)
	}
}

// Step 1: without an override the release-only change is refused, and the
// refusal names the file and the workflow that uses it.
func TestReleaseOnlyChange_RefusedWithoutOverride(t *testing.T) {
	selected, decisions := selectFirepit(t, firepitParent(t), nil, releaseControlJob)
	assertOnlyBasePR(t, selected)
	refused := refusedPaths(decisions)
	if len(refused) != 1 || refused[0].Path != releaseControlJob || refused[0].WorkflowID != "release" {
		t.Fatalf("expected %s refused for workflow release: %+v", releaseControlJob, decisions)
	}
}

// Step 2: override-ci for the head SHA accepts the change. The pr workflow
// keeps base CI (it does not use the file) and no release job runs.
func TestReleaseOnlyChange_OverrideAccepts(t *testing.T) {
	parent := firepitParent(t)
	withRunAuthority(t, parent, approvalstate.RunAuthority{Override: &approvalstate.RunOverride{
		ApprovalID: "override-1", Person: "github/todpunk", Profile: "pr-untrusted"}})
	selected, decisions := selectFirepit(t, parent, nil, releaseControlJob)
	assertOnlyBasePR(t, selected)
	if refused := refusedPaths(decisions); len(refused) != 0 {
		t.Fatalf("override-ci must accept the change: %+v", refused)
	}
	if len(decisions) != 1 || decisions[0].Authority.Rule != overrideRuleID || decisions[0].Authority.ApprovalID != "override-1" {
		t.Fatalf("the decision must name the override approval: %+v", decisions)
	}
}

// A policy rule for the workflow that owns the file accepts it, also when
// that workflow does not run for the event.
func TestReleaseOnlyChange_PolicyRuleForReleaseWorkflowAccepts(t *testing.T) {
	policy := `version: 1
defaults: {ci_source: base, profile: standard}
head_ci:
- id: release-dev
  actors: {any: [vcs_user:github/todpunk]}
  workflows: [release]
  paths: ['.reactorcide/**']
  use: {ci_source: head, profile: pr-untrusted, workers: default}`
	parent := prEvalParent(t, []byte(policy))
	parent.JobEnvVars["REACTORCIDE_ACTOR_SUBJECTS"] = `["vcs_user:github/todpunk"]`
	parent.JobEnvVars["REACTORCIDE_IS_FORK_PR"] = "false"
	selected, decisions := selectFirepit(t, parent, nil, releaseControlJob)
	assertOnlyBasePR(t, selected)
	if len(decisions) != 1 || decisions[0].Authority == nil || decisions[0].Authority.Rule != "release-dev" ||
		decisions[0].Authority.WorkflowID != "release" {
		t.Fatalf("policy rule release-dev must accept the change: %+v", decisions)
	}
}

// Step 3: an owner approval binds to its head SHA, and covers only the
// files of the workflow it owns.
func TestReleaseOnlyChange_OwnerApprovalScope(t *testing.T) {
	parent := firepitParent(t)
	withRunAuthority(t, parent, approvalstate.RunAuthority{OwnerOverride: &approvalstate.RunOwnerOverride{Profile: "pr-untrusted"}})
	owner := "reactorcide_group:release-team"

	// An approval of an earlier head does not cover the new head.
	_, decisions := selectFirepit(t, parent, []models.CIApproval{headCIApproval("old", "old-head-sha", owner)}, releaseControlJob)
	if refused := refusedPaths(decisions); len(refused) != 1 || refused[0].Path != releaseControlJob {
		t.Fatalf("an approval of another head SHA must not cover the change: %+v", decisions)
	}

	// An approval of the current head by the release owner covers only the
	// release files. The nightly file still needs an approval.
	nightlyJob := ".reactorcide/jobs/nightly.yaml"
	selected, decisions := selectFirepit(t, parent, []models.CIApproval{headCIApproval("current", *parent.SourceRef, owner)},
		releaseControlJob, nightlyJob)
	assertOnlyBasePR(t, selected)
	byPath := map[string]ciPathDecision{}
	for _, decision := range decisions {
		byPath[decision.Path] = decision
	}
	if accepted := byPath[releaseControlJob]; accepted.Authority == nil || accepted.Authority.ApprovalID != "current" ||
		accepted.Authority.WorkflowID != "release" {
		t.Fatalf("the owner approval must accept the release file: %+v", accepted)
	}
	if refused := byPath[nightlyJob]; refused.Authority != nil || refused.WorkflowID != "nightly" {
		t.Fatalf("the nightly file is outside the approval's scope and must stay refused: %+v", refused)
	}
}

// A head grant for one workflow does not hide the refusal of a file that
// only another workflow uses.
func TestReleaseOnlyChange_HeadGrantDoesNotCoverOtherWorkflowFile(t *testing.T) {
	selected, decisions := selectFirepit(t, firepitParent(t), nil, ".reactorcide/workflows/pr.yaml", releaseControlJob)
	if len(selected) != 1 || selected[0].ID != "pr" || selected[0].CIOrigin != "head" {
		t.Fatalf("precondition: the pr workflow gets head CI: %+v", selected)
	}
	refused := refusedPaths(decisions)
	if len(refused) != 1 || refused[0].Path != releaseControlJob {
		t.Fatalf("the release file must stay refused: %+v", decisions)
	}
}

// The ci-dev rule with workflows ["*"]: the actor match is enough. A change
// that only the release workflow uses is accepted with no override-ci, and
// nothing of the release workflow runs.
func TestReleaseOnlyChange_WildcardRuleAccepts(t *testing.T) {
	policy := strings.Replace(firepitPolicy, "workflows: [pr]", `workflows: ["*"]`, 1)
	parent := prEvalParent(t, []byte(policy))
	parent.JobEnvVars["REACTORCIDE_ACTOR_SUBJECTS"] = `["vcs_user:github/todpunk"]`
	parent.JobEnvVars["REACTORCIDE_IS_FORK_PR"] = "false"
	selected, decisions := selectFirepit(t, parent, nil, releaseControlJob)
	assertOnlyBasePR(t, selected)
	if len(decisions) != 1 || decisions[0].Authority == nil || decisions[0].Authority.Rule != "ci-dev" ||
		decisions[0].Authority.WorkflowID != "release" {
		t.Fatalf("rule ci-dev must accept the release file: %+v", decisions)
	}

	// A change to the pr workflow and the release workflow: pr runs head CI
	// and both files are accepted.
	selected, decisions = selectFirepit(t, parent, nil, ".reactorcide/workflows/pr.yaml", releaseControlJob)
	if len(selected) != 1 || selected[0].CIOrigin != "head" {
		t.Fatalf("the pr workflow must run head CI: %+v", selected)
	}
	if refused := refusedPaths(decisions); len(refused) != 0 {
		t.Fatalf("no file may be refused: %+v", refused)
	}

	// Another actor still needs override-ci.
	parent.JobEnvVars["REACTORCIDE_ACTOR_SUBJECTS"] = `["vcs_user:github/someone"]`
	_, decisions = selectFirepit(t, parent, nil, releaseControlJob)
	if refused := refusedPaths(decisions); len(refused) != 1 {
		t.Fatalf("the rule must not accept another actor: %+v", decisions)
	}
}

// With no head candidates, an evaluation whose trusted CI is the event's own
// revision (a push to the default branch, a tag) refuses nothing. Another
// revision refuses each changed file, unless override-ci accepts it.
func TestNoCandidatePathDecisions(t *testing.T) {
	head := "pushed-sha"
	parent := &models.Job{SourceRef: &head, CISHA: head, JobEnvVars: models.JSONB{}}
	if decisions := noCandidatePathDecisions(parent, []string{releaseControlJob}); len(decisions) != 0 {
		t.Fatalf("the event's own CI ran; nothing may be refused: %+v", decisions)
	}
	parent.CISHA = "before-sha"
	decisions := noCandidatePathDecisions(parent, []string{releaseControlJob})
	if len(decisions) != 1 || decisions[0].Authority != nil {
		t.Fatalf("trusted base CI ran; the change must be refused: %+v", decisions)
	}
	withRunAuthority(t, parent, approvalstate.RunAuthority{Override: &approvalstate.RunOverride{ApprovalID: "o-1", Person: "github/todpunk", Profile: "pr-untrusted"}})
	decisions = noCandidatePathDecisions(parent, []string{releaseControlJob})
	if len(decisions) != 1 || decisions[0].Authority == nil || decisions[0].Authority.ApprovalID != "o-1" {
		t.Fatalf("override-ci must accept the change: %+v", decisions)
	}
}
