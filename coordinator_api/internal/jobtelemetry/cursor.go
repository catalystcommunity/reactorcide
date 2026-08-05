package jobtelemetry

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

const (
	cursorVersion   = 1
	maxCursorLength = 64 * 1024
	maxCursorTracks = 512
)

var ErrInvalidCursor = errors.New("invalid telemetry cursor")

type cursorEnvelope struct {
	Version   int            `json:"v"`
	Kind      string         `json:"kind"`
	JobID     string         `json:"job_id"`
	Stream    string         `json:"stream,omitempty"`
	Positions []*cursorTrack `json:"positions,omitempty"`
}

type cursorTrack struct {
	LeaseID string          `json:"lease_id"`
	Stream  string          `json:"stream,omitempty"`
	Through int64           `json:"through"`
	Seen    []int64         `json:"seen,omitempty"`
	Partial []cursorPartial `json:"partial,omitempty"`
}

type cursorPartial struct {
	Sequence int64 `json:"sequence"`
	Entries  int   `json:"entries"`
}

func newCursor(kind, jobID, stream string) cursorEnvelope {
	return cursorEnvelope{Version: cursorVersion, Kind: kind, JobID: jobID, Stream: stream}
}

func decodeCursor(raw, kind, jobID, stream string) (cursorEnvelope, error) {
	if raw == "" {
		return newCursor(kind, jobID, stream), nil
	}
	if len(raw) > maxCursorLength {
		return cursorEnvelope{}, fmt.Errorf("%w: cursor is too large", ErrInvalidCursor)
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursorEnvelope{}, fmt.Errorf("%w: cursor is invalid", ErrInvalidCursor)
	}
	var cursor cursorEnvelope
	if err := json.Unmarshal(data, &cursor); err != nil {
		return cursorEnvelope{}, fmt.Errorf("%w: cursor is invalid", ErrInvalidCursor)
	}
	if cursor.Version != cursorVersion || cursor.Kind != kind || cursor.JobID != jobID || cursor.Stream != stream {
		return cursorEnvelope{}, fmt.Errorf("%w: cursor does not match this query", ErrInvalidCursor)
	}
	if len(cursor.Positions) > maxCursorTracks {
		return cursorEnvelope{}, fmt.Errorf("%w: cursor has too many positions", ErrInvalidCursor)
	}
	for _, position := range cursor.Positions {
		if position == nil || position.LeaseID == "" || position.Through < -1 || len(position.Seen) > maxCursorTracks || len(position.Partial) > maxCursorTracks {
			return cursorEnvelope{}, fmt.Errorf("%w: cursor position is invalid", ErrInvalidCursor)
		}
		for _, sequence := range position.Seen {
			if sequence < 0 {
				return cursorEnvelope{}, fmt.Errorf("%w: cursor sequence is invalid", ErrInvalidCursor)
			}
		}
		for _, partial := range position.Partial {
			if partial.Sequence < 0 || partial.Entries < 0 {
				return cursorEnvelope{}, fmt.Errorf("%w: cursor partial position is invalid", ErrInvalidCursor)
			}
		}
	}
	return cursor, nil
}

func encodeCursor(cursor cursorEnvelope) (string, error) {
	sort.Slice(cursor.Positions, func(i, j int) bool {
		if cursor.Positions[i].LeaseID == cursor.Positions[j].LeaseID {
			return cursor.Positions[i].Stream < cursor.Positions[j].Stream
		}
		return cursor.Positions[i].LeaseID < cursor.Positions[j].LeaseID
	})
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > maxCursorLength {
		return "", errors.New("cursor is too large")
	}
	return encoded, nil
}

func cursorTrackKey(leaseID, stream string) string {
	return leaseID + "\x00" + stream
}

func cursorTracks(cursor *cursorEnvelope) map[string]*cursorTrack {
	tracks := make(map[string]*cursorTrack, len(cursor.Positions))
	for _, track := range cursor.Positions {
		tracks[cursorTrackKey(track.LeaseID, track.Stream)] = track
	}
	return tracks
}

func ensureCursorTrack(cursor *cursorEnvelope, tracks map[string]*cursorTrack, leaseID, stream string) *cursorTrack {
	key := cursorTrackKey(leaseID, stream)
	if track := tracks[key]; track != nil {
		return track
	}
	track := &cursorTrack{LeaseID: leaseID, Stream: stream, Through: -1}
	cursor.Positions = append(cursor.Positions, track)
	tracks[key] = track
	return track
}

func trackHasSequence(track *cursorTrack, sequence int64) bool {
	if track == nil {
		return false
	}
	if sequence <= track.Through {
		return true
	}
	for _, seen := range track.Seen {
		if seen == sequence {
			return true
		}
	}
	return false
}

func markSequenceSeen(track *cursorTrack, sequence int64) {
	if sequence <= track.Through || trackHasSequence(track, sequence) {
		return
	}
	track.Seen = append(track.Seen, sequence)
	sort.Slice(track.Seen, func(i, j int) bool { return track.Seen[i] < track.Seen[j] })
	for len(track.Seen) > 0 && track.Seen[0] == track.Through+1 {
		track.Through = track.Seen[0]
		track.Seen = track.Seen[1:]
	}
}

func partialEntries(track *cursorTrack, sequence int64) int {
	if track == nil {
		return 0
	}
	for _, partial := range track.Partial {
		if partial.Sequence == sequence {
			return partial.Entries
		}
	}
	return 0
}

func setPartialEntries(track *cursorTrack, sequence int64, entries int) {
	for i := range track.Partial {
		if track.Partial[i].Sequence == sequence {
			track.Partial[i].Entries = entries
			return
		}
	}
	track.Partial = append(track.Partial, cursorPartial{Sequence: sequence, Entries: entries})
}

func completePartialSequence(track *cursorTrack, sequence int64) {
	for i := range track.Partial {
		if track.Partial[i].Sequence == sequence {
			track.Partial = append(track.Partial[:i], track.Partial[i+1:]...)
			break
		}
	}
	markSequenceSeen(track, sequence)
}
