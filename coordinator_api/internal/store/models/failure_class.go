package models

import (
	"strings"
)

// Failure classes of a terminal job. A class tells who must act on a
// failure: the pipeline owner (job, timeout, oom), the CI owner (the
// infra.* classes), or nobody (cancelled). infra.image_pull is the pipeline
// owner's when the image is missing and the CI owner's when the registry is
// down. Keep this set equal to the jobs.failure_class CHECK constraint
// (coredb/migrations/000047_job_failure_class.sql).
const (
	FailureClassJob        = "job"
	FailureClassTimeout    = "timeout"
	FailureClassOOM        = "oom"
	FailureClassImagePull  = "infra.image_pull"
	FailureClassScheduling = "infra.scheduling"
	FailureClassWorkerLost = "infra.worker_lost"
	FailureClassPlatform   = "infra.platform"
	FailureClassCancelled  = "cancelled"
)

// FailureClasses lists every valid failure class, in display order.
var FailureClasses = []string{
	FailureClassJob, FailureClassTimeout, FailureClassOOM,
	FailureClassImagePull, FailureClassScheduling, FailureClassWorkerLost, FailureClassPlatform,
	FailureClassCancelled,
}

// Owners of a failure class, see FailureOwner.
const (
	FailureOwnerPipeline       = "pipeline"
	FailureOwnerInfrastructure = "infrastructure"
)

// IsValidFailureClass reports whether class is one of FailureClasses.
func IsValidFailureClass(class string) bool {
	for _, c := range FailureClasses {
		if c == class {
			return true
		}
	}
	return false
}

// IsInfraFailureClass reports whether class is an infrastructure class.
func IsInfraFailureClass(class string) bool {
	return strings.HasPrefix(class, "infra.") && IsValidFailureClass(class)
}

// FailureOwner returns who must act on a failure of this class:
// FailureOwnerPipeline, FailureOwnerInfrastructure, or "" (cancelled,
// unknown, or no failure). infra.image_pull is reported as infrastructure;
// the report text says that a missing image is the pipeline's.
func FailureOwner(class string) string {
	switch class {
	case FailureClassJob, FailureClassTimeout, FailureClassOOM:
		return FailureOwnerPipeline
	}
	if IsInfraFailureClass(class) {
		return FailureOwnerInfrastructure
	}
	return ""
}

// LostJobErrorPrefix starts the last_error of a job the lost-job reconciler
// failed. The migration backfill and DeriveFailureClass match it.
const LostJobErrorPrefix = "lost:"

// PodStartupFailureClass maps a Kubernetes pod startup reason to a failure
// class. Image reasons are infra.image_pull; scheduling reasons are
// infra.scheduling; every other startup reason (container create errors,
// crash loops of a sidecar) is infra.platform.
func PodStartupFailureClass(reason string) string {
	switch reason {
	case "ImagePullBackOff", "ErrImagePull", "ImageInspectError", "ErrImageNeverPull", "InvalidImageName":
		return FailureClassImagePull
	case "Unschedulable", "SchedulingBlocked", "ResourceRequestTooLarge", "StartupTimeout":
		return FailureClassScheduling
	}
	return FailureClassPlatform
}

// platformErrorPrefixes start the last_error of a failure the worker or the
// coordinator reports before or around the job's own command: workspace
// preparation, spawn, wait, and queue submission. See
// internal/coordinatorworker/lease.go.
var platformErrorPrefixes = []string{
	"failed to create workspace",
	"failed to prepare",
	"failed to spawn job",
	"failed to submit",
	"failed to wait",
	"error waiting for container",
	"failed to list pods",
	"lost: replacement task submission failed",
}

// DeriveFailureClass computes a class from the fields every worker reports:
// the terminal status, the exit code, and the error text. The coordinator
// uses it for a result without a class (a worker older than failure
// classes) and as the safety net in Job.BeforeSave. It returns "" for a
// status that is not a failure.
func DeriveFailureClass(status string, exitCode *int, lastError string) string {
	switch status {
	case "cancelled":
		return FailureClassCancelled
	case "timeout":
		return FailureClassTimeout
	case "failed":
	default:
		return ""
	}
	msg := strings.TrimSpace(lastError)
	if strings.HasPrefix(msg, LostJobErrorPrefix) && !strings.HasPrefix(msg, "lost: replacement task submission failed") {
		return FailureClassWorkerLost
	}
	if idx := strings.Index(msg, "pod failed to start: "); idx >= 0 {
		reason := msg[idx+len("pod failed to start: "):]
		if end := strings.Index(reason, " "); end >= 0 {
			reason = reason[:end]
		}
		return PodStartupFailureClass(reason)
	}
	for _, reason := range []string{"ImagePullBackOff", "ErrImagePull", "ImageInspectError", "ErrImageNeverPull", "InvalidImageName",
		"SchedulingBlocked", "ResourceRequestTooLarge", "StartupTimeout"} {
		if strings.Contains(msg, reason) {
			return PodStartupFailureClass(reason)
		}
	}
	for _, prefix := range platformErrorPrefixes {
		if strings.HasPrefix(msg, prefix) {
			return FailureClassPlatform
		}
	}
	if strings.Contains(msg, " denied: ") {
		// A claim refused a secret or checkout credential the job asked
		// for: the pipeline's configuration, not the infrastructure.
		return FailureClassJob
	}
	if exitCode != nil && *exitCode > 0 {
		return FailureClassJob
	}
	return FailureClassPlatform
}

// normalizeFailureClass keeps the failure class consistent with the status
// before a save: no class on a job that did not fail, a derived class on a
// failed job that has none, and never a value outside the CHECK constraint.
func (j *Job) normalizeFailureClass() {
	switch j.Status {
	case "failed", "cancelled", "timeout":
	default:
		j.FailureClass = nil
		j.FailureDetail = ""
		return
	}
	if j.FailureClass != nil && IsValidFailureClass(*j.FailureClass) {
		return
	}
	class := DeriveFailureClass(j.Status, j.ExitCode, j.LastError)
	j.FailureClass = &class
}

// SetFailure records the class (and an optional detail) of a terminal job.
// An invalid class is replaced by the derived class at save time.
func (j *Job) SetFailure(class, detail string) {
	if class == "" {
		j.FailureClass = nil
	} else {
		c := class
		j.FailureClass = &c
	}
	j.FailureDetail = detail
}

// FailureClassValue returns the job's failure class, or "".
func (j *Job) FailureClassValue() string {
	if j == nil || j.FailureClass == nil {
		return ""
	}
	return *j.FailureClass
}

// FailureReportText is the sentence a VCS report (status, comment) shows
// for a failure class. It says whether the failure is the pipeline's or the
// infrastructure's. It returns "" for no class.
func FailureReportText(class string) string {
	switch class {
	case FailureClassJob:
		return "pipeline failure: the job exited with an error"
	case FailureClassTimeout:
		return "pipeline failure: the job exceeded its timeout"
	case FailureClassOOM:
		return "pipeline failure: the job exceeded its memory limit"
	case FailureClassImagePull:
		return "image pull failure: make sure that the image exists; if it exists, this is an infrastructure failure"
	case FailureClassScheduling:
		return "infrastructure failure: the job could not be scheduled"
	case FailureClassWorkerLost:
		return "infrastructure failure: the worker was lost"
	case FailureClassPlatform:
		return "infrastructure failure: a platform error stopped the job"
	case FailureClassCancelled:
		return "cancelled"
	}
	return ""
}
