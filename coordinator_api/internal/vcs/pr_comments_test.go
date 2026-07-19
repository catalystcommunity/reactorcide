package vcs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/stretchr/testify/mock"
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

func TestLegacyPRCommentsIncludeDeprecationNotice(t *testing.T) {
	updater := NewJobStatusUpdater()
	job := models.Job{
		JobID:     "job-1",
		Name:      "legacy-job",
		Status:    "completed",
		CreatedAt: time.Unix(0, 0),
	}
	exitCode := 0
	job.ExitCode = &exitCode

	rolling := updater.renderRollingCommentBody([]models.Job{job}, "abcdef123456", "<!-- marker -->")
	if !strings.Contains(rolling, deprecatedJobFlowNotice) {
		t.Fatalf("rolling legacy comment should include deprecation notice, got:\n%s", rolling)
	}

	perJob := updater.renderPerJobCommentBody(&job, "<!-- marker -->")
	if !strings.Contains(perJob, deprecatedJobFlowNotice) {
		t.Fatalf("per-job legacy comment should include deprecation notice, got:\n%s", perJob)
	}
}

// TestPostPerJobComment_RetryUpdatesSameCommentInPlace verifies the
// post-merge per-job comment marker is stable across a retry: a retried job
// (jobcontrol.RetryJob clones a brand-new JobID but carries the same Name
// forward) must update the SAME PR comment the original job posted, not
// create a second one alongside it — last run wins, no duplicate rows. A
// different job name on the same commit still gets its own distinct marker.
func TestPostPerJobComment_RetryUpdatesSameCommentInPlace(t *testing.T) {
	updater := NewJobStatusUpdater()
	mockClient := new(MockClient)

	metadata := &JobMetadata{Repo: "owner/repo", PRNumber: 42, CommitSHA: "abc123def456"}
	original := &models.Job{JobID: "job-orig", Name: "deploy", Status: "failed"}
	parentID := original.JobID
	retried := &models.Job{JobID: "job-retry-1", Name: "deploy", Status: "completed", ParentJobID: &parentID}
	other := &models.Job{JobID: "job-other", Name: "lint", Status: "completed"}

	var markers []string
	mockClient.On("UpsertPRCommentByMarker", mock.Anything, metadata.Repo, metadata.PRNumber, mock.AnythingOfType("string"), mock.AnythingOfType("string")).
		Run(func(args mock.Arguments) {
			markers = append(markers, args.String(3))
		}).
		Return(nil)

	updater.postPerJobComment(context.Background(), mockClient, original, metadata)
	updater.postPerJobComment(context.Background(), mockClient, retried, metadata)
	updater.postPerJobComment(context.Background(), mockClient, other, metadata)

	if len(markers) != 3 {
		t.Fatalf("expected 3 UpsertPRCommentByMarker calls, got %d: %v", len(markers), markers)
	}
	if markers[0] != markers[1] {
		t.Errorf("expected the retried job's comment update to target the same marker as the original job (last run wins, no duplicate comment), got %q vs %q", markers[0], markers[1])
	}
	if markers[2] == markers[0] {
		t.Errorf("expected a different job name to get a distinct comment marker, got %q for both", markers[2])
	}
}

func jobIDs(jobs []models.Job) []string {
	out := make([]string, len(jobs))
	for i, j := range jobs {
		out[i] = j.JobID
	}
	return out
}
