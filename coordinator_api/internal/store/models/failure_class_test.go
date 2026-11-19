package models

import "testing"

func intPtr(v int) *int { return &v }

func TestDeriveFailureClass(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		exitCode *int
		message  string
		want     string
	}{
		{"completed has no class", "completed", intPtr(0), "", ""},
		{"running has no class", "running", nil, "", ""},
		{"cancelled", "cancelled", nil, "cancelled", FailureClassCancelled},
		{"timeout", "timeout", intPtr(137), "", FailureClassTimeout},
		{"non-zero exit", "failed", intPtr(2), "job exited with code 2", FailureClassJob},
		{"lost job", "failed", nil, "lost: no lease after coordinator error; retries exhausted", FailureClassWorkerLost},
		{"image pull", "failed", intPtr(-1), "pod failed to start: ErrImagePull - not found", FailureClassImagePull},
		{"unschedulable", "failed", intPtr(-1), "pod failed to start: ResourceRequestTooLarge - too big", FailureClassScheduling},
		{"scheduling text of an older worker", "failed", intPtr(-1), "SchedulingBlocked: node selector cannot match", FailureClassScheduling},
		{"crash loop", "failed", intPtr(-1), "pod failed to start: CrashLoopBackOff - back-off", FailureClassPlatform},
		{"spawn failure with exit 1", "failed", intPtr(1), "failed to spawn job: no such image", FailureClassPlatform},
		{"workspace failure", "failed", intPtr(1), "failed to prepare workspace permissions: EPERM", FailureClassPlatform},
		{"corndogs submit", "failed", nil, "failed to submit to Corndogs: unavailable", FailureClassPlatform},
		{"secret denial", "failed", nil, "secret resolution denied: not granted", FailureClassJob},
		{"wait error without exit", "failed", intPtr(-1), "error waiting for container: EOF", FailureClassPlatform},
		{"unknown failure", "failed", nil, "something", FailureClassPlatform},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveFailureClass(tc.status, tc.exitCode, tc.message); got != tc.want {
				t.Fatalf("DeriveFailureClass(%q, %v, %q) = %q, want %q", tc.status, tc.exitCode, tc.message, got, tc.want)
			}
		})
	}
}

func TestJobBeforeSaveKeepsFailureClassConsistent(t *testing.T) {
	// A requeued job loses its class.
	job := &Job{Status: "submitted"}
	job.SetFailure(FailureClassPlatform, "detail")
	if err := job.BeforeSave(nil); err != nil {
		t.Fatal(err)
	}
	if job.FailureClass != nil || job.FailureDetail != "" {
		t.Fatalf("a job that is not terminal must have no class, got %v %q", job.FailureClass, job.FailureDetail)
	}

	// A failed job without a class gets the derived class.
	job = &Job{Status: "failed", ExitCode: intPtr(3), LastError: "job exited with code 3"}
	_ = job.BeforeSave(nil)
	if job.FailureClassValue() != FailureClassJob {
		t.Fatalf("derived class = %q, want job", job.FailureClassValue())
	}

	// An explicit class wins; an invalid class is replaced.
	job = &Job{Status: "failed", ExitCode: intPtr(137)}
	job.SetFailure(FailureClassOOM, "OOMKilled")
	_ = job.BeforeSave(nil)
	if job.FailureClassValue() != FailureClassOOM || job.FailureDetail != "OOMKilled" {
		t.Fatalf("explicit class lost: %q %q", job.FailureClassValue(), job.FailureDetail)
	}
	job.SetFailure("bogus", "")
	_ = job.BeforeSave(nil)
	if job.FailureClassValue() != FailureClassJob {
		t.Fatalf("an invalid class must be replaced by the derived class, got %q", job.FailureClassValue())
	}

	// A completed job never has a class.
	job = &Job{Status: "completed"}
	job.SetFailure(FailureClassJob, "")
	_ = job.BeforeSave(nil)
	if job.FailureClass != nil {
		t.Fatal("a completed job must have no class")
	}
}

func TestFailureOwner(t *testing.T) {
	want := map[string]string{
		FailureClassJob:        FailureOwnerPipeline,
		FailureClassTimeout:    FailureOwnerPipeline,
		FailureClassOOM:        FailureOwnerPipeline,
		FailureClassImagePull:  FailureOwnerInfrastructure,
		FailureClassScheduling: FailureOwnerInfrastructure,
		FailureClassWorkerLost: FailureOwnerInfrastructure,
		FailureClassPlatform:   FailureOwnerInfrastructure,
		FailureClassCancelled:  "",
		"":                     "",
		"infra.unknown":        "",
	}
	for class, owner := range want {
		if got := FailureOwner(class); got != owner {
			t.Errorf("FailureOwner(%q) = %q, want %q", class, got, owner)
		}
	}
	for _, class := range FailureClasses {
		if FailureReportText(class) == "" {
			t.Errorf("FailureReportText(%q) is empty", class)
		}
	}
}

func TestEffectiveInfraRetries(t *testing.T) {
	cases := []struct {
		project, org *int
		want         int
	}{
		{nil, nil, DefaultInfraRetries},
		{nil, intPtr(3), 3},
		{intPtr(0), intPtr(3), 0},
		{intPtr(4), nil, 4},
		{intPtr(99), intPtr(2), 2},
	}
	for _, tc := range cases {
		if got := EffectiveInfraRetries(tc.project, tc.org); got != tc.want {
			t.Errorf("EffectiveInfraRetries(%v, %v) = %d, want %d", tc.project, tc.org, got, tc.want)
		}
	}
}

func TestInfraRetryBudget(t *testing.T) {
	for _, class := range []string{FailureClassJob, FailureClassTimeout, FailureClassOOM, FailureClassCancelled, FailureClassImagePull, FailureClassScheduling, ""} {
		if InfraRetryBudget(class, MaxInfraRetries) != 0 {
			t.Errorf("class %q must never be retried automatically", class)
		}
	}
	if got := InfraRetryBudget(FailureClassPlatform, 1); got != 1 {
		t.Errorf("platform budget = %d, want 1", got)
	}
	if got := InfraRetryBudget(FailureClassPlatform, 0); got != 0 {
		t.Errorf("platform budget with infra_retries 0 = %d, want 0", got)
	}
	// A lost job keeps the reconciler's budget of 2, and a larger setting raises it.
	if got := InfraRetryBudget(FailureClassWorkerLost, 0); got != MinLostJobRetries {
		t.Errorf("lost budget with infra_retries 0 = %d, want %d", got, MinLostJobRetries)
	}
	if got := InfraRetryBudget(FailureClassWorkerLost, 4); got != 4 {
		t.Errorf("lost budget with infra_retries 4 = %d, want 4", got)
	}
	if !ShouldRetryInfraFailure(FailureClassPlatform, 0, 1) || ShouldRetryInfraFailure(FailureClassPlatform, 1, 1) {
		t.Error("platform retry must stop when retry_count reaches the budget")
	}
}
