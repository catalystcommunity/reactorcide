package vcs

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// checkRunClient records check runs (an App installation client).
type checkRunClient struct {
	recordingClient
	runs []CheckRunUpdate
}

func (c *checkRunClient) PublishCheckRun(_ context.Context, _ string, update CheckRunUpdate) error {
	c.runs = append(c.runs, update)
	return nil
}

// checkRunDetailStore adds workflow nodes and test results to the fake.
type checkRunDetailStore struct {
	*fakeChecksStore
	nodes   map[string][]models.WorkflowNode
	results map[string][]models.TestResult
}

func (s *checkRunDetailStore) ListWorkflowNodes(_ context.Context, workflowID string) ([]models.WorkflowNode, error) {
	return s.nodes[workflowID], nil
}
func (s *checkRunDetailStore) ListJobTestResults(_ context.Context, jobID string, _ []string, _ int) ([]models.TestResult, error) {
	return s.results[jobID], nil
}
func TestPublishEventChecksPostsCheckRunForAppClient(t *testing.T) {
	ctx := context.Background()
	updater, fake, _ := newChecksFixture()
	jobID := "job-1"
	class := "user.test"
	line := 42
	pr := 7
	fake.event.PRNumber = &pr
	fake.jobs["eval-job"].Status = "completed"
	fake.jobs[jobID] = &models.Job{JobID: jobID, Status: "failed", FailureClass: &class, LastError: "\nexit status 1\nmore"}
	fake.workflows = []models.WorkflowInstance{{WorkflowID: "wf-1", Name: "build", Status: "failed"},
		{WorkflowID: "wf-2", Name: "lint", Status: "success"}}
	detail := &checkRunDetailStore{fakeChecksStore: fake,
		nodes: map[string][]models.WorkflowNode{"wf-1": {{Name: "unit", Status: "failed", JobID: &jobID}}},
		results: map[string][]models.TestResult{jobID: {{Name: "TestAdd", Classname: "calc", Status: "fail",
			Message: "want 2, got 3\nstack", File: "calc/add_test.go", Line: &line}}}}
	updater.SetStore(detail)
	client := &checkRunClient{}
	updater.AddVCSClient(GitHub, client)

	if err := updater.PublishEventChecks(ctx, testEventID); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 0 {
		t.Fatalf("an App client got %d commit statuses, want none", len(client.updates))
	}
	if len(client.runs) != 1 {
		t.Fatalf("check runs = %d, want 1", len(client.runs))
	}
	run := client.runs[0]
	if run.Name != "Reactorcide Checks" || run.HeadSHA != "abc123" || run.State != StatusFailure || run.ExternalID != testEventID {
		t.Fatalf("run = %+v", run)
	}
	for _, want := range []string{"[build](https://ci.example/app/workflows/wf-1) | failed |", "| success |", "### Failed jobs", "`user.test`",
		"`exit status 1`", "test `calc.TestAdd` fail: want 2, got 3", "https://ci.example/app/jobs/job-1"} {
		if !strings.Contains(run.Summary, want) {
			t.Fatalf("summary has no %q:\n%s", want, run.Summary)
		}
	}
	if len(run.Annotations) != 1 || run.Annotations[0].Path != "calc/add_test.go" || run.Annotations[0].StartLine != 42 {
		t.Fatalf("annotations = %+v", run.Annotations)
	}
	if len(run.Actions) != 1 || run.Actions[0].Identifier != RerunActionIdentifier {
		t.Fatalf("a finished pull request run has the Re-run action: %+v", run.Actions)
	}
}

func TestPublishEventChecksPendingRunHasNoRerunAction(t *testing.T) {
	updater, _, _ := newChecksFixture()
	client := &checkRunClient{}
	updater.AddVCSClient(GitHub, client)
	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	if len(client.runs) != 1 || client.runs[0].State != StatusPending || len(client.runs[0].Actions) != 0 {
		t.Fatalf("runs = %+v", client.runs)
	}
}

// fakeAppSource gives an App client for one repository.
type fakeAppSource struct {
	repo   string
	client Client
}

func (f *fakeAppSource) ClientForRepo(_ context.Context, _ Provider, repo string) (Client, bool, error) {
	if strings.EqualFold(repo, f.repo) {
		return f.client, true, nil
	}
	return nil, false, nil
}
func (f *fakeAppSource) CheckoutToken(context.Context, Provider, string, string) (string, bool, error) {
	return "", false, nil
}

