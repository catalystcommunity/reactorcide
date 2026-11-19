package diagnosis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/ratelimit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcsreport"
)

func TestSanitizeMarkdownRemovesHTMLImagesAndMentions(t *testing.T) {
	in := "Hi @octocat and @acme/core-team, see <img src=x onerror=alert(1)> and <script>x</script>.\n" +
		"![chart](https://example.com/a.png)\n" +
		"<!-- reactorcide:workflow:ci:end -->\n" +
		"mail me at dev@example.com\n" +
		"`@inline` stays, `<b>` stays\n" +
		"```\n@incode <tag> /reactorcide retry\n<!-- x -->\n```\n" +
		"\u202eevil\u0007"
	out := SanitizeMarkdown(in)
	for _, bad := range []string{"<img", "<script", "![", "<!--", "-->", "@octocat", "@acme/core-team", "@example", "@inline", "@incode",
		"<b>", "<tag>", "\u202e", "\u0007"} {
		if strings.Contains(out, bad) {
			t.Fatalf("sanitized text still has %q:\n%s", bad, out)
		}
	}
	// Code keeps its visible text: only zero-width spaces are added.
	for _, kept := range []string{"@\u200boctocat", "@\u200bacme/core-team", "[chart](https://example.com/a.png)",
		"dev@\u200bexample.com", "`@\u200binline`", "`<\u200bb>`", "@\u200bincode <\u200btag> /reactorcide retry", "&lt;img"} {
		if !strings.Contains(out, kept) {
			t.Fatalf("sanitized text lost %q:\n%s", kept, out)
		}
	}
	if strings.ReplaceAll(out, "\u200b", "") == out {
		t.Fatal("no zero-width space")
	}
}

func TestSanitizeMarkdownAdjacentMentions(t *testing.T) {
	out := SanitizeMarkdown("@a @b,@c (@d)")
	if strings.Count(out, "@\u200b") != 4 {
		t.Fatalf("every mention is neutralized: %q", out)
	}
}

// pings reports whether rendered Markdown text can still mention a user: an
// "@" directly before a login character. Text in code also counts, because
// the sanitizer and GitHub can disagree about where code is.
func pings(out string) bool {
	for i := 0; i+1 < len(out); i++ {
		if out[i] == '@' {
			c := out[i+1]
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
				return true
			}
		}
	}
	return false
}

// Each bypass of the first sanitizer from the wave 4 review.
func TestSanitizeMarkdownBypasses(t *testing.T) {
	cases := map[string]string{
		// A backtick fence info string cannot hold a backtick: this is not
		// a fence, so the next line is text.
		"backtick in the info string": "```x`y\n@admin <img src=x>",
		// Four spaces after a blank line is an indented code block, not a
		// fence: the next line is text.
		"indented fence":         "text\n\n    ```\n@admin <img src=x>",
		"tab-indented fence":     "\t```\n@admin <img src=x>",
		"emphasis around a team": "_@org/team_ and *@org/team*",
		"unclosed backtick":      "`code @admin",
		"numeric reference":      "&#64;admin &#60;img src=x&#62;",
		"named reference":        "&commat;admin &lt;img src=x&gt;",
		"tilde fence then text":  "~~~\ncode\n~~~\n@admin",
		"longer closing fence":   "````\ncode\n```\n@admin\n````\n@admin",
		"list item fence":        "- ```\n  code\n  ```\n@admin <img src=x>",
		"block quote fence":      "> ```\n> code\n@admin <img src=x>",
		"double backtick span":   "``a ` @admin``",
	}
	for name, in := range cases {
		out := SanitizeMarkdown(in)
		if pings(out) {
			t.Errorf("%s: a mention is left:\n%s", name, out)
		}
		if strings.Contains(out, "<img") || strings.Contains(out, "&#") || strings.Contains(out, "&commat;") {
			t.Errorf("%s: HTML or a character reference is left:\n%s", name, out)
		}
	}
}

