package models

import "testing"

func TestTrustSettingsAcceptWorkflowOwnerOnlyInOverrideApprovers(t *testing.T) {
	if err := (CITrustSettings{OverrideApprovers: []string{"workflow_owner", "project_owner"}}).Validate(); err != nil {
		t.Fatalf("override_approvers must accept workflow_owner: %v", err)
	}
	if err := (CITrustSettings{RunApprovers: []string{"workflow_owner"}}).Validate(); err == nil {
		t.Fatal("run_approvers must refuse workflow_owner")
	}
}

func TestOwnershipSnapshotHelpers(t *testing.T) {
	var unknown WorkflowOwnership
	if unknown.Known() || unknown.HasOwner() || unknown.GroupName() != "" {
		t.Fatal("zero snapshot is unknown")
	}
	group := "pay"
	wf := &WorkflowInstance{}
	wf.SetOwnership(WorkflowOwnership{Group: &group, Subjects: []string{"vcs_user:github/a"}, Contact: &WorkflowContact{Channel: "#c"}, CISHA: "s"}, OwnerSourceCI)
	if wf.OwnerGroup == nil || *wf.OwnerGroup != "pay" || wf.OwnerSource != OwnerSourceCI || wf.OwnerCISHA != "s" || wf.OwnerContact.Channel != "#c" {
		t.Fatalf("SetOwnership = %+v", wf)
	}
	job := &Job{}
	job.CopyOwnershipFrom(wf)
	if job.Ownership().GroupName() != "pay" || len(job.OwnerSubjects) != 1 {
		t.Fatalf("CopyOwnershipFrom = %+v", job.Ownership())
	}
	group = "changed"
	if *wf.OwnerGroup != "pay" {
		t.Fatal("SetOwnership must copy the group")
	}
	wf.SetOwnership(WorkflowOwnership{}, OwnerSourceCI)
	if wf.OwnerGroup != nil || wf.OwnerSource != "" {
		t.Fatal("an unknown snapshot clears the owner")
	}
}
