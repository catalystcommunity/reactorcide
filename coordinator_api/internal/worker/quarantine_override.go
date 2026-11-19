package worker

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// quarantineOverrideReason is the node reason of a quarantine override.
const quarantineOverrideReason = "job failed, but every failed test is quarantined (quarantine_policy: ignore_quarantined)"

// quarantineOverride reports whether the workflow node of a failed job
// counts as a success (WP15, docs/artifacts-and-tests.md). The coordinator
// computes the decision when the job result arrives and stores it in the
// test summary of the job; only a node with quarantine_policy
// ignore_quarantined can get it.
func (tp *TriggerProcessor) quarantineOverride(ctx context.Context, job *models.Job) bool {
	if job == nil || job.Status != "failed" {
		return false
	}
	results, ok := tp.store.(interface {
		GetJobTestSummary(ctx context.Context, jobID string) (*models.JobTestSummary, error)
	})
	if !ok {
		return false
	}
	summary, err := results.GetJobTestSummary(ctx, job.JobID)
	if err != nil || summary == nil {
		return false
	}
	return summary.QuarantineOverride
}
