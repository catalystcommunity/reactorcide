package jobtelemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
)

const CompactionThreshold = 64

// CompactJob replaces ingress objects with immutable archive objects. It
// writes each archive before it deletes the source objects. Readers remove
// duplicate lease and sequence pairs, so an interrupted delete is safe.
func CompactJob(ctx context.Context, store objects.ObjectStore, jobID string, force bool) error {
	if store == nil {
		return fmt.Errorf("object storage is not configured")
	}
	if err := compactMetrics(ctx, store, jobID, force); err != nil {
		return err
	}
	for _, stream := range []string{"stdout", "stderr"} {
		if err := compactLogs(ctx, store, jobID, stream, force); err != nil {
			return err
		}
	}
	return nil
}

func compactMetrics(ctx context.Context, store objects.ObjectStore, jobID string, force bool) error {
	infos, err := store.List(ctx, fmt.Sprintf("%s/%s/metrics/", ObjectPrefix, jobID))
	if err != nil || (!force && len(infos) < CompactionThreshold) {
		return err
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Key < infos[j].Key })
	archive := metricArchive{}
	keys := make([]string, 0, len(infos))
	if force {
		archiveInfos, listErr := store.List(ctx, fmt.Sprintf("%s/%s/compacted/metrics/", ObjectPrefix, jobID))
		if listErr != nil {
			return listErr
		}
		for _, info := range archiveInfos {
			reader, getErr := store.Get(ctx, info.Key)
			if getErr != nil {
				return getErr
			}
			var prior metricArchive
			decodeErr := json.NewDecoder(reader).Decode(&prior)
			reader.Close()
			if decodeErr != nil {
				continue
			}
			archive.Batches = append(archive.Batches, prior.Batches...)
			keys = append(keys, info.Key)
		}
	}
	for _, info := range infos {
		reader, getErr := store.Get(ctx, info.Key)
		if getErr != nil {
			return getErr
		}
		var batch MetricBatch
		decodeErr := json.NewDecoder(reader).Decode(&batch)
		reader.Close()
		if decodeErr != nil {
			continue
		}
		archive.Batches = append(archive.Batches, batch)
		keys = append(keys, info.Key)
	}
	if len(keys) == 1 && len(infos) == 0 {
		return nil
	}
	return writeArchiveAndDelete(ctx, store, fmt.Sprintf("%s/%s/compacted/metrics/%s.json", ObjectPrefix, jobID, archiveID(keys)), archive, keys)
}

func compactLogs(ctx context.Context, store objects.ObjectStore, jobID, stream string, force bool) error {
	infos, err := store.List(ctx, fmt.Sprintf("%s/%s/logs/", ObjectPrefix, jobID))
	if err != nil {
		return err
	}
	selected := infos[:0]
	for _, info := range infos {
		if strings.Contains(info.Key, "/"+stream+"/") {
			selected = append(selected, info)
		}
	}
	if !force && len(selected) < CompactionThreshold {
		return nil
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Key < selected[j].Key })
	archive := logArchive{}
	keys := make([]string, 0, len(selected))
	if force {
		archiveInfos, listErr := store.List(ctx, fmt.Sprintf("%s/%s/compacted/logs/%s/", ObjectPrefix, jobID, stream))
		if listErr != nil {
			return listErr
		}
		for _, info := range archiveInfos {
			reader, getErr := store.Get(ctx, info.Key)
			if getErr != nil {
				return getErr
			}
			var prior logArchive
			decodeErr := json.NewDecoder(reader).Decode(&prior)
			reader.Close()
			if decodeErr != nil {
				continue
			}
			archive.Batches = append(archive.Batches, prior.Batches...)
			keys = append(keys, info.Key)
		}
	}
	for _, info := range selected {
		reader, getErr := store.Get(ctx, info.Key)
		if getErr != nil {
			return getErr
		}
		var batch LogBatch
		decodeErr := json.NewDecoder(reader).Decode(&batch)
		reader.Close()
		if decodeErr != nil {
			continue
		}
		archive.Batches = append(archive.Batches, batch)
		keys = append(keys, info.Key)
	}
	if len(keys) == 1 && len(selected) == 0 {
		return nil
	}
	return writeArchiveAndDelete(ctx, store, fmt.Sprintf("%s/%s/compacted/logs/%s/%s.json", ObjectPrefix, jobID, stream, archiveID(keys)), archive, keys)
}

func writeArchiveAndDelete(ctx context.Context, store objects.ObjectStore, key string, archive any, sources []string) error {
	if len(sources) == 0 {
		return nil
	}
	data, err := json.Marshal(archive)
	if err != nil {
		return err
	}
	if err := store.Put(ctx, key, bytes.NewReader(data), "application/json"); err != nil {
		return fmt.Errorf("write telemetry archive: %w", err)
	}
	for _, source := range sources {
		if err := store.Delete(ctx, source); err != nil && err != objects.ErrNotFound {
			return fmt.Errorf("delete compacted telemetry: %w", err)
		}
	}
	return nil
}

func archiveID(keys []string) string {
	sum := sha256.Sum256([]byte(strings.Join(keys, "\x00")))
	return hex.EncodeToString(sum[:12])
}
