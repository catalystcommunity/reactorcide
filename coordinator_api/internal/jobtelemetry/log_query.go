package jobtelemetry

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
)

const (
	DefaultLogPageSize = 1000
	MaxLogPageSize     = 5000
)

type logCandidate struct {
	batch    LogBatch
	index    int
	observed time.Time
}

// QueryLogs returns entries that the supplied cursor has not acknowledged.
// The cursor records each lease and stream independently. A late retry of an
// older batch remains visible after newer batches arrive.
func QueryLogs(ctx context.Context, store objects.ObjectStore, jobID, stream, rawCursor string, limit int) (LogPage, error) {
	if stream != "stdout" && stream != "stderr" && stream != "combined" {
		return LogPage{}, errors.New("stream must be stdout, stderr, or combined")
	}
	if limit <= 0 {
		limit = DefaultLogPageSize
	}
	if limit > MaxLogPageSize {
		limit = MaxLogPageSize
	}
	cursor, err := decodeCursor(rawCursor, "logs", jobID, stream)
	if err != nil {
		return LogPage{}, err
	}
	tracks := cursorTracks(&cursor)
	batches, err := readLogBatches(ctx, store, jobID)
	if err != nil {
		return LogPage{}, err
	}

	queues := map[string][]logCandidate{}
	for _, batch := range batches {
		if stream != "combined" && batch.Stream != stream {
			continue
		}
		track := ensureCursorTrack(&cursor, tracks, batch.LeaseID, batch.Stream)
		if trackHasSequence(track, batch.Sequence) {
			continue
		}
		start := partialEntries(track, batch.Sequence)
		if start >= len(batch.Entries) {
			completePartialSequence(track, batch.Sequence)
			continue
		}
		key := cursorTrackKey(batch.LeaseID, batch.Stream)
		for index := start; index < len(batch.Entries); index++ {
			queues[key] = append(queues[key], logCandidate{batch: batch, index: index, observed: batch.Entries[index].ObservedAt})
		}
	}
	for key := range queues {
		sort.SliceStable(queues[key], func(i, j int) bool {
			if queues[key][i].batch.Sequence == queues[key][j].batch.Sequence {
				return queues[key][i].index < queues[key][j].index
			}
			return queues[key][i].batch.Sequence < queues[key][j].batch.Sequence
		})
	}

	page := LogPage{Complete: true}
	for len(page.Entries) < limit {
		var selectedKey string
		var selected logCandidate
		found := false
		for key, queue := range queues {
			if len(queue) == 0 {
				continue
			}
			candidate := queue[0]
			if !found || candidate.observed.Before(selected.observed) || (candidate.observed.Equal(selected.observed) && key < selectedKey) {
				selectedKey, selected, found = key, candidate, true
			}
		}
		if !found {
			break
		}
		queues[selectedKey] = queues[selectedKey][1:]
		page.Entries = append(page.Entries, LogResultEntry{LogEntry: selected.batch.Entries[selected.index], Stream: selected.batch.Stream})
		track := tracks[selectedKey]
		consumed := selected.index + 1
		if consumed == len(selected.batch.Entries) {
			completePartialSequence(track, selected.batch.Sequence)
		} else {
			setPartialEntries(track, selected.batch.Sequence, consumed)
		}
	}
	for _, queue := range queues {
		if len(queue) > 0 {
			page.HasMore = true
			break
		}
	}
	page.NextCursor, err = encodeCursor(cursor)
	if err != nil {
		return LogPage{}, err
	}
	return page, nil
}
