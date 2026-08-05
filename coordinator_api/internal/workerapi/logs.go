package workerapi

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// AppendLogs is push log ingestion: resolves the caller's session, verifies
// the lease belongs to it and is still active, and converts the old request
// to one immutable batch. New workers use AppendLogBatch directly. This
// operation remains for old workers.
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

	sequence := legacyLogSequence()
	batch := jobtelemetry.LogBatch{LeaseID: lease.LeaseID, Stream: stream, Sequence: sequence}
	for _, entry := range entries {
		observedAt, parseErr := time.Parse(time.RFC3339Nano, entry.Timestamp)
		if parseErr != nil {
			observedAt = time.Now().UTC()
		}
		batch.Entries = append(batch.Entries, jobtelemetry.LogEntry{
			ObservedAt: observedAt.UTC(), Level: entry.Level, Message: entry.Message,
		})
	}
	if err := jobtelemetry.ValidateLogBatch(&batch); err != nil {
		return csilapi.AppendLogsResponse{}, uiapi.NewServiceError("invalid_argument", err.Error())
	}
	err = jobtelemetry.PutLogBatch(ctx, s.deps.ObjectStore, lease.JobID, batch)
	if err != nil {
		logging.Log.WithError(err).WithFields(map[string]interface{}{"job_id": lease.JobID, "stream": stream}).Error("Failed to append log chunk")
		return csilapi.AppendLogsResponse{}, uiapi.NewServiceError("internal", "failed to persist log chunk")
	}

	if s.deps.Publisher != nil {
		s.deps.Publisher.PublishLogAvailable(ctx, lease.JobID, stream, sequence, int64(len(req.Chunk)))
	}

	return csilapi.AppendLogsResponse{Ok: true}, nil
}

func legacyLogSequence() int64 {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return int64(binary.BigEndian.Uint64(raw[:]) & (1<<63 - 1))
	}
	return time.Now().UTC().UnixNano()
}

// chunkToLogEntries splits a pushed AppendLogs chunk into worker.LogEntry
// rows, masking secrets as a backstop. A line that is itself a valid
// worker.LogEntry JSON object (a runnerlib-structured line) is passed
// through with its own timestamp/level; anything else becomes a fresh entry
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
