package worker

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// MockJobStatusUpdater implements vcs.JobStatusUpdaterInterface for testing.
// Originally defined alongside the (now-removed) legacy direct-corndogs
// worker tests; kept here as a shared test helper (e.g. embedded by
// workflow_runtime_test.go's workflowRuntimeStatusUpdater).
type MockJobStatusUpdater struct {
	UpdateJobStatusFunc  func(ctx context.Context, job *models.Job) error
	UpdateJobStatusCalls []models.Job
}

func (m *MockJobStatusUpdater) UpdateJobStatus(ctx context.Context, job *models.Job) error {
	m.UpdateJobStatusCalls = append(m.UpdateJobStatusCalls, *job)
	if m.UpdateJobStatusFunc != nil {
		return m.UpdateJobStatusFunc(ctx, job)
	}
	return nil
}

// Ensure MockJobStatusUpdater satisfies vcs.JobStatusUpdaterInterface
var _ vcs.JobStatusUpdaterInterface = (*MockJobStatusUpdater)(nil)
