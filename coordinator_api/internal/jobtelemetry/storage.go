package jobtelemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
)

var ErrSequenceConflict = errors.New("telemetry sequence content conflicts with existing content")

func MetricObjectKey(jobID, leaseID string, sequence int64) string {
	return fmt.Sprintf("%s/%s/metrics/%s/%020d.json", ObjectPrefix, jobID, leaseID, sequence)
}

func LogObjectKey(jobID, leaseID, stream string, sequence int64) string {
	return fmt.Sprintf("%s/%s/logs/%s/%s/%020d.json", ObjectPrefix, jobID, leaseID, stream, sequence)
}

func PutMetricBatch(ctx context.Context, store objects.ObjectStore, jobID string, batch MetricBatch) error {
	return putImmutableJSON(ctx, store, MetricObjectKey(jobID, batch.LeaseID, batch.Sequence), batch)
}

func PutLogBatch(ctx context.Context, store objects.ObjectStore, jobID string, batch LogBatch) error {
	return putImmutableJSON(ctx, store, LogObjectKey(jobID, batch.LeaseID, batch.Stream, batch.Sequence), batch)
}

func putImmutableJSON(ctx context.Context, store objects.ObjectStore, key string, value any) error {
	if store == nil {
		return errors.New("object storage is not configured")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode telemetry batch: %w", err)
	}
	existing, err := store.Get(ctx, key)
	if err == nil {
		defer existing.Close()
		old, readErr := io.ReadAll(existing)
		if readErr != nil {
			return fmt.Errorf("read telemetry sequence: %w", readErr)
		}
		if !bytes.Equal(old, data) {
			return fmt.Errorf("%w: old=%s new=%s", ErrSequenceConflict, digest(old), digest(data))
		}
		return nil
	}
	if !errors.Is(err, objects.ErrNotFound) {
		return fmt.Errorf("check telemetry sequence: %w", err)
	}
	if err := store.Put(ctx, key, bytes.NewReader(data), "application/json"); err != nil {
		return fmt.Errorf("write telemetry batch: %w", err)
	}
	return nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func ReadLogEntries(ctx context.Context, store objects.ObjectStore, jobID, stream string) ([]LogEntry, error) {
	if stream != "stdout" && stream != "stderr" {
		return nil, errors.New("stream must be stdout or stderr")
	}
	infos, err := store.List(ctx, fmt.Sprintf("%s/%s/logs/", ObjectPrefix, jobID))
	if err != nil {
		return nil, err
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Key < infos[j].Key })
	entries := []LogEntry{}
	seen := map[string]bool{}
	archiveInfos, err := store.List(ctx, fmt.Sprintf("%s/%s/compacted/logs/%s/", ObjectPrefix, jobID, stream))
	if err != nil {
		return nil, err
	}
	for _, info := range archiveInfos {
		reader, getErr := store.Get(ctx, info.Key)
		if getErr != nil {
			return nil, getErr
		}
		var archive logArchive
		decodeErr := json.NewDecoder(reader).Decode(&archive)
		reader.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		for _, batch := range archive.Batches {
			id := fmt.Sprintf("%s\x00%d\x00%s", batch.LeaseID, batch.Sequence, batch.Stream)
			if batch.Stream == stream && !seen[id] {
				entries = append(entries, batch.Entries...)
				seen[id] = true
			}
		}
	}
	for _, info := range infos {
		if !strings.Contains(info.Key, "/"+stream+"/") {
			continue
		}
		reader, getErr := store.Get(ctx, info.Key)
		if getErr != nil {
			return nil, getErr
		}
		var batch LogBatch
		decodeErr := json.NewDecoder(reader).Decode(&batch)
		reader.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		id := fmt.Sprintf("%s\x00%d\x00%s", batch.LeaseID, batch.Sequence, batch.Stream)
		if !seen[id] {
			entries = append(entries, batch.Entries...)
			seen[id] = true
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ObservedAt.Before(entries[j].ObservedAt) })
	return entries, nil
}

func readMetricBatches(ctx context.Context, store objects.ObjectStore, jobID string) ([]MetricBatch, bool, error) {
	complete := true
	batches := []MetricBatch{}
	seen := map[string]bool{}
	archiveInfos, err := store.List(ctx, fmt.Sprintf("%s/%s/compacted/metrics/", ObjectPrefix, jobID))
	if err != nil {
		return nil, false, err
	}
	for _, info := range archiveInfos {
		reader, getErr := store.Get(ctx, info.Key)
		if getErr != nil {
			complete = false
			continue
		}
		var archive metricArchive
		decodeErr := json.NewDecoder(reader).Decode(&archive)
		reader.Close()
		if decodeErr != nil {
			complete = false
			continue
		}
		for _, batch := range archive.Batches {
			id := fmt.Sprintf("%s\x00%d", batch.LeaseID, batch.Sequence)
			if !seen[id] {
				batches = append(batches, batch)
				seen[id] = true
			}
		}
	}
	infos, err := store.List(ctx, fmt.Sprintf("%s/%s/metrics/", ObjectPrefix, jobID))
	if err != nil {
		return nil, false, err
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Key < infos[j].Key })
	for _, info := range infos {
		reader, getErr := store.Get(ctx, info.Key)
		if getErr != nil {
			complete = false
			continue
		}
		var batch MetricBatch
		decodeErr := json.NewDecoder(reader).Decode(&batch)
		reader.Close()
		if decodeErr != nil {
			complete = false
			continue
		}
		id := fmt.Sprintf("%s\x00%d", batch.LeaseID, batch.Sequence)
		if !seen[id] {
			batches = append(batches, batch)
			seen[id] = true
		}
	}
	return batches, complete, nil
}

// DeleteJob removes all immutable telemetry objects for one job.
func DeleteJob(ctx context.Context, store objects.ObjectStore, jobID string) error {
	for _, prefix := range []string{fmt.Sprintf("%s/%s/", ObjectPrefix, jobID), fmt.Sprintf("logs/%s/", jobID)} {
		infos, err := store.List(ctx, prefix)
		if err != nil {
			return fmt.Errorf("list job telemetry: %w", err)
		}
		for _, info := range infos {
			if err := store.Delete(ctx, info.Key); err != nil && !errors.Is(err, objects.ErrNotFound) {
				return fmt.Errorf("delete job telemetry %q: %w", info.Key, err)
			}
		}
	}
	return nil
}
