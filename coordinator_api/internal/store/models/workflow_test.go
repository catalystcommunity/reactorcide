package models

import "testing"

// TestWorkflowInstance_IsRetryable mirrors TestJob_IsRetryable: only
// "failed" and "cancelled" workflow instances are retryable, per the retry
// feature spec (see internal/jobcontrol.RetryWorkflow).
func TestWorkflowInstance_IsRetryable(t *testing.T) {
	tests := []struct {
		status        string
		wantRetryable bool
	}{
		{status: "evaluating", wantRetryable: false},
		{status: "running", wantRetryable: false},
		{status: "cancelling", wantRetryable: false},
		{status: "success", wantRetryable: false},
		{status: "skipped", wantRetryable: false},
		{status: "failed", wantRetryable: true},
		{status: "cancelled", wantRetryable: true},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			wf := &WorkflowInstance{Status: tt.status}
			if got := wf.IsRetryable(); got != tt.wantRetryable {
				t.Errorf("IsRetryable() = %v, want %v", got, tt.wantRetryable)
			}
		})
	}
}

func TestWorkflowOwnershipOrganization(t *testing.T) {
	tests := []struct {
		name   string
		orgID  string
		userID string
		want   string
	}{
		{name: "organization", orgID: "org-1", userID: "user-1", want: "org-1"},
		{name: "legacy user fallback", userID: "user-1", want: "user-1"},
		{name: "empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflow := &WorkflowInstance{OrgID: tt.orgID, UserID: tt.userID}
			if got := workflow.OwnershipOrgID(); got != tt.want {
				t.Fatalf("WorkflowInstance.OwnershipOrgID() = %q, want %q", got, tt.want)
			}
			summary := &WorkflowSummary{OrgID: tt.orgID, UserID: tt.userID}
			if got := summary.OwnershipOrgID(); got != tt.want {
				t.Fatalf("WorkflowSummary.OwnershipOrgID() = %q, want %q", got, tt.want)
			}
		})
	}
}
