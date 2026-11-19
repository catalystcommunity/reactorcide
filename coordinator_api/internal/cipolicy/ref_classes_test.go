package cipolicy

import (
	"strings"
	"testing"
)

const refClassPolicy = `version: 1
defaults:
  ci_source: base
  profile: standard
  by_ref_class:
    - ref_classes: {any: [fork]}
      profile: pr-untrusted
    - ref_classes: {any: [pr], none: [fork]}
      profile: pr-trusted
head_ci:
  - id: backend
    actors:
      any: [repository_write]
    workflows: [backend-tests]
    paths: [.reactorcide/jobs/backend/**]
    ref_classes:
      any: [pr]
      none: [fork]
    use:
      ci_source: head
      profile: pr-untrusted
      workers: default
`

func TestRefClassConditionOnRule(t *testing.T) {
	policy, err := ParseDocument([]byte(refClassPolicy))
	if err != nil {
		t.Fatal(err)
	}
	facts := Facts{WorkflowID: "backend-tests", ChangedCIPaths: []string{".reactorcide/jobs/backend/x.yaml"},
		HeadRepositoryRelation: "same", ActorSubjects: map[string]bool{"repository_write": true}}
	facts.RefClasses = []string{"pr"}
	if decision, err := Decide(policy, facts); err != nil || !decision.Allowed {
		t.Fatalf("a pr must match: %+v %v", decision, err)
	}
	facts.RefClasses = []string{"fork", "pr"}
	if decision, err := Decide(policy, facts); err != nil || decision.Allowed {
		t.Fatalf("a fork pr must not match: %+v %v", decision, err)
	}
	facts.RefClasses = nil
	if decision, err := Decide(policy, facts); err != nil || decision.Allowed {
		t.Fatalf("no classes must not match a rule that needs pr: %+v %v", decision, err)
	}
}

func TestDefaultProfileByRefClass(t *testing.T) {
	policy, err := ParseDocument([]byte(refClassPolicy))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{"pr-untrusted": {"fork", "pr"}, "pr-trusted": {"pr"}, "standard": {"default"}}
	for want, classes := range cases {
		if got, _ := policy.DefaultProfileFor(classes); got != want {
			t.Fatalf("DefaultProfileFor(%v) = %q, want %q", classes, got, want)
		}
	}
	if got, index := policy.DefaultProfileFor(nil); got != "standard" || index != -1 {
		t.Fatalf("no classes: %q %d", got, index)
	}
	var nilPolicy *Policy
	if got, _ := nilPolicy.DefaultProfileFor([]string{"pr"}); got != "standard" {
		t.Fatalf("nil policy: %q", got)
	}
}

// A policy without class conditions keeps the revision it had before ref
// classes existed, so stored policies and pinned evaluations do not change.
func TestRefClassesDoNotChangeOldRevision(t *testing.T) {
	policy, err := ParseDocument([]byte(validPolicy))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalDocument(policy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(canonical), "ref_classes") || strings.Contains(string(canonical), "by_ref_class") {
		t.Fatalf("canonical form of an old policy names ref classes: %s", canonical)
	}
	withClasses, err := ParseDocument([]byte(refClassPolicy))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err = CanonicalDocument(withClasses)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(canonical), `"ref_classes":{"any":["pr"],"none":["fork"]}`) {
		t.Fatalf("canonical form lacks the rule condition: %s", canonical)
	}
	// Order of the class lists does not change the revision.
	reordered := strings.Replace(refClassPolicy, "{any: [pr], none: [fork]}", "{none: [fork], any: [pr]}", 1)
	again, err := ParseDocument([]byte(reordered))
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != withClasses.Revision {
		t.Fatal("equivalent class conditions must give the same revision")
	}
}

func TestRefClassPolicyValidation(t *testing.T) {
	for name, doc := range map[string]string{
		"bad class":                     strings.Replace(refClassPolicy, "any: [pr]\n      none", "any: [Bad Class]\n      none", 1),
		"empty default entry":           strings.Replace(refClassPolicy, "ref_classes: {any: [fork]}", "ref_classes: {}", 1),
		"default entry without profile": strings.Replace(refClassPolicy, "      profile: pr-trusted\n", "", 1),
		"unknown key":                   strings.Replace(refClassPolicy, "none: [fork]\n    use", "none: [fork]\n      all: [x]\n    use", 1),
	} {
		if _, err := ParseDocument([]byte(doc)); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}
