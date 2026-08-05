package models

import "testing"

// TestJob_StatusHelpers covers the full job status lifecycle, including the
// "cancelling" transient status introduced for graceful cancel/kill:
// CanBeCancelled admits submitted/queued/running; IsCancelling identifies the
// transient state; IsCompleted deliberately excludes "cancelling" (it is not
// terminal).
func TestJob_StatusHelpers(t *testing.T) {
	tests := []struct {
		status             string
		wantRunning        bool
		wantCancelling     bool
		wantCompleted      bool
		wantCanBeCancelled bool
	}{
		{status: "submitted", wantCanBeCancelled: true},
		{status: "queued", wantCanBeCancelled: true},
		{status: "running", wantRunning: true, wantCanBeCancelled: true},
		{status: "cancelling", wantCancelling: true},
		{status: "completed", wantCompleted: true},
		{status: "failed", wantCompleted: true},
		{status: "cancelled", wantCompleted: true},
		{status: "timeout", wantCompleted: true},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			job := &Job{Status: tt.status}

			if got := job.IsRunning(); got != tt.wantRunning {
				t.Errorf("IsRunning() = %v, want %v", got, tt.wantRunning)
			}
			if got := job.IsCancelling(); got != tt.wantCancelling {
				t.Errorf("IsCancelling() = %v, want %v", got, tt.wantCancelling)
			}
			if got := job.IsCompleted(); got != tt.wantCompleted {
				t.Errorf("IsCompleted() = %v, want %v", got, tt.wantCompleted)
			}
			if got := job.CanBeCancelled(); got != tt.wantCanBeCancelled {
				t.Errorf("CanBeCancelled() = %v, want %v", got, tt.wantCanBeCancelled)
			}

			// IsCancelling and IsCompleted must never both be true: the
			// transient "cancelling" state is explicitly non-terminal.
			if job.IsCancelling() && job.IsCompleted() {
				t.Error("IsCancelling() and IsCompleted() must not both be true")
			}
		})
	}
}

// TestJob_CanBeCancelled_ExcludesCancelling verifies that a job already in
// the "cancelling" transient state cannot be cancelled again (there's nothing
// new to do — the worker is already driving it to "cancelled").
func TestJob_CanBeCancelled_ExcludesCancelling(t *testing.T) {
	job := &Job{Status: "cancelling"}
	if job.CanBeCancelled() {
		t.Error("expected CanBeCancelled() to be false for a job already in the cancelling state")
	}
}

// TestJob_CanBeKilled verifies kill can escalate a stuck graceful cancel
// (CanBeKilled admits "cancelling" in addition to everything CanBeCancelled
// admits), but still refuses terminal jobs.
func TestJob_CanBeKilled(t *testing.T) {
	tests := []struct {
		status          string
		wantCanBeKilled bool
	}{
		{status: "submitted", wantCanBeKilled: true},
		{status: "queued", wantCanBeKilled: true},
		{status: "running", wantCanBeKilled: true},
		{status: "cancelling", wantCanBeKilled: true},
		{status: "completed", wantCanBeKilled: false},
		{status: "failed", wantCanBeKilled: false},
		{status: "cancelled", wantCanBeKilled: false},
		{status: "timeout", wantCanBeKilled: false},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			job := &Job{Status: tt.status}
			if got := job.CanBeKilled(); got != tt.wantCanBeKilled {
				t.Errorf("CanBeKilled() = %v, want %v", got, tt.wantCanBeKilled)
			}
		})
	}
}

// TestJob_IsRetryable verifies the retry feature's rule: "failed",
// "cancelled", and "timeout" are retryable; nothing else is. Notably narrower
// than IsCompleted only in that "completed" (nothing to retry) is excluded —
// every other terminal-but-unsuccessful status IS retryable.
func TestJob_IsRetryable(t *testing.T) {
	tests := []struct {
		status        string
		wantRetryable bool
	}{
		{status: "submitted", wantRetryable: false},
		{status: "queued", wantRetryable: false},
		{status: "running", wantRetryable: false},
		{status: "cancelling", wantRetryable: false},
		{status: "completed", wantRetryable: false},
		{status: "timeout", wantRetryable: true},
		{status: "failed", wantRetryable: true},
		{status: "cancelled", wantRetryable: true},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			job := &Job{Status: tt.status}
			if got := job.IsRetryable(); got != tt.wantRetryable {
				t.Errorf("IsRetryable() = %v, want %v", got, tt.wantRetryable)
			}
		})
	}
}

// TestJob_IsKillRequested verifies IsKillRequested reflects CancelMode rather
// than the old LastError-sentinel scheme (see Finding 3: cancel_mode is a
// dedicated column now, not smuggled through last_error).
func TestJob_IsKillRequested(t *testing.T) {
	if (&Job{CancelMode: "kill"}).IsKillRequested() != true {
		t.Error("expected IsKillRequested() to be true for CancelMode 'kill'")
	}
	if (&Job{CancelMode: "cancel"}).IsKillRequested() != false {
		t.Error("expected IsKillRequested() to be false for CancelMode 'cancel'")
	}
	if (&Job{}).IsKillRequested() != false {
		t.Error("expected IsKillRequested() to be false for empty CancelMode")
	}
}

// TestJob_ResourceFieldsRoundTrip verifies the resource_cpu_request/
// resource_cpu_limit/resource_memory_limit fields (coredb/migrations/
// 000021_job_resources.sql) hold whatever k8s-quantity strings are set on
// them without transformation -- these fields are plain passthrough storage,
// validated upstream by internal/resources.ParseResources.
func TestJob_ResourceFieldsRoundTrip(t *testing.T) {
	job := &Job{
		ResourceCPURequest:  "500m",
		ResourceCPULimit:    "2",
		ResourceMemoryLimit: "4Gi",
	}

	if job.ResourceCPURequest != "500m" {
		t.Errorf("ResourceCPURequest = %q, want %q", job.ResourceCPURequest, "500m")
	}
	if job.ResourceCPULimit != "2" {
		t.Errorf("ResourceCPULimit = %q, want %q", job.ResourceCPULimit, "2")
	}
	if job.ResourceMemoryLimit != "4Gi" {
		t.Errorf("ResourceMemoryLimit = %q, want %q", job.ResourceMemoryLimit, "4Gi")
	}

	// Zero-value Job (as constructed in Go, before any DB round trip) has
	// empty strings -- the migration's NOT NULL DEFAULTs are what supply
	// "1"/"2"/"4Gi" once a row is actually inserted; this is not something
	// the Go zero value can assert without a live DB.
	var zero Job
	if zero.ResourceCPURequest != "" || zero.ResourceCPULimit != "" || zero.ResourceMemoryLimit != "" {
		t.Errorf("expected zero-value Job to have empty resource fields, got (%q, %q, %q)",
			zero.ResourceCPURequest, zero.ResourceCPULimit, zero.ResourceMemoryLimit)
	}
}
