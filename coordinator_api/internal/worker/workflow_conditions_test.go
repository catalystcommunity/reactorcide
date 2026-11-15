package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// A job whose `if:` is false becomes a skipped node with the eval's reasons;
// dependents follow the normal condition rules and record a structured
// dependency reason; a submitted node keeps its `if:` reasons.
func TestProcessTriggersFromData_JobIfSkipsNodeWithReasons(t *testing.T) {
	baseStore := newWorkflowRuntimeStore()
	profileStore := &workflowRuntimeProfileStore{
		workflowRuntimeStore: baseStore,
		profile:              &models.ExecutionProfile{OrgID: "org-1", Name: "standard", RuntimeCapabilities: []string{}},
	}
	parent := &models.Job{
		JobID: "eval-job", UserID: "user-1", OrgID: "org-1",
		ExecutionProfile: "standard", WorkerClass: "default", QueueName: "queue-1",
	}
	baseStore.GetJobByIDFunc = func(_ context.Context, jobID string) (*models.Job, error) {
		if jobID != parent.JobID {
			return nil, store.ErrNotFound
		}
		copy := *parent
		return &copy, nil
	}
	baseStore.CreateJobFunc = func(_ context.Context, job *models.Job) error {
		job.JobID = "job-" + job.Name
		return nil
	}

	no, yes := false, true
	count := 0
	matchedCount := 1
	payload := triggersFile{
		Type: "trigger_job", TriggerType: "runnerlib", OperationID: "operation-1",
		Workflows: []triggerWorkflowSpec{{Name: "ci", ID: "ci", Jobs: []triggerJobSpec{
			{JobName: "build"},
			{JobName: "docs", Skip: true, DecisionReasons: []models.DecisionReason{{
				Kind: "paths", Matched: &no, Include: []string{"docs/**"}, MatchedCount: &count,
			}}},
			{JobName: "api", DecisionReasons: []models.DecisionReason{{
				Kind: "paths", Matched: &yes, Include: []string{"api/**"},
				MatchedFiles: []string{"api/x.go"}, MatchedCount: &matchedCount,
			}}},
			{JobName: "publish-docs", DependsOn: []string{"docs"}},
			{JobName: "notify", DependsOn: []string{"docs"}, Condition: "always"},
		}}},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	tp := NewTriggerProcessor(profileStore, nil)
	if _, err := tp.ProcessTriggersFromDataWithOutcomes(context.Background(), data, "", parent); err != nil {
		t.Fatalf("trigger processing failed: %v", err)
	}

	nodes := map[string]*models.WorkflowNode{}
	for _, node := range baseStore.nodes {
		nodes[node.Name] = node
	}

	docs := nodes["docs"]
	if docs.Status != "skipped" || docs.JobID != nil || docs.CompletedAt == nil {
		t.Fatalf("docs node = status %q job %v completed %v, want skipped without a job", docs.Status, docs.JobID, docs.CompletedAt)
	}
	if len(docs.DecisionReasons) != 1 || docs.DecisionReasons[0].Kind != "paths" {
		t.Fatalf("docs reasons = %#v", docs.DecisionReasons)
	}
	if docs.DecisionReason != "skipped by if: no changed file matches paths include [docs/**]" {
		t.Fatalf("docs summary = %q", docs.DecisionReason)
	}

	publish := nodes["publish-docs"]
	if publish.Status != "skipped" {
		t.Fatalf("publish-docs status = %q, want skipped", publish.Status)
	}
	dep := publish.DecisionReasons[len(publish.DecisionReasons)-1]
	if dep.Kind != "dependency" || dep.Matched == nil || *dep.Matched || dep.Condition != "all_success" ||
		len(dep.Deps) != 1 || dep.Deps[0] != (models.DependencyOutcome{Name: "docs", Status: "skipped"}) {
		t.Fatalf("publish-docs dependency reason = %#v", dep)
	}
	if !strings.HasPrefix(publish.DecisionReason, "skipped: condition all_success is false (docs skipped)") {
		t.Fatalf("publish-docs summary = %q", publish.DecisionReason)
	}

	notify := nodes["notify"]
	if notify.Status != "submitted" || notify.JobID == nil {
		t.Fatalf("notify = status %q job %v, want submitted", notify.Status, notify.JobID)
	}
	if got := notify.DecisionReasons; len(got) != 1 || got[0].Kind != "dependency" || got[0].Condition != "always" || got[0].Matched == nil || !*got[0].Matched {
		t.Fatalf("notify reasons = %#v", got)
	}

	api := nodes["api"]
	if api.Status != "submitted" {
		t.Fatalf("api status = %q", api.Status)
	}
	if len(api.DecisionReasons) != 2 || api.DecisionReasons[0].Kind != "paths" || api.DecisionReasons[1].Kind != "dependency" {
		t.Fatalf("api reasons = %#v, want the if reason then the dependency reason", api.DecisionReasons)
	}
	if api.DecisionReason != "submitted: 1 changed file matches paths include [api/**]; no dependencies" {
		t.Fatalf("api summary = %q", api.DecisionReason)
	}

	var skippedEvent bool
	for _, event := range baseStore.events {
		if event.NodeID != nil && *event.NodeID == docs.NodeID && event.EventType == "node_skipped" {
			skippedEvent = true
		}
	}
	if !skippedEvent {
		t.Fatal("no node_skipped workflow event for the docs node")
	}
}

// A workflow whose jobs are all skipped by `if:` is itself skipped.
func TestProcessTriggersFromData_AllJobsSkippedByIfSkipsWorkflow(t *testing.T) {
	baseStore := newWorkflowRuntimeStore()
	parent := &models.Job{JobID: "eval-job", UserID: "user-1", OrgID: "org-1", QueueName: "queue-1"}
	baseStore.GetJobByIDFunc = func(_ context.Context, _ string) (*models.Job, error) {
		copy := *parent
		return &copy, nil
	}
	no := false
	payload := triggersFile{
		Type: "trigger_job", TriggerType: "runnerlib", OperationID: "operation-1",
		Workflows: []triggerWorkflowSpec{{Name: "docs", ID: "docs", Jobs: []triggerJobSpec{
			{JobName: "docs", Skip: true, DecisionReasons: []models.DecisionReason{{Kind: "branch", Matched: &no, Value: "dev", Patterns: []string{"main"}}}},
		}}},
	}
	data, _ := json.Marshal(payload)
	tp := NewTriggerProcessor(baseStore, nil)
	result, err := tp.ProcessTriggersFromDataWithOutcomes(context.Background(), data, "", parent)
	if err != nil {
		t.Fatalf("trigger processing failed: %v", err)
	}
	if len(result.Workflows) != 1 || result.Workflows[0].Status != "skipped" {
		t.Fatalf("workflow outcomes = %#v, want one skipped workflow", result.Workflows)
	}
}

func TestSummarizeDecisionReasons(t *testing.T) {
	yes, no := true, false
	two := 2
	cases := []struct {
		reasons []models.DecisionReason
		want    string
	}{
		{[]models.DecisionReason{{Kind: "event", Matched: &yes, Value: "push", Patterns: []string{"push"}}}, "event push matches [push]"},
		{[]models.DecisionReason{{Kind: "branch", Matched: &no, Value: "dev", Patterns: []string{"main"}}}, "branch dev does not match [main]"},
		{[]models.DecisionReason{{Kind: "paths", Matched: &yes, Include: []string{"a/**"}, MatchedCount: &two}}, "2 changed files match paths include [a/**]"},
		{[]models.DecisionReason{{Kind: "schedule", Matched: &no, Required: &yes}}, "not a schedule event; the job runs only on schedules"},
		{[]models.DecisionReason{{Kind: "dependency", Detail: "waiting on build"}}, "waiting on build"},
		{[]models.DecisionReason{{Kind: "dependency", Matched: &yes, Condition: "any_failed", Deps: []models.DependencyOutcome{{Name: "test", Status: "failed"}}}}, "condition any_failed is true (test failed)"},
	}
	for _, tc := range cases {
		if got := models.SummarizeDecisionReasons(tc.reasons); got != tc.want {
			t.Errorf("summary = %q, want %q", got, tc.want)
		}
	}
}
