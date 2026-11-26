package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// eventRecordingStore adds the CI event store to the workflow runtime fake.
type eventRecordingStore struct {
	*transactionalWorkflowRuntimeProfileStore
	base      *workflowRuntimeStore
	decisions map[string]models.CIEventWorkflowDecision
	statuses  []string
	summary   *models.CIEventSummary
}

func newEventRecordingStore() *eventRecordingStore {
	base := newWorkflowRuntimeStore()
	profileStore := &workflowRuntimeProfileStore{
		workflowRuntimeStore: base,
		profile:              &models.ExecutionProfile{OrgID: "org-1", Name: "standard"},
	}
	jobs := 0
	base.CreateJobFunc = func(_ context.Context, job *models.Job) error {
		jobs++
		job.JobID = fmt.Sprintf("job-%d", jobs)
		return nil
	}
	return &eventRecordingStore{
		transactionalWorkflowRuntimeProfileStore: &transactionalWorkflowRuntimeProfileStore{workflowRuntimeProfileStore: profileStore},
		base:                                     base,
		decisions:                                map[string]models.CIEventWorkflowDecision{},
	}
}

func (s *eventRecordingStore) RecordCIEvent(context.Context, *models.CIEvent) (*models.CIEvent, store.CIEventRecordOutcome, error) {
	return nil, "", fmt.Errorf("not used")
}
func (s *eventRecordingStore) GetCIEvent(_ context.Context, eventID string) (*models.CIEvent, error) {
	return &models.CIEvent{EventID: eventID, Summary: models.CIEventSummary{Action: "synchronize"}}, nil
}
func (s *eventRecordingStore) AttachCIEventEvalJob(context.Context, string, string) error { return nil }
func (s *eventRecordingStore) SetCIEventStatus(_ context.Context, _ string, status string) error {
	s.statuses = append(s.statuses, status)
	return nil
}
func (s *eventRecordingStore) SetCIEventSummary(_ context.Context, _ string, summary models.CIEventSummary) error {
	s.summary = &summary
	return nil
}
func (s *eventRecordingStore) UpsertCIEventWorkflowDecisions(_ context.Context, decisions []models.CIEventWorkflowDecision) error {
	for _, decision := range decisions {
		if prior, ok := s.decisions[decision.WorkflowSecurityID]; ok && decision.WorkflowID == nil {
			decision.WorkflowID = prior.WorkflowID
		}
		s.decisions[decision.WorkflowSecurityID] = decision
	}
	return nil
}
func (s *eventRecordingStore) ListCIEventWorkflowDecisions(context.Context, string) ([]models.CIEventWorkflowDecision, error) {
	return nil, nil
}
func (s *eventRecordingStore) GetCIEventWorkflowDecisionByWorkflow(context.Context, string) (*models.CIEventWorkflowDecision, error) {
	return nil, store.ErrNotFound
}
func (s *eventRecordingStore) GetRootWorkflowInstanceByEvent(_ context.Context, eventID, securityID string) (*models.WorkflowInstance, error) {
	for _, wf := range s.base.workflows {
		if wf.EventID != nil && *wf.EventID == eventID && wf.WorkflowSecurityID == securityID && wf.ParentWorkflowID == nil {
			return wf, nil
		}
	}
	return nil, store.ErrNotFound
}

