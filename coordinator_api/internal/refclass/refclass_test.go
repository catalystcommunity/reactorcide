package refclass

import (
	"reflect"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func boolPtr(v bool) *bool { return &v }

func TestClassifyBuiltins(t *testing.T) {
	cases := []struct {
		name  string
		facts Facts
		want  []string
	}{
		{"default branch push", Facts{Source: SourceWebhook, EventType: "push", Branch: "main", DefaultBranch: "main"}, []string{"default"}},
		{"other branch push", Facts{Source: SourceWebhook, EventType: "push", Branch: "feature/x", DefaultBranch: "main"}, []string{}},
		{"push with unknown default branch", Facts{Source: SourceWebhook, EventType: "push", Branch: "main"}, []string{}},
		{"tag", Facts{Source: SourceWebhook, EventType: "tag_created", Tag: "v1.0.0", DefaultBranch: "main"}, []string{"tag"}},
		{"same repo pr", Facts{Source: SourceWebhook, EventType: "pull_request_opened", Branch: "feature", BaseBranch: "main", DefaultBranch: "main"}, []string{"pr"}},
		{"fork pr", Facts{Source: SourceWebhook, EventType: "pull_request_updated", Branch: "feature", BaseBranch: "main", IsFork: true}, []string{"fork", "pr"}},
		{"fork flag without pr is ignored", Facts{Source: SourceWebhook, EventType: "push", Branch: "x", IsFork: true}, []string{}},
		{"merged pr", Facts{Source: SourceWebhook, EventType: "pull_request_merged", BaseBranch: "main", DefaultBranch: "main"}, []string{"pr"}},
		{"schedule", Facts{Source: SourceSchedule, EventType: "schedule", Branch: "main"}, []string{"schedule"}},
		{"manual", Facts{Source: SourceManual, EventType: "manual", Branch: "main", DefaultBranch: "main"}, []string{"manual"}},
		{"approval re-evaluation of a pr", Facts{Source: models.CIEventSourceApproval, EventType: "pull_request_updated", IsFork: true}, []string{"fork", "pr"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.facts, nil)
			if !reflect.DeepEqual(got.Classes, tc.want) {
				t.Fatalf("classes = %v, want %v", got.Classes, tc.want)
			}
			if len(got.RuleIDs) != 0 {
				t.Fatalf("rule ids = %v, want none", got.RuleIDs)
			}
		})
	}
}

func TestClassifyRules(t *testing.T) {
	project := "p1"
	rules := []models.RefClassRule{
		{RuleID: "r-prod", Name: "production", Enabled: true, EventTypes: []string{"push"}, IsDefaultBranch: boolPtr(true), Classes: []string{"production"}},
		{RuleID: "r-release", Name: "release", Enabled: true, BranchGlob: "release/*", Classes: []string{"release", "Release"}},
		{RuleID: "r-tag", Name: "semver", Enabled: true, TagGlob: "v*", Classes: []string{"release"}},
		{RuleID: "r-off", Name: "off", Enabled: false, Classes: []string{"never"}},
		{RuleID: "r-trusted-pr", Name: "trusted-pr", ProjectID: &project, Enabled: true, IsPR: boolPtr(true), IsFork: boolPtr(false), BaseBranchGlob: "main", Classes: []string{"test"}},
		{RuleID: "r-all", Name: "all", ProjectID: &project, Enabled: true, Classes: []string{"ci"}},
	}
	cases := []struct {
		name    string
		facts   Facts
		want    []string
		ruleIDs []string
	}{
		{"default push", Facts{Source: SourceWebhook, EventType: "push", Branch: "main", DefaultBranch: "main"}, []string{"ci", "default", "production"}, []string{"r-prod", "r-all"}},
		{"manual on main gets no production", Facts{Source: SourceManual, EventType: "manual", Branch: "main", DefaultBranch: "main"}, []string{"ci", "manual"}, []string{"r-all"}},
		{"release branch push", Facts{Source: SourceWebhook, EventType: "push", Branch: "release/1.2", DefaultBranch: "main"}, []string{"ci", "release"}, []string{"r-release", "r-all"}},
		{"release glob stays in segment", Facts{Source: SourceWebhook, EventType: "push", Branch: "release/1.2/hotfix"}, []string{"ci"}, []string{"r-all"}},
		{"semver tag", Facts{Source: SourceWebhook, EventType: "tag_created", Tag: "v2.0.0"}, []string{"ci", "release", "tag"}, []string{"r-tag", "r-all"}},
		{"branch glob does not match a tag", Facts{Source: SourceWebhook, EventType: "tag_created", Tag: "release/1"}, []string{"ci", "tag"}, []string{"r-all"}},
		{"same repo pr", Facts{Source: SourceWebhook, EventType: "pull_request_opened", Branch: "f", BaseBranch: "main"}, []string{"ci", "pr", "test"}, []string{"r-trusted-pr", "r-all"}},
		{"fork pr does not get test", Facts{Source: SourceWebhook, EventType: "pull_request_opened", Branch: "f", BaseBranch: "main", IsFork: true}, []string{"ci", "fork", "pr"}, []string{"r-all"}},
		{"fork pr head named release/1 gets no release", Facts{Source: SourceWebhook, EventType: "pull_request_opened", HeadBranch: "release/1", BaseBranch: "main", IsFork: true}, []string{"ci", "fork", "pr"}, []string{"r-all"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.facts, rules)
			if !reflect.DeepEqual(got.Classes, tc.want) {
				t.Fatalf("classes = %v, want %v", got.Classes, tc.want)
			}
			if !reflect.DeepEqual(got.RuleIDs, tc.ruleIDs) {
				t.Fatalf("rule ids = %v, want %v", got.RuleIDs, tc.ruleIDs)
			}
		})
	}
}

