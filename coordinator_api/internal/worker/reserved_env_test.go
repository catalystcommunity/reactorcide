package worker

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func triggerChildEnv(t *testing.T, parent *models.Job, env map[string]string) models.JSONB {
	t.Helper()
	tmpDir := t.TempDir()
	writeTriggersFile(t, tmpDir, triggersFile{Type: "trigger_job", Jobs: []triggerJobSpec{
		{JobName: "child", JobCommand: "echo child", Env: env},
	}})
	var created *models.Job
	mockStore := &MockStore{CreateJobFunc: func(_ context.Context, job *models.Job) error {
		job.JobID = "child-id"
		created = job
		return nil
	}}
	if err := NewTriggerProcessor(mockStore, corndogs.NewMockClient()).ProcessTriggers(context.Background(), tmpDir, parent); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created == nil {
		t.Fatal("expected a child job")
	}
	return created.JobEnvVars
}

// R1: a trigger of a CI event cannot set or replace coordinator facts.
func TestTriggerCannotReplaceEventFacts(t *testing.T) {
	eventID := "event-1"
	parent := &models.Job{JobID: "parent", UserID: "user", QueueName: "q", EventID: &eventID, JobEnvVars: models.JSONB{
		"REACTORCIDE_CI_APPROVALS":   `[{"approval_id":"real"}]`,
		"REACTORCIDE_ACTOR_SUBJECTS": `["vcs_user:github/author"]`,
		"REACTORCIDE_SHA":            "head123",
	}}
	env := triggerChildEnv(t, parent, map[string]string{
		"REACTORCIDE_CI_APPROVALS":   "[]",
		"REACTORCIDE_RUN_AUTHORITY":  `{"gated":true}`,
		"REACTORCIDE_ACTOR_SUBJECTS": `["project_owner"]`,
		"REACTORCIDE_SHA":            "forged",
		"REACTORCIDE_DIFF_BASE":      "forged",
		"reactorcide_head_actor":     "forged",
		"REACTORCIDE_CI_JOB":         "trust-guard",
	})
	want := map[string]interface{}{
		"REACTORCIDE_CI_APPROVALS":   `[{"approval_id":"real"}]`,
		"REACTORCIDE_ACTOR_SUBJECTS": `["vcs_user:github/author"]`,
		"REACTORCIDE_SHA":            "head123",
		"REACTORCIDE_CI_JOB":         "trust-guard",
	}
	for key, value := range want {
		if env[key] != value {
			t.Errorf("%s = %v, want %v", key, env[key], value)
		}
	}
	for _, key := range []string{"REACTORCIDE_RUN_AUTHORITY", "REACTORCIDE_DIFF_BASE", "reactorcide_head_actor"} {
		if _, ok := env[key]; ok {
			t.Errorf("%s was set by a trigger of a CI event", key)
		}
	}
}

// A parent with no CI event may still get event context from a trigger, but
// never authority facts.
func TestTriggerWithoutEventSuppliesContextNotAuthority(t *testing.T) {
	parent := &models.Job{JobID: "parent", UserID: "user", QueueName: "q", JobEnvVars: models.JSONB{}}
	env := triggerChildEnv(t, parent, map[string]string{
		"REACTORCIDE_EVENT_TYPE":     "push",
		"REACTORCIDE_ACTOR_SUBJECTS": `["project_owner"]`,
		"REACTORCIDE_CI_APPROVALS":   "[]",
	})
	if env["REACTORCIDE_EVENT_TYPE"] != "push" {
		t.Errorf("REACTORCIDE_EVENT_TYPE = %v, want push", env["REACTORCIDE_EVENT_TYPE"])
	}
	for _, key := range []string{"REACTORCIDE_ACTOR_SUBJECTS", "REACTORCIDE_CI_APPROVALS"} {
		if _, ok := env[key]; ok {
			t.Errorf("%s was supplied by a trigger", key)
		}
	}
}

func TestIsReservedEventEnv(t *testing.T) {
	for _, name := range []string{"REACTORCIDE_CI_APPROVALS", "REACTORCIDE_HEAD_ACTOR", "REACTORCIDE_BASE_REF",
		"REACTORCIDE_PR_NUMBER", "reactorcide_sha", "REACTORCIDE_RUN_AUTHORITY"} {
		if !isReservedEventEnv(name) {
			t.Errorf("%s should be reserved", name)
		}
	}
	for _, name := range []string{"REACTORCIDE_CI_JOB", "REACTORCIDE_CI_SOURCE_REF", "REACTORCIDE_CHECKOUT_MODE", "MY_VAR"} {
		if isReservedEventEnv(name) {
			t.Errorf("%s should not be reserved", name)
		}
	}
}
