package uiapi

import (
	"context"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func putCPUTelemetry(t *testing.T, store objects.ObjectStore, jobID string, at time.Time, millicores ...int64) {
	t.Helper()
	batch := jobtelemetry.MetricBatch{LeaseID: "lease", Series: []jobtelemetry.SeriesDefinition{
		{SeriesID: 1, Name: "cpu.utilization", Unit: "millicores", Kind: "gauge", Labels: []jobtelemetry.Label{{Key: "cpu", Value: "total"}}},
	}}
	for i, value := range millicores {
		batch.Samples = append(batch.Samples, jobtelemetry.Sample{ObservedAt: at.Add(time.Duration(i) * 2 * time.Second), Values: []jobtelemetry.Value{{SeriesID: 1, Value: value}}})
	}
	if err := jobtelemetry.PutMetricBatch(context.Background(), store, jobID, batch); err != nil {
		t.Fatal(err)
	}
}

func TestGetJobResourceHistoryAuthorizationAndShape(t *testing.T) {
	deps, st := newTestDeps(t)
	deps.ObjectStore = objects.NewMemoryObjectStore()
	st.putUser(models.User{UserID: "org-1"})
	outsider := st.putUser(models.User{UserID: "outsider"})
	project := st.putProject(models.Project{UserID: strPtr("org-1"), IsPrivate: true})
	other := st.putProject(models.Project{UserID: strPtr("org-1"), IsPrivate: true})
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	started := base.Add(time.Minute)
	completed := started.Add(90 * time.Second)

	var runs []models.Job
	for i := 0; i < 3; i++ {
		created := base.Add(time.Duration(i) * time.Hour)
		job := st.putJob(models.Job{
			UserID: "submitter", ProjectID: &project.ProjectID, Name: "build", Status: "completed",
			CreatedAt: created, StartedAt: &started, CompletedAt: &completed,
			ResourceCPURequest: "500m", ResourceCPULimit: "2", ResourceMemoryLimit: "4Gi",
		})
		putCPUTelemetry(t, deps.ObjectStore, job.JobID, created, 100, int64(200*(i+1)))
		runs = append(runs, job)
	}
	// Not the same job: a different project, a different name, a running run.
	st.putJob(models.Job{UserID: "submitter", ProjectID: &other.ProjectID, Name: "build", Status: "completed", CreatedAt: base})
	st.putJob(models.Job{UserID: "submitter", ProjectID: &project.ProjectID, Name: "test", Status: "completed", CreatedAt: base})
	running := st.putJob(models.Job{UserID: "submitter", ProjectID: &project.ProjectID, Name: "build", Status: "running", CreatedAt: base.Add(5 * time.Hour)})

	service := NewUiService(deps)
	outsiderCtx := mintSessionCtx(t, deps, outsider.UserID)
	if _, err := service.GetJobResourceHistory(outsiderCtx, csilapi.GetJobResourceHistoryRequest{JobId: runs[0].JobID}); serviceErrCode(t, err) != "not_found" {
		t.Fatalf("outsider error = %v", err)
	}

	ownerCtx := mintSessionCtx(t, deps, "org-1")
	// The reference job may itself be unfinished: history is about its siblings.
	history, err := service.GetJobResourceHistory(ownerCtx, csilapi.GetJobResourceHistoryRequest{JobId: running.JobID})
	if err != nil {
		t.Fatal(err)
	}
	if history.Name != "build" || history.ProjectId == nil || *history.ProjectId != project.ProjectID || history.Pending != 0 {
		t.Fatalf("history header %+v", history)
	}
	if len(history.Runs) != 3 {
		t.Fatalf("runs = %d, want 3", len(history.Runs))
	}
	for i, run := range history.Runs {
		if run.JobId != runs[i].JobID {
			t.Fatalf("run %d is %s, want oldest first", i, run.JobId)
		}
		if run.CpuMaxMillicores == nil || *run.CpuMaxMillicores != int64(200*(i+1)) || run.SampleCount != 2 {
			t.Fatalf("run %d summary %+v", i, run)
		}
	}
	first := history.Runs[0]
	if first.DurationSeconds == nil || *first.DurationSeconds != 90 {
		t.Fatalf("duration %v", first.DurationSeconds)
	}
	// Docker-style telemetry has no request series: the configured values fill in.
	if *first.CpuRequestMillicores != 500 || *first.CpuLimitMillicores != 2000 || *first.MemoryLimitBytes != 4<<30 {
		t.Fatalf("configured fallbacks %+v", first)
	}
	if first.MemoryMaxBytes != nil {
		t.Fatal("memory reported without memory telemetry")
	}
	if len(st.resourceSummaries) != 3 {
		t.Fatalf("stored summaries = %d, want 3", len(st.resourceSummaries))
	}

	limit := int64(2)
	limited, err := service.GetJobResourceHistory(ownerCtx, csilapi.GetJobResourceHistoryRequest{JobId: runs[0].JobID, Limit: &limit})
	if err != nil || len(limited.Runs) != 2 || limited.Runs[0].JobId != runs[1].JobID {
		t.Fatalf("limited %+v err %v", limited, err)
	}
	bad := int64(1001)
	if _, err := service.GetJobResourceHistory(ownerCtx, csilapi.GetJobResourceHistoryRequest{JobId: runs[0].JobID, Limit: &bad}); serviceErrCode(t, err) != "invalid_argument" {
		t.Fatalf("limit error = %v", err)
	}
	badTime := "yesterday"
	if _, err := service.GetJobResourceHistory(ownerCtx, csilapi.GetJobResourceHistoryRequest{JobId: runs[0].JobID, FromTime: &badTime}); serviceErrCode(t, err) != "invalid_argument" {
		t.Fatalf("from_time error = %v", err)
	}
}

func TestGetJobResourceHistoryBoundsLazyRefreshAndRecomputesOldVersions(t *testing.T) {
	deps, st := newTestDeps(t)
	deps.ObjectStore = objects.NewMemoryObjectStore()
	st.putUser(models.User{UserID: "org-1"})
	project := st.putProject(models.Project{UserID: strPtr("org-1")})
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	total := resourceHistoryRefreshPerRequest + 5
	var ids []string
	for i := 0; i < total; i++ {
		job := st.putJob(models.Job{UserID: "submitter", ProjectID: &project.ProjectID, Name: "build", Status: "failed", CreatedAt: base.Add(time.Duration(i) * time.Minute)})
		ids = append(ids, job.JobID)
	}
	// An old-version row for the newest run: computed again, so it counts
	// against the bound like a missing one.
	if err := st.UpsertJobResourceSummary(context.Background(), &models.JobResourceSummary{JobID: ids[total-1], SummaryVersion: jobtelemetry.SummaryVersion - 1}); err != nil {
		t.Fatal(err)
	}
	service := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, "org-1")

	first, err := service.GetJobResourceHistory(ctx, csilapi.GetJobResourceHistoryRequest{JobId: ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	if first.Pending != 5 || len(first.Runs) != resourceHistoryRefreshPerRequest {
		t.Fatalf("first read pending=%d runs=%d", first.Pending, len(first.Runs))
	}
	// The newest runs are computed first.
	if first.Runs[len(first.Runs)-1].JobId != ids[total-1] {
		t.Fatalf("newest run not refreshed first")
	}
	// No telemetry still makes a row: sample_count 0, nothing measured.
	if first.Runs[0].SampleCount != 0 || first.Runs[0].CpuMaxMillicores != nil {
		t.Fatalf("empty run %+v", first.Runs[0])
	}
	if got := st.resourceSummaries[ids[total-1]].SummaryVersion; got != jobtelemetry.SummaryVersion {
		t.Fatalf("old version not recomputed: %d", got)
	}

	second, err := service.GetJobResourceHistory(ctx, csilapi.GetJobResourceHistoryRequest{JobId: ids[0]})
	if err != nil || second.Pending != 0 || len(second.Runs) != total {
		t.Fatalf("second read pending=%d runs=%d err=%v", second.Pending, len(second.Runs), err)
	}
}

func TestGetJobResourceHistoryWithoutObjectStoreReportsPending(t *testing.T) {
	deps, st := newTestDeps(t)
	deps.ObjectStore = nil
	st.putUser(models.User{UserID: "org-1"})
	project := st.putProject(models.Project{UserID: strPtr("org-1")})
	job := st.putJob(models.Job{UserID: "submitter", ProjectID: &project.ProjectID, Name: "build", Status: "completed"})
	service := NewUiService(deps)
	history, err := service.GetJobResourceHistory(mintSessionCtx(t, deps, "org-1"), csilapi.GetJobResourceHistoryRequest{JobId: job.JobID})
	if err != nil || history.Pending != 1 || len(history.Runs) != 0 {
		t.Fatalf("history %+v err %v", history, err)
	}
}
