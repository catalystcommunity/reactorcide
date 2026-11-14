package store

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// TerminalJobStatuses are the statuses of a finished job. Only a finished job
// has a resource summary.
var TerminalJobStatuses = []string{"completed", "failed", "cancelled", "timeout"}

// JobRunHistoryFilter selects the runs of one job for a resource history read.
// A "job" is every run with the same organization, project and name.
type JobRunHistoryFilter struct {
	OrgID string
	// ProjectID nil selects runs with no project.
	ProjectID *string
	Name      string
	// From and To bound created_at, inclusive. Nil means unbounded.
	From *time.Time
	To   *time.Time
	// Limit is the number of newest runs to return.
	Limit int
}

// ResourceSummaryStore stores the per-run resource summaries.
//
// It is separate from Store, so the many full Store fakes do not each need
// these methods. The Postgres store implements it; a caller type-asserts and
// treats a store without it as "history is not available".
type ResourceSummaryStore interface {
	// ListJobRunsWithSummaries returns finished runs matching the filter,
	// newest first, each with its summary (nil when none is stored).
	ListJobRunsWithSummaries(ctx context.Context, filter JobRunHistoryFilter) ([]models.JobRunWithSummary, error)
	// UpsertJobResourceSummary inserts or replaces the summary of one job.
	UpsertJobResourceSummary(ctx context.Context, summary *models.JobResourceSummary) error
}