func TestClassifyReasons(t *testing.T) {
	rules := []models.RefClassRule{{RuleID: "r1", Name: "prod", Enabled: true, Classes: []string{"production"}}}
	got := Classify(Facts{Source: SourceWebhook, EventType: "push", Branch: "main", DefaultBranch: "main"}, rules)
	want := []models.RefClassReason{
		{Class: "default", RuleID: "builtin:default-branch", RuleName: "default-branch", Scope: "builtin"},
		{Class: "production", RuleID: "r1", RuleName: "prod", Scope: "org"},
	}
	if !reflect.DeepEqual(got.Reasons, want) {
		t.Fatalf("reasons = %#v", got.Reasons)
	}
	reason := Reason(got.Classes, got.RuleIDs)
	if reason.Kind != "ref_class" || !reflect.DeepEqual(reason.Classes, []string{"default", "production"}) || !reflect.DeepEqual(reason.RuleIDs, []string{"r1"}) {
		t.Fatalf("decision reason = %#v", reason)
	}
}

func TestSatisfies(t *testing.T) {
	cases := []struct {
		classes, anyOf, noneOf []string
		want                   bool
	}{
		{nil, nil, nil, true},
		{[]string{"pr"}, nil, nil, true},
		{[]string{"default"}, []string{"default", "tag"}, nil, true},
		{[]string{"pr"}, []string{"default"}, nil, false},
		{nil, []string{"default"}, nil, false},
		{[]string{"pr", "fork"}, nil, []string{"fork"}, false},
		{[]string{"default", "production"}, []string{"production"}, []string{"fork"}, true},
		{[]string{"Default"}, []string{"default"}, nil, true},
	}
	for _, tc := range cases {
		if got := Satisfies(tc.classes, tc.anyOf, tc.noneOf); got != tc.want {
			t.Fatalf("Satisfies(%v, %v, %v) = %v, want %v", tc.classes, tc.anyOf, tc.noneOf, got, tc.want)
		}
	}
}

func TestValidateRule(t *testing.T) {
	valid := models.RefClassRule{Name: "prod", Classes: []string{"production"}, EventTypes: []string{"push"}}
	if err := ValidateRule(valid); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []models.RefClassRule{
		{Name: "Bad Name", Classes: []string{"x"}},
		{Name: "empty"},
		{Name: "class", Classes: []string{"Not Valid"}},
		{Name: "event", Classes: []string{"x"}, EventTypes: []string{"pushed"}},
	} {
		if err := ValidateRule(bad); err == nil {
			t.Fatalf("rule %+v must be invalid", bad)
		}
	}
}