// process runs one eval result the way SubmitTriggers does.
func (s *eventRecordingStore) process(t *testing.T, parent *models.Job, data []byte) TriggerProcessingResult {
	t.Helper()
	s.base.GetJobByIDFunc = func(_ context.Context, _ string) (*models.Job, error) {
		copy := *parent
		return &copy, nil
	}
	result, err := NewTriggerProcessor(s, nil).ProcessTriggersFromDataWithOutcomes(context.Background(), data, "", parent)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func eventEvalParent(t *testing.T, jobID, eventID string) *models.Job {
	t.Helper()
	ciRepo, ciSHA := "https://example.test/repo.git", "base-sha"
	sourceRef := "head-sha"
	parent := &models.Job{
		JobID: jobID, UserID: "user-1", OrgID: "org-1", ExecutionProfile: "standard", WorkerClass: "default",
		QueueName: "queue-1", CIRepository: ciRepo, CISHA: ciSHA, SourceURL: &ciRepo, SourceRef: &sourceRef,
		EventID: &eventID, JobEnvVars: models.JSONB{"REACTORCIDE_EVENT_TYPE": "pull_request_updated", "REACTORCIDE_JOB_KIND": "eval"},
	}
	if err := (&vcs.JobMetadata{VCSProvider: "github", Repo: "org/repo", PRNumber: 7, CommitSHA: sourceRef, IsEval: true}).ApplyToJob(parent); err != nil {
		t.Fatal(err)
	}
	return parent
}

func evalPayload(t *testing.T, parent *models.Job, withDecisions bool) []byte {
	t.Helper()
	payload := map[string]any{
		"type": "trigger_job", "trigger_type": "runnerlib_eval", "operation_id": "random-client-operation",
		"workflows": []map[string]any{{
			"id": "build", "name": "Build", "source_file": ".reactorcide/workflows/build.yaml",
			"ci_origin": "base", "ci_repository": parent.CIRepository, "ci_sha": parent.CISHA,
			"execution_profile": "standard", "worker_class": "default", "event_matched": true,
			"jobs": []map[string]any{{"job_name": "compile", "job_command": "make"}},
		}},
		"changed_files":       []string{"src/b.go", "src/a.go"},
		"changed_files_total": 2,
	}
	if withDecisions {
		payload["decisions"] = []map[string]any{
			{"id": "build", "name": "Build", "source_file": ".reactorcide/workflows/build.yaml", "decision": "run",
				"reasons": []map[string]any{{"kind": "event", "matched": true, "value": "pull_request_updated"}}},
			{"id": "docs", "name": "Docs", "source_file": ".reactorcide/workflows/docs.yaml", "decision": "skipped",
				"reasons": []map[string]any{{"kind": "paths", "matched": false, "include": []string{"docs/**"}, "matched_count": 0}}},
			{"id": "", "name": "bad.yaml", "source_file": ".reactorcide/workflows/bad.yaml", "decision": "invalid",
				"reasons": []map[string]any{{"kind": "invalid", "detail": "jobs must be a mapping"}}},
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEvalDecisions_RecordedForEveryWorkflowWithPolicyAndWorkflowLink(t *testing.T) {
	st := newEventRecordingStore()
	parent := eventEvalParent(t, "eval-1", "event-1")
	result := st.process(t, parent, evalPayload(t, parent, true))
	if len(result.Workflows) != 1 || result.Workflows[0].Disposition != "accepted" {
		t.Fatalf("outcomes = %+v", result.Workflows)
	}
	var created *models.WorkflowInstance
	for _, wf := range st.base.workflows {
		created = wf
	}
	if created == nil || created.TriggerOperationID != "event-1" || stringValue(created.EventID) != "event-1" {
		t.Fatalf("workflow must use the event as its operation and carry it: %+v", created)
	}
	if len(st.decisions) != 3 {
		t.Fatalf("decisions = %+v", st.decisions)
	}
	build := st.decisions["build"]
	if build.Decision != models.WorkflowDecisionRun || stringValue(build.WorkflowID) != created.WorkflowID {
		t.Fatalf("build decision = %+v", build)
	}
	// Each decision ends with the ref class reason of the event (WP3).
	if len(build.Reasons) != 3 || build.Reasons[0].Kind != "event" || build.Reasons[1].Kind != "policy" || build.Reasons[1].CIOrigin != "base" ||
		build.Reasons[2].Kind != "ref_class" {
		t.Fatalf("build reasons = %+v", build.Reasons)
	}
	if st.decisions["docs"].Decision != models.WorkflowDecisionSkipped || st.decisions["docs"].Reasons[0].Include[0] != "docs/**" {
		t.Fatalf("docs decision = %+v", st.decisions["docs"])
	}
	invalid, ok := st.decisions["file:.reactorcide/workflows/bad.yaml"]
	if !ok || invalid.Decision != models.WorkflowDecisionInvalid || invalid.Reasons[0].Detail != "jobs must be a mapping" {
		t.Fatalf("invalid decision = %+v", st.decisions)
	}
	if st.summary == nil || st.summary.ChangedFilesCount != 2 || strings.Join(st.summary.ChangedFilesPreview, ",") != "src/a.go,src/b.go" || st.summary.Action != "synchronize" {
		t.Fatalf("summary = %+v", st.summary)
	}
	if len(st.statuses) == 0 || st.statuses[len(st.statuses)-1] != models.CIEventStatusEvaluated {
		t.Fatalf("event statuses = %v", st.statuses)
	}
	for _, job := range st.base.CreateJobCalls {
		if stringValue(job.EventID) != "event-1" {
			t.Fatalf("node job does not carry the event: %+v", job)
		}
	}
}

func TestEvalDecisions_RetriedEvalReusesEventWorkflow(t *testing.T) {
	st := newEventRecordingStore()
	first := eventEvalParent(t, "eval-1", "event-1")
	retry := eventEvalParent(t, "eval-2", "event-1")
	for _, parent := range []*models.Job{first, retry} {
		st.process(t, parent, evalPayload(t, parent, true))
	}
	if len(st.base.workflows) != 1 {
		t.Fatalf("a retried eval of one event made %d workflows, want 1", len(st.base.workflows))
	}
}

func TestEvalDecisions_OldPayloadWithoutDecisionsRecordsCreatedWorkflows(t *testing.T) {
	st := newEventRecordingStore()
	parent := eventEvalParent(t, "eval-1", "event-1")
	st.process(t, parent, evalPayload(t, parent, false))
	if len(st.decisions) != 1 || st.decisions["build"].Decision != models.WorkflowDecisionRun || st.decisions["build"].WorkflowID == nil {
		t.Fatalf("decisions = %+v", st.decisions)
	}
}

func TestEvalDecisions_JobWithoutEventRecordsNothing(t *testing.T) {
	st := newEventRecordingStore()
	parent := eventEvalParent(t, "eval-1", "event-1")
	parent.EventID = nil
	st.process(t, parent, evalPayload(t, parent, true))
	if len(st.decisions) != 0 || len(st.statuses) != 0 {
		t.Fatalf("an eval without an event must not record event data: %+v %v", st.decisions, st.statuses)
	}
	for _, wf := range st.base.workflows {
		if wf.TriggerOperationID != "random-client-operation" {
			t.Fatalf("without an event the client operation ID is kept: %q", wf.TriggerOperationID)
		}
	}
}

func pushEvalParent(t *testing.T, subjects string) *models.Job {
	t.Helper()
	ciRepo, before, after := "https://example.test/repo.git", "before-sha", "after-sha"
	projectID := "project-1"
	parent := &models.Job{
		JobID: "eval-push", OrgID: "org-1", ProjectID: &projectID, ExecutionProfile: "standard",
		CIRepository: ciRepo, CISHA: before, SourceURL: &ciRepo, SourceRef: &after,
		JobEnvVars: models.JSONB{
			"REACTORCIDE_EVENT_TYPE": "push", "REACTORCIDE_BRANCH": "main",
			"REACTORCIDE_ACTOR_SUBJECTS": subjects, "REACTORCIDE_HEAD_ACTOR": "pusher",
		},
	}
	if err := (&vcs.JobMetadata{VCSProvider: "github", Repo: "org/repo", Branch: "main", CommitSHA: after, IsEval: true}).ApplyToJob(parent); err != nil {
		t.Fatal(err)
	}
	return parent
}

func pushCandidates() *policyCandidateSet {
	return &policyCandidateSet{
		Base: []triggerWorkflowSpec{{ID: "ci", Name: "CI", EventMatched: true,
			DependencyPaths: []string{".reactorcide/workflows/ci.yaml"}, Jobs: []triggerJobSpec{{JobName: "test", JobCommand: "base"}}}},
		Head: []triggerWorkflowSpec{{ID: "ci", Name: "CI", EventMatched: true,
			DependencyPaths: []string{".reactorcide/workflows/ci.yaml"}, Jobs: []triggerJobSpec{{JobName: "test", JobCommand: "head"}}}},
	}
}

// Without a stored policy there is nothing to decide: the eval job already
// runs the event's own CI code, so the evaluator sends no candidates. A stale
// evaluator that sends some gets the base definitions, and no violation is
// recorded because no policy refused anything.
func TestSelectPolicyCandidates_PushWithoutPolicyRunsBaseWithoutViolation(t *testing.T) {
	tp := NewTriggerProcessor(newWorkflowRuntimeStore(), nil)
	changed := []string{".reactorcide/workflows/ci.yaml"}

	parent := pushEvalParent(t, `["vcs_user:github/pusher","repository_write"]`)
	selected, violations, notes, err := tp.selectPolicyCandidatesRefused(context.Background(), parent, pushCandidates(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].CIOrigin != "base" || selected[0].Jobs[0].JobCommand != "base" || len(violations) != 0 {
		t.Fatalf("selected=%+v violations=%+v", selected, violations)
	}
	if note := notes["ci"]; !strings.Contains(strings.Join(note.Reasons, " "), "no coordinator CI policy") {
		t.Fatalf("policy note = %+v", note)
	}
}

func TestSelectPolicyCandidates_PushWithStoredPolicyFollowsPolicy(t *testing.T) {
	policyDocument := []byte(`version: 1
head_ci:
  - id: pushers
    actors: {any: ["vcs_user:github/maintainer"]}
    workflows: [ci]
    paths: [".reactorcide/**"]
    events: [push]
    use: {ci_source: head, profile: standard, workers: default}
`)
	parent, _ := mixedTrustEvalParent(t, policyDocument)
	push := pushEvalParent(t, `["vcs_user:github/pusher","repository_write"]`)
	push.JobEnvVars["REACTORCIDE_CI_POLICY"] = parent.JobEnvVars["REACTORCIDE_CI_POLICY"]
	push.JobEnvVars["REACTORCIDE_CI_POLICY_REVISION"] = parent.JobEnvVars["REACTORCIDE_CI_POLICY_REVISION"]
	tp := NewTriggerProcessor(newWorkflowRuntimeStore(), nil)
	changed := []string{".reactorcide/workflows/ci.yaml"}

	// With a stored policy, the policy decides: repository_write alone is not
	// a grant.
	selected, _, _, err := tp.selectPolicyCandidates(context.Background(), push, pushCandidates(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].CIOrigin != "base" {
		t.Fatalf("stored policy must deny this pusher: %+v", selected)
	}

	push.JobEnvVars["REACTORCIDE_ACTOR_SUBJECTS"] = `["vcs_user:github/maintainer"]`
	selected, _, notes, err := tp.selectPolicyCandidates(context.Background(), push, pushCandidates(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].CIOrigin != "head" || selected[0].PolicyRuleID != "pushers" || notes["ci"].RuleID != "pushers" {
		t.Fatalf("stored policy must allow the maintainer: %+v %+v", selected, notes)
	}
}

// A pull request eval that read its CI from the base marks each workflow
// that the base definition matched, so "Reactorcide Checks" can fail a
// required workflow that the head removed or renamed.
func TestEvalDecisions_MarkBaseDefinitionsOfPullRequests(t *testing.T) {
	st := newEventRecordingStore()
	parent := eventEvalParent(t, "eval-1", "event-1")
	parent.JobEnvVars["REACTORCIDE_PR_NUMBER"] = "7"
	st.process(t, parent, evalPayload(t, parent, true))
	hasBase := func(decision models.CIEventWorkflowDecision) bool {
		for _, reason := range decision.Reasons {
			if reason.Kind == models.DecisionReasonBaseDefinition {
				return true
			}
		}
		return false
	}
	if !hasBase(st.decisions["build"]) {
		t.Fatalf("a matched base workflow must be marked: %+v", st.decisions["build"].Reasons)
	}
	if hasBase(st.decisions["docs"]) {
		t.Fatal("a base workflow that did not match the event must not be marked")
	}

	// The eval read the head itself (no stored policy): not a base view.
	head := newEventRecordingStore()
	headParent := eventEvalParent(t, "eval-2", "event-2")
	headParent.JobEnvVars["REACTORCIDE_PR_NUMBER"] = "7"
	headParent.CISHA = *headParent.SourceRef
	head.process(t, headParent, evalPayload(t, headParent, true))
	if hasBase(head.decisions["build"]) {
		t.Fatal("decisions of the head's own CI are not base definitions")
	}
}
