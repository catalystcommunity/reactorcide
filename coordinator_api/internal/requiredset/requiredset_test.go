package requiredset

import (
	"reflect"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

func entry(id, pattern string) models.RequiredWorkflow {
	return models.RequiredWorkflow{WorkflowSecurityID: id, BranchPattern: pattern}
}

func TestClassify(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	main := Target{Branch: "main", Known: true}
	if got := (Set{}).Classify("anything", main, now); !got.Blocking() {
		t.Fatalf("set off must make every workflow required, got %+v", got)
	}
	set := Set{Enabled: true, Entries: []models.RequiredWorkflow{entry("build", ""), entry("deploy", "release/*")}}
	cases := []struct {
		id     string
		target Target
		state  string
	}{
		{"build", main, StateRequired},
		{"lint", main, StateAdvisory},
		{"deploy", main, StateAdvisory},
		{"deploy", Target{Branch: "release/1.2", Known: true}, StateRequired},
		// An unknown target branch matches every entry (fail closed).
		{"deploy", Target{}, StateRequired},
		// A workflow with no security ID cannot be promoted: it blocks.
		{"", main, StateRequired},
	}
	for _, c := range cases {
		if got := set.Classify(c.id, c.target, now); got.State != c.state {
			t.Errorf("Classify(%q, %+v) = %s, want %s", c.id, c.target, got.State, c.state)
		}
	}
}

func TestClassifyQuarantineExpires(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	until := now.Add(48 * time.Hour)
	quarantined := entry("build", "")
	quarantined.QuarantinedUntil, quarantined.QuarantineReason = &until, "flaky"
	set := Set{Enabled: true, Entries: []models.RequiredWorkflow{quarantined}}
	target := Target{Branch: "main", Known: true}
	got := set.Classify("build", target, now)
	if got.State != StateQuarantined || got.Blocking() || !got.QuarantinedUntil.Equal(until) || got.QuarantineReason != "flaky" {
		t.Fatalf("quarantined = %+v", got)
	}
	if label := got.Label(); label != "quarantined until 2026-11-18 12:00 UTC — not required" {
		t.Fatalf("label = %q", label)
	}
	// At the expiry the entry is required again, with no write.
	if got := set.Classify("build", target, until); got.State != StateRequired {
		t.Fatalf("at expiry = %+v, want required", got)
	}
	// A second entry that is not quarantined keeps the workflow required.
	set.Entries = append(set.Entries, entry("build", "main"))
	if got := set.Classify("build", target, now); got.State != StateRequired {
		t.Fatalf("with an active second entry = %+v", got)
	}
}

func TestTargetOfEvent(t *testing.T) {
	pr := 7
	cases := []struct {
		name  string
		event models.CIEvent
		want  Target
	}{
		{"push", models.CIEvent{Branch: "main"}, Target{Branch: "main", Known: true}},
		{"pull request from facts", models.CIEvent{PRNumber: &pr, Branch: "feature",
			RefClassFacts: &models.RefClassFacts{IsPR: true, HeadBranch: "main", BaseBranch: "release/1"}}, Target{Branch: "release/1", Known: true}},
		{"pull request from the summary", models.CIEvent{PRNumber: &pr, Summary: models.CIEventSummary{BaseRef: "refs/heads/main"}},
			Target{Branch: "main", Known: true}},
		{"pull request with no base", models.CIEvent{PRNumber: &pr, Branch: "main"}, Target{}},
		{"tag", models.CIEvent{RefClassFacts: &models.RefClassFacts{Tag: "v1"}}, Target{Branch: "", Known: true}},
	}
	for _, c := range cases {
		event := c.event
		if got := TargetOfEvent(&event); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestApproversFor(t *testing.T) {
	rules := []models.RequiredSetApproverRule{
		{Branches: []string{"release/*", "hotfix/**"}, Approvers: []string{"reactorcide_group:release"}},
		{Branches: []string{"**"}, Approvers: []string{"reactorcide_group:everyone"}},
	}
	cases := []struct {
		pattern string
		want    []string
		source  string
	}{
		{"release/1.2", []string{"reactorcide_group:release"}, "rule 1"},
		{"release/*", []string{"reactorcide_group:release"}, "rule 1"},
		{"hotfix/a/b", []string{"reactorcide_group:release"}, "rule 1"},
		{"main", []string{"reactorcide_group:everyone"}, "rule 2"},
		{"", []string{"reactorcide_group:everyone"}, "rule 2"},
	}
	for _, c := range cases {
		got, source := ApproversFor([]string{"reactorcide_group:ci"}, rules, c.pattern)
		if !reflect.DeepEqual(got, c.want) || source != c.source {
			t.Errorf("ApproversFor(%q) = %v %s, want %v %s", c.pattern, got, source, c.want, c.source)
		}
	}
	// Without a "**" rule, an entry for every branch uses the setting.
	got, source := ApproversFor([]string{"reactorcide_group:ci"}, rules[:1], "")
	if !reflect.DeepEqual(got, []string{"reactorcide_group:ci"}) || source != "setting" {
		t.Fatalf("every-branch entry = %v %s", got, source)
	}
	// No subject configured: org admins.
	if got, source := ApproversFor(nil, nil, "main"); got != nil || source != "org_admins" {
		t.Fatalf("no setting = %v %s", got, source)
	}
}

func TestEffectiveApproverSettings(t *testing.T) {
	org := &models.OrgRequiredSetSettings{Approvers: []string{"reactorcide_group:org"},
		ApproverRules: []models.RequiredSetApproverRule{{Branches: []string{"main"}, Approvers: []string{"project_owner"}}}}
	approvers, rules := EffectiveApproverSettings(org, &models.ProjectRequiredSetSettings{})
	if !reflect.DeepEqual(approvers, []string{"reactorcide_group:org"}) || len(rules) != 1 {
		t.Fatalf("inherit = %v %v", approvers, rules)
	}
	empty := []models.RequiredSetApproverRule{}
	approvers, rules = EffectiveApproverSettings(org, &models.ProjectRequiredSetSettings{Approvers: []string{"reactorcide_group:p"}, ApproverRules: &empty})
	if !reflect.DeepEqual(approvers, []string{"reactorcide_group:p"}) || len(rules) != 0 {
		t.Fatalf("override = %v %v", approvers, rules)
	}
}

func TestRows(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	until := now.Add(time.Hour)
	quarantined := entry("e2e", "")
	quarantined.QuarantinedUntil = &until
	known := []models.KnownWorkflow{{WorkflowSecurityID: "build", Name: "Build"}, {WorkflowSecurityID: "lint"}, {WorkflowSecurityID: "e2e"}}
	pending := []models.RequiredSetProposal{{WorkflowSecurityID: "new-one", State: models.RequiredSetProposalPending}}
	rows := Rows(true, known, []models.RequiredWorkflow{entry("build", ""), quarantined}, pending, now)
	got := map[string]string{}
	for _, row := range rows {
		got[row.WorkflowSecurityID] = row.State
	}
	want := map[string]string{"build": StateRequired, "e2e": StateQuarantined, "lint": StateAdvisory, "new-one": StateAdvisory}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v", got)
	}
	if rows[0].WorkflowSecurityID != "build" || rows[0].Name != "Build" {
		t.Fatalf("rows are sorted by ID and carry the name: %+v", rows[0])
	}
	off := Rows(false, known, nil, nil, now)
	for _, row := range off {
		if row.State != StateRequired {
			t.Fatalf("set off: %+v", row)
		}
	}
}

func TestParseQuarantineExpiry(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"7d":                   now.Add(7 * 24 * time.Hour),
		"36h":                  now.Add(36 * time.Hour),
		"2026-11-20T00:00:00Z": time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
	}
	for text, want := range cases {
		got, err := ParseExpiryText(text, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParseExpiryText(%q) = %v, %v; want %v", text, got, err, want)
		}
	}
	for _, bad := range []string{"", "soon", "7w"} {
		if _, err := ParseExpiryText(bad, now); err == nil {
			t.Errorf("ParseExpiryText(%q) must fail", bad)
		}
	}
	if _, err := ParseQuarantineExpiry("2026-11-20T00:00:00Z", "7d", now); err == nil {
		t.Error("until and duration together must fail")
	}
}

func TestValidation(t *testing.T) {
	if err := ValidateSecurityID("file:.reactorcide/x.yaml"); err == nil {
		t.Error("an invalid-file key is not a workflow ID")
	}
	if err := ValidateApproverRules([]models.RequiredSetApproverRule{{Branches: []string{"main"}, Approvers: []string{"bogus"}}}); err == nil {
		t.Error("an invalid subject must fail")
	}
	if err := ValidateApproverRules([]models.RequiredSetApproverRule{{Branches: []string{"main"}}}); err == nil {
		t.Error("a rule with no approvers must fail")
	}
	if err := ValidateApproverRules([]models.RequiredSetApproverRule{{Branches: []string{"release/*"}, Approvers: []string{"*", "reactorcide_group:release"}}}); err != nil {
		t.Errorf("valid rule: %v", err)
	}
}

func TestTokenAllowsServicePrincipalTokens(t *testing.T) {
	issued, _ := tokencaps.New(tokencaps.ProjectsRead, tokencaps.PoliciesManage)
	sp := &checkauth.Principal{CredentialType: "service_token", PrincipalID: "p1", IntersectRoles: true,
		OrganizationIDs: []string{"org-1"}, Capabilities: issued}
	sp.RecordIssuedCapabilities()
	// NarrowServicePrincipalToken leaves a non-admin read capabilities only.
	sp.Capabilities, _ = tokencaps.New(tokencaps.ProjectsRead)
	caller := Caller{Identity: authz.Identity{PrincipalID: "p1", Token: sp}}
	if !tokenAllows(caller, "org-1", tokencaps.PoliciesManage) {
		t.Fatal("an issued policies:manage capability must let roles decide")
	}
	if tokenAllows(caller, "org-2", tokencaps.PoliciesManage) {
		t.Fatal("a token must name the organization")
	}
	readOnly, _ := tokencaps.New(tokencaps.ProjectsRead)
	reader := &checkauth.Principal{CredentialType: "service_token", PrincipalID: "p2", IntersectRoles: true,
		OrganizationIDs: []string{"org-1"}, Capabilities: readOnly}
	reader.RecordIssuedCapabilities()
	readerCaller := Caller{Identity: authz.Identity{PrincipalID: "p2", Token: reader}}
	if tokenAllows(readerCaller, "org-1", tokencaps.PoliciesManage) {
		t.Fatal("a token issued without policies:manage must not propose or approve")
	}
	if !tokenAllows(readerCaller, "org-1", tokencaps.ProjectsRead) {
		t.Fatal("a read capability allows view")
	}
	if !tokenAllows(Caller{}, "org-1", tokencaps.PoliciesManage) {
		t.Fatal("a session with no token passes the token check")
	}
}
