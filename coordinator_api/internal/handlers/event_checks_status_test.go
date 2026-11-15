package handlers

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// recordingJobStatusUpdater records UpdateJobStatus calls and, optionally,
// event checks publishes.
type recordingJobStatusUpdater struct {
	jobs          []string
	events        []string
	supportsEvent bool
}

func (r *recordingJobStatusUpdater) UpdateJobStatus(_ context.Context, job *models.Job) error {
	r.jobs = append(r.jobs, job.JobID)
	return nil
}

func (r *recordingJobStatusUpdater) SupportsEventChecks() bool { return r.supportsEvent }

func (r *recordingJobStatusUpdater) PublishEventChecks(_ context.Context, eventID string) error {
	r.events = append(r.events, eventID)
	return nil
}

// statusRecordingVCSClient records commit statuses; other methods panic.
type statusRecordingVCSClient struct {
	vcs.Client
	updates []vcs.StatusUpdate
}

func (c *statusRecordingVCSClient) UpdateCommitStatus(_ context.Context, _ string, update vcs.StatusUpdate) error {
	c.updates = append(c.updates, update)
	return nil
}

type mergedPRStore struct {
	*WebhookMockStore
	jobs []models.Job
}

func (m *mergedPRStore) ListJobsForPR(context.Context, string, int) ([]models.Job, error) {
	return m.jobs, nil
}

func TestHandlePRMergedRefreshesOnlyLegacyStandaloneJobs(t *testing.T) {
	node, event := "node-1", "event-1"
	st := &mergedPRStore{WebhookMockStore: &WebhookMockStore{}, jobs: []models.Job{
		{JobID: "legacy-standalone", Status: "running"},
		{JobID: "workflow-node", Status: "running", WorkflowNodeID: &node},
		{JobID: "event-eval", Status: "running", EventID: &event},
		{JobID: "finished", Status: "completed"},
	}}
	handler := NewWebhookHandler(st, nil)
	updater := &recordingJobStatusUpdater{}
	handler.SetStatusUpdater(updater)

	handler.handlePRMerged(&vcs.WebhookEvent{
		Repository:  vcs.RepositoryInfo{FullName: "org/repo"},
		PullRequest: &vcs.PullRequestInfo{Number: 7},
	})

	if len(updater.jobs) != 1 || updater.jobs[0] != "legacy-standalone" {
		t.Fatalf("refreshed %v, want only the legacy standalone job", updater.jobs)
	}
}

func TestPostEvalPendingStatusUsesEventChecks(t *testing.T) {
	handler := NewWebhookHandler(&WebhookMockStore{}, nil)
	updater := &recordingJobStatusUpdater{supportsEvent: true}
	handler.SetStatusUpdater(updater)
	client := &statusRecordingVCSClient{}
	event := "event-1"

	handler.postEvalPendingStatus(&models.Job{JobID: "eval", EventID: &event}, client, "org/repo", "abc123")

	if len(updater.events) != 1 || updater.events[0] != event {
		t.Fatalf("event checks published for %v", updater.events)
	}
	if len(client.updates) != 0 {
		t.Fatalf("an event eval also posted %+v", client.updates)
	}
}

func TestPostEvalPendingStatusLegacyPostsEvalContext(t *testing.T) {
	handler := NewWebhookHandler(&WebhookMockStore{}, nil)
	// A store without CI events: the updater cannot publish event checks.
	handler.SetStatusUpdater(&recordingJobStatusUpdater{supportsEvent: false})
	client := &statusRecordingVCSClient{}
	event := "event-1"

	handler.postEvalPendingStatus(&models.Job{JobID: "eval", EventID: &event}, client, "org/repo", "abc123")

	if len(client.updates) != 1 || client.updates[0].Context != "reactorcide/eval" || client.updates[0].State != vcs.StatusPending {
		t.Fatalf("legacy pending status = %+v", client.updates)
	}
}
