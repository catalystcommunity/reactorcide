package reporting

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/codeowners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/lib/pq"
)

func strPtr(value string) *string { return &value }

func TestResolveReportingSettingsInherits(t *testing.T) {
	defaults := models.ResolveReportingSettings(nil, nil)
	if defaults.ReportMode != models.ReportModeFull || defaults.CodeownersImport != models.CodeownersImportOff ||
		defaults.PathAcceptance != models.PathAcceptanceOff || defaults.CodeownersCacheTTLSeconds != 600 || defaults.ReportModeSource != "default" {
		t.Fatalf("defaults %+v", defaults)
	}
	org := models.DefaultOrgReportingSettings("org")
	org.ReportMode = models.ReportModeStatusesOnly
	org.AgreementContexts = pq.StringArray{"ci/build"}
	project := &models.ProjectReportingSettings{ReportMode: strPtr(models.ReportModeSilent)}
	got := models.ResolveReportingSettings(org, project)
	if got.ReportMode != models.ReportModeSilent || got.ReportModeSource != "project" {
		t.Fatalf("project mode %+v", got)
	}
	if len(got.AgreementContexts) != 1 || got.AgreementContextsSource != "org" {
		t.Fatalf("org contexts %+v", got)
	}
	empty := pq.StringArray{}
	project.AgreementContexts = &empty
	if got := models.ResolveReportingSettings(org, project); len(got.AgreementContexts) != 0 || got.AgreementContextsSource != "project" {
		t.Fatalf("an empty project list is a value: %+v", got)
	}
}

func TestModeAllows(t *testing.T) {
	if !AllowsStatuses(models.ReportModeFull) || !AllowsComments(models.ReportModeFull) {
		t.Fatal("full")
	}
	if !AllowsStatuses(models.ReportModeStatusesOnly) || AllowsComments(models.ReportModeStatusesOnly) {
		t.Fatal("statuses_only")
	}
	if AllowsStatuses(models.ReportModeSilent) || AllowsComments(models.ReportModeSilent) {
		t.Fatal("silent")
	}
	if Mode(context.Background(), struct{}{}, "org", "project") != models.ReportModeFull {
		t.Fatal("a store without settings is full")
	}
}

func TestNormalizeAgreementContexts(t *testing.T) {
	got, err := NormalizeAgreementContexts([]string{" ci/build ", "ci/build", "", "test (*"})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := NormalizeAgreementContexts([]string{"Reactorcide Checks"}); err == nil {
		t.Fatal("an own context must be refused")
	}
	if _, err := NormalizeAgreementContexts([]string{"ci/[build"}); err == nil {
		t.Fatal("a bad glob must be refused")
	}
}

func TestNormalizePathOwners(t *testing.T) {
	rules, err := NormalizePathOwners([]models.PathOwnerRule{{Pattern: " /payments/ ", Owners: []string{"reactorcide_group:payments", "reactorcide_group:payments"}},
		{Pattern: "/docs/", Owners: nil}})
	if err != nil || len(rules) != 2 || len(rules[0].Owners) != 1 || rules[0].Pattern != "/payments/" {
		t.Fatalf("rules %+v %v", rules, err)
	}
	for _, bad := range []models.PathOwnerRule{
		{Pattern: "!x", Owners: []string{"reactorcide_group:a"}},
		{Pattern: "/a/", Owners: []string{"*"}},
		{Pattern: "/a/", Owners: []string{"repository_write"}},
		{Pattern: "a b", Owners: []string{"reactorcide_group:a"}},
		{Pattern: "", Owners: nil},
	} {
		if _, err := NormalizePathOwners([]models.PathOwnerRule{bad}); err == nil {
			t.Errorf("rule %+v must be refused", bad)
		}
	}
}

func TestNormalizeStates(t *testing.T) {
	cases := map[[2]string]string{
		{"completed", "success"}: "success", {"completed", "neutral"}: "success", {"completed", "skipped"}: "success",
		{"completed", "failure"}: "failure", {"completed", "timed_out"}: "failure", {"completed", "cancelled"}: "failure",
		{"in_progress", ""}: "pending", {"queued", ""}: "pending", {"completed", "stale"}: "pending",
	}
	for in, want := range cases {
		if got := NormalizeCheckState(in[0], in[1]); got != want {
			t.Errorf("%v: got %s want %s", in, got, want)
		}
	}
	if NormalizeStatusState("error") != "failure" || NormalizeStatusState("pending") != "pending" || NormalizeStatusState("success") != "success" {
		t.Fatal("status states")
	}
	if r, ok := ReactorcideResult("cancelled"); !ok || r != "failure" {
		t.Fatal("cancelled is a failure")
	}
	if _, ok := ReactorcideResult("pending"); ok {
		t.Fatal("pending is not final")
	}
}

