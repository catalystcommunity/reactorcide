package uiapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
)

// GetJobLogs returns logs after it applies the same visibility rule as metrics.
func (s *UiService) GetJobLogs(ctx context.Context, req csilapi.GetJobLogsRequest) (csilapi.GetJobLogsResponse, error) {
	identity, _ := s.deps.resolveIdentity(ctx)
	job, err := s.deps.Store.GetJobByID(ctx, req.JobId)
	if err != nil {
		return csilapi.GetJobLogsResponse{}, mapStoreErr(err, "job not found")
	}
	visible, err := s.deps.Resolver.CanViewJob(ctx, identity, job)
	if err != nil {
		return csilapi.GetJobLogsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !visible {
		return csilapi.GetJobLogsResponse{}, NewServiceError("forbidden", "you do not have permission to view this job")
	}
	if req.Stream != "stdout" && req.Stream != "stderr" && req.Stream != "combined" {
		return csilapi.GetJobLogsResponse{}, NewServiceError("invalid_argument", "stream must be stdout, stderr, or combined")
	}
	if s.deps.ObjectStore == nil {
		return csilapi.GetJobLogsResponse{}, NewServiceError("internal", "object storage is not configured")
	}
	streams := []string{req.Stream}
	if req.Stream == "combined" {
		streams = []string{"stdout", "stderr"}
	}
	var entries []csilapi.JobLogEntry
	for _, stream := range streams {
		batchEntries, readErr := jobtelemetry.ReadLogEntries(ctx, s.deps.ObjectStore, job.JobID, stream)
		if readErr != nil {
			return csilapi.GetJobLogsResponse{}, NewServiceError("internal", "failed to read job logs")
		}
		for _, entry := range batchEntries {
			entries = append(entries, csilapi.JobLogEntry{Timestamp: entry.ObservedAt.Format(timeLayout), Stream: stream, Level: entry.Level, Message: entry.Message})
		}
		if len(batchEntries) == 0 {
			legacy, legacyErr := readLegacyLogs(ctx, s.deps.ObjectStore, job.JobID, stream)
			if legacyErr != nil {
				return csilapi.GetJobLogsResponse{}, NewServiceError("internal", "failed to read job logs")
			}
			entries = append(entries, legacy...)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Timestamp < entries[j].Timestamp })
	return csilapi.GetJobLogsResponse{Entries: entries}, nil
}

const timeLayout = "2006-01-02T15:04:05.999999999Z07:00"

func readLegacyLogs(ctx context.Context, store objects.ObjectStore, jobID, stream string) ([]csilapi.JobLogEntry, error) {
	reader, err := store.Get(ctx, fmt.Sprintf("logs/%s/%s.json", jobID, stream))
	if errors.Is(err, objects.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var old []worker.LogEntry
	if err := json.NewDecoder(reader).Decode(&old); err != nil {
		return nil, err
	}
	entries := make([]csilapi.JobLogEntry, 0, len(old))
	for _, entry := range old {
		entries = append(entries, csilapi.JobLogEntry{Timestamp: entry.Timestamp, Stream: entry.Stream, Level: entry.Level, Message: entry.Message})
	}
	return entries, nil
}