func TestSanitizeMarkdownBacklinksAndAmpersands(t *testing.T) {
	out := SanitizeMarkdown("Fixes #123 and GH-45, see a&b. `#7 &x`")
	for _, bad := range []string{"#123", "GH-45", "a&b", "#7"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q is left: %q", bad, out)
		}
	}
	for _, kept := range []string{"#\u200b123", "GH-\u200b45", "a&amp;b", "#\u200b7"} {
		if !strings.Contains(out, kept) {
			t.Fatalf("%q is lost: %q", kept, out)
		}
	}
}

func TestFenceRules(t *testing.T) {
	opens := map[string]bool{
		"```":      true,
		"   ```go": true,
		"    ```":  false,
		"\t```":    false,
		"``":       false,
		"```x`y":   false,
		"~~~x`y":   true,
		"~~~~":     true,
	}
	for line, want := range opens {
		if _, ok := fenceOpen(line); ok != want {
			t.Errorf("fenceOpen(%q) = %v, want %v", line, ok, want)
		}
	}
	f, _ := fenceOpen("````")
	for line, want := range map[string]bool{"````": true, "`````  ": true, "```": false, "   ````": true, "    ````": false, "```` x": false, "~~~~": false} {
		if got := f.closes(line); got != want {
			t.Errorf("closes(%q) = %v, want %v", line, got, want)
		}
	}
}

// An unclosed fence is closed at the end of the body, so it does not swallow
// the next section of the comment.
func TestSanitizeMarkdownClosesOpenFence(t *testing.T) {
	out := SanitizeMarkdown("look:\n~~~~\nstack trace")
	if !strings.HasSuffix(out, "\n~~~~") {
		t.Fatalf("the open fence is not closed: %q", out)
	}
	if openFence(out) != "" {
		t.Fatalf("still open: %q", out)
	}
	report := &models.DiagnosisReport{TargetType: "job", Title: "t", Body: "```\nunclosed", AuthorKind: "service", AuthorDisplayName: "bot"}
	_, _, body := CommentSection(report, "build", "")
	if openFence(body) != "" {
		t.Fatalf("the section ends in a fence:\n%s", body)
	}
	if !strings.Contains(body, "_Posted by the") || strings.Index(body, "_Posted by the") < strings.LastIndex(body, "```") {
		t.Fatalf("the footer is inside the code block:\n%s", body)
	}
}

func TestSanitizeInline(t *testing.T) {
	got := SanitizeInline("Bot [x](javascript:1) @team\n<b>*bold*</b>")
	for _, bad := range []string{"\n", "<b>", "@team", " [x](", "*bold*"} {
		if strings.Contains(got, bad) {
			t.Fatalf("inline text has %q: %q", bad, got)
		}
	}
}

func TestInputValidate(t *testing.T) {
	ok := Input{Title: "  Missing\tdependency \n", Body: "The lockfile is stale.\r\n", Confidence: "High", Category: "Dependency",
		Links:           []models.DiagnosisLink{{Name: "docs", URL: "https://example.com/x"}},
		SuggestedShifts: map[string]any{"memory": "8Gi", "timeout": "2h"}}
	clean, err := ok.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if clean.Title != "Missing dependency" || clean.Body != "The lockfile is stale." || clean.Confidence != "high" || clean.Category != "dependency" {
		t.Fatalf("clean = %+v", clean)
	}
	if clean.SuggestedShifts == nil || clean.SuggestedShifts.MemoryLimit != "8Gi" || clean.SuggestedShifts.TimeoutSeconds != 7200 {
		t.Fatalf("shifts = %+v", clean.SuggestedShifts)
	}

	cases := map[string]Input{
		"no title":           {Title: " "},
		"long title":         {Title: strings.Repeat("x", MaxTitleRunes+1)},
		"body over the cap":  {Title: "t", Body: strings.Repeat("x", MaxBodyBytes+1)},
		"body grows too big": {Title: "t", Body: strings.Repeat("<", MaxBodyBytes/2)},
		"bad confidence":     {Title: "t", Confidence: "certain"},
		"bad category":       {Title: "t", Category: "has space"},
		"javascript link":    {Title: "t", Links: []models.DiagnosisLink{{Name: "x", URL: "javascript:alert(1)"}}},
		"user info link":     {Title: "t", Links: []models.DiagnosisLink{{Name: "x", URL: "https://u:p@example.com"}}},
		"empty link name":    {Title: "t", Links: []models.DiagnosisLink{{Name: " ", URL: "https://example.com"}}},
		"too many links":     {Title: "t", Links: make([]models.DiagnosisLink, MaxLinks+1)},
		"image shift":        {Title: "t", SuggestedShifts: map[string]any{"image": "evil:latest"}},
		"bad memory":         {Title: "t", SuggestedShifts: map[string]any{"memory": "lots"}},
	}
	for name, in := range cases {
		if _, err := in.Validate(); err == nil || !IsValidationError(err) {
			t.Fatalf("%s: want a validation error, got %v", name, err)
		}
	}
}