func agreementRow(result string, completed time.Time) models.CIAgreement {
	return models.CIAgreement{ProjectID: "p", CommitSHA: "sha", ReactorcideResult: result, ReactorcideCompletedAt: completed,
		Status: models.AgreementStatusPending}
}

func TestObserveLateOtherCI(t *testing.T) {
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	patterns := []string{"ci/build", "lint"}
	row := agreementRow(models.AgreementResultFailure, start)

	// Nothing yet.
	row = Observe(row, nil, patterns, start.Add(time.Second))
	if row.Status != models.AgreementStatusPending || row.NextCheckAt == nil || row.Attempts != 1 {
		t.Fatalf("no results yet: %+v", row)
	}
	// One context done, one missing: still pending (not all present).
	row = Observe(row, []models.AgreementOtherResult{{Name: "ci/build", Kind: "status", State: "success"}}, patterns, start.Add(2*time.Minute))
	if row.Status != models.AgreementStatusPending || row.OtherResult != "success" || row.Agree != nil {
		t.Fatalf("partial: %+v", row)
	}
	if delay := row.NextCheckAt.Sub(start.Add(2 * time.Minute)); delay != 2*time.Minute {
		t.Fatalf("backoff after 2 reads = %s", delay)
	}
	// Both final, one failure: other CI failed, agrees with Reactorcide.
	row = Observe(row, []models.AgreementOtherResult{{Name: "ci/build", Kind: "status", State: "success"},
		{Name: "lint", Kind: "check", State: "failure"}}, patterns, start.Add(10*time.Minute))
	if row.Status != models.AgreementStatusComplete || row.Agree == nil || !*row.Agree || row.OtherResult != "failure" || row.NextCheckAt != nil {
		t.Fatalf("complete: %+v", row)
	}
}

func TestObserveWindowCloses(t *testing.T) {
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	row := Observe(agreementRow(models.AgreementResultSuccess, start), nil, []string{"ci/*"}, start.Add(AgreementWindow))
	if row.Status != models.AgreementStatusNoOther || row.NextCheckAt != nil {
		t.Fatalf("no other: %+v", row)
	}
	row = Observe(agreementRow(models.AgreementResultSuccess, start), []models.AgreementOtherResult{
		{Name: "ci/a", Kind: "status", State: "failure"}, {Name: "ci/b", Kind: "status", State: "pending"}}, []string{"ci/*"}, start.Add(AgreementWindow))
	if row.Status != models.AgreementStatusComplete || row.Agree == nil || *row.Agree {
		t.Fatalf("window closed with a failure: %+v", row)
	}
	row = ObserveError(agreementRow(models.AgreementResultSuccess, start), errors.New("boom"), start.Add(time.Minute))
	if row.Status != models.AgreementStatusPending || row.LastError != "boom" || row.NextCheckAt == nil {
		t.Fatalf("error: %+v", row)
	}
}

func TestSelectOtherResultsGlobAndOwnContexts(t *testing.T) {
	got := SelectOtherResults([]models.AgreementOtherResult{
		{Name: "build (ubuntu, 3.11)", Kind: "check", State: "success"},
		{Name: "build (ubuntu, 3.11)", Kind: "check", State: "failure"},
		{Name: "Reactorcide Checks", Kind: "status", State: "success"},
		{Name: "deploy", Kind: "status", State: "success"},
	}, []string{"build (*", "Reactorcide*"})
	if len(got) != 1 || got[0].State != "success" {
		t.Fatalf("got %+v", got)
	}
}

func TestNextDelayCaps(t *testing.T) {
	if NextDelay(1) != time.Minute || NextDelay(3) != 4*time.Minute || NextDelay(50) != time.Hour {
		t.Fatalf("delays %s %s %s", NextDelay(1), NextDelay(3), NextDelay(50))
	}
}

