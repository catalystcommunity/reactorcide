package vcs

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type linkStore struct {
	store.Store
	links map[string][]models.JobLink
}

func (s linkStore) ReplaceJobLinks(context.Context, string, []models.JobLink) error { return nil }
func (s linkStore) ListJobLinks(_ context.Context, id string) ([]models.JobLink, error) {
	return s.links[id], nil
}
func (s linkStore) ListJobLinksForJobs(context.Context, []string) (map[string][]models.JobLink, error) {
	return s.links, nil
}

func TestCommentBodiesShowJobLinks(t *testing.T) {
	updater := NewJobStatusUpdater()
	updater.SetStore(linkStore{links: map[string][]models.JobLink{
		"job-1": {{Name: "Build [results]", URL: "https://builds.example/run/1"}, {Name: "Trace", URL: "https://trace.example/a(b)"}},
	}})
	jobID := "job-1"
	body := updater.renderWorkflowCommentBody(&models.WorkflowInstance{Name: "CI", CommitSHA: "abcdef123"},
		[]models.WorkflowNode{{Name: "build", Status: "completed", JobID: &jobID}, {Name: "lint", Status: "completed"}}, "m")
	for _, want := range []string{"Links:", "- build: [Build \\[results\\]](https://builds.example/run/1) · [Trace](https://trace.example/a%28b%29)"} {
		if !strings.Contains(body, want) {
			t.Fatalf("workflow body lacks %q:\n%s", want, body)
		}
	}
	perJob := updater.renderPerJobCommentBody(&models.Job{JobID: "job-1", Name: "build", Status: "completed"}, "m")
	if !strings.Contains(perJob, "**Links:** [Build \\[results\\]]") {
		t.Fatalf("per-job body:\n%s", perJob)
	}
	if body := NewJobStatusUpdater().renderWorkflowCommentBody(&models.WorkflowInstance{}, []models.WorkflowNode{{Name: "x", JobID: &jobID}}, "m"); strings.Contains(body, "Links:") {
		t.Fatal("no store: no links")
	}
}