func sampleReport() *models.DiagnosisReport {
	job := "job-1"
	shifts := models.RunShifts{MemoryLimit: "8Gi"}
	return &models.DiagnosisReport{TargetType: models.DiagnosisTargetJob, JobID: &job, AuthorPrincipalID: "p-1",
		AuthorKind: "service", AuthorDisplayName: "Diag @Bot", Title: "OOM in <tests>", Body: "Use more memory. cc @alice",
		Confidence: "medium", Category: "oom", SuggestedShifts: &shifts,
		Links: []models.DiagnosisLink{{Name: "run (1)", URL: "https://example.com/a(b)"}}}
}

func TestCommentSectionLabelsTheAuthorAndRendersInTheReport(t *testing.T) {
	report := sampleReport()
	title, status, body := CommentSection(report, "build", "https://ci.example.com/app/jobs/job-1")
	if !strings.Contains(title, "Diagnosis of build") || strings.Contains(title, "<tests>") {
		t.Fatalf("title = %q", title)
	}
	if status != "by Diag @\u200bBot (service)" {
		t.Fatalf("status = %q", status)
	}
	for _, want := range []string{"**Confidence:** medium", "`oom`", "@\u200balice", "`memory=8Gi`",
		"[run (1)](https://example.com/a%28b%29)", "service principal Diag @\u200bBot"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body has no %q:\n%s", want, body)
		}
	}
	rendered := vcsreport.Render([]vcsreport.Entry{{Key: ReportEntryKey(report), Title: title, Status: status, Body: body, Generation: 1}}, 1, true)
	if !strings.Contains(rendered, "### "+title+" — "+status) {
		t.Fatalf("the report has the section heading:\n%s", rendered)
	}
	if strings.Count(rendered, ":begin -->") != 1 || strings.Count(rendered, ":end -->") != 1 {
		t.Fatalf("the section has one marker pair:\n%s", rendered)
	}
}

func TestCommentSectionTruncatesAndClosesAFence(t *testing.T) {
	report := sampleReport()
	report.Body = "start\n```\n" + strings.Repeat("line of log output\n", 400)
	_, _, body := CommentSection(report, "build", "https://ci.example.com/app/jobs/job-1")
	if openFence(body) != "" {
		t.Fatalf("a cut body closes its code block:\n%s", body)
	}
	if !strings.Contains(body, "[Read all of it](https://ci.example.com/app/jobs/job-1)") {
		t.Fatal("a cut body links to the full entry")
	}
	if len([]rune(body)) > MaxCommentBodyRunes+1000 {
		t.Fatalf("the comment body is bounded: %d runes", len([]rune(body)))
	}
}

// fakeStore is an in-memory DiagnosisStore with job and workflow readers.
type fakeStore struct {
	reports    map[string]*models.DiagnosisReport
	jobs       map[string]*models.Job
	workflows  map[string]*models.WorkflowInstance
	nodes      map[string][]models.WorkflowNode
	principals map[string]*models.Principal
	audits     int
}

