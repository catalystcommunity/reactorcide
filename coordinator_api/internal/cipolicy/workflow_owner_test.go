package cipolicy

import "testing"

func ownerApprovalPolicy(t *testing.T, approval string) (*Policy, error) {
	t.Helper()
	input := `version: 1
defaults: {ci_source: base, profile: standard}
head_ci:
- id: payments
  workflows: [payments]
  paths: [.reactorcide/**]
  approval: {any: ` + approval + `}
  use: {ci_source: head, profile: pr-untrusted, workers: default}`
	return ParseDocument([]byte(input))
}

func TestWorkflowOwnerIsValidOnlyInApprovalLists(t *testing.T) {
	if _, err := ownerApprovalPolicy(t, `[workflow_owner]`); err != nil {
		t.Fatalf("workflow_owner must be valid in a head_ci approval list: %v", err)
	}
	input := `version: 1
defaults: {ci_source: base, profile: standard}
head_ci:
- id: payments
  actors: {any: [workflow_owner]}
  workflows: [payments]
  paths: [.reactorcide/**]
  use: {ci_source: head, profile: pr-untrusted, workers: default}`
	if _, err := ParseDocument([]byte(input)); err == nil {
		t.Fatal("workflow_owner must not be valid in an actor list")
	}
}

func TestWorkflowOwnerApprovalExpandsToTheTrustedOwner(t *testing.T) {
	policy, err := ownerApprovalPolicy(t, `[workflow_owner]`)
	if err != nil {
		t.Fatal(err)
	}
	facts := func(approvals map[string]bool, owner []string) Facts {
		return Facts{WorkflowID: "payments", ChangedCIPaths: []string{".reactorcide/workflows/payments.yaml"},
			HeadRepositoryRelation: "same", ApprovalSubjects: approvals, WorkflowOwnerSubjects: owner}
	}
	owner := []string{"reactorcide_group:payments-ci"}

	decision, err := Decide(policy, facts(map[string]bool{"reactorcide_group:payments-ci": true}, owner))
	if err != nil || !decision.Allowed {
		t.Fatalf("an approval by an owner must match: %+v %v", decision, err)
	}
	if len(decision.ApprovalSubjects) != 1 || decision.ApprovalSubjects[0] != "reactorcide_group:payments-ci" {
		t.Fatalf("the decision must carry the expanded subjects, got %v", decision.ApprovalSubjects)
	}
	if decision, _ := Decide(policy, facts(map[string]bool{"reactorcide_group:other": true}, owner)); decision.Allowed {
		t.Fatal("an approval by a non-owner must not match")
	}
	if decision, _ := Decide(policy, facts(map[string]bool{"reactorcide_group:payments-ci": true}, nil)); decision.Allowed {
		t.Fatal("a workflow with no known owner must match nothing for workflow_owner")
	}
	if decision, _ := Decide(policy, facts(map[string]bool{"workflow_owner": true}, nil)); decision.Allowed {
		t.Fatal("the literal placeholder is never an approval fact")
	}
}
