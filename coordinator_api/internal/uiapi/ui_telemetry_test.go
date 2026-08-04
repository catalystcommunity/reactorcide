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

func TestJobTelemetryUsesProjectVisibilityForMetricsAndLogs(t *testing.T) {
	deps, st := newTestDeps(t)
	deps.ObjectStore = objects.NewMemoryObjectStore()
	st.putUser(models.User{UserID: "org-1"})
	outsider := st.putUser(models.User{UserID: "outsider"})
	project := st.putProject(models.Project{UserID: strPtr("org-1"), IsPrivate: true})
	job := st.putJob(models.Job{UserID: "submitter", ProjectID: &project.ProjectID, Name: "private-job"})
	now := time.Now().UTC()
	if err := jobtelemetry.PutMetricBatch(context.Background(), deps.ObjectStore, job.JobID, jobtelemetry.MetricBatch{
		LeaseID: "lease", Series: []jobtelemetry.SeriesDefinition{{SeriesID: 1, Name: "memory.usage", Unit: "bytes", Kind: "gauge"}},
		Samples: []jobtelemetry.Sample{{ObservedAt: now, Values: []jobtelemetry.Value{{SeriesID: 1, Value: 64}}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := jobtelemetry.PutLogBatch(context.Background(), deps.ObjectStore, job.JobID, jobtelemetry.LogBatch{
		LeaseID: "lease", Stream: "stdout", Entries: []jobtelemetry.LogEntry{{ObservedAt: now, Level: "info", Message: "private"}},
	}); err != nil {
		t.Fatal(err)
	}
	service := NewUiService(deps)
	outsiderCtx := mintSessionCtx(t, deps, outsider.UserID)
	if _, err := service.GetJobMetrics(outsiderCtx, csilapi.GetJobMetricsRequest{JobId: job.JobID, MaxPoints: 10}); serviceErrCode(t, err) != "forbidden" {
		t.Fatalf("metrics error = %v", err)
	}
	if _, err := service.GetJobLogs(outsiderCtx, csilapi.GetJobLogsRequest{JobId: job.JobID, Stream: "combined"}); serviceErrCode(t, err) != "forbidden" {
		t.Fatalf("logs error = %v", err)
	}
	ownerCtx := mintSessionCtx(t, deps, "org-1")
	metrics, err := service.GetJobMetrics(ownerCtx, csilapi.GetJobMetricsRequest{JobId: job.JobID, MaxPoints: 10})
	if err != nil || len(metrics.Series) != 1 {
		t.Fatalf("owner metrics: response=%+v err=%v", metrics, err)
	}
	logs, err := service.GetJobLogs(ownerCtx, csilapi.GetJobLogsRequest{JobId: job.JobID, Stream: "combined"})
	if err != nil || len(logs.Entries) != 1 || logs.Entries[0].Message != "private" {
		t.Fatalf("owner logs: response=%+v err=%v", logs, err)
	}
}