func newFake() *fakeStore {
	wf := "wf-1"
	node := "node-1"
	return &fakeStore{
		reports: map[string]*models.DiagnosisReport{},
		jobs: map[string]*models.Job{
			"job-1": {JobID: "job-1", OrgID: "org-a", Name: "build", WorkflowID: &wf, WorkflowNodeID: &node},
		},
		workflows: map[string]*models.WorkflowInstance{"wf-1": {WorkflowID: "wf-1", OrgID: "org-a", Name: "ci"}},
		nodes: map[string][]models.WorkflowNode{"wf-1": {
			{NodeID: "node-1", WorkflowID: "wf-1", Name: "build", JobID: strPtr("job-1")},
			{NodeID: "node-2", WorkflowID: "wf-1", Name: "lint"},
		}},
		principals: map[string]*models.Principal{
			"svc":      {PrincipalID: "svc", Kind: "service", Name: "diag", DisplayName: "Diag Bot", Status: "active"},
			"disabled": {PrincipalID: "disabled", Kind: "service", Name: "old", Status: models.PrincipalStatusDisabled},
		},
	}
}

func strPtr(s string) *string { return &s }

func (f *fakeStore) key(r *models.DiagnosisReport) string {
	if r.TargetType == models.DiagnosisTargetJob {
		return "job:" + *r.JobID + ":" + r.AuthorPrincipalID
	}
	return "wf:" + *r.WorkflowID + ":" + r.AuthorPrincipalID
}

func (f *fakeStore) UpsertDiagnosisReport(ctx context.Context, r *models.DiagnosisReport) (bool, error) {
	key := f.key(r)
	if existing, ok := f.reports[key]; ok {
		r.DiagnosisID, r.Revision, r.CreatedAt = existing.DiagnosisID, existing.Revision+1, existing.CreatedAt
		copy := *r
		f.reports[key] = &copy
		return false, nil
	}
	r.DiagnosisID, r.Revision, r.CreatedAt = "d-"+key, 1, time.Now()
	copy := *r
	f.reports[key] = &copy
	return true, nil
}

