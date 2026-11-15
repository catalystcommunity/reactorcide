package worker

import (
	"context"
	"testing"

	approvalstate "github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

func withRunAuthority(t *testing.T, parent *models.Job, authority approvalstate.RunAuthority) {
	t.Helper()
	encoded, err := authority.Encode()
	if err != nil {
		t.Fatal(err)
	}
	parent.JobEnvVars[approvalstate.RunAuthorityEnv] = encoded
}

func prEvalParent(t *testing.T, policyDocument []byte) *models.Job {
	t.Helper()
	parent, _ := mixedTrustEvalParent(t, policyDocument)
	projectID := "project"
	parent.ProjectID = &projectID
	if err := (&vcs.JobMetadata{VCSProvider: "github", Repo: "org/repo", PRNumber: 7, CommitSHA: *parent.SourceRef, IsEval: true}).ApplyToJob(parent); err != nil {
		t.Fatal(err)
	}
	return parent
}

// approve-ci runs trusted base CI. A policy rule that would grant head CI
// does not apply to a gated run; the head's CI changes need override-ci.
func TestSelectPolicyCandidates_GatedRunUsesBaseCI(t *testing.T) {
	parent := prEvalParent(t, []byte(mixedTrustPolicy))
	withRunAuthority(t, parent, approvalstate.RunAuthority{Gated: true, Profile: "pr-untrusted", RunApprovalID: "run-1"})
	selected, violations, notes, err := newMixedTrustProcessor().selectPolicyCandidates(context.Background(), parent,
		mixedTrustCandidates(), []string{".reactorcide/workflows/pr.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].CIOrigin != "base" || selected[0].ExecutionProfile != "pr-untrusted" {
		t.Fatalf("a gated run must use base CI with the gated profile: %+v", selected)
	}
	if len(violations) != 0 {
		t.Fatalf("the gate, not the policy, held the head's CI: %+v", violations)
	}
	if note := notes["csilgen-pr"]; note.CIOrigin != "base" || note.Profile != "pr-untrusted" {
		t.Fatalf("unexpected note %+v", note)
	}
}

// override-ci lets the head's own CI run with the override profile, also with
// no stored policy and no matching rule.
func TestSelectPolicyCandidates_OverrideWithoutPolicy(t *testing.T) {
	parent := prEvalParent(t, []byte(mixedTrustPolicy))
	delete(parent.JobEnvVars, "REACTORCIDE_CI_POLICY")
	delete(parent.JobEnvVars, "REACTORCIDE_CI_POLICY_REVISION")
	withRunAuthority(t, parent, approvalstate.RunAuthority{Override: &approvalstate.RunOverride{
		ApprovalID: "override-1", Person: "github/maintainer", Profile: "pr-untrusted"}})
	tp := newMixedTrustProcessor()
	selected, _, _, err := tp.selectPolicyCandidates(context.Background(), parent, mixedTrustCandidates(), []string{".reactorcide/workflows/pr.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].CIOrigin != "head" || selected[0].ExecutionProfile != "pr-untrusted" ||
		selected[0].PolicyRuleID != overrideRuleID || selected[0].ApprovalID == nil || *selected[0].ApprovalID != "override-1" {
		t.Fatalf("override-ci must grant head CI: %+v", selected)
	}
	selected[0].TriggerType = "runnerlib_eval"
	if err := tp.validateWorkflowAuthority(context.Background(), parent, &selected[0], nil); err != nil {
		t.Fatalf("an override workflow must pass authority validation: %v", err)
	}
}

func TestSelectPolicyCandidates_OverrideLosesToMatchingRule(t *testing.T) {
	parent := prEvalParent(t, []byte(mixedTrustPolicy))
	withRunAuthority(t, parent, approvalstate.RunAuthority{Override: &approvalstate.RunOverride{
		ApprovalID: "override-1", Person: "github/maintainer", Profile: "pr-untrusted"}})
	selected, _, _, err := newMixedTrustProcessor().selectPolicyCandidates(context.Background(), parent,
		mixedTrustCandidates(), []string{".reactorcide/workflows/pr.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].PolicyRuleID != "csilgen" || len(selected[0].NodeAuthority) != 2 {
		t.Fatalf("a matching policy rule (with its base nodes) must win over the override: %+v", selected)
	}
}

func TestRunAuthorityFactIsValidated(t *testing.T) {
	parent := prEvalParent(t, []byte(mixedTrustPolicy))
	parent.JobEnvVars[approvalstate.RunAuthorityEnv] = `{"override":{"approval_id":""}}`
	if _, _, _, err := newMixedTrustProcessor().selectPolicyCandidates(context.Background(), parent,
		mixedTrustCandidates(), []string{".reactorcide/workflows/pr.yaml"}); err == nil {
		t.Fatal("an override with no approval must fail closed")
	}
}

// carryStore adds the CI event reads the re-evaluate rule uses.
type carryStore struct {
	*multiProfileStore
	events    map[string]*models.CIEvent
	decisions map[string][]models.CIEventWorkflowDecision
	head      []models.CIEvent
}

func (s *carryStore) GetCIEvent(_ context.Context, id string) (*models.CIEvent, error) {
	if event, ok := s.events[id]; ok {
		return event, nil
	}
	return nil, store.ErrNotFound
}
func (s *carryStore) ListCIEventWorkflowDecisions(_ context.Context, id string) ([]models.CIEventWorkflowDecision, error) {
	return s.decisions[id], nil
}
func (s *carryStore) ListCIEventsForPRHead(context.Context, string, int, string) ([]models.CIEvent, error) {
	return s.head, nil
}
func (s *carryStore) ListPRHeadUpdateActors(context.Context, string, int) ([]store.PRHeadUpdate, error) {
	return nil, nil
}
func (s *carryStore) RecordCIEvent(context.Context, *models.CIEvent) (*models.CIEvent, store.CIEventRecordOutcome, error) {
	return nil, "", nil
}
func (s *carryStore) AttachCIEventEvalJob(context.Context, string, string) error { return nil }
func (s *carryStore) SetCIEventStatus(context.Context, string, string) error     { return nil }
func (s *carryStore) SetCIEventSummary(context.Context, string, models.CIEventSummary) error {
	return nil
}
func (s *carryStore) UpsertCIEventWorkflowDecisions(context.Context, []models.CIEventWorkflowDecision) error {
	return nil
}
func (s *carryStore) GetCIEventWorkflowDecisionByWorkflow(context.Context, string) (*models.CIEventWorkflowDecision, error) {
	return nil, store.ErrNotFound
}

// A re-evaluation after an approval keeps a workflow whose decision did not
// change; a workflow whose decision changed runs.
func TestCarryUnchangedWorkflows(t *testing.T) {
	pr := 7
	earlierWorkflow := "wf-earlier"
	backing := &carryStore{multiProfileStore: &multiProfileStore{workflowRuntimeStore: newWorkflowRuntimeStore(), profiles: mixedTrustProfiles()},
		events: map[string]*models.CIEvent{
			"approval-event": {EventID: "approval-event", Source: models.CIEventSourceApproval, PRNumber: &pr, CommitSHA: "head-sha"},
		},
		head: []models.CIEvent{
			{EventID: "approval-event", Status: models.CIEventStatusEvaluating},
			{EventID: "first", Status: models.CIEventStatusEvaluated},
		},
		decisions: map[string][]models.CIEventWorkflowDecision{"first": {
			{WorkflowSecurityID: "same", Decision: models.WorkflowDecisionRun, WorkflowID: &earlierWorkflow,
				Reasons: []models.DecisionReason{policyNote("base", "standard", "default", "", "rev")}},
			{WorkflowSecurityID: "changed", Decision: models.WorkflowDecisionRun, WorkflowID: &earlierWorkflow,
				Reasons: []models.DecisionReason{policyNote("base", "standard", "default", "", "rev")}},
		}},
	}
	tp := NewTriggerProcessor(backing, nil)
	parent := prEvalParent(t, []byte(mixedTrustPolicy))
	eventID := "approval-event"
	parent.EventID = &eventID
	batches := []triggerWorkflowSpec{{ID: "same", Name: "Same"}, {ID: "changed", Name: "Changed"}, {ID: "new", Name: "New"}}
	notes := map[string]models.DecisionReason{
		"same":    policyNote("base", "standard", "default", "", "rev"),
		"changed": policyNote("head", "pr-untrusted", "default", "override-ci", ""),
		"new":     policyNote("base", "standard", "default", "", "rev"),
	}
	kept, carried := tp.carryUnchangedWorkflows(context.Background(), parent, batches, notes)
	if len(kept) != 2 || kept[0].ID != "changed" || kept[1].ID != "new" {
		t.Fatalf("kept %+v", kept)
	}
	if carry, ok := carried["same"]; !ok || carry.WorkflowID != earlierWorkflow || len(carried) != 1 {
		t.Fatalf("carried %+v", carried)
	}

	// A webhook event is never changed.
	backing.events["approval-event"].Source = models.CIEventSourceWebhook
	kept, carried = tp.carryUnchangedWorkflows(context.Background(), parent, batches, notes)
	if len(kept) != 3 || carried != nil {
		t.Fatalf("a webhook event must run every workflow: kept=%d carried=%v", len(kept), carried)
	}
}