func TestSummarize(t *testing.T) {
	yes, no := true, false
	summary := Summarize([]models.CIAgreement{
		{Status: models.AgreementStatusComplete, Agree: &yes, ReactorcideResult: "success"},
		{Status: models.AgreementStatusComplete, Agree: &no, ReactorcideResult: "failure", CommitSHA: "a"},
		{Status: models.AgreementStatusComplete, Agree: &no, ReactorcideResult: "success", CommitSHA: "b"},
		{Status: models.AgreementStatusPending},
		{Status: models.AgreementStatusNoOther},
	})
	if summary.Total != 5 || summary.Compared != 3 || summary.Agreed != 1 || summary.Disagreed != 2 || summary.Pending != 1 ||
		summary.NoOther != 1 || summary.FalseFailures != 1 || summary.MissedFailures != 1 || len(summary.Disagreements) != 2 {
		t.Fatalf("summary %+v", summary)
	}
	if summary.Rate == nil || *summary.Rate < 0.33 || *summary.Rate > 0.34 {
		t.Fatalf("rate %v", summary.Rate)
	}
	if Summarize(nil).Rate != nil {
		t.Fatal("no rate with nothing compared")
	}
}

type fakeReader struct {
	files map[string]string
	calls int
	err   error
}

func (f *fakeReader) GetFileContent(_ context.Context, _, filePath, _ string) ([]byte, bool, error) {
	f.calls++
	if f.err != nil {
		return nil, false, f.err
	}
	content, ok := f.files[filePath]
	return []byte(content), ok, nil
}

func TestCodeownersCacheFirstLocationAndTTL(t *testing.T) {
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	cache := &CodeownersCache{now: func() time.Time { return now }}
	reader := &fakeReader{files: map[string]string{"CODEOWNERS": "* @root-owner\n", "docs/CODEOWNERS": "* @docs-owner\n"}}
	loaded, err := cache.Load(context.Background(), reader, "github", "acme/app", "base1", time.Minute)
	if err != nil || loaded.Path != "CODEOWNERS" || loaded.File.Owners("x")[0].Value != "root-owner" {
		t.Fatalf("loaded %+v %v", loaded, err)
	}
	// .github/CODEOWNERS (missing) then CODEOWNERS: two reads.
	if reader.calls != 2 {
		t.Fatalf("calls %d", reader.calls)
	}
	if _, err := cache.Load(context.Background(), reader, "github", "acme/app", "base1", time.Minute); err != nil || reader.calls != 2 {
		t.Fatalf("cache hit read again: %d", reader.calls)
	}
	now = now.Add(2 * time.Minute)
	if _, err := cache.Load(context.Background(), reader, "github", "acme/app", "base1", time.Minute); err != nil || reader.calls != 4 {
		t.Fatalf("expired entry not read again: %d", reader.calls)
	}
	// Another SHA is another entry.
	if _, err := cache.Load(context.Background(), reader, "github", "acme/app", "base2", time.Minute); err != nil || reader.calls != 6 {
		t.Fatalf("calls %d", reader.calls)
	}
	none := &fakeReader{files: map[string]string{}}
	loaded, err = cache.Load(context.Background(), none, "github", "acme/app", "base3", time.Minute)
	if err != nil || loaded.Path != "" {
		t.Fatalf("no file: %+v %v", loaded, err)
	}
	if _, err := cache.Load(context.Background(), &fakeReader{err: errors.New("403")}, "github", "acme/app", "base4", time.Minute); err == nil {
		t.Fatal("a read error must fail")
	}
	if _, err := cache.Load(context.Background(), reader, "github", "acme/app", "", time.Minute); err == nil {
		t.Fatal("no base SHA must fail")
	}
}

func TestSubjectMapper(t *testing.T) {
	var warned []string
	mapper := SubjectMapper{Provider: "github", Repo: "Acme/app", Warn: func(owner codeowners.Owner, _ string) { warned = append(warned, owner.Value) }}
	ctx := context.Background()
	if got, _ := mapper.Subject(ctx, codeowners.Owner{Kind: codeowners.OwnerUser, Value: "Octocat"}); got != "vcs_user:github/octocat" {
		t.Fatalf("user %s", got)
	}
	if got, _ := mapper.Subject(ctx, codeowners.Owner{Kind: codeowners.OwnerTeam, Value: "acme/Platform"}); got != "vcs_team:Acme/platform" {
		t.Fatalf("team %s", got)
	}
	if got, _ := mapper.Subject(ctx, codeowners.Owner{Kind: codeowners.OwnerTeam, Value: "other/devs"}); got != "vcs_team:other/devs" {
		t.Fatalf("other org team %s", got)
	}
	if _, ok := mapper.Subject(ctx, codeowners.Owner{Kind: codeowners.OwnerEmail, Value: "dev@example.com"}); ok || len(warned) != 1 {
		t.Fatalf("an unlinked email must be ignored with a warning: %v", warned)
	}
}