func (f *fakeStore) ListDiagnosisReportsForJob(ctx context.Context, jobID string) ([]models.DiagnosisReport, error) {
	var out []models.DiagnosisReport
	for _, r := range f.reports {
		if r.JobID != nil && *r.JobID == jobID {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *fakeStore) ListDiagnosisReportsForWorkflow(ctx context.Context, workflowID string) ([]models.DiagnosisReport, error) {
	var out []models.DiagnosisReport
	for _, r := range f.reports {
		if r.WorkflowID != nil && *r.WorkflowID == workflowID {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *fakeStore) GetJobByID(ctx context.Context, id string) (*models.Job, error) {
	if job, ok := f.jobs[id]; ok {
		return job, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) GetWorkflowInstance(ctx context.Context, id string) (*models.WorkflowInstance, error) {
	if wf, ok := f.workflows[id]; ok {
		return wf, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) ListWorkflowNodes(ctx context.Context, id string) ([]models.WorkflowNode, error) {
	return f.nodes[id], nil
}

func (f *fakeStore) GetPrincipal(ctx context.Context, id string) (*models.Principal, error) {
	if p, ok := f.principals[id]; ok {
		return p, nil
	}
	return nil, store.ErrNotFound
}

func TestServicePostReplacesTheEntryOfThePrincipal(t *testing.T) {
	SetRateLimiter(nil)
	fake := newFake()
	var published []*models.DiagnosisReport
	service := NewService(fake, func(ctx context.Context, target *Resolved, report *models.DiagnosisReport) error {
		published = append(published, report)
		return errors.New("a publish failure does not fail the post")
	})
	if !service.Available() {
		t.Fatal("the fake has every reader")
	}
	ctx := context.Background()

	target, err := service.Resolve(ctx, Target{WorkflowID: "wf-1", NodeName: "build"})
	if err != nil {
		t.Fatal(err)
	}
	if target.TargetType != models.DiagnosisTargetJob || target.Job.JobID != "job-1" || target.NodeName != "build" {
		t.Fatalf("a node resolves to its job: %+v", target)
	}
	first, created, err := service.Post(ctx, target, "svc", Input{Title: "first"})
	if err != nil || !created || first.Revision != 1 {
		t.Fatalf("first post: created=%v err=%v report=%+v", created, err, first)
	}
	if first.AuthorKind != "service" || first.AuthorDisplayName != "Diag Bot" || first.WorkflowID == nil || *first.WorkflowID != "wf-1" {
		t.Fatalf("the entry records its author and workflow: %+v", first)
	}

	byJob, err := service.Resolve(ctx, Target{JobID: "job-1"})
	if err != nil || byJob.NodeName != "build" || byJob.Workflow == nil {
		t.Fatalf("a job target finds its node: %+v %v", byJob, err)
	}
	second, created, err := service.Post(ctx, byJob, "svc", Input{Title: "second"})
	if err != nil || created || second.Revision != 2 || second.DiagnosisID != first.DiagnosisID {
		t.Fatalf("a second post replaces: created=%v err=%v report=%+v", created, err, second)
	}
	entries, _ := service.List(ctx, byJob)
	if len(entries) != 1 || entries[0].Title != "second" {
		t.Fatalf("one entry for each principal: %+v", entries)
	}
	if len(published) != 2 {
		t.Fatalf("each post publishes: %d", len(published))
	}

	wfTarget, err := service.Resolve(ctx, Target{WorkflowID: "wf-1"})
	if err != nil || wfTarget.TargetType != models.DiagnosisTargetWorkflow {
		t.Fatalf("a workflow target: %+v %v", wfTarget, err)
	}
	if _, _, err := service.Post(ctx, wfTarget, "svc", Input{Title: "workflow"}); err != nil {
		t.Fatal(err)
	}
	all, _ := service.List(ctx, wfTarget)
	if len(all) != 2 {
		t.Fatalf("a workflow lists its own and its job entries: %+v", all)
	}

	if _, err := service.Resolve(ctx, Target{WorkflowID: "wf-1", NodeName: "lint"}); !IsValidationError(err) {
		t.Fatalf("a node with no job is refused: %v", err)
	}
	if _, err := service.Resolve(ctx, Target{WorkflowID: "wf-1", NodeName: "nope"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an unknown node is not found: %v", err)
	}
	if _, err := service.Resolve(ctx, Target{JobID: "job-1", WorkflowID: "wf-1"}); !IsValidationError(err) {
		t.Fatalf("two targets are refused: %v", err)
	}
	if _, _, err := service.Post(ctx, byJob, "", Input{Title: "x"}); !errors.Is(err, ErrNoPrincipal) {
		t.Fatalf("no author principal: %v", err)
	}
	if _, _, err := service.Post(ctx, byJob, "disabled", Input{Title: "x"}); !errors.Is(err, ErrNoPrincipal) {
		t.Fatalf("a disabled author: %v", err)
	}
}

func TestServicePostRateLimit(t *testing.T) {
	limiter := ratelimit.New(60, 2)
	SetRateLimiter(limiter)
	defer SetRateLimiter(nil)
	fake := newFake()
	service := NewService(fake, nil)
	ctx := context.Background()
	target, _ := service.Resolve(ctx, Target{JobID: "job-1"})
	for i := 0; i < 2; i++ {
		if _, _, err := service.Post(ctx, target, "svc", Input{Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := service.Post(ctx, target, "svc", Input{Title: "t"})
	var limited *RateLimitError
	if !errors.As(err, &limited) || limited.Wait <= 0 {
		t.Fatalf("the third post in the burst is limited: %v", err)
	}
	fake.principals["other"] = &models.Principal{PrincipalID: "other", Kind: "service", Status: "active"}
	if _, _, err := service.Post(ctx, target, "other", Input{Title: "t"}); err != nil {
		t.Fatalf("the limit is for each principal: %v", err)
	}
}
