package worker

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// JobProcessorInterface defines the interface for job processing
type JobProcessorInterface interface {
	ProcessJob(ctx context.Context, job *models.Job) *JobResult
}

// Ensure JobProcessor implements JobProcessorInterface
var _ JobProcessorInterface = (*JobProcessor)(nil)
