package cipolicy

import (
	"strings"
	"testing"
)

const validPolicy = `version: 1
defaults:
  ci_source: base
  profile: standard
head_ci:
  - id: backend
    actors:
      any: [repository_write]
    workflows: [backend-tests]
    paths: [.reactorcide/jobs/backend/**]
    events: [pull_request_updated]
    base_branches: [main]
    head_repository: same
    use:
      ci_source: head
      profile: pr-untrusted
      workers: default
`

func TestParseAndDecide(t *testing.T) {
	policy, err := ParseDocument([]byte(validPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if policy.Revision == "" {
		t.Fatal("revision is empty")
	}
	decision, err := Decide(policy, Facts{WorkflowID: "backend-tests", ChangedCIPaths: []string{".reactorcide/jobs/backend/test.yaml"}, Event: "pull_request_updated", BaseBranch: "main", HeadRepositoryRelation: "same", ActorSubjects: map[string]bool{"repository_write": true}})
	if err != nil || !decision.Allowed || decision.CISource != "head" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	denied, err := Decide(policy, Facts{WorkflowID: "backend-tests", ChangedCIPaths: []string{".reactorcide/jobs/release.yaml"}, Event: "pull_request_updated", BaseBranch: "main", HeadRepositoryRelation: "same", ActorSubjects: map[string]bool{"repository_write": true}})
	if err != nil || denied.Allowed || denied.CISource != "base" {
		t.Fatalf("denied=%+v err=%v", denied, err)
	}
}

func TestVCSUserCanAuthorizeHeadCI(t *testing.T) {
	input := `version: 1
defaults: {ci_source: base, profile: standard}
head_ci:
- id: maintainers
  actors: {any: [vcs_user:github/todpunk, vcs_user:github/junipuff]}
  workflows: [backend-tests]
  paths: [.reactorcide/jobs/backend/**]
  use: {ci_source: head, profile: pr-untrusted, workers: default}`
	policy, err := ParseDocument([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := Decide(policy, Facts{WorkflowID: "backend-tests", ChangedCIPaths: []string{".reactorcide/jobs/backend/test.yaml"}, HeadRepositoryRelation: "same", ActorSubjects: map[string]bool{"vcs_user:github/junipuff": true}})
	if err != nil || !decision.Allowed || decision.CISource != "head" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}

func TestApprovalSubjectsAreScopedToProfile(t *testing.T) {
	input := `version: 1
defaults: {ci_source: base, profile: standard}
head_ci:
- id: approved
  approval: {any: [project_owner]}
  workflows: [backend-tests]
  paths: [.reactorcide/jobs/backend/**]
  use: {ci_source: head, profile: pr-untrusted, workers: default}`
	policy, err := ParseDocument([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	facts := Facts{
		WorkflowID: "backend-tests", ChangedCIPaths: []string{".reactorcide/jobs/backend/test.yaml"},
		HeadRepositoryRelation: "same",
		ApprovalSubjectsByProfile: map[string]map[string]bool{
			"standard": {"project_owner": true},
		},
	}
	decision, err := Decide(policy, facts)
	if err != nil || decision.Allowed {
		t.Fatalf("wrong-profile approval was accepted: decision=%+v err=%v", decision, err)
	}
	facts.ApprovalSubjectsByProfile["pr-untrusted"] = map[string]bool{"project_owner": true}
	decision, err = Decide(policy, facts)
	if err != nil || !decision.Allowed || len(decision.ApprovalSubjects) != 1 || decision.ApprovalSubjects[0] != "project_owner" {
		t.Fatalf("profile-scoped approval was rejected: decision=%+v err=%v", decision, err)
	}
}

func TestRejectsUnsafeAndUnknownInput(t *testing.T) {
	cases := []string{
		`version: 1
unknown: true`,
		validPolicy + "\n---\nversion: 1\n",
		`version: 1
head_ci:
- id: x
  workflows: [safe]
  paths: [../job.yaml]
  use: {ci_source: head, profile: p, workers: default}`,
		`version: 1
head_ci:
- id: x
  actors: {any: [unknown:actor]}
  workflows: [safe]
  paths: [.reactorcide/jobs/**]
  use: {ci_source: head, profile: p, workers: default}`,
		`version: 1
head_ci:
- id: x
  workflows: [safe]
  paths: [/absolute]
  use: {ci_source: head, profile: p, workers: default}`,
		`version: 1
head_ci:
- id: x
  workflows: [safe]
  paths: ['.reactorcide\\jobs\\safe.yaml']
  use: {ci_source: head, profile: p, workers: default}`,
		`version: 1
head_ci:
- id: x
  workflows: [safe]
  paths: [.reactorcide/jobs/safe.yaml]
  use: {ci_source: head, profile: p, workers: default}
- id: x
  workflows: [other]
  paths: [.reactorcide/jobs/other.yaml]
  use: {ci_source: head, profile: p, workers: default}`,
		`version: 1
head_ci:
- id: x
  workflows: [safe]
  paths: [.reactorcide/jobs/safe.yaml]
  use: {ci_source: head, profile: p, workers: default}
- id: y
  workflows: [safe]
  paths: [.reactorcide/jobs/other.yaml]
  use: {ci_source: head, profile: p, workers: default}`,
	}
	for _, input := range cases {
		if _, err := ParseDocument([]byte(input)); err == nil {
			t.Fatalf("accepted invalid policy:\n%s", input)
		}
	}
}

const nodeAuthorityPolicy = `version: 1
defaults: {ci_source: base, profile: standard}
head_ci:
- id: csilgen
  actors: {any: [repository_write]}
  workflows: [csilgen-pr]
  paths: ['.reactorcide/**']
  events: [pull_request_opened, pull_request_updated]
  base_branches: [main]
  head_repository: any
  use:
    ci_source: head
    profile: pr-untrusted
    workers: default
    base_nodes:
    - nodes: [asset-prepare, asset-seal]
      ci_source: base
      profile: standard
      workers: default
`

func TestParseAndDecideNodeAuthority(t *testing.T) {
	policy, err := ParseDocument([]byte(nodeAuthorityPolicy))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := Decide(policy, Facts{WorkflowID: "csilgen-pr", ChangedCIPaths: []string{".reactorcide/workflows/pr.yaml"}, Event: "pull_request_updated", BaseBranch: "main", HeadRepositoryRelation: "same", ActorSubjects: map[string]bool{"repository_write": true}})
	if err != nil || !decision.Allowed {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	if len(decision.BaseNodes) != 2 {
		t.Fatalf("base nodes=%+v", decision.BaseNodes)
	}
	for _, name := range []string{"asset-prepare", "asset-seal"} {
		grant, ok := decision.BaseNodes[name]
		if !ok || grant.CISource != "base" || grant.Profile != "standard" || grant.WorkerClass != "default" {
			t.Fatalf("node %q grant=%+v ok=%t", name, grant, ok)
		}
	}
	// A policy without base_nodes yields no node authority.
	plain, err := ParseDocument([]byte(validPolicy))
	if err != nil {
		t.Fatal(err)
	}
	plainDecision, err := Decide(plain, Facts{WorkflowID: "backend-tests", ChangedCIPaths: []string{".reactorcide/jobs/backend/test.yaml"}, Event: "pull_request_updated", BaseBranch: "main", HeadRepositoryRelation: "same", ActorSubjects: map[string]bool{"repository_write": true}})
	if err != nil || !plainDecision.Allowed || plainDecision.BaseNodes != nil {
		t.Fatalf("plain decision=%+v err=%v", plainDecision, err)
	}
}

func TestBaseNodesValidation(t *testing.T) {
	template := `version: 1
head_ci:
- id: x
  workflows: [safe]
  paths: [.reactorcide/jobs/**]
  use:
    ci_source: head
    profile: pr-untrusted
    workers: default
    base_nodes:
%s`
	cases := []string{
		"    - nodes: []\n      ci_source: base\n      profile: standard\n      workers: default",
		"    - nodes: ['bad name']\n      ci_source: base\n      profile: standard\n      workers: default",
		"    - nodes: [seal]\n      ci_source: head\n      profile: standard\n      workers: default",
		"    - nodes: [seal]\n      ci_source: base\n      profile: ''\n      workers: default",
		"    - nodes: [seal]\n      ci_source: base\n      profile: standard\n      workers: ''",
		"    - nodes: [seal]\n      ci_source: base\n      profile: standard\n      workers: default\n    - nodes: [seal]\n      ci_source: base\n      profile: standard\n      workers: default",
	}
	for _, entry := range cases {
		input := []byte(strings.ReplaceAll(template, "%s", entry))
		if _, err := ParseDocument(input); err == nil {
			t.Fatalf("accepted invalid base_nodes:\n%s", entry)
		}
	}
}

func TestNodeAuthorityRevisionMatchesRunnerlibCanonicalForm(t *testing.T) {
	policy, err := ParseDocument([]byte(nodeAuthorityPolicy))
	if err != nil {
		t.Fatal(err)
	}
	// Keep the revision stable for stored policies and SHA-bound approvals.
	const expected = "d8cdd4911ee9b9e51e369d447deeffaa0f0c80cfe744a43e01ebe556cbc1a04b"
	if policy.Revision != expected {
		t.Fatalf("revision=%s want %s", policy.Revision, expected)
	}
}

func TestRevisionMatchesRunnerlibCanonicalForm(t *testing.T) {
	input := `version: 1
defaults: {ci_source: base, profile: standard}
head_ci:
- id: backend
  actors: {any: [repository_write]}
  workflows: [backend-tests]
  paths: [.reactorcide/workflows/backend.yaml, .reactorcide/jobs/backend/**]
  events: [pull_request_updated]
  base_branches: [main]
  head_repository: any
  use: {ci_source: head, profile: pr-untrusted, workers: default}`
	policy, err := ParseDocument([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	const expected = "46de77ac374340d3ebca826a7f04345129e4b99344e622d79f75303f0f883e34"
	if policy.Revision != expected {
		t.Fatalf("revision=%s want %s", policy.Revision, expected)
	}
}
