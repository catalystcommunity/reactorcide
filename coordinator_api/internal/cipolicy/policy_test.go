package cipolicy

import "testing"

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
	policy, err := Parse(map[string][]byte{".reactorcide/policy.yaml": []byte(validPolicy)})
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

func TestRejectsUnsafeAndUnknownInput(t *testing.T) {
	cases := []string{
		`version: 1
unknown: true`,
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
		if _, err := Parse(map[string][]byte{".reactorcide/policy.yaml": []byte(input)}); err == nil {
			t.Fatalf("accepted invalid policy:\n%s", input)
		}
	}
}

func TestFragmentOrderIsCanonical(t *testing.T) {
	fragment := []byte(`version: 1
head_ci:
- id: docs
  workflows: [docs]
  paths: [.reactorcide/jobs/docs/**]
  use: {ci_source: head, profile: pr-untrusted, workers: default}`)
	one, err := Parse(map[string][]byte{".reactorcide/policy.yaml": []byte(validPolicy), ".reactorcide/policies/z.yaml": fragment})
	if err != nil {
		t.Fatal(err)
	}
	two, err := Parse(map[string][]byte{".reactorcide/policies/z.yaml": fragment, ".reactorcide/policy.yaml": []byte(validPolicy)})
	if err != nil {
		t.Fatal(err)
	}
	if one.Revision != two.Revision {
		t.Fatalf("revision differs: %s != %s", one.Revision, two.Revision)
	}
}

func TestRevisionMatchesRunnerlibCanonicalForm(t *testing.T) {
	input := `version: 1
defaults: {ci_source: base, profile: standard}
policy_maintainers: {any: [reactorcide_group:ci-admins]}
head_ci:
- id: backend
  actors: {any: [repository_write]}
  workflows: [backend-tests]
  paths: [.reactorcide/workflows/backend.yaml, .reactorcide/jobs/backend/**]
  events: [pull_request_updated]
  base_branches: [main]
  head_repository: any
  use: {ci_source: head, profile: pr-untrusted, workers: default}`
	policy, err := Parse(map[string][]byte{".reactorcide/policy.yaml": []byte(input)})
	if err != nil {
		t.Fatal(err)
	}
	const expected = "a85ce6d79bcd69eb24ee4768b1ff32e0b848befc528cb512681041dd2caf59da"
	if policy.Revision != expected {
		t.Fatalf("revision=%s want %s", policy.Revision, expected)
	}
}
