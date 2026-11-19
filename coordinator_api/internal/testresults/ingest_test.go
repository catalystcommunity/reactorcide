package testresults

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts/artifactstest"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type fakeResults struct {
	store.TestResultStore
	results []models.TestResult
	summary *models.JobTestSummary
	flaky   []string
}

func (f *fakeResults) ReplaceJobTestResults(_ context.Context, _ string, results []models.TestResult, summary *models.JobTestSummary) error {
	f.results, f.summary = results, summary
	return nil
}
func (f *fakeResults) SaveJobTestSummary(_ context.Context, summary *models.JobTestSummary) error {
	f.summary = summary
	return nil
}
func (f *fakeResults) RecordTestRuns(context.Context, string, string, []models.TestResult, time.Duration) ([]string, error) {
	return f.flaky, nil
}

type fakeQuarantines struct {
	store.TestQuarantineStore
	rows []models.TestQuarantine
}

func (f fakeQuarantines) ListTestQuarantines(context.Context, string, bool, time.Time) ([]models.TestQuarantine, error) {
	return f.rows, nil
}

const report = `<testsuites><testsuite name="unit">
<testcase classname="pkg" name="ok"/>
<testcase classname="pkg" name="flaky"><failure message="timeout secret-value-1">at line 1</failure></testcase>
<testcase classname="pkg" name="broken"><error message="boom"/></testcase>
<testcase classname="pkg.secret-value-1" name="leak secret-value-1" file="src/secret-value-1.go"/>
<testcase classname="pkg" name="skip"><skipped/></testcase>
</testsuite></testsuites>`

