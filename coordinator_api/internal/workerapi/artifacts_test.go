package workerapi

import (
	"context"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts/artifactstest"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// recordingResults is an in-memory store.TestResultStore.
type recordingResults struct {
	results map[string][]models.TestResult
	summary map[string]*models.JobTestSummary
}

func (r *recordingResults) ReplaceJobTestResults(_ context.Context, jobID string, results []models.TestResult, summary *models.JobTestSummary) error {
	r.results[jobID], r.summary[jobID] = results, summary
	return nil
}
func (r *recordingResults) SaveJobTestSummary(_ context.Context, s *models.JobTestSummary) error {
	r.summary[s.JobID] = s
	return nil
}
func (r *recordingResults) GetJobTestSummary(_ context.Context, jobID string) (*models.JobTestSummary, error) {
	return r.summary[jobID], nil
}
func (r *recordingResults) ListJobTestSummaries(context.Context, []string) (map[string]models.JobTestSummary, error) {
	return nil, nil
}
func (r *recordingResults) ListJobTestResults(_ context.Context, jobID string, _ []string, _ int) ([]models.TestResult, error) {
	return r.results[jobID], nil
}
func (r *recordingResults) RecordTestRuns(context.Context, string, string, []models.TestResult, time.Duration) ([]string, error) {
	return nil, nil
}
func (r *recordingResults) ListProjectFlakyTests(context.Context, string, time.Time, int) ([]models.TestFlakiness, error) {
	return nil, nil
}
func (r *recordingResults) GetTestFlakiness(context.Context, string, []string) (map[string]models.TestFlakiness, error) {
	return nil, nil
}

type wp15Store struct {
	*fakeStore
	*artifactstest.MemoryStore
	*recordingResults
}

func TestUploadArtifactThenReportResultIngestsTests(t *testing.T) {
	h := newTestHarness()
	results := &recordingResults{results: map[string][]models.TestResult{}, summary: map[string]*models.JobTestSummary{}}
	memory := artifactstest.NewMemoryStore()
	h.deps.Store = wp15Store{fakeStore: h.store, MemoryStore: memory, recordingResults: results}
	job, leaseID, token := claimOneJob(t, h)
	ctx := ctxWithAuth(token)

	report := `<testsuite name="s"><testcase classname="c" name="ok"/><testcase classname="c" name="bad"><failure message="no"/></testcase></testsuite>`
	resp, err := h.service.UploadArtifact(ctx, csilapi.UploadArtifactRequest{LeaseId: leaseID, Path: "out/results.xml", Data: []byte(report), Final: true})
	if err != nil || !resp.Ok || resp.Sha256 == nil || resp.Size != int64(len(report)) {
		t.Fatalf("upload: %v %+v", err, resp)
	}
	if _, err := h.service.UploadArtifact(ctx, csilapi.UploadArtifactRequest{LeaseId: leaseID, Path: "../escape", Data: []byte("x"), Final: true}); err == nil {
		t.Fatal("a bad path must be refused")
	} else if se, ok := err.(*uiapi.ServiceErr); !ok || se.Code != "invalid_argument" {
		t.Fatalf("bad path code: %v", err)
	}
	if _, err := h.service.UploadArtifact(context.Background(), csilapi.UploadArtifactRequest{LeaseId: leaseID, Path: "a", Final: true}); err == nil {
		t.Fatal("no session must be refused")
	}

	output := `{"test_reports": ["out/results.xml"]}`
	if _, err := h.service.ReportResult(ctx, csilapi.ReportResultRequest{LeaseId: leaseID, ExitCode: 1, Status: "failed", WorkflowOutput: &output}); err != nil {
		t.Fatal(err)
	}
	summary := results.summary[job.JobID]
	if summary == nil || summary.Total != 2 || summary.Failed != 1 || summary.Reports != 1 {
		t.Fatalf("summary %+v", summary)
	}
	list, _ := memory.ListJobArtifacts(context.Background(), job.JobID)
	if len(list) != 1 || list[0].Kind != models.ArtifactKindTestReport {
		t.Fatalf("the declared report is a test report: %+v", list)
	}
	// After the result the lease is closed: no more uploads.
	if _, err := h.service.UploadArtifact(ctx, csilapi.UploadArtifactRequest{LeaseId: leaseID, Path: "late.txt", Final: true}); err == nil {
		t.Fatal("an upload after the result must be refused")
	}
}
