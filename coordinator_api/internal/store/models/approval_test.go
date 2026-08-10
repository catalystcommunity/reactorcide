package models

import "testing"

func TestCIApprovalValidate(t *testing.T) {
	valid := CIApproval{OrgID: "org", ProjectID: "project", PRNumber: 1, HeadRepository: "acme/repo",
		HeadSHA: "head", BaseSHA: "base", PolicyRevision: "revision", WorkflowScope: "backend-tests",
		ExecutionProfile: "pr-untrusted", ApproverSubject: "vcs_team:acme/backend"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.WorkflowScope = "../backend"
	if err := invalid.Validate(); err == nil {
		t.Fatal("an unsafe workflow scope was accepted")
	}
	invalid = valid
	invalid.ApproverSubject = "unverified:alice"
	if err := invalid.Validate(); err == nil {
		t.Fatal("an unknown approver subject was accepted")
	}
}
