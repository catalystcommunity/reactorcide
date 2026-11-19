package cipolicy

import "testing"

func principalPolicy(t *testing.T, actors string) *Policy {
	t.Helper()
	input := `version: 1
defaults: {ci_source: base, profile: standard}
head_ci:
- id: principals
  actors: {any: ` + actors + `}
  workflows: [backend-tests]
  paths: [.reactorcide/jobs/backend/**]
  use: {ci_source: head, profile: pr-untrusted, workers: default}`
	policy, err := ParseDocument([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func decideFor(t *testing.T, policy *Policy, actors map[string]bool) Decision {
	t.Helper()
	decision, err := Decide(policy, Facts{WorkflowID: "backend-tests", ChangedCIPaths: []string{".reactorcide/jobs/backend/x.yaml"},
		HeadRepositoryRelation: "same", ActorSubjects: actors})
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

func TestPrincipalSubjectsInPolicy(t *testing.T) {
	policy := principalPolicy(t, `["principal:0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", "service:acme/deployer", "principal_kind:service"]`)
	if !decideFor(t, policy, map[string]bool{"principal:0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b": true}).Allowed {
		t.Fatal("a principal subject must match")
	}
	if !decideFor(t, policy, map[string]bool{"principal_kind:service": true}).Allowed {
		t.Fatal("a principal kind is an opt-in fact")
	}
	if decideFor(t, policy, map[string]bool{"principal_kind:human": true}).Allowed {
		t.Fatal("another kind must not match")
	}
}

func TestAnyoneSubjectInPolicy(t *testing.T) {
	policy := principalPolicy(t, `["*"]`)
	if !decideFor(t, policy, map[string]bool{"vcs_user:github/someone": true}).Allowed {
		t.Fatal("* must match an identified actor")
	}
	if decideFor(t, policy, map[string]bool{}).Allowed {
		t.Fatal("* must not match an actor with no facts")
	}
}

func TestInvalidPrincipalSubjectsAreRejected(t *testing.T) {
	for _, actors := range []string{`["principal:nope"]`, `["service:Bad Name"]`, `["principal_kind:robot"]`, `["project_owner"]`} {
		input := `version: 1
defaults: {ci_source: base, profile: standard}
head_ci:
- id: bad
  actors: {any: ` + actors + `}
  workflows: [backend-tests]
  paths: [.reactorcide/jobs/backend/**]
  use: {ci_source: head, profile: pr-untrusted, workers: default}`
		if _, err := ParseDocument([]byte(input)); err == nil {
			t.Fatalf("actors %s must fail validation", actors)
		}
	}
}