func setup(t *testing.T, xml string) (*artifacts.Service, []models.JobArtifact, *models.Job) {
	t.Helper()
	service := &artifacts.Service{Store: artifactstest.NewMemoryStore(), Objects: objects.NewMemoryObjectStore(), Limits: artifacts.DefaultLimits}
	project, sha := "11111111-1111-1111-1111-111111111111", "abc"
	job := &models.Job{JobID: "job-1", ProjectID: &project, CommitSHA: &sha, Status: "failed"}
	job.SetFailure(models.FailureClassJob, "")
	if _, err := service.AppendChunk(context.Background(), job, "", artifacts.ChunkRequest{Path: "junit.xml", Data: []byte(xml), Final: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendChunk(context.Background(), job, "", artifacts.ChunkRequest{Path: "other.bin", Data: []byte("x"), Final: true}); err != nil {
		t.Fatal(err)
	}
	list, _ := service.Store.ListJobArtifacts(context.Background(), job.JobID)
	return service, list, job
}

func TestTestIDIsStable(t *testing.T) {
	a := TestID("p", "s", "c", "n")
	if a != TestID("p", "s", "c", "n") || len(a) != TestIDLength || !ValidTestID(a) {
		t.Fatalf("id %s", a)
	}
	if a == TestID("p2", "s", "c", "n") || a == TestID("p", "s", "cn", "") || a == TestID("p", "sc", "", "n") {
		t.Fatal("IDs must differ for other projects and other name splits")
	}
	if ValidTestID(strings.ToUpper(a)) || ValidTestID("xyz") {
		t.Fatal("bad IDs must be refused")
	}
}

func TestIngestCountsQuarantineAndMask(t *testing.T) {
	service, list, job := setup(t, report)
	results := &fakeResults{flaky: []string{"x"}}
	flakyID := TestID(*job.ProjectID, "unit", "pkg", "flaky")
	brokenID := TestID(*job.ProjectID, "unit", "pkg", "broken")
	in := &Ingestor{Results: results, Artifacts: service, Mask: func(s string) string { return strings.ReplaceAll(s, "secret-value-1", "***") },
		Quarantines: fakeQuarantines{rows: []models.TestQuarantine{{TestID: flakyID}}}}
	summary, err := in.Ingest(context.Background(), job, list, models.QuarantinePolicyIgnoreQuarantined)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Reports != 1 || summary.Total != 5 || summary.Passed != 2 || summary.Failed != 1 || summary.Errored != 1 || summary.Skipped != 1 {
		t.Fatalf("summary %+v", summary)
	}
	if summary.QuarantinedFailures != 1 || summary.UnquarantinedFailures() != 1 || summary.QuarantineOverride {
		t.Fatalf("one failure is not quarantined: %+v", summary)
	}
	if summary.Flaky != 1 || results.summary.Flaky != 1 {
		t.Fatalf("flaky %+v", results.summary)
	}
	for _, r := range results.results {
		for _, field := range []string{r.Message, r.Suite, r.Classname, r.Name, r.File} {
			if strings.Contains(field, "secret-value-1") {
				t.Fatalf("result text must be masked: %+v", r)
			}
		}
		if r.Name == "leak ***" && r.TestID != TestID(*job.ProjectID, "unit", "pkg.***", "leak ***") {
			t.Fatal("the test ID is a hash of the masked names")
		}
		if r.CommitSHA != "abc" || r.ArtifactID == nil {
			t.Fatalf("result %+v", r)
		}
	}
	// Quarantine the second failure too. Without test_exit in the workflow
	// output, the failure of the job stays: a later step can have failed.
	in.Quarantines = fakeQuarantines{rows: []models.TestQuarantine{{TestID: flakyID}, {TestID: brokenID}}}
	summary, err = in.Ingest(context.Background(), job, list, models.QuarantinePolicyIgnoreQuarantined)
	if err != nil || summary.QuarantineOverride || summary.TestExit {
		t.Fatalf("all failures quarantined, no test_exit: %v %+v", err, summary)
	}
	// With test_exit: true the node may now pass.
	in.TestExit = DeclaredTestExit([]byte(`{"test_exit": true}`))
	summary, err = in.Ingest(context.Background(), job, list, models.QuarantinePolicyIgnoreQuarantined)
	if err != nil || !summary.QuarantineOverride || !summary.TestExit {
		t.Fatalf("all failures quarantined, test_exit: %v %+v", err, summary)
	}
	summary, _ = in.Ingest(context.Background(), job, list, "")
	if summary.QuarantineOverride {
		t.Fatal("without the node option the failure stays")
	}
}

func TestIngestWithBadReportNeverOverrides(t *testing.T) {
	service, list, job := setup(t, `<testsuite><testcase name="a"><failure/></testcase>`)
	results := &fakeResults{}
	id := TestID(*job.ProjectID, "", "", "a")
	in := &Ingestor{Results: results, Artifacts: service, Quarantines: fakeQuarantines{rows: []models.TestQuarantine{{TestID: id}}}}
	summary, err := in.Ingest(context.Background(), job, list, models.QuarantinePolicyIgnoreQuarantined)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.ParseErrors) != 1 || summary.QuarantineOverride || summary.Failed != 1 {
		t.Fatalf("an unreadable report must not let a failure pass: %+v", summary)
	}
}

func TestIngestNoReports(t *testing.T) {
	in := &Ingestor{Results: &fakeResults{}, Artifacts: &artifacts.Service{}}
	summary, err := in.Ingest(context.Background(), &models.Job{JobID: "j"}, []models.JobArtifact{{Kind: models.ArtifactKindFile, Status: models.ArtifactStatusComplete}}, "")
	if err != nil || summary != nil {
		t.Fatalf("%v %+v", err, summary)
	}
}

func TestQuarantineOverrideRules(t *testing.T) {
	base := models.JobTestSummary{Failed: 2, QuarantinedFailures: 2, QuarantinePolicy: models.QuarantinePolicyIgnoreQuarantined, TestExit: true}
	job := &models.Job{Status: "failed"}
	job.SetFailure(models.FailureClassJob, "")
	if !QuarantineOverride(job, &base) {
		t.Fatal("all quarantined, opted in")
	}
	timeout := &models.Job{Status: "timeout"}
	timeout.SetFailure(models.FailureClassTimeout, "")
	oom := &models.Job{Status: "failed"}
	oom.SetFailure(models.FailureClassOOM, "")
	for name, tc := range map[string]struct {
		job     *models.Job
		summary models.JobTestSummary
	}{
		"timeout":      {timeout, base},
		"oom":          {oom, base},
		"completed":    {&models.Job{Status: "completed"}, base},
		"not opted":    {job, models.JobTestSummary{Failed: 1, QuarantinedFailures: 1}},
		"one open":     {job, models.JobTestSummary{Failed: 2, QuarantinedFailures: 1, QuarantinePolicy: base.QuarantinePolicy, TestExit: true}},
		"no test_exit": {job, models.JobTestSummary{Failed: 2, QuarantinedFailures: 2, QuarantinePolicy: base.QuarantinePolicy}},
		"no failures":  {job, models.JobTestSummary{QuarantinePolicy: base.QuarantinePolicy}},
		"truncated":    {job, models.JobTestSummary{Failed: 1, QuarantinedFailures: 1, Truncated: true, QuarantinePolicy: base.QuarantinePolicy}},
	} {
		summary := tc.summary
		if QuarantineOverride(tc.job, &summary) {
			t.Errorf("%s: must not override", name)
		}
	}
}

func TestDeclaredTestExit(t *testing.T) {
	for doc, want := range map[string]bool{
		`{"test_exit": true}`:   true,
		`{"test_exit": false}`:  false,
		`{"test_exit": "true"}`: false,
		`{}`:                    false,
		`not json`:              false,
		``:                      false,
	} {
		if got := DeclaredTestExit([]byte(doc)); got != want {
			t.Errorf("%q: %v, want %v", doc, got, want)
		}
	}
}
