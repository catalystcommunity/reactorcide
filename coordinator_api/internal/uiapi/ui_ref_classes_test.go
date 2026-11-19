package uiapi

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

const testRefClassPolicy = `version: 1
defaults:
  ci_source: base
  profile: standard
  by_ref_class:
    - ref_classes: {any: [fork]}
      profile: pr-untrusted
head_ci:
  - id: tests
    workflows: [tests]
    paths: [.reactorcide/**]
    ref_classes: {any: [pr], none: [fork]}
    use:
      ci_source: head
      profile: pr-untrusted
      workers: default
`

// The ref class fields of the CI policy survive the typed CSIL wire.
func TestCIPolicyRefClassesRoundTrip(t *testing.T) {
	wire := testPolicyDocument(t, testRefClassPolicy)
	if wire.HeadCi[0].RefClasses == nil || len(wire.HeadCi[0].RefClasses.Any) != 1 {
		t.Fatalf("rule ref_classes missing on the wire: %+v", wire.HeadCi[0])
	}
	if wire.Defaults == nil || len(wire.Defaults.ByRefClass) != 1 {
		t.Fatalf("defaults.by_ref_class missing on the wire: %+v", wire.Defaults)
	}
	back, err := policyDocumentToPolicy(wire)
	if err != nil {
		t.Fatal(err)
	}
	original, err := cipolicy.ParseDocument([]byte(testRefClassPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if back.Revision != original.Revision {
		t.Fatal("the policy revision changed across the wire")
	}
}

func TestRefClassWireConversions(t *testing.T) {
	yes := true
	project := "p1"
	rule := models.RefClassRule{RuleID: "r1", ProjectID: &project, Name: "prod", Position: 2, EventTypes: []string{"push"},
		BranchGlob: "main", IsDefaultBranch: &yes, Classes: []string{"production"}, Enabled: true}
	wire := refClassRuleToWire(rule)
	if wire.Name != "prod" || wire.Position != 2 || *wire.BranchGlob != "main" || !*wire.IsDefaultBranch || wire.TagGlob != nil {
		t.Fatalf("rule wire = %+v", wire)
	}
	facts := models.RefClassFacts{Source: "webhook", EventType: "pull_request_opened", Branch: "f", BaseBranch: "main", IsPR: true, IsFork: true}
	if back := refClassFactsFromWire(ptr(refClassFactsToWire(facts))); back != facts {
		t.Fatalf("facts round trip = %+v", back)
	}
	if got := refClassFactsFromWire(nil); got != (models.RefClassFacts{}) {
		t.Fatalf("nil facts = %+v", got)
	}
	grant := &models.SecretGrant{}
	if err := setGrantRefClasses(grant, []string{"production", "default"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(grant.RefClassesAny) != 2 || grant.RefClassesAny[0] != "default" || len(grant.RefClassesNone) != 0 {
		t.Fatalf("grant classes = %v %v", grant.RefClassesAny, grant.RefClassesNone)
	}
	if err := setGrantRefClasses(grant, []string{"Bad Class"}, nil); err == nil {
		t.Fatal("an invalid class must be refused")
	}
}

func ptr[T any](value T) *T { return &value }
