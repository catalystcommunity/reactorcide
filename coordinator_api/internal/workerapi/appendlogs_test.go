package workerapi

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

func TestAppendLogs_WritesExpectedObjectKeyAndAccumulates(t *testing.T) {
	h := newTestHarness()
	job, leaseID, token := claimOneJob(t, h)

	resp, err := h.service.AppendLogs(ctxWithAuth(token), csilapi.AppendLogsRequest{
		LeaseId: leaseID,
		Stream:  "stdout",
		Chunk:   "line one\nline two\n",
	})
	if err != nil {
		t.Fatalf("AppendLogs failed: %v", err)
	}
	if !resp.Ok {
		t.Fatalf("expected ok=true")
	}

	entries := readLogEntries(t, h, job.JobID, "stdout")
	if len(entries) != 2 || entries[0].Message != "line one" || entries[1].Message != "line two" {
		t.Fatalf("unexpected entries after first chunk: %+v", entries)
	}

	// A second chunk must be appended, not overwrite the first.
	if _, err := h.service.AppendLogs(ctxWithAuth(token), csilapi.AppendLogsRequest{
		LeaseId: leaseID,
		Stream:  "stdout",
		Chunk:   "line three\n",
	}); err != nil {
		t.Fatalf("AppendLogs (second chunk) failed: %v", err)
	}
	entries = readLogEntries(t, h, job.JobID, "stdout")
	if len(entries) != 3 || entries[2].Message != "line three" {
		t.Fatalf("expected accumulation across chunks, got %+v", entries)
	}

	// stderr is a distinct object key from stdout.
	if _, err := h.service.AppendLogs(ctxWithAuth(token), csilapi.AppendLogsRequest{
		LeaseId: leaseID,
		Stream:  "stderr",
		Chunk:   "err line\n",
	}); err != nil {
		t.Fatalf("AppendLogs (stderr) failed: %v", err)
	}
	stderrEntries := readLogEntries(t, h, job.JobID, "stderr")
	if len(stderrEntries) != 1 || stderrEntries[0].Message != "err line" {
		t.Fatalf("unexpected stderr entries: %+v", stderrEntries)
	}
}

func TestAppendLogs_MasksCachedLeaseSecretsAsBackstop(t *testing.T) {
	h := newTestHarness()
	job, token := seedQueueAndJobWithSecretRef(t, h, true)

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil || !resp.HasLease {
		t.Fatalf("expected a lease: err=%v resp=%+v", err, resp)
	}

	// Simulate a worker that failed to mask the secret in its own output --
	// the coordinator's backstop must catch it before persisting.
	if _, err := h.service.AppendLogs(ctxWithAuth(token), csilapi.AppendLogsRequest{
		LeaseId: resp.Lease.LeaseId,
		Stream:  "stdout",
		Chunk:   "here is the value: sooper-seekrit-value\n",
	}); err != nil {
		t.Fatalf("AppendLogs failed: %v", err)
	}

	entries := readLogEntries(t, h, job.JobID, "stdout")
	if len(entries) != 1 {
		t.Fatalf("expected one entry, got %+v", entries)
	}
	if strings.Contains(entries[0].Message, "sooper-seekrit-value") {
		t.Fatalf("AppendLogs must mask a lease's known secret values as a backstop; got %q", entries[0].Message)
	}
}

func TestAppendLogs_RejectsLeaseNotOwnedByCaller(t *testing.T) {
	h := newTestHarness()
	_, leaseID, _ := claimOneJob(t, h)

	otherToken, _ := h.registerWorker(t, "other-worker", "linux", "amd64", nil)
	_, err := h.service.AppendLogs(ctxWithAuth(otherToken), csilapi.AppendLogsRequest{
		LeaseId: leaseID,
		Stream:  "stdout",
		Chunk:   "line\n",
	})
	if err == nil {
		t.Fatalf("expected an error when a worker appends logs to a lease it doesn't own")
	}
}

func readLogEntries(t *testing.T, h *testHarness, jobID, stream string) []jobtelemetry.LogEntry {
	t.Helper()
	entries, err := jobtelemetry.ReadLogEntries(context.Background(), h.objectStore, jobID, stream)
	if err != nil {
		t.Fatalf("failed to read %s entries: %v", stream, err)
	}
	return entries
}