func TestPublishEventChecksUsesAppInstallationForAppProject(t *testing.T) {
	updater, fake, global := newChecksFixture()
	projectID := "p1"
	fake.event.ProjectID = &projectID
	updater.SetProjectLookup(func(_ context.Context, id string) (*models.Project, error) {
		return &models.Project{ProjectID: id, RepoURL: "github.com/org/repo"}, nil
	})
	app := &checkRunClient{}
	SetAppInstallationSource(&fakeAppSource{repo: "org/repo", client: app})
	t.Cleanup(func() { SetAppInstallationSource(nil) })

	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	if len(app.runs) != 1 || len(global.updates) != 0 {
		t.Fatalf("app runs = %d, global statuses = %d; want the App installation", len(app.runs), len(global.updates))
	}
}

func TestPublishEventChecksNonAppProjectKeepsCommitStatus(t *testing.T) {
	updater, fake, global := newChecksFixture()
	projectID := "p1"
	fake.event.ProjectID = &projectID
	updater.SetProjectLookup(func(_ context.Context, id string) (*models.Project, error) {
		return &models.Project{ProjectID: id, RepoURL: "github.com/other/repo"}, nil
	})
	app := &checkRunClient{}
	SetAppInstallationSource(&fakeAppSource{repo: "org/repo", client: app})
	t.Cleanup(func() { SetAppInstallationSource(nil) })

	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	if len(app.runs) != 0 || len(global.updates) != 1 || global.last().Context != "Reactorcide Checks" {
		t.Fatalf("app runs = %d, global statuses = %+v; want the Commit Status path", len(app.runs), global.updates)
	}
}

func TestGitHubRepoFullName(t *testing.T) {
	cases := map[string]string{
		"github.com/Org/Repo":             "Org/Repo",
		"https://github.com/org/repo.git": "org/repo",
		"gitlab.com/org/repo":             "",
		"github.com/org":                  "",
		"github.com/org/repo/extra":       "",
	}
	for in, want := range cases {
		if got := GitHubRepoFullName(in); got != want {
			t.Errorf("GitHubRepoFullName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Report modes (WP10) gate check runs as they gate commit statuses: silent
// posts nothing, statuses_only and full post the check run.
func TestReportModeCheckRuns(t *testing.T) {
	for _, tc := range []struct {
		mode string
		runs int
	}{{models.ReportModeFull, 1}, {models.ReportModeStatusesOnly, 1}, {models.ReportModeSilent, 0}} {
		t.Run(tc.mode, func(t *testing.T) {
			updater, st, _ := reportingFixture(tc.mode)
			client := &checkRunClient{}
			updater.AddVCSClient(GitHub, client)
			if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
				t.Fatal(err)
			}
			if len(client.runs) != tc.runs || len(client.updates) != 0 {
				t.Fatalf("runs = %d, statuses = %d; want %d runs", len(client.runs), len(client.updates), tc.runs)
			}
			if st.saveCalls != 1 {
				t.Fatalf("save calls = %d", st.saveCalls)
			}
		})
	}
}

// The agreement report reads check runs with the App installation client of
// an App project.
func TestProjectClientUsesAppInstallation(t *testing.T) {
	updater, _, global := newChecksFixture()
	updater.SetProjectLookup(func(_ context.Context, id string) (*models.Project, error) {
		return &models.Project{ProjectID: id, RepoURL: "github.com/org/repo"}, nil
	})
	app := &checkRunClient{}
	SetAppInstallationSource(&fakeAppSource{repo: "org/repo", client: app})
	t.Cleanup(func() { SetAppInstallationSource(nil) })
	if got := updater.ProjectClient(context.Background(), "p1", GitHub); got != Client(app) {
		t.Fatalf("ProjectClient = %T, want the App client (global %T)", got, global)
	}
}

func TestRenderCheckRunSummaryListsPolicyPaths(t *testing.T) {
	summary := RenderCheckRunSummary(EventChecksResult{Description: "Policy refused 1 CI change"}, CheckRunDetails{
		PolicyViolations: 1,
		PolicyPaths: []models.CIPolicyPathDecision{
			{Path: ".reactorcide/jobs/nightly.yaml", WorkflowID: "nightly"},
			{Path: ".reactorcide/jobs/release-control.yaml", Accepted: true, WorkflowID: "release", RuleID: "override-ci",
				ApprovalID: "approval-1", Reason: "override-ci approved by github/todpunk"},
		},
	})
	for _, want := range []string{
		"| `.reactorcide/jobs/nightly.yaml` | refused: needs approval (workflow nightly) |",
		"| `.reactorcide/jobs/release-control.yaml` | accepted: override-ci approved by github/todpunk (approval approval-1) |",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary does not contain %q:\n%s", want, summary)
		}
	}
}
