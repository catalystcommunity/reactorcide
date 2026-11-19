package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// A workflow node whose job_file cannot be loaded is recorded on the run as
// not admitted with the load error. It is not dropped silently.
func TestProcessTriggersFromData_JobFileLoadFailureIsRecordedOnNode(t *testing.T) {
	workspace := t.TempDir()
	jobsDir := filepath.Join(workspace, "src", ".reactorcide", "jobs")
	if err := os.MkdirAll(jobsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobsDir, "good.yaml"), []byte("name: good\njob:\n  image: alpine:3\n  command: echo ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

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

	payload := triggersFile{
		Type: "trigger_job", TriggerType: "runnerlib", OperationID: "operation-1",
		Workflows: []triggerWorkflowSpec{{
			Name: "checks", ID: "checks",
			Jobs: []triggerJobSpec{
				{JobName: "broken", JobFile: ".reactorcide/jobs/missing.yaml"},
				{JobName: "good", JobFile: ".reactorcide/jobs/good.yaml"},
			},
		}},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	tp := NewTriggerProcessor(profileStore, nil)
	result, err := tp.ProcessTriggersFromDataWithOutcomes(context.Background(), data, workspace, parent)
	if err != nil {
		t.Fatalf("trigger processing failed: %v", err)
	}
	if len(result.Workflows) != 1 {
		t.Fatalf("workflow outcomes = %d, want 1", len(result.Workflows))
	}
	outcome := result.Workflows[0]
	if outcome.Disposition != "not_admitted" {
		t.Fatalf("outcome = %#v, want not_admitted", outcome)
	}
	if !strings.Contains(outcome.Reason, "missing.yaml") {
		t.Fatalf("reason %q does not name the job file", outcome.Reason)
	}
	if strings.Contains(outcome.Reason, workspace) {
		t.Fatalf("reason %q contains the host workspace path", outcome.Reason)
	}

	var broken, good *models.WorkflowNode
	for _, node := range baseStore.nodes {
		switch node.DisplayName {
		case "broken":
			broken = node
		case "good":
			good = node
		}
	}
	if broken == nil {
		t.Fatal("the node with the failed job_file was dropped")
	}
	if broken.Status != "failed" || !strings.Contains(broken.DecisionReason, "workflow not admitted") ||
		!strings.Contains(broken.DecisionReason, "could not be loaded") {
		t.Fatalf("broken node = status %q reason %q", broken.Status, broken.DecisionReason)
	}
	// The node with the loaded job_file is registered. As for other admission
	// failures, the workflow stops at the refused node.
	if good == nil || good.JobSpec["container_image"] != "alpine:3" {
		t.Fatalf("sibling node was not registered from its job file: %#v", good)
	}
	if wf := baseStore.workflows[outcome.WorkflowID]; wf == nil || !strings.Contains(wf.LastError, "could not be loaded") {
		t.Fatal("workflow last error does not hold the load error")
	}
}

// A workflow entry over a job_file changes only the run_as fields that it
// sets, the same as runnerlib eval (_parse_workflow_job).
func TestOverlaySpecRunAsMergesFieldByField(t *testing.T) {
	tp := &TriggerProcessor{}
	admin := true
	base := triggerJobSpec{JobName: "win", RunAsUser: "builder", RunAsAdministrator: &admin}

	no := false
	got := tp.overlaySpec(base, triggerJobSpec{RunAsAdministrator: &no})
	if got.RunAsUser != "builder" || got.RunAsAdministrator == nil || *got.RunAsAdministrator {
		t.Fatalf("administrator-only overlay = user %q admin %v", got.RunAsUser, got.RunAsAdministrator)
	}
	got = tp.overlaySpec(base, triggerJobSpec{RunAsUser: "root"})
	if got.RunAsUser != "root" || got.RunAsAdministrator == nil || !*got.RunAsAdministrator {
		t.Fatalf("user-only overlay = user %q admin %v", got.RunAsUser, got.RunAsAdministrator)
	}
}