func TestDerivedReviewsPerOwnerGroup(t *testing.T) {
	file := codeowners.Parse([]byte("* @acme/core\n/payments/ @acme/payments @alice\n/docs/\n*.md dev@example.com\n"))
	ownership := CodeownersMap(context.Background(), file, SubjectMapper{Provider: "github", Repo: "acme/app"})
	groups := GroupFilesByOwners(ownership, []string{"main.go", "payments/api.go", "payments/db.go", "docs/a.go", "README.md"})
	// README.md: the email owner is not linked, so the rule has no owner.
	if len(groups) != 2 {
		t.Fatalf("groups %+v", groups)
	}
	reviews := DerivedReviews(groups)
	ids := []string{reviews[0].ID, reviews[1].ID}
	if !strings.HasPrefix(ids[0], "codeowners-acme-core-") || !strings.HasPrefix(ids[1], "codeowners-acme-payments-alice-") {
		t.Fatalf("ids %v", ids)
	}
	for _, review := range reviews {
		if review.Scope != ScopeCodeowners || len(review.Paths) != 0 || review.RequiredCount() != 1 {
			t.Fatalf("review %+v", review)
		}
		if !models.ValidApprovalLabel(review.ID) {
			t.Fatalf("id %q is not a valid accept label", review.ID)
		}
	}
	if !strings.Contains(reviews[1].Description, "@acme/payments") || !strings.Contains(reviews[1].Description, "/payments/") {
		t.Fatalf("description %q", reviews[1].Description)
	}
	all := AllOwnerGroups(ownership)
	if len(all) != 2 {
		t.Fatalf("all groups %+v", all)
	}
}

func TestDerivedReviewIDLong(t *testing.T) {
	owners := []string{"vcs_team:acme/" + strings.Repeat("very-long-team-name-", 5), "vcs_user:github/someone"}
	id := DerivedReviewID(owners)
	if len(id) > 63 || !models.ValidApprovalLabel(id) || !strings.HasPrefix(id, CodeownersReviewPrefix) {
		t.Fatalf("id %q", id)
	}
	if DerivedReviewID(owners) != id {
		t.Fatal("the ID must be stable")
	}
}

func TestDerivedReviewsEvaluate(t *testing.T) {
	file := codeowners.Parse([]byte("/payments/ @acme/payments\n"))
	ownership := CodeownersMap(context.Background(), file, SubjectMapper{Provider: "github", Repo: "acme/app"})
	reviews := DerivedReviews(GroupFilesByOwners(ownership, []string{"payments/a.go"}))
	now := time.Now().UTC()
	accept := models.CIApproval{Kind: models.CIApprovalKindAccept, Action: models.CIApprovalActionApproved, HeadSHA: "head",
		Person: "github/bob", Subjects: []string{"vcs_team:acme/payments"}, CreatedAt: now}
	stranger := models.CIApproval{Kind: models.CIApprovalKindAccept, Action: models.CIApprovalActionApproved, HeadSHA: "head",
		Person: "github/eve", Subjects: []string{"repository_write"}, CreatedAt: now}
	results := approvals.EvaluateReviews(reviews, []models.CIApproval{stranger}, "head", nil, approvals.AuthorFacts{}, now)
	if len(results) != 1 || results[0].Satisfied {
		t.Fatalf("a person outside the team must not satisfy: %+v", results)
	}
	results = approvals.EvaluateReviews(reviews, []models.CIApproval{stranger, accept}, "head", nil, approvals.AuthorFacts{}, now)
	if !results[0].Satisfied || results[0].Accepted[0] != "github/bob" {
		t.Fatalf("a team member must satisfy: %+v", results)
	}
	unreadable := UnreadableCodeownersReview("timeout")
	results = approvals.EvaluateReviews([]models.EffectiveRequiredReview{unreadable}, []models.CIApproval{accept}, "head", nil, approvals.AuthorFacts{}, now)
	if results[0].Satisfied {
		t.Fatal("an unreadable code owners file must block")
	}
}

