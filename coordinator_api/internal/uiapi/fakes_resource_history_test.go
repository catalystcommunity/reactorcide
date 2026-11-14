package uiapi

import (
	"context"
	"sort"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// summaries returns the lazily made summary map. Callers hold f.mu.
func (f *fakeStore) summaries() map[string]models.JobResourceSummary {
	if f.resourceSummaries == nil {
		f.resourceSummaries = map[string]models.JobResourceSummary{}
	}
	return f.resourceSummaries
}

func (f *fakeStore) ListJobRunsWithSummaries(_ context.Context, filter store.JobRunHistoryFilter) ([]models.JobRunWithSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	terminal := map[string]bool{}
	for _, status := range store.TerminalJobStatuses {
		terminal[status] = true
	}
	var jobs []models.Job
	for _, job := range f.jobs {
		if job.OrgID != filter.OrgID || job.Name != filter.Name || !terminal[job.Status] {
			continue
		}
		if (filter.ProjectID == nil) != (job.ProjectID == nil) || (filter.ProjectID != nil && *filter.ProjectID != *job.ProjectID) {
			continue
		}
		if filter.From != nil && job.CreatedAt.Before(*filter.From) {
			continue
		}
		if filter.To != nil && job.CreatedAt.After(*filter.To) {
			continue
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
	if len(jobs) > filter.Limit {
		jobs = jobs[:filter.Limit]
	}
	out := make([]models.JobRunWithSummary, 0, len(jobs))
	for _, job := range jobs {
		run := models.JobRunWithSummary{Job: job}
		if summary, ok := f.summaries()[job.JobID]; ok {
			copy := summary
			run.Summary = &copy
		}
		out = append(out, run)
	}
	return out, nil
}

func (f *fakeStore) UpsertJobResourceSummary(_ context.Context, summary *models.JobResourceSummary) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summaries()[summary.JobID] = *summary
	return nil
}
