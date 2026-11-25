package jobcontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/lib/pq"
)

// The graph of request-retry-unsuccessful.md: a fails, b (needs a) is
// skipped by its dependency, c (needs b, always) runs, and d (needs a) is
// skipped by its own `if:`.
func newSkippedDownstreamFixture(t *testing.T, workflowStatus string) *retryMockStore {
	t.Helper()
	st := newRetryMockStore()
	st.addJob(&models.Job{JobID: "eval-job", UserID: "user-1", QueueName: "reactorcide-jobs",
		RunnerImage: "quay.io/catalystcommunity/reactorcide_runner", CodeDir: "/job/src", JobDir: "/job/src"})
	st.addWorkflow(&models.WorkflowInstance{WorkflowID: "wf-1", UserID: "user-1", ParentJobID: strPtr("eval-job"),
		Name: "Release", Status: workflowStatus, QueueName: "reactorcide-jobs"})
	no := false
	spec := func(name string) models.JSONB { return models.JSONB{"job_name": name, "job_command": "make " + name} }
	st.addNode(&models.WorkflowNode{NodeID: "node-a", WorkflowID: "wf-1", Name: "a", DisplayName: "a",
		Status: "failed", JobID: strPtr("job-a"), JobSpec: spec("a")})
	st.addJob(&models.Job{JobID: "job-a", UserID: "user-1", Status: "failed", Name: "a",
		WorkflowID: strPtr("wf-1"), WorkflowNodeID: strPtr("node-a")})
	st.addNode(&models.WorkflowNode{NodeID: "node-b", WorkflowID: "wf-1", Name: "b", DisplayName: "b",
		Status: "skipped", DependsOn: pq.StringArray{"a"}, JobSpec: spec("b"),
		DecisionReason:  "skipped: condition all_success(needs) is false",
		DecisionReasons: []models.DecisionReason{{Kind: "dependency", Matched: &no, Condition: "all_success"}}})
	st.addNode(&models.WorkflowNode{NodeID: "node-c", WorkflowID: "wf-1", Name: "c", DisplayName: "c",
		Status: "completed", DependsOn: pq.StringArray{"b"}, Condition: "always", JobID: strPtr("job-c"), JobSpec: spec("c")})
	st.addJob(&models.Job{JobID: "job-c", UserID: "user-1", Status: "completed", Name: "c",
		WorkflowID: strPtr("wf-1"), WorkflowNodeID: strPtr("node-c")})
	st.addNode(&models.WorkflowNode{NodeID: "node-d", WorkflowID: "wf-1", Name: "d", DisplayName: "d",
		Status: "skipped", DependsOn: pq.StringArray{"a"}, JobSpec: spec("d"),
		DecisionReason:  "skipped by if: branch is not main",
		DecisionReasons: []models.DecisionReason{{Kind: "if", Matched: &no}}})
	return st
}

func nodeStatus(t *testing.T, st *retryMockStore, nodeID string) string {
	t.Helper()
	node, ok := st.nodes[nodeID]
	if !ok {
		t.Fatalf("node %s not found", nodeID)
	}
	return node.Status
}

func workflowStatus(t *testing.T, st *retryMockStore) string {
	t.Helper()
	wf, err := st.GetWorkflowInstance(context.Background(), "wf-1")
	if err != nil {
		t.Fatal(err)
	}
	return wf.Status
}

// finishNodeJob completes the current job of a node and runs the normal
// completion hook, as the worker does.
func finishNodeJob(t *testing.T, st *retryMockStore, cd corndogs.ClientInterface, nodeID string) {
	t.Helper()
	node := st.nodes[nodeID]
	if node.JobID == nil {
		t.Fatalf("node %s has no job to finish (status %s)", nodeID, node.Status)
	}
	job := st.jobs[*node.JobID]
	job.Status = "completed"
	if err := worker.NewTriggerProcessor(st, cd).ProcessWorkflowCompletion(context.Background(), "", job); err != nil {
		t.Fatalf("completion of %s: %v", nodeID, err)
	}
}

