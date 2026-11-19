package workerapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// infraRetryFake adds the infra_retries setting to the reconciler fake.
type infraRetryFake struct {
	*lostJobFake
	value int
}

func (f *infraRetryFake) EffectiveInfraRetries(context.Context, string, *string) (int, error) {
	return f.value, nil
}

func (f *infraRetryFake) SetOrganizationInfraRetries(_ context.Context, _ string, value int) error {
	f.value = value
	return nil
}

func withInfraRetries(h *testHarness, value int) {
	lf, ok := h.deps.Store.(*lostJobFake)
	if !ok {
		lf = &lostJobFake{fakeStore: h.store, openNodeJobs: map[string]bool{}}
	}
	h.deps.Store = &infraRetryFake{lostJobFake: lf, value: value}
}

func text(v string) *string { return &v }

func counterValue(c prometheus.Counter) float64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}

func reportFailure(t *testing.T, h *testHarness, token, leaseID string, req csilapi.ReportResultRequest) {
	t.Helper()
	req.LeaseId = leaseID
	if _, err := h.service.ReportResult(ctxWithAuth(token), req); err != nil {
		t.Fatalf("ReportResult failed: %v", err)
	}
}

func TestReportResultStoresReportedFailureClass(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)
	before := counterValue(metrics.JobsFinished.WithLabelValues("failed", models.FailureClassOOM, ""))

	reportFailure(t, h, token, leaseID, csilapi.ReportResultRequest{
		ExitCode: 137, Status: "failed", Error: text("job exited with code 137 (OOMKilled)"),
		FailureClass: text(models.FailureClassOOM), FailureDetail: text("OOMKilled"),
	})

	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "failed" || saved.FailureClassValue() != models.FailureClassOOM || saved.FailureDetail != "OOMKilled" {
		t.Fatalf("job = %s/%q/%q, want failed/oom/OOMKilled", saved.Status, saved.FailureClassValue(), saved.FailureDetail)
	}
	if got := counterValue(metrics.JobsFinished.WithLabelValues("failed", models.FailureClassOOM, "")) - before; got != 1 {
		t.Fatalf("reactorcide_jobs_finished_total{failed,oom} grew by %v, want 1", got)
	}
}

func TestReportResultDerivesClassForOlderWorker(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)
	reportFailure(t, h, token, leaseID, csilapi.ReportResultRequest{ExitCode: 2, Status: "failed", Error: text("job exited with code 2")})

	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.FailureClassValue() != models.FailureClassJob {
		t.Fatalf("derived class = %q, want job", saved.FailureClassValue())
	}
}

func TestReportResultRefusesAClassThatDisagreesWithStatus(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)
	// A worker cannot claim worker_lost; only the reconciler knows that.
	reportFailure(t, h, token, leaseID, csilapi.ReportResultRequest{ExitCode: 1, Status: "failed", Error: text("job exited with code 1"), FailureClass: text(models.FailureClassWorkerLost)})
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.FailureClassValue() != models.FailureClassJob {
		t.Fatalf("class = %q, want the derived class job", saved.FailureClassValue())
	}
}

func TestReportResultTimeoutStatus(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)
	reportFailure(t, h, token, leaseID, csilapi.ReportResultRequest{
		ExitCode: 143, Status: "timeout", Error: text("job exceeded its timeout of 60 seconds"), FailureClass: text(models.FailureClassTimeout),
	})

	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "timeout" || saved.FailureClassValue() != models.FailureClassTimeout || saved.CompletedAt == nil {
		t.Fatalf("job = %s/%q, want timeout/timeout", saved.Status, saved.FailureClassValue())
	}
	if len(h.corndogs.UpdateTaskCalls) == 0 {
		t.Fatal("a timed out job must fail its corndogs task")
	}
	lease, _ := h.store.GetWorkerLeaseByID(context.Background(), leaseID)
	if lease.IsActive() || lease.Outcome != "timeout" {
		t.Fatalf("lease active=%v outcome=%q, want released with timeout", lease.IsActive(), lease.Outcome)
	}
}

