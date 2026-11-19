package vcs

import (
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestReportsSayWhoseFailureItIs(t *testing.T) {
	u := &JobStatusUpdater{}
	lost := models.FailureClassWorkerLost
	infra := &models.Job{JobID: "a", Name: "build", Status: "failed", LastError: "lost: no lease", FailureClass: &lost}
	if got := u.getStatusDescription(infra); !strings.HasPrefix(got, "Infrastructure failure (infra.worker_lost)") {
		t.Fatalf("status description = %q", got)
	}
	if _, text := renderStatus(infra); text != "failed (infrastructure)" {
		t.Fatalf("rolling comment status = %q", text)
	}
	body := u.renderPerJobCommentBody(infra, "<!-- m -->")
	if !strings.Contains(body, "**Failure:** infrastructure failure: the worker was lost") {
		t.Fatalf("comment body = %s", body)
	}

	jobClass := models.FailureClassJob
	code := 1
	pipeline := &models.Job{JobID: "b", Name: "test", Status: "failed", ExitCode: &code, LastError: "job exited with code 1", FailureClass: &jobClass}
	if got := u.getStatusDescription(pipeline); got != "CI build failed: job exited with code 1" {
		t.Fatalf("pipeline status description = %q", got)
	}
	if _, text := renderStatus(pipeline); text != "failed (pipeline)" {
		t.Fatalf("rolling comment status = %q", text)
	}
	if body := u.renderPerJobCommentBody(pipeline, "m"); !strings.Contains(body, "pipeline failure: the job exited with an error") {
		t.Fatalf("comment body = %s", body)
	}
}
