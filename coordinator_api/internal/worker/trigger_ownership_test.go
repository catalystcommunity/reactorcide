package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/owners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// Workflow ownership comes from the TRUSTED CI definition only (WP7).

func ownedCandidates() *policyCandidateSet {
	candidates := mixedTrustCandidates()
	candidates.Base[0].Owners = &owners.Spec{Group: "payments-ci"}
	candidates.Base[0].Contact = &owners.ContactSpec{Channel: "#payments-ci"}
	// The pull request tries to take the workflow over.
	candidates.Head[0].Owners = &owners.Spec{Group: "attacker"}
	candidates.Head[0].Contact = &owners.ContactSpec{Channel: "#attacker"}
	return candidates
}

func ownerPREvalParent(t *testing.T, policy string) *models.Job {
	t.Helper()
	parent, _ := mixedTrustEvalParent(t, []byte(policy))
	projectID := "project"
	parent.ProjectID = &projectID
	if err := (&vcs.JobMetadata{VCSProvider: "github", Repo: "org/repo", PRNumber: 7, CommitSHA: *parent.SourceRef, IsEval: true}).ApplyToJob(parent); err != nil {
		t.Fatal(err)
	}
	return parent
}

func TestOwnershipComesFromBaseWhenPolicyGrantsHeadCI(t *testing.T) {
	parent := ownerPREvalParent(t, mixedTrustPolicy)
	tp := newMixedTrustProcessor()
	selected, _, _, err := tp.selectPolicyCandidates(context.Background(), parent, ownedCandidates(), []string{".reactorcide/workflows/pr.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].CIOrigin != "head" {
		t.Fatalf("precondition: head CI is granted, got %+v", selected)
	}
	own := selected[0].Ownership
	if own == nil || own.GroupName() != "payments-ci" || own.Contact == nil || own.Contact.Channel != "#payments-ci" {
		t.Fatalf("a pull request changed the owner of the workflow it runs: %+v", own)
	}
	if own.CISHA != parent.CISHA || selected[0].OwnershipSource != models.OwnerSourceCI {
		t.Fatalf("the owner must name the trusted base SHA: %+v source=%q", own, selected[0].OwnershipSource)
	}
}

func TestOwnershipFromBaseWhenHeadCIIsRefused(t *testing.T) {
	refusing := strings.Replace(mixedTrustPolicy, "actors: {any: [repository_write]}", "actors: {any: [vcs_user:github/nobody]}", 1)
	parent := ownerPREvalParent(t, refusing)
	tp := newMixedTrustProcessor()
	selected, _, _, err := tp.selectPolicyCandidates(context.Background(), parent, ownedCandidates(), []string{".reactorcide/workflows/pr.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].CIOrigin != "base" || selected[0].Ownership.GroupName() != "payments-ci" {
		t.Fatalf("base run must keep the base owner: %+v", selected)
	}
}

func TestOwnershipOfAWorkflowThatOnlyTheHeadHasIsNone(t *testing.T) {
	policy := `version: 1
defaults: {ci_source: base, profile: standard}
head_ci:
- id: new
  actors: {any: [repository_write]}
  workflows: [brand-new]
  paths: ['.reactorcide/**']
  head_repository: any
  use: {ci_source: head, profile: pr-untrusted, workers: default}`
	parent := ownerPREvalParent(t, policy)
	candidates := &policyCandidateSet{Head: []triggerWorkflowSpec{{
		ID: "brand-new", Name: "New", EventMatched: true, ExplicitID: true,
		DependencyPaths: []string{".reactorcide/workflows/new.yaml"}, Owners: &owners.Spec{Group: "attacker"},
		Jobs: []triggerJobSpec{{JobName: "x", JobCommand: "true"}},
	}}}
	selected, _, _, err := newMixedTrustProcessor().selectPolicyCandidates(context.Background(), parent, candidates, []string{".reactorcide/workflows/new.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].CIOrigin != "head" {
		t.Fatalf("precondition: head CI is granted, got %+v", selected)
	}
	own := selected[0].Ownership
	if own == nil || !own.Known() || own.HasOwner() {
		t.Fatalf("a workflow that the trusted base does not have has no owner, got %+v", own)
	}
}

// priorOwnerStore serves an earlier trusted snapshot, the stored facts of
// the event, and the project.
type priorOwnerStore struct {
	*eventRecordingStore
	prior   *models.WorkflowOwnership
	event   *models.CIEvent
	project *models.Project
}

func (s *priorOwnerStore) GetCIEvent(ctx context.Context, eventID string) (*models.CIEvent, error) {
	if s.event != nil {
		copy := *s.event
		copy.EventID = eventID
		return &copy, nil
	}
	return s.eventRecordingStore.GetCIEvent(ctx, eventID)
}

func (s *priorOwnerStore) GetProjectByID(context.Context, string) (*models.Project, error) {
	return s.project, nil
}

// prDefaultEvent is the stored event of a pull request to the default branch.
func prEvent(base string) *models.CIEvent {
	return &models.CIEvent{RefClasses: []string{"pr"}, RefClassFacts: &models.RefClassFacts{
		EventType: "pull_request_updated", IsPR: true, HeadBranch: "feature", BaseBranch: base, DefaultBranch: "main"}}
}

func trustedPrior(group string) *models.WorkflowOwnership {
	return &models.WorkflowOwnership{Group: &group, CISHA: "main-sha", Source: models.OwnerSourceCI, Trusted: true}
}

// runOwnedEval processes an eval payload whose owners block names group and
// returns the created workflow.
func runOwnedEval(t *testing.T, st *priorOwnerStore, parent *models.Job, group string) *models.WorkflowInstance {
	t.Helper()
	st.eventRecordingStore.base.GetJobByIDFunc = func(_ context.Context, _ string) (*models.Job, error) { copy := *parent; return &copy, nil }
	if _, err := NewTriggerProcessor(st, nil).ProcessTriggersFromDataWithOutcomes(context.Background(), ownedEvalPayload(t, parent, group), "", parent); err != nil {
		t.Fatal(err)
	}
	return createdWorkflow(t, st.eventRecordingStore)
}

func (s *priorOwnerStore) LatestWorkflowOwnership(context.Context, string, string) (*models.WorkflowOwnership, error) {
	if s.prior == nil {
		return nil, store.ErrNotFound
	}
	return s.prior, nil
}

func ownedEvalPayload(t *testing.T, parent *models.Job, group string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"type": "trigger_job", "trigger_type": "runnerlib_eval",
		"workflows": []map[string]any{{
			"id": "build", "name": "Build", "source_file": ".reactorcide/workflows/build.yaml",
			"ci_origin": "base", "ci_repository": parent.CIRepository, "ci_sha": parent.CISHA,
			"execution_profile": "standard", "worker_class": "default", "event_matched": true,
			"owners": map[string]any{"group": group}, "contact": map[string]any{"channel": "#" + group},
			"jobs": []map[string]any{{"job_name": "compile", "job_command": "make"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func createdWorkflow(t *testing.T, st *eventRecordingStore) *models.WorkflowInstance {
	t.Helper()
	for _, wf := range st.base.workflows {
		return wf
	}
	t.Fatal("no workflow was created")
	return nil
}

func TestEvalSnapshotsTrustedOwnerOnWorkflowAndJobs(t *testing.T) {
	// A pull request to the default branch: the base SHA is trusted.
	st := newEventRecordingStore()
	wrapped := &priorOwnerStore{eventRecordingStore: st, event: prEvent("main")}
	parent := eventEvalParent(t, "eval-1", "event-1")
	wf := runOwnedEval(t, wrapped, parent, "payments-ci")
	if wf.OwnerGroup == nil || *wf.OwnerGroup != "payments-ci" || wf.OwnerSource != models.OwnerSourceCI || wf.OwnerCISHA != "base-sha" {
		t.Fatalf("workflow snapshot = %+v", wf.Ownership())
	}
	if !wf.OwnerTrusted || len(wf.OwnerRefClasses) != 1 || wf.OwnerRefClasses[0] != "pr" {
		t.Fatalf("the snapshot must be trusted and keep the ref classes: trusted=%v classes=%v", wf.OwnerTrusted, wf.OwnerRefClasses)
	}
	if wf.OwnerContact == nil || wf.OwnerContact.Channel != "#payments-ci" {
		t.Fatalf("contact = %+v", wf.OwnerContact)
	}
	if len(st.base.CreateJobCalls) == 0 {
		t.Fatal("no job was created")
	}
	for _, job := range st.base.CreateJobCalls {
		if job.OwnerGroup == nil || *job.OwnerGroup != "payments-ci" || job.OwnerContact == nil {
			t.Fatalf("job %s snapshot = %+v", job.Name, job.Ownership())
		}
	}
}

func TestEvalOfPullRequestHeadCIUsesPriorTrustedOwner(t *testing.T) {
	// No stored CI policy: the eval read the CI definition from the pull
	// request head itself, so its owners block is not trusted.
	base := newEventRecordingStore()
	group := "payments-ci"
	st := &priorOwnerStore{eventRecordingStore: base, prior: &models.WorkflowOwnership{Group: &group, CISHA: "main-sha", Trusted: true}, event: prEvent("main")}
	parent := eventEvalParent(t, "eval-1", "event-1")
	projectID := "project"
	parent.ProjectID = &projectID
	parent.CISHA = *parent.SourceRef
	parent.JobEnvVars["REACTORCIDE_PR_NUMBER"] = "7"
	base.base.GetJobByIDFunc = func(_ context.Context, _ string) (*models.Job, error) { copy := *parent; return &copy, nil }
	if _, err := NewTriggerProcessor(st, nil).ProcessTriggersFromDataWithOutcomes(context.Background(), ownedEvalPayload(t, parent, "attacker"), "", parent); err != nil {
		t.Fatal(err)
	}
	wf := createdWorkflow(t, base)
	if wf.OwnerGroup == nil || *wf.OwnerGroup != "payments-ci" || wf.OwnerSource != models.OwnerSourcePrior {
		t.Fatalf("the pull request set its own owner: %+v", wf.Ownership())
	}

	// With no earlier trusted snapshot the owner is unknown, not the head's.
	base2 := newEventRecordingStore()
	st2 := &priorOwnerStore{eventRecordingStore: base2, event: prEvent("main")}
	base2.base.GetJobByIDFunc = base.base.GetJobByIDFunc
	if _, err := NewTriggerProcessor(st2, nil).ProcessTriggersFromDataWithOutcomes(context.Background(), ownedEvalPayload(t, parent, "attacker"), "", parent); err != nil {
		t.Fatal(err)
	}
	if wf := createdWorkflow(t, base2); wf.OwnerGroup != nil {
		t.Fatalf("an unknown owner must stay unknown: %+v", wf.Ownership())
	}
}

func ownerPushEvalParent(t *testing.T, classes ...string) *models.Job {
	t.Helper()
	parent := eventEvalParent(t, "eval-1", "event-1")
	projectID := "project"
	parent.ProjectID = &projectID
	parent.CISHA = *parent.SourceRef // no policy: the push's own commit
	parent.JobEnvVars["REACTORCIDE_EVENT_TYPE"] = "push"
	delete(parent.JobEnvVars, "REACTORCIDE_PR_NUMBER")
	parent.PRNumber = nil
	parent.RefClasses = classes
	return parent
}

func TestEvalOfDefaultBranchPushTrustsItsCIDefinition(t *testing.T) {
	st := &priorOwnerStore{eventRecordingStore: newEventRecordingStore()}
	wf := runOwnedEval(t, st, ownerPushEvalParent(t, "default"), "payments-ci")
	if wf.OwnerGroup == nil || *wf.OwnerGroup != "payments-ci" || wf.OwnerSource != models.OwnerSourceCI || !wf.OwnerTrusted {
		t.Fatalf("snapshot = %+v", wf.Ownership())
	}
}

func TestEvalOfTagTrustsItsCIDefinition(t *testing.T) {
	st := &priorOwnerStore{eventRecordingStore: newEventRecordingStore()}
	wf := runOwnedEval(t, st, ownerPushEvalParent(t, "tag"), "payments-ci")
	if wf.OwnerGroup == nil || *wf.OwnerGroup != "payments-ci" || !wf.OwnerTrusted {
		t.Fatalf("snapshot = %+v", wf.Ownership())
	}
}

// A push to another branch with no policy: the CI definition is the pushed
// commit, which the pusher controls. It must not write a "ci" snapshot.
func TestEvalOfFeatureBranchPushNeverWritesItsOwner(t *testing.T) {
	st := &priorOwnerStore{eventRecordingStore: newEventRecordingStore(), prior: trustedPrior("payments-ci")}
	wf := runOwnedEval(t, st, ownerPushEvalParent(t), "attacker")
	if wf.OwnerGroup == nil || *wf.OwnerGroup != "payments-ci" || wf.OwnerSource != models.OwnerSourcePrior || !wf.OwnerTrusted {
		t.Fatalf("a feature-branch push set its own owner: %+v", wf.Ownership())
	}
	empty := &priorOwnerStore{eventRecordingStore: newEventRecordingStore()}
	if wf := runOwnedEval(t, empty, ownerPushEvalParent(t), "attacker"); wf.OwnerGroup != nil {
		t.Fatalf("with no prior trusted owner the owner must be unknown: %+v", wf.Ownership())
	}
}

// A push with a policy: the base is push.before, the pusher's own earlier
// commit. On a feature branch that base is not trusted.
func TestPolicyPushOnFeatureBranchDropsTheBaseOwner(t *testing.T) {
	group := "attacker"
	batches := []triggerWorkflowSpec{{ID: "build", Ownership: &models.WorkflowOwnership{Group: &group, CISHA: "before-sha"},
		OwnershipSource: models.OwnerSourceCI}}
	st := &priorOwnerStore{eventRecordingStore: newEventRecordingStore(), prior: trustedPrior("payments-ci")}
	parent := ownerPushEvalParent(t)
	parent.CISHA = "before-sha"
	NewTriggerProcessor(st, nil).assignTrustedOwnership(context.Background(), parent, batches)
	if batches[0].Ownership == nil || batches[0].Ownership.GroupName() != "payments-ci" || batches[0].OwnershipSource != models.OwnerSourcePrior {
		t.Fatalf("a policy push on a feature branch kept the base owner: %+v source=%q", batches[0].Ownership, batches[0].OwnershipSource)
	}

	// On the default branch the base (the earlier default commit) is trusted.
	group = "payments-ci"
	batches = []triggerWorkflowSpec{{ID: "build", Ownership: &models.WorkflowOwnership{Group: &group, CISHA: "before-sha"},
		OwnershipSource: models.OwnerSourceCI}}
	NewTriggerProcessor(st, nil).assignTrustedOwnership(context.Background(), ownerPushEvalParent(t, "default"), batches)
	if batches[0].OwnershipSource != models.OwnerSourceCI || !batches[0].Ownership.Trusted {
		t.Fatalf("a default-branch base owner must stay: %+v", batches[0].Ownership)
	}
}

// A pull request to a branch that is not the default: its base SHA is
// content that any pusher of that branch wrote.
func TestPullRequestToFeatureBranchUsesPriorOwner(t *testing.T) {
	st := &priorOwnerStore{eventRecordingStore: newEventRecordingStore(), prior: trustedPrior("payments-ci"), event: prEvent("release-candidate")}
	parent := eventEvalParent(t, "eval-1", "event-1")
	projectID := "project"
	parent.ProjectID = &projectID
	wf := runOwnedEval(t, st, parent, "attacker")
	if wf.OwnerGroup == nil || *wf.OwnerGroup != "payments-ci" || wf.OwnerSource != models.OwnerSourcePrior {
		t.Fatalf("snapshot = %+v", wf.Ownership())
	}
}

func TestSeparateCIRepositoryIsTrusted(t *testing.T) {
	parent := ownerPushEvalParent(t)
	st := &priorOwnerStore{eventRecordingStore: newEventRecordingStore(),
		project: &models.Project{ProjectID: "project", DefaultCISourceURL: parent.CIRepository}}
	wf := runOwnedEval(t, st, parent, "payments-ci")
	if wf.OwnerGroup == nil || *wf.OwnerGroup != "payments-ci" || wf.OwnerSource != models.OwnerSourceCI || !wf.OwnerTrusted {
		t.Fatalf("snapshot = %+v", wf.Ownership())
	}
}

// A prior snapshot that is not trusted is never copied.
func TestUntrustedPriorIsNotCopied(t *testing.T) {
	group := "attacker"
	st := &priorOwnerStore{eventRecordingStore: newEventRecordingStore(), prior: &models.WorkflowOwnership{Group: &group}}
	if wf := runOwnedEval(t, st, ownerPushEvalParent(t), "attacker"); wf.OwnerGroup != nil {
		t.Fatalf("an untrusted prior snapshot was copied: %+v", wf.Ownership())
	}
}

func TestNonEvalTriggerNeverSetsOwner(t *testing.T) {
	st := newEventRecordingStore()
	parent := eventEvalParent(t, "job-1", "event-1")
	data, _ := json.Marshal(map[string]any{
		"type": "trigger_job",
		"workflows": []map[string]any{{"id": "child", "name": "Child", "owners": map[string]any{"group": "attacker"},
			"jobs": []map[string]any{{"job_name": "x", "job_command": "true"}}}},
	})
	st.process(t, parent, data)
	if wf := createdWorkflow(t, st); wf.OwnerGroup != nil {
		t.Fatalf("a job's own trigger output set an owner: %+v", wf.Ownership())
	}
}

func TestOwnerOverrideGrantsHeadCIForOwnedWorkflowOnly(t *testing.T) {
	// No policy rule grants head CI; override_approvers names workflow_owner.
	refusing := strings.Replace(mixedTrustPolicy, "actors: {any: [repository_write]}", "actors: {any: [vcs_user:github/nobody]}", 1)
	parent := ownerPREvalParent(t, refusing)
	authority, err := approvals.RunAuthority{OwnerOverride: &approvals.RunOwnerOverride{Profile: "pr-untrusted"}}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	parent.JobEnvVars[approvals.RunAuthorityEnv] = authority
	approval := func(subject string) models.CIApproval {
		return models.CIApproval{ApprovalID: "approval-" + subject, Kind: models.CIApprovalKindHeadCI, Action: models.CIApprovalActionApproved,
			OrgID: "org", ProjectID: "project", PRNumber: 7, HeadRepository: "fork/repo", HeadSHA: *parent.SourceRef,
			Person: "github/carol", Subjects: []string{"vcs_user:github/carol", subject}, Channel: models.CIApprovalChannelComment}
	}
	run := func(subject string) []triggerWorkflowSpec {
		st := &multiProfileStore{workflowRuntimeStore: newWorkflowRuntimeStore(), profiles: mixedTrustProfiles(),
			approvals: []models.CIApproval{approval(subject)}}
		selected, _, _, err := NewTriggerProcessor(st, nil).selectPolicyCandidates(context.Background(), parent, ownedCandidates(), []string{".reactorcide/workflows/pr.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		return selected
	}
	if selected := run("reactorcide_group:payments-ci"); len(selected) != 1 || selected[0].CIOrigin != "head" ||
		selected[0].PolicyRuleID != overrideRuleID || selected[0].ExecutionProfile != "pr-untrusted" {
		t.Fatalf("an owner's override-ci must grant head CI for the owned workflow: %+v", selected)
	}
	if selected := run("reactorcide_group:attacker"); len(selected) != 1 || selected[0].CIOrigin != "base" {
		t.Fatalf("the head's owner must never approve: %+v", selected)
	}
}
