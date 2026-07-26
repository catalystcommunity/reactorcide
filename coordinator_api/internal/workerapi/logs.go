package workerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// AppendLogs is push log ingestion (WORKERS_PLAN.md "Workers"): resolves
// the caller's session, verifies the lease belongs to it and is still
// active, and accumulates the chunk into the SAME object layout
// internal/worker/log_shipper.go's pull-based LogShipper writes --
// logs/{jobID}/{stdout|stderr}.json, a JSON array of worker.LogEntry --  so
// the UI's existing log reader needs no changes regardless of which path
// produced a job's logs. Applies a value-based secret-masking backstop
// (leaseSecretCache) before writing, then publishes PublishLogAvailable.
func (s *WorkerService) AppendLogs(ctx context.Context, req csilapi.AppendLogsRequest) (csilapi.AppendLogsResponse, error) {
	wkr, _, err := s.resolveSession(ctx)
	if err != nil {
		return csilapi.AppendLogsResponse{}, err
	}

	stream := req.Stream
	if stream != "stdout" && stream != "stderr" {
		return csilapi.AppendLogsResponse{}, uiapi.NewServiceError("invalid_argument", "stream must be stdout or stderr")
	}

	lease, err := s.deps.Store.GetWorkerLeaseByID(ctx, req.LeaseId)
	if err != nil || lease.WorkerID != wkr.WorkerID {
		return csilapi.AppendLogsResponse{}, uiapi.NewServiceError("not_found", "lease not found for this worker")
	}
	if !lease.IsActive() {
		return csilapi.AppendLogsResponse{}, uiapi.NewServiceError("conflict", "lease is no longer active")
	}

	if s.deps.ObjectStore == nil {
		return csilapi.AppendLogsResponse{}, uiapi.NewServiceError("internal", "object storage is not configured")
	}

	masker := secrets.NewMasker()
	masker.RegisterSecrets(s.secrets.get(lease.LeaseID))

	entries := chunkToLogEntries(req.Chunk, stream, masker)
	if len(entries) == 0 {
		return csilapi.AppendLogsResponse{Ok: true}, nil
	}

	objectKey := fmt.Sprintf("logs/%s/%s.json", lease.JobID, stream)
	totalBytes, err := s.appendLogEntries(ctx, objectKey, entries)
	if err != nil {
		logging.Log.WithError(err).WithFields(map[string]interface{}{"job_id": lease.JobID, "stream": stream}).Error("Failed to append log chunk")
		return csilapi.AppendLogsResponse{}, uiapi.NewServiceError("internal", "failed to persist log chunk")
	}

	if s.deps.Publisher != nil {
		s.deps.Publisher.PublishLogAvailable(ctx, lease.JobID, stream, 0, totalBytes)
	}

	return csilapi.AppendLogsResponse{Ok: true}, nil
}

// appendLogEntries reads the existing JSON-array object at key (if any),
// appends entries, and writes the merged array back -- a read-modify-write
// accumulator, mirroring internal/worker/log_shipper.go's uploadChunk
// exactly (same object layout, same "start fresh on unparsable existing
// data" fallback), just driven by pushed chunks instead of a periodic
// ticker. Returns the new total object size in bytes.
func (s *WorkerService) appendLogEntries(ctx context.Context, objectKey string, entries []worker.LogEntry) (int64, error) {
	var all []worker.LogEntry

	existing, err := s.deps.ObjectStore.Get(ctx, objectKey)
	if err != nil && err != objects.ErrNotFound {
		return 0, fmt.Errorf("failed to read existing log content: %w", err)
	}
	if err == nil {
		defer existing.Close()
		data, readErr := io.ReadAll(existing)
		if readErr != nil {
			return 0, fmt.Errorf("failed to read existing log data: %w", readErr)
		}
		if jsonErr := json.Unmarshal(data, &all); jsonErr != nil {
			logging.Log.WithError(jsonErr).WithField("object_key", objectKey).Warn("Failed to parse existing log data as JSON array, starting fresh")
			all = nil
		}
	}

	all = append(all, entries...)

	jsonData, err := json.Marshal(all)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal log entries: %w", err)
	}
	if err := s.deps.ObjectStore.Put(ctx, objectKey, bytes.NewReader(jsonData), "application/json"); err != nil {
		return 0, fmt.Errorf("failed to upload log chunk: %w", err)
	}
	return int64(len(jsonData)), nil
}

// chunkToLogEntries splits a pushed AppendLogs chunk into worker.LogEntry
// rows, masking secrets as a backstop. A line that is itself a valid
// worker.LogEntry JSON object (matching internal/worker/log_shipper.go's
// parseLogLine behavior for a runnerlib-structured line) is passed through
// with its own timestamp/level; anything else becomes a fresh entry
// timestamped on arrival at the coordinator.
func chunkToLogEntries(chunk, stream string, masker *secrets.Masker) []worker.LogEntry {
	lines := strings.Split(chunk, "\n")
	entries := make([]worker.LogEntry, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		masked := masker.MaskString(line)
		entries = append(entries, parseLogLine(masked, stream))
	}
	return entries
}

func parseLogLine(line, stream string) worker.LogEntry {
	var existing worker.LogEntry
	if err := json.Unmarshal([]byte(line), &existing); err == nil && existing.Timestamp != "" && existing.Message != "" {
		if existing.Stream == "" {
			existing.Stream = stream
		}
		return existing
	}
	return worker.LogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Stream:    stream,
		Level:     "info",
		Message:   line,
	}
}
