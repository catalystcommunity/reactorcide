package jobcontrol

import (
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// StateError is a refused retry or re-run: the job or the workflow is not in
// a state that allows it. Its message says which states allow it and the
// current status, for the caller to read (REST and CSIL). It unwraps to the
// sentinel (ErrNotRetryable, ErrWorkflowNotRetryable, or
// ErrWorkflowNotRerunnable), so errors.Is works.
type StateError struct {
	sentinel error
	message  string
}

func (e *StateError) Error() string { return e.message }
func (e *StateError) Unwrap() error { return e.sentinel }

func statusOf(status string) string {
	if status == "" {
		return "unknown"
	}
	return status
}

// JobNotRetryable is the error of a retry of job in a state that does not
// allow it (models.Job.IsRetryable).
func JobNotRetryable(job *models.Job) error {
	status, id := "", ""
	if job != nil {
		status, id = job.Status, job.JobID
	}
	return &StateError{sentinel: ErrNotRetryable, message: fmt.Sprintf(
		"only a failed, cancelled, or timed-out job can be retried; job %s is %s", id, statusOf(status))}
}

// WorkflowNotRetryable is the error of a retry of wf in a state that does
// not allow it (models.WorkflowInstance.IsRetryable).
func WorkflowNotRetryable(wf *models.WorkflowInstance) error {
	status, id := "", ""
	if wf != nil {
		status, id = wf.Status, wf.WorkflowID
	}
	return &StateError{sentinel: ErrWorkflowNotRetryable, message: fmt.Sprintf(
		"only a failed or cancelled workflow can be retried; workflow %s is %s", id, statusOf(status))}
}

// WorkflowNotRerunnable is the error of a re-run of wf before it finished
// (IsWorkflowRerunnable).
func WorkflowNotRerunnable(wf *models.WorkflowInstance) error {
	status, id := "", ""
	if wf != nil {
		status, id = wf.Status, wf.WorkflowID
	}
	return &StateError{sentinel: ErrWorkflowNotRerunnable, message: fmt.Sprintf(
		"only a finished (completed, failed, cancelled, or timed-out) workflow can be re-run; workflow %s is %s",
		id, statusOf(status))}
}