func TestNormalizeAndEnv(t *testing.T) {
	if got := Normalize(nil); got == nil || len(got) != 0 {
		t.Fatalf("Normalize(nil) = %#v", got)
	}
	if got := EnvValue([]string{"pr", "fork", "pr", " "}); got != "fork,pr" {
		t.Fatalf("EnvValue = %q", got)
	}
	job := &models.Job{RefClasses: []string{"tag", "default"}}
	if got := ForJob(job); !reflect.DeepEqual(got, []string{"default", "tag"}) {
		t.Fatalf("ForJob = %v", got)
	}
	if got := ForJob(nil); len(got) != 0 {
		t.Fatalf("ForJob(nil) = %v", got)
	}
}

func TestSortRules(t *testing.T) {
	project := "p"
	rules := []models.RefClassRule{
		{Name: "b", ProjectID: &project, Position: 0},
		{Name: "z", Position: 2},
		{Name: "a", Position: 2},
		{Name: "first", Position: 1},
	}
	SortRules(rules)
	var names []string
	for _, rule := range rules {
		names = append(names, rule.Name)
	}
	if !reflect.DeepEqual(names, []string{"first", "a", "z", "b"}) {
		t.Fatalf("order = %v", names)
	}
}

// A manual event states its own ref. A ref condition (branch_glob, tag_glob,
// is_default_branch) applies to it only in a rule that names "manual".
func TestManualEventsIgnoreRefConditions(t *testing.T) {
	rules := []models.RefClassRule{
		{RuleID: "r-branch", Name: "branch", Enabled: true, BranchGlob: "main", Classes: []string{"production"}},
		{RuleID: "r-tag", Name: "tag", Enabled: true, TagGlob: "v*", Classes: []string{"release"}},
		{RuleID: "r-default", Name: "default", Enabled: true, IsDefaultBranch: boolPtr(false), Classes: []string{"nondefault"}},
		{RuleID: "r-manual-main", Name: "manual-main", Enabled: true, EventTypes: []string{"manual"}, BranchGlob: "main", Classes: []string{"manual-main"}},
		{RuleID: "r-any", Name: "any", Enabled: true, Classes: []string{"ci"}},
	}
	manualBranch := Classify(Facts{Source: SourceManual, EventType: "push", Branch: "main", DefaultBranch: "main"}, rules)
	if !reflect.DeepEqual(manualBranch.Classes, []string{"ci", "manual", "manual-main"}) {
		t.Fatalf("manual on main: %v", manualBranch.Classes)
	}
	manualTag := Classify(Facts{Source: SourceManual, EventType: "tag_created", Tag: "v1.0.0"}, rules)
	if !reflect.DeepEqual(manualTag.Classes, []string{"ci", "manual"}) {
		t.Fatalf("manual on a tag: %v", manualTag.Classes)
	}
	push := Classify(Facts{Source: SourceWebhook, EventType: "push", Branch: "main", DefaultBranch: "main"}, rules)
	if !reflect.DeepEqual(push.Classes, []string{"ci", "default", "production"}) {
		t.Fatalf("webhook push on main: %v", push.Classes)
	}
}

// A manual run of a fork pull request keeps "pr" and "fork": the coordinator
// read those facts from the VCS. An API submit that states a pull request
// event type is not a pull request.
func TestManualPullRequestRunKeepsForkClasses(t *testing.T) {
	run := Classify(Facts{Source: SourceManual, EventType: "pull_request_updated", IsPR: true, IsFork: true,
		HeadBranch: "main", BaseBranch: "main"}, nil)
	if !reflect.DeepEqual(run.Classes, []string{"fork", "manual", "pr"}) {
		t.Fatalf("manual fork pr run: %v", run.Classes)
	}
	submit := Classify(Facts{Source: SourceManual, EventType: "pull_request_opened", Branch: "main"}, nil)
	if !reflect.DeepEqual(submit.Classes, []string{"manual"}) {
		t.Fatalf("api submit: %v", submit.Classes)
	}
}