func TestReportResultRetriesPlatformFailure(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)
	oldTask := *job.CorndogsTaskID
	submitsBefore := len(h.corndogs.SubmitTaskToQueueCalls)

	reportFailure(t, h, token, leaseID, csilapi.ReportResultRequest{
		ExitCode: -1, Status: "failed", Error: text("failed to list pods for job: connection refused"), FailureClass: text(models.FailureClassPlatform),
		MainStarted: boolPtr(false),
	})

	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "submitted" || saved.RetryCount != 1 || saved.FailureClass != nil || saved.CompletedAt != nil {
		t.Fatalf("job = %s retry=%d class=%v; want submitted, retry 1, no class", saved.Status, saved.RetryCount, saved.FailureClass)
	}
	if !strings.Contains(saved.LastError, "infra.platform (automatic retry 1 of 1)") {
		t.Fatalf("decision reason = %q", saved.LastError)
	}
	if saved.CorndogsTaskID == nil || *saved.CorndogsTaskID == oldTask || len(h.corndogs.SubmitTaskToQueueCalls) != submitsBefore+1 {
		t.Fatalf("the retry must have a new corndogs task, got %v", saved.CorndogsTaskID)
	}
	lease, _ := h.store.GetWorkerLeaseByID(context.Background(), leaseID)
	if lease.IsActive() || lease.Outcome != "requeued:infra.platform" {
		t.Fatalf("lease active=%v outcome=%q", lease.IsActive(), lease.Outcome)
	}
}

func TestReportResultDoesNotRetryWhenBudgetIsUsed(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)
	stored, _ := h.store.GetJobByID(context.Background(), job.JobID)
	stored.RetryCount = 1
	_ = h.store.UpdateJob(context.Background(), stored)

	reportFailure(t, h, token, leaseID, csilapi.ReportResultRequest{ExitCode: 1, Status: "failed", Error: text("failed to spawn job: x"), FailureClass: text(models.FailureClassPlatform), MainStarted: boolPtr(false)})
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "failed" || saved.FailureClassValue() != models.FailureClassPlatform {
		t.Fatalf("job = %s/%q, want failed/infra.platform", saved.Status, saved.FailureClassValue())
	}
}

func TestReportResultNeverRetriesPipelineFailures(t *testing.T) {
	for _, class := range []string{models.FailureClassJob, models.FailureClassOOM, models.FailureClassImagePull, models.FailureClassScheduling} {
		t.Run(class, func(t *testing.T) {
			h := newTestHarness()
			withInfraRetries(h, models.MaxInfraRetries)
			job, leaseID, token := claimOneJob(t, h)
			reportFailure(t, h, token, leaseID, csilapi.ReportResultRequest{ExitCode: 1, Status: "failed", Error: text("x"), FailureClass: text(class), MainStarted: boolPtr(false)})
			saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
			if saved.Status != "failed" || saved.RetryCount != 0 || saved.FailureClassValue() != class {
				t.Fatalf("job = %s retry=%d class=%q", saved.Status, saved.RetryCount, saved.FailureClassValue())
			}
		})
	}
}

func TestReportResultRespectsInfraRetriesZero(t *testing.T) {
	h := newTestHarness()
	withInfraRetries(h, 0)
	job, leaseID, token := claimOneJob(t, h)
	reportFailure(t, h, token, leaseID, csilapi.ReportResultRequest{ExitCode: 1, Status: "failed", Error: text("x"), FailureClass: text(models.FailureClassPlatform), MainStarted: boolPtr(false)})
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "failed" || saved.RetryCount != 0 {
		t.Fatalf("infra_retries 0 must not retry: %s retry=%d", saved.Status, saved.RetryCount)
	}
}

func boolPtr(v bool) *bool { return &v }

// A platform failure after the main container started (an eviction, a lost
// container runtime, a failed output read after exit 0) must not run the
// job command again. An older worker that sends no main_started is treated
// the same way.
func TestReportResultDoesNotRetryPlatformFailureAfterStart(t *testing.T) {
	cases := map[string]*bool{"started": boolPtr(true), "older worker": nil}
	for name, mainStarted := range cases {
		t.Run(name, func(t *testing.T) {
			h := newTestHarness()
			withInfraRetries(h, models.MaxInfraRetries)
			job, leaseID, token := claimOneJob(t, h)
			reportFailure(t, h, token, leaseID, csilapi.ReportResultRequest{
				ExitCode: -1, Status: "failed", Error: text("Evicted"), FailureClass: text(models.FailureClassPlatform),
				FailureDetail: text("Evicted"), MainStarted: mainStarted,
			})
			saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
			if saved.Status != "failed" || saved.RetryCount != 0 || saved.FailureClassValue() != models.FailureClassPlatform {
				t.Fatalf("job = %s retry=%d class=%q; want failed, no retry, infra.platform", saved.Status, saved.RetryCount, saved.FailureClassValue())
			}
		})
	}
}

