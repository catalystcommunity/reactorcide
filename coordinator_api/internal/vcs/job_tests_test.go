package vcs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type testReportStore struct {
	store.Store
	store.TestResultStore
	store.TestQuarantineStore
	summaries map[string]models.JobTestSummary
	failures  map[string][]models.TestResult
	active    []models.TestQuarantine
}

func (s testReportStore) ListJobTestSummaries(context.Context, []string) (map[string]models.JobTestSummary, error) {
	return s.summaries, nil
}
func (s testReportStore) ListJobTestResults(_ context.Context, jobID string, _ []string, _ int) ([]models.TestResult, error) {
	return s.failures[jobID], nil
}
func (s testReportStore) ListTestQuarantines(context.Context, string, bool, time.Time) ([]models.TestQuarantine, error) {
	return s.active, nil
}

func TestCommentBodiesShowTestResults(t *testing.T) {
	project := "p-1"
	line := 42
	updater := NewJobStatusUpdater()
	updater.SetStore(testReportStore{
		summaries: map[string]models.JobTestSummary{
			"job-1": {Total: 10, Passed: 7, Failed: 2, Errored: 0, Skipped: 1, QuarantinedFailures: 1, Flaky: 1},
			"job-2": {Total: 3, Passed: 2, Failed: 1, QuarantinedFailures: 1, QuarantineOverride: true},
		},
		failures: map[string][]models.TestResult{
			"job-1": {
				{TestID: "t-q", ProjectID: &project, Classname: "pkg.A", Name: "quarantined", Status: "fail", Message: "flaky timeout"},
				{TestID: "t-1", ProjectID: &project, Classname: "pkg.A", Name: "breaks `code`", Status: "fail", File: "a_test.go", Line: &line,
					Message: "expected 1\n```\n</details><script>x</script>\nline 3\nline 4"},
			},
			"job-2": {{TestID: "t-q", ProjectID: &project, Name: "q", Status: "fail", Message: "m"}},
		},
		active: []models.TestQuarantine{{TestID: "t-q"}},
	})
	job1, job2 := "job-1", "job-2"
	body := updater.renderWorkflowCommentBody(&models.WorkflowInstance{Name: "CI", CommitSHA: "abcdef123"},
		[]models.WorkflowNode{{Name: "unit", Status: "failed", JobID: &job1}, {Name: "api", Status: "completed", JobID: &job2}, {Name: "lint", Status: "completed"}}, "m")
	for _, want := range []string{
		"Tests:",
		"- unit: ❌ 10 tests: 7 passed, 2 failed (1 quarantined), 1 skipped, 1 flaky",
		"`pkg.A › breaks 'code'` (failed) at `a_test.go:42`",
		"`pkg.A › quarantined` (failed, quarantined)",
		"- api: 🟨 3 tests: 2 passed, 1 failed (1 quarantined) · the node passed: every failure is quarantined",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("workflow body lacks %q:\n%s", want, body)
		}
	}
	if strings.Index(body, "breaks") > strings.Index(body, "› quarantined") {
		t.Fatal("open failures come before quarantined failures")
	}
	if strings.Count(body, "```") != 6 || strings.Contains(body, "line 4") {
		t.Fatalf("a message cannot close the code block, and only the first lines show:\n%s", body)
	}
	perJob := updater.renderPerJobCommentBody(&models.Job{JobID: "job-2", Name: "api", Status: "failed"}, "m")
	if !strings.Contains(perJob, "**Tests:** 🟨 3 tests") {
		t.Fatalf("per-job body:\n%s", perJob)
	}
	if body := NewJobStatusUpdater().renderWorkflowCommentBody(&models.WorkflowInstance{}, []models.WorkflowNode{{Name: "x", JobID: &job1}}, "m"); strings.Contains(body, "Tests:") {
		t.Fatal("no store: no tests")
	}
}
