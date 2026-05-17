package vcs

import (
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestDedupeJobsByName(t *testing.T) {
	// Helper to build a job with a name and a created-at offset (seconds).
	mk := func(id, name string, offsetSec int, status string) models.Job {
		return models.Job{
			JobID:     id,
			Name:      name,
			Status:    status,
			CreatedAt: time.Unix(int64(offsetSec), 0),
		}
	}

	tests := []struct {
		name    string
		in      []models.Job
		wantIDs []string
	}{
		{
			name:    "empty input",
			in:      nil,
			wantIDs: nil,
		},
		{
			name:    "single job passes through",
			in:      []models.Job{mk("a", "eval", 0, "completed")},
			wantIDs: []string{"a"},
		},
		{
			name: "no duplicates: all preserved in order",
			in: []models.Job{
				mk("a", "eval", 0, "completed"),
				mk("b", "test-go", 1, "completed"),
				mk("c", "test-py", 2, "completed"),
			},
			wantIDs: []string{"a", "b", "c"},
		},
		{
			name: "retry of same name: keep latest, position of first occurrence is replaced",
			in: []models.Job{
				mk("old-eval", "eval", 0, "completed"),
				mk("old-cc", "conventional-commits", 1, "failed"),
				mk("new-eval", "eval", 10, "completed"),
				mk("new-cc", "conventional-commits", 11, "completed"),
				mk("test-go", "test-go", 12, "completed"),
			},
			// Expected order: the newer eval and conventional-commits are the
			// kept ones (later CreatedAt), so they appear in their positions
			// in the original ASC stream — slots 2 and 3 — followed by test-go.
			// The older runs are dropped entirely.
			wantIDs: []string{"new-eval", "new-cc", "test-go"},
		},
		{
			name: "jobs with empty name dedupe by JobID (never collapsed together)",
			in: []models.Job{
				mk("a", "", 0, "completed"),
				mk("b", "", 1, "completed"),
			},
			wantIDs: []string{"a", "b"},
		},
		{
			name: "different-but-similar names (e.g. eval opened vs updated) stay separate",
			in: []models.Job{
				mk("e1", "eval: PR #60 opened on org/repo", 0, "completed"),
				mk("e2", "eval: PR #60 updated on org/repo", 1, "completed"),
			},
			wantIDs: []string{"e1", "e2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupeJobsByName(tt.in)
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("dedupeJobsByName: got %d jobs, want %d (got IDs: %v)", len(got), len(tt.wantIDs), jobIDs(got))
			}
			for i, want := range tt.wantIDs {
				if got[i].JobID != want {
					t.Errorf("position %d: got JobID %q, want %q (full order: %v)", i, got[i].JobID, want, jobIDs(got))
				}
			}
		})
	}
}

func jobIDs(jobs []models.Job) []string {
	out := make([]string, len(jobs))
	for i, j := range jobs {
		out[i] = j.JobID
	}
	return out
}