func TestReportResultDoesNotRetryAnOlderWorkerGuess(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)
	// No failure_class: the coordinator derives infra.platform from the
	// text, but does not retry a guess.
	reportFailure(t, h, token, leaseID, csilapi.ReportResultRequest{ExitCode: -1, Status: "failed", Error: text("error waiting for container: EOF")})
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "failed" || saved.FailureClassValue() != models.FailureClassPlatform {
		t.Fatalf("job = %s/%q", saved.Status, saved.FailureClassValue())
	}
}

func TestReconcilerFailsLostJobAsWorkerLost(t *testing.T) {
	h, _, _ := newReconcilerHarness()
	job, _ := seedLostJob(t, h, "88888888-8888-8888-8888-888888888891", 3*time.Hour, maxLostJobRetries)
	h.service.reconcileLostJobs(context.Background())
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "failed" || saved.FailureClassValue() != models.FailureClassWorkerLost {
		t.Fatalf("lost job = %s/%q, want failed/infra.worker_lost", saved.Status, saved.FailureClassValue())
	}
	if !strings.HasPrefix(saved.LastError, models.LostJobErrorPrefix) {
		t.Fatalf("last_error = %q, want the lost prefix the backfill matches", saved.LastError)
	}
}

func TestReconcilerLostBudgetFollowsInfraRetries(t *testing.T) {
	h, _, _ := newReconcilerHarness()
	withInfraRetries(h, 4)
	job, _ := seedLostJob(t, h, "88888888-8888-8888-8888-888888888892", 3*time.Hour, maxLostJobRetries)
	h.service.reconcileLostJobs(context.Background())
	saved, _ := h.store.GetJobByID(context.Background(), job.JobID)
	if saved.Status != "submitted" || saved.RetryCount != maxLostJobRetries+1 {
		t.Fatalf("infra_retries 4 must raise the lost budget: %s retry=%d", saved.Status, saved.RetryCount)
	}
	if !strings.Contains(saved.LastError, "infra.worker_lost (automatic retry 3 of 4)") {
		t.Fatalf("decision reason = %q", saved.LastError)
	}

	// infra_retries 0 keeps the reconciler's own budget of 2.
	h2, _, _ := newReconcilerHarness()
	withInfraRetries(h2, 0)
	job2, _ := seedLostJob(t, h2, "88888888-8888-8888-8888-888888888893", 3*time.Hour, 1)
	h2.service.reconcileLostJobs(context.Background())
	saved2, _ := h2.store.GetJobByID(context.Background(), job2.JobID)
	if saved2.Status != "submitted" || saved2.RetryCount != 2 {
		t.Fatalf("a lost job keeps at least 2 retries: %s retry=%d", saved2.Status, saved2.RetryCount)
	}
}

func TestResultFailureClass(t *testing.T) {
	cases := []struct {
		status, message string
		exitCode        int
		reported        *string
		wantClass       string
		wantReported    bool
	}{
		{"completed", "", 0, text(models.FailureClassJob), "", false},
		{"cancelled", "cancelled", 143, nil, models.FailureClassCancelled, false},
		{"timeout", "", 143, text(models.FailureClassTimeout), models.FailureClassTimeout, true},
		{"failed", "x", 1, text(models.FailureClassImagePull), models.FailureClassImagePull, true},
		{"failed", "job exited with code 1", 1, text("bogus"), models.FailureClassJob, false},
		{"failed", "job exited with code 1", 1, text(models.FailureClassTimeout), models.FailureClassJob, false},
	}
	for _, tc := range cases {
		class, _, reported := resultFailureClass(tc.status, tc.exitCode, tc.message, tc.reported, nil)
		if class != tc.wantClass || reported != tc.wantReported {
			t.Errorf("resultFailureClass(%s, %q, %v) = %q/%v, want %q/%v", tc.status, tc.message, tc.reported, class, reported, tc.wantClass, tc.wantReported)
		}
	}
}
