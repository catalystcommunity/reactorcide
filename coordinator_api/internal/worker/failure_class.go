package worker

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// FailureError carries a failure class (models.FailureClass*) from a runner
// to the worker, which reports it with the job result. Runners wrap the
// errors whose cause they know: an image pull, a scheduling failure. An
// error without a class is infra.platform.
type FailureError struct {
	Class  string
	Detail string
	Err    error
}

func (e *FailureError) Error() string { return e.Err.Error() }
func (e *FailureError) Unwrap() error { return e.Err }

// NewFailureError wraps err with a failure class and a short detail.
func NewFailureError(class, detail string, err error) error {
	if err == nil {
		return nil
	}
	return &FailureError{Class: class, Detail: detail, Err: err}
}

// ExitClassifier is an optional runner capability. The worker calls
// ClassifyExit after WaitForCompletion returns a non-zero exit code, and
// before Cleanup. The runner returns a class when the exit was not the job
// command's own failure: models.FailureClassOOM when the runtime killed the
// container for its memory limit, an infra.* class when the container did
// not run. It returns "" when it does not know, and the class is then
// "job".
type ExitClassifier interface {
	ClassifyExit(ctx context.Context, jobID string, exitCode int) (class, detail string)
}

// ClassifyExecutionError returns the failure class and detail of one
// execution from the error of SpawnJob or WaitForCompletion and the exit
// code. A nil error with a zero exit code is no failure (""). A nil error
// with a non-zero exit code is the job's own failure. An error carries its
// class (FailureError, PodStartupError); any other error is a platform
// failure. The caller applies ExitClassifier for a non-zero exit code.
func ClassifyExecutionError(err error, exitCode int) (class, detail string) {
	if err == nil {
		if exitCode == 0 {
			return "", ""
		}
		return models.FailureClassJob, ""
	}
	var failure *FailureError
	if errors.As(err, &failure) && models.IsValidFailureClass(failure.Class) {
		return failure.Class, failure.Detail
	}
	var startup *PodStartupError
	if errors.As(err, &startup) {
		return models.PodStartupFailureClass(startup.Reason), startup.Reason
	}
	return models.FailureClassPlatform, ""
}

// ClassifyExit applies runner's ExitClassifier, when it has one, to a
// non-zero exit code of jobID. It returns "job" when the runner does not
// know a better class.
func ClassifyExit(ctx context.Context, runner JobRunner, jobID string, exitCode int) (class, detail string) {
	if exitCode == 0 {
		return "", ""
	}
	if classifier, ok := runner.(ExitClassifier); ok {
		if class, detail := classifier.ClassifyExit(ctx, jobID, exitCode); models.IsValidFailureClass(class) {
			return class, detail
		}
	}
	return models.FailureClassJob, ""
}
