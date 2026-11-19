// Retries and re-runs with shifts (owner decisions D5, D18, D25).
//
// A retry or a re-run can change only the shifts: the CPU request and limit,
// the memory limit, the ephemeral storage request and limit, and the timeout
// (see internal/runshifts). The new run keeps the authority of the original
// run, because every authority field is cloned from the original row and the
// shifts touch only resource and timeout fields. The re-runner needs only the
// permission to retry; shifts need no extra permission (D5). The
// rerun_shift_limits setting (organization, with a project override) bounds
// the shift values, and the execution profile ceilings still apply.
package jobcontrol

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
)

// ErrWorkflowNotRerunnable is returned for a re-run of a workflow that has not
// finished.
var ErrWorkflowNotRerunnable = errors.New("workflow cannot be re-run until it is finished")

// IsShiftError reports whether err is a refused or invalid shift. Callers map
// it to a client error (HTTP 400, CSIL invalid_argument).
func IsShiftError(err error) bool {
	return errors.Is(err, runshifts.ErrRefused) || errors.Is(err, runshifts.ErrInvalid) || errors.Is(err, runshifts.ErrOverLimit)
}

// RetryJobWithShifts is RetryJob with shifts. Zero shifts is a plain retry.
func RetryJobWithShifts(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, job *models.Job, shifts models.RunShifts) (*models.Job, error) {
	return retryJob(ctx, st, corndogsClient, job, shifts)
}

// RetryWorkflowWithShifts is RetryWorkflow with shifts: every job of the new
// instance gets them.
func RetryWorkflowWithShifts(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, workflowID string, shifts models.RunShifts) (*models.WorkflowInstance, error) {
	return retryWorkflow(ctx, st, corndogsClient, workflowID, shifts, false)
}

// RerunWorkflow runs a finished workflow again as a new instance, with the
// recorded authority of the original instance and optional shifts. Unlike
// RetryWorkflow it also accepts a workflow that completed.
func RerunWorkflow(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, workflowID string, shifts models.RunShifts) (*models.WorkflowInstance, error) {
	return retryWorkflow(ctx, st, corndogsClient, workflowID, shifts, true)
}

// RetryUnsuccessfulJobsWithShifts is RetryUnsuccessfulJobs with shifts for
// each retried job.
func RetryUnsuccessfulJobsWithShifts(ctx context.Context, st store.Store, corndogsClient corndogs.ClientInterface, workflowID string, shifts models.RunShifts) ([]*models.Job, error) {
	return retryUnsuccessfulJobs(ctx, st, corndogsClient, workflowID, shifts)
}

// IsWorkflowRerunnable reports whether a workflow instance is finished.
func IsWorkflowRerunnable(wf *models.WorkflowInstance) bool {
	if wf == nil {
		return false
	}
	switch wf.Status {
	case "completed", "failed", "cancelled", "timeout":
		return true
	}
	return false
}

// CheckShifts validates shifts against the rerun_shift_limits setting of the
// organization and project, over the default bounds (runshifts.DefaultLimits). It is exported so a caller can refuse a request
// before it changes anything.
func CheckShifts(ctx context.Context, st store.Store, orgID string, projectID *string, shifts models.RunShifts) error {
	if shifts.IsZero() {
		return nil
	}
	if err := runshifts.Validate(shifts); err != nil {
		return err
	}
	limits, err := shiftLimits(ctx, st, orgID, projectID)
	if err != nil {
		return err
	}
	// A key with no configured limit gets the default bound.
	return runshifts.CheckLimits(shifts, runshifts.EffectiveLimits(limits))
}

func shiftLimits(ctx context.Context, st store.Store, orgID string, projectID *string) (models.RerunShiftLimits, error) {
	trust, ok := st.(store.CITrustSettingsStore)
	if !ok || orgID == "" {
		return models.RerunShiftLimits{}, nil
	}
	if projectID != nil && *projectID != "" {
		effective, err := trust.EffectiveCITrust(ctx, orgID, *projectID)
		if err != nil {
			return models.RerunShiftLimits{}, fmt.Errorf("reading rerun_shift_limits: %w", err)
		}
		return effective.RerunShiftLimits, nil
	}
	row, err := trust.GetCITrustSettings(ctx, orgID, nil)
	if errors.Is(err, store.ErrNotFound) || (err == nil && row == nil) {
		return models.RerunShiftLimits{}, nil
	}
	if err != nil {
		return models.RerunShiftLimits{}, fmt.Errorf("reading rerun_shift_limits: %w", err)
	}
	if row.RerunShiftLimits == nil {
		return models.RerunShiftLimits{}, nil
	}
	return *row.RerunShiftLimits, nil
}

type executionProfileStore interface {
	GetExecutionProfile(ctx context.Context, orgID, name string) (*models.ExecutionProfile, error)
}

// applyRetryShifts applies shifts to a cloned retry job, after the limits
// and before the job is stored. The execution profile ceilings of the job
// still apply to the shifted values.
func applyRetryShifts(ctx context.Context, st store.Store, newJob *models.Job, shifts models.RunShifts) error {
	if shifts.IsZero() {
		return nil
	}
	if err := CheckShifts(ctx, st, newJob.OrgID, newJob.ProjectID, shifts); err != nil {
		return err
	}
	if err := runshifts.Apply(newJob, shifts); err != nil {
		return err
	}
	profiles, ok := st.(executionProfileStore)
	if !ok || newJob.ExecutionProfile == "" {
		return nil
	}
	profile, err := profiles.GetExecutionProfile(ctx, newJob.OrgID, newJob.ExecutionProfile)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: execution profile %q was not found", runshifts.ErrOverLimit, newJob.ExecutionProfile)
		}
		return fmt.Errorf("loading execution profile: %w", err)
	}
	if err := worker.EnforceProfileLimits(newJob, profile); err != nil {
		return fmt.Errorf("%w: %v", runshifts.ErrOverLimit, err)
	}
	return nil
}
