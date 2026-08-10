package coordinatorworker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

func TestTelemetrySpoolRetainsFailureAndReplaysSuccess(t *testing.T) {
	dir := t.TempDir()
	client := &fakeClient{AppendMetricBatchFunc: func(context.Context, csilapi.AppendMetricBatchRequest) (csilapi.AppendMetricBatchResponse, error) {
		return csilapi.AppendMetricBatchResponse{}, errors.New("coordinator unavailable")
	}}
	req := csilapi.AppendMetricBatchRequest{LeaseId: "lease-a", Sequence: 4}
	if _, err := persistAndSendMetrics(client, dir, 12, req); err == nil {
		t.Fatal("expected send failure")
	}
	files, err := filepath.Glob(filepath.Join(dir, "telemetry", "lease-a", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one durable spool record: files=%v err=%v", files, err)
	}
	client.AppendMetricBatchFunc = nil
	replayTelemetrySpool(client, dir)
	files, _ = filepath.Glob(filepath.Join(dir, "telemetry", "lease-a", "*.json"))
	if len(files) != 0 {
		t.Fatalf("acknowledged spool record remains: %v", files)
	}
}

func TestTelemetrySpoolDropsOldestRecordAtLimit(t *testing.T) {
	dir := t.TempDir()
	client := &fakeClient{AppendMetricBatchFunc: func(context.Context, csilapi.AppendMetricBatchRequest) (csilapi.AppendMetricBatchResponse, error) {
		return csilapi.AppendMetricBatchResponse{}, errors.New("coordinator unavailable")
	}}
	for sequence := int64(0); sequence < 4; sequence++ {
		dropped, err := persistAndSendMetrics(client, dir, 2, csilapi.AppendMetricBatchRequest{LeaseId: "lease-a", Sequence: sequence})
		if err == nil {
			t.Fatal("expected send failure")
		}
		if dropped != (sequence >= 2) {
			t.Fatalf("sequence %d: dropped=%v", sequence, dropped)
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "telemetry", "lease-a", "*.json"))
	if err != nil || len(files) != 2 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	for _, path := range files {
		if strings.Contains(path, "00000000000000000000") || strings.Contains(path, "00000000000000000001") {
			t.Fatalf("old record remains: %s", path)
		}
	}
}

func TestTelemetrySpoolIgnoresCorruptRecord(t *testing.T) {
	dir := t.TempDir()
	spoolDir := filepath.Join(dir, "telemetry", "lease-a")
	if err := os.MkdirAll(spoolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(spoolDir, "metrics-00000000000000000001.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	replayTelemetrySpool(&fakeClient{}, dir)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a corrupt record must remain for operator inspection: %v", err)
	}
}