func TestRetryUnsuccessfulJobs_RunsDependencySkippedNodes(t *testing.T) {
	st := newSkippedDownstreamFixture(t, "failed")
	cd := corndogs.NewMockClient()

	retried, err := RetryUnsuccessfulJobs(context.Background(), st, cd, "wf-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(retried) != 1 || retried[0].Name != "a" {
		t.Fatalf("expected only a to be retried at once, got %+v", retried)
	}
	if got := nodeStatus(t, st, "node-b"); got != "waiting" {
		t.Errorf("b: expected waiting for the retried a, got %s", got)
	}
	if got := nodeStatus(t, st, "node-c"); got != "waiting" {
		t.Errorf("c: expected waiting for b, got %s", got)
	}
	if st.nodes["node-c"].JobID != nil {
		t.Error("c: expected the old job to be unbound from the reset node")
	}
	if got := nodeStatus(t, st, "node-d"); got != "skipped" {
		t.Errorf("d: a node skipped by its own if: must stay skipped, got %s", got)
	}
	if got := workflowStatus(t, st); got != "running" {
		t.Errorf("workflow: expected running, got %s", got)
	}

	finishNodeJob(t, st, cd, "node-a")
	if got := nodeStatus(t, st, "node-b"); got != "submitted" {
		t.Fatalf("b: expected submitted after a passed, got %s", got)
	}
	if got := workflowStatus(t, st); got != "running" {
		t.Errorf("workflow: expected running while b can run, got %s", got)
	}

	finishNodeJob(t, st, cd, "node-b")
	if got := nodeStatus(t, st, "node-c"); got != "submitted" {
		t.Fatalf("c: expected submitted again after b, got %s", got)
	}
	if got := workflowStatus(t, st); got != "running" {
		t.Errorf("workflow: expected running until c finishes, got %s", got)
	}

	finishNodeJob(t, st, cd, "node-c")
	if got := workflowStatus(t, st); got != "success" {
		t.Errorf("workflow: expected success at the end, got %s", got)
	}
	if got := nodeStatus(t, st, "node-d"); got != "skipped" {
		t.Errorf("d: expected skipped at the end, got %s", got)
	}
	if st.jobs["job-c"].Status != "completed" {
		t.Error("the old job of c must stay in the history")
	}
}

// The state of the csilgen release after the old retry-unsuccessful: a
// passed on retry, b is still skipped, and the workflow is success. A second
// retry-unsuccessful runs b at once.
func TestRetryUnsuccessfulJobs_SuccessWorkflowRunsDependencySkippedNode(t *testing.T) {
	st := newSkippedDownstreamFixture(t, "success")
	st.nodes["node-a"].Status = "completed"
	st.jobs["job-a"].Status = "completed"
	cd := corndogs.NewMockClient()

	started, err := RetryUnsuccessfulJobs(context.Background(), st, cd, "wf-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(started) != 1 || started[0].Name != "b" {
		t.Fatalf("expected b to start, got %+v", started)
	}
	if got := nodeStatus(t, st, "node-b"); got != "submitted" {
		t.Errorf("b: expected submitted, got %s", got)
	}
	if got := workflowStatus(t, st); got != "running" {
		t.Errorf("workflow: expected running, got %s", got)
	}
}

func TestRetryUnsuccessfulJobs_NothingToRetryIsRefused(t *testing.T) {
	st := newSkippedDownstreamFixture(t, "success")
	no := false
	st.nodes["node-a"].Status = "completed"
	st.jobs["job-a"].Status = "completed"
	// b's condition was false with its dependency successful: final.
	st.nodes["node-b"].Condition = "any_failed"
	// e needs the if:-skipped d: a skipped dependency that is final.
	st.addNode(&models.WorkflowNode{NodeID: "node-e", WorkflowID: "wf-1", Name: "e", Status: "skipped",
		DependsOn: pq.StringArray{"d"}, DecisionReasons: []models.DecisionReason{{Kind: "dependency", Matched: &no}}})

	_, err := RetryUnsuccessfulJobs(context.Background(), st, corndogs.NewMockClient(), "wf-1")
	var stateErr *StateError
	if !errors.As(err, &stateErr) || !errors.Is(err, ErrWorkflowNotRetryable) {
		t.Fatalf("expected a StateError, got %v", err)
	}
	if got := workflowStatus(t, st); got != "success" {
		t.Errorf("workflow: expected unchanged success, got %s", got)
	}
}

// A failed node downstream of another failed node waits for its upstream:
// it is reset, not retried at once.
func TestPlanRetryUnsuccessful_DownstreamRetryableNodeIsReset(t *testing.T) {
	no := false
	nodes := []models.WorkflowNode{
		{NodeID: "a", Name: "a", Status: "failed"},
		{NodeID: "b", Name: "b", Status: "skipped", DependsOn: pq.StringArray{"a"},
			DecisionReasons: []models.DecisionReason{{Kind: "dependency", Matched: &no}}},
		{NodeID: "c", Name: "c", Status: "failed", DependsOn: pq.StringArray{"b"}, Condition: "always"},
		{NodeID: "x", Name: "x", Status: "completed"},
	}
	plan := planRetryUnsuccessful(nodes, map[string]*models.Job{"a": {JobID: "ja"}, "c": {JobID: "jc"}})
	ids := func(ns []*models.WorkflowNode) []string {
		var out []string
		for _, n := range ns {
			out = append(out, n.NodeID)
		}
		return out
	}
	if got := ids(plan.retry); len(got) != 1 || got[0] != "a" {
		t.Errorf("retry: expected [a], got %v", got)
	}
	if got := ids(plan.reset); len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Errorf("reset: expected [b c], got %v", got)
	}
}

func TestIsWorkflowRerunnable_FinishedStatuses(t *testing.T) {
	for status, want := range map[string]bool{
		"success": true, "skipped": true, "failed": true, "cancelled": true,
		"running": false, "evaluating": false, "cancelling": false,
	} {
		if got := IsWorkflowRerunnable(&models.WorkflowInstance{Status: status}); got != want {
			t.Errorf("%s: got %v, want %v", status, got, want)
		}
	}
}