func TestCheckPaths(t *testing.T) {
	ownership := RegistryMap([]models.PathOwnerRule{
		{Pattern: "/payments/", Owners: []string{"reactorcide_group:payments"}},
		{Pattern: "/payments/public/", Owners: nil},
		{Pattern: "/web/", Owners: []string{"reactorcide_group:web"}},
	})
	files := []string{"payments/a.go", "payments/public/x.go", "web/b.ts", "README.md"}
	// The web team owns build; payments is foreign; public and README have no owner.
	decision := CheckPaths(files, "build", []string{"reactorcide_group:web"}, ownership, nil)
	if len(decision.Kept) != 3 || len(decision.Unaccepted) != 1 || decision.Unaccepted[0] != "payments/a.go" {
		t.Fatalf("decision %+v", decision)
	}
	if reason := ForeignReason(decision.Foreign); reason != "paths in /payments/ are not accepted by owner reactorcide_group:payments" {
		t.Fatalf("reason %q", reason)
	}
	// An acceptance of the payments owner for build covers the file.
	index := NewAcceptanceIndex([]models.PathAcceptance{
		{WorkflowSecurityID: "build", PathPattern: "/payments/", OwnerSubject: "reactorcide_group:payments", State: models.PathAcceptanceAccepted},
		{WorkflowSecurityID: "lint", PathPattern: "/web/", OwnerSubject: "reactorcide_group:web", State: models.PathAcceptanceAccepted},
	})
	if got := CheckPaths(files, "build", []string{"reactorcide_group:web"}, ownership, index); len(got.Unaccepted) != 0 {
		t.Fatalf("accepted: %+v", got)
	}
	// A revoked or requested row does not count.
	revoked := time.Now()
	index = NewAcceptanceIndex([]models.PathAcceptance{
		{WorkflowSecurityID: "build", PathPattern: "/payments/", OwnerSubject: "reactorcide_group:payments", State: models.PathAcceptanceRevoked, RevokedAt: &revoked},
		{WorkflowSecurityID: "build", PathPattern: "/payments/", OwnerSubject: "reactorcide_group:payments", State: models.PathAcceptanceRequested},
	})
	if got := CheckPaths(files, "build", []string{"reactorcide_group:web"}, ownership, index); len(got.Unaccepted) != 1 {
		t.Fatalf("revoked must not count: %+v", got)
	}
	// A workflow with no owner is foreign to every owned area.
	if got := CheckPaths(files, "build", nil, ownership, nil); len(got.Unaccepted) != 2 || len(got.Foreign) != 2 {
		t.Fatalf("no owner: %+v", got)
	}
	// An acceptance by another owner of the same pattern does not count.
	index = NewAcceptanceIndex([]models.PathAcceptance{
		{WorkflowSecurityID: "build", PathPattern: "/payments/", OwnerSubject: "reactorcide_group:web", State: models.PathAcceptanceAccepted},
	})
	if got := CheckPaths(files, "build", []string{"reactorcide_group:web"}, ownership, index); len(got.Unaccepted) != 1 {
		t.Fatalf("wrong owner: %+v", got)
	}
}

// Owner sets with the same slug get different IDs, and a team and a user
// with the same joined name are different owner sets.
func TestDerivedReviewIDCollisions(t *testing.T) {
	sets := [][]string{
		{"vcs_team:acme/a-b"},
		{"vcs_team:acme/a.b"},
		{"vcs_team:acme/a_b"},
		{"vcs_team:acme/a", "vcs_user:github/b"},
		{"vcs_team:acme/a-b", "vcs_user:github/x"},
		{"vcs_team:acme/a", "vcs_team:acme/b"},
	}
	seen := map[string][]string{}
	for _, owners := range sets {
		id := DerivedReviewID(owners)
		if !models.ValidApprovalLabel(id) || len(id) > 63 {
			t.Fatalf("id %q of %v is not a valid accept label", id, owners)
		}
		if other, ok := seen[id]; ok {
			t.Fatalf("%v and %v share the ID %q", other, owners, id)
		}
		seen[id] = owners
	}
	// The dedupe of DerivedReviews keys on the owner set, not the ID.
	reviews := DerivedReviews([]OwnerGroup{{Owners: sets[0]}, {Owners: sets[1]}, {Owners: sets[0]}})
	if len(reviews) != 2 {
		t.Fatalf("reviews %+v, want one review for each distinct owner set", reviews)
	}
}
