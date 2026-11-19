package vcs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// requiredSetChecksStore adds a required set to the checks fake.
type requiredSetChecksStore struct {
	*fakeChecksStore
	enabled bool
	entries []models.RequiredWorkflow
}

func (f *requiredSetChecksStore) GetProjectRequiredSetSettings(_ context.Context, projectID string) (*models.ProjectRequiredSetSettings, error) {
	return &models.ProjectRequiredSetSettings{ProjectID: projectID, Enabled: f.enabled}, nil
}
func (f *requiredSetChecksStore) GetOrgRequiredSetSettings(_ context.Context, orgID string) (*models.OrgRequiredSetSettings, error) {
	return &models.OrgRequiredSetSettings{OrgID: orgID, MaxQuarantineDays: 30}, nil
}
func (f *requiredSetChecksStore) SetRequiredSetEnabled(context.Context, string, string, bool, *string) ([]string, error) {
	return nil, nil
}
func (f *requiredSetChecksStore) SetProjectRequiredSetApprovers(context.Context, string, string, []string, *[]models.RequiredSetApproverRule) error {
	return nil
}
func (f *requiredSetChecksStore) UpsertOrgRequiredSetSettings(context.Context, *models.OrgRequiredSetSettings) error {
	return nil
}
func (f *requiredSetChecksStore) ListRequiredWorkflows(context.Context, string) ([]models.RequiredWorkflow, error) {
	return f.entries, nil
}
func (f *requiredSetChecksStore) ListKnownWorkflows(context.Context, string) ([]models.KnownWorkflow, error) {
	return nil, nil
}
func (f *requiredSetChecksStore) SetRequiredWorkflowQuarantine(context.Context, string, string, string, *time.Time, string, *string) error {
	return nil
}
func (f *requiredSetChecksStore) CreateRequiredSetProposal(context.Context, *models.RequiredSetProposal) error {
	return nil
}
func (f *requiredSetChecksStore) GetRequiredSetProposal(context.Context, string) (*models.RequiredSetProposal, error) {
	return nil, nil
}
func (f *requiredSetChecksStore) ListRequiredSetProposals(context.Context, string, string, int) ([]models.RequiredSetProposal, error) {
	return nil, nil
}
func (f *requiredSetChecksStore) DecideRequiredSetProposal(context.Context, string, string, *string, string) (*models.RequiredSetProposal, error) {
	return nil, nil
}

func newRequiredSetFixture(enabled bool, required ...string) (*JobStatusUpdater, *requiredSetChecksStore, *recordingClient) {
	updater, fake, client := newChecksFixture()
	projectID := "project-1"
	fake.event.ProjectID = &projectID
	fake.event.Branch = "main"
	fake.jobs["eval-job"].Status = "completed"
	wrapped := &requiredSetChecksStore{fakeChecksStore: fake, enabled: enabled}
	for _, id := range required {
		wrapped.entries = append(wrapped.entries, models.RequiredWorkflow{ProjectID: projectID, WorkflowSecurityID: id})
	}
	updater.SetStore(wrapped)
	return updater, wrapped, client
}

func TestComputeEventChecksAdvisory(t *testing.T) {
	tests := []struct {
		name        string
		in          EventChecksInput
		state       StatusState
		description string
	}{
		{"advisory failure does not fail",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{
				{ID: "a", Name: "build", Status: "success"}, {ID: "b", Name: "lint", Status: "failed", Advisory: true}}},
			StatusSuccess, "1 workflow passed; 1 advisory failed"},
		{"required failure fails",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{
				{ID: "a", Name: "build", Status: "failed"}, {ID: "b", Name: "lint", Status: "success", Advisory: true}}},
			StatusFailure, "1 of 1 workflow failed: build"},
		{"running advisory does not keep the status pending",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{
				{ID: "a", Name: "build", Status: "success"}, {ID: "b", Name: "lint", Status: "running", Advisory: true}}},
			StatusSuccess, "1 workflow passed"},
		{"only advisory workflows",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{{ID: "b", Name: "lint", Status: "running", Advisory: true}}},
			StatusSuccess, "No required workflows"},
		{"advisory not admitted does not fail",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{{ID: "a", Name: "build", Status: "success"}},
				NotAdmittedAdvisory: []string{"deploy"}},
			StatusSuccess, "1 workflow passed; 1 advisory failed"},
		{"policy refusal still fails",
			EventChecksInput{EvalJobStatus: "completed", Workflows: []EventChecksWorkflow{{ID: "b", Name: "lint", Status: "success", Advisory: true}},
				PolicyViolations: 1},
			StatusFailure, "Policy refused 1 CI change"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeEventChecks(tt.in)
			if got.State != tt.state || got.Description != tt.description {
				t.Fatalf("got %s %q, want %s %q", got.State, got.Description, tt.state, tt.description)
			}
		})
	}
}

func TestPublishEventChecksSetOffEveryWorkflowBlocks(t *testing.T) {
	updater, fake, client := newRequiredSetFixture(false)
	fake.workflows = []models.WorkflowInstance{
		{WorkflowID: "w1", Name: "build", WorkflowSecurityID: "build", Status: "success"},
		{WorkflowID: "w2", Name: "lint", WorkflowSecurityID: "lint", Status: "failed"},
	}
	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	if got := client.last(); got.State != StatusFailure {
		t.Fatalf("set off: %+v, want failure", got)
	}
}

func TestPublishEventChecksSetOnCountsOnlyRequired(t *testing.T) {
	updater, fake, client := newRequiredSetFixture(true, "build")
	fake.workflows = []models.WorkflowInstance{
		{WorkflowID: "w1", Name: "build", WorkflowSecurityID: "build", Status: "success"},
		{WorkflowID: "w2", Name: "lint", WorkflowSecurityID: "lint", Status: "failed"},
	}
	fake.decisions = []models.CIEventWorkflowDecision{
		{WorkflowSecurityID: "deploy", WorkflowName: "deploy", Decision: models.WorkflowDecisionNotAdmitted},
	}
	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	got := client.last()
	if got.State != StatusSuccess || got.Description != "1 workflow passed; 2 advisory failed" {
		t.Fatalf("set on: %+v", got)
	}

	// A required failure fails.
	fake.workflows[0].Status = "failed"
	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	if got := client.last(); got.State != StatusFailure || !strings.Contains(got.Description, "build") {
		t.Fatalf("required failure: %+v", got)
	}
}

func TestPublishEventChecksQuarantineAndExpiry(t *testing.T) {
	updater, fake, client := newRequiredSetFixture(true, "build")
	future := time.Now().Add(time.Hour)
	fake.entries[0].QuarantinedUntil = &future
	fake.workflows = []models.WorkflowInstance{{WorkflowID: "w1", Name: "build", WorkflowSecurityID: "build", Status: "failed"}}
	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	if got := client.last(); got.State != StatusSuccess {
		t.Fatalf("quarantined: %+v, want success", got)
	}
	// At the expiry the workflow is required again, with no other change.
	past := time.Now().Add(-time.Minute)
	fake.entries[0].QuarantinedUntil = &past
	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	if got := client.last(); got.State != StatusFailure {
		t.Fatalf("expired quarantine: %+v, want failure", got)
	}
}

func TestPublishEventChecksBranchScopedEntry(t *testing.T) {
	updater, fake, client := newRequiredSetFixture(true)
	fake.entries = []models.RequiredWorkflow{{ProjectID: "project-1", WorkflowSecurityID: "build", BranchPattern: "release/*"}}
	fake.workflows = []models.WorkflowInstance{{WorkflowID: "w1", Name: "build", WorkflowSecurityID: "build", Status: "failed"}}
	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	if got := client.last(); got.State != StatusSuccess {
		t.Fatalf("main is not release/*: %+v, want success", got)
	}
	fake.event.Branch = "release/1.2"
	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	if got := client.last(); got.State != StatusFailure {
		t.Fatalf("release/1.2: %+v, want failure", got)
	}
}

func TestUpdateWorkflowStatusAdvisorySuppressesExtraStatus(t *testing.T) {
	updater, fake, client := newRequiredSetFixture(true, "other")
	fake.workflows = []models.WorkflowInstance{{WorkflowID: "wf-1", Name: "build", WorkflowSecurityID: "build", Status: "failed"}}
	wf := eventWorkflow("failed", "lint")
	wf.WorkflowSecurityID = "build"
	if err := updater.UpdateWorkflowStatus(context.Background(), wf, nil); err != nil {
		t.Fatal(err)
	}
	got := client.contexts()
	if len(got) != 1 || got[0] != models.CIEventChecksContext {
		t.Fatalf("contexts = %v, want only the aggregate (no lint status for an advisory workflow)", got)
	}
	if client.last().State != StatusSuccess {
		t.Fatalf("aggregate = %+v, want success", client.last())
	}
}

func TestUpdateWorkflowStatusRequiredKeepsExtraStatus(t *testing.T) {
	updater, fake, client := newRequiredSetFixture(true, "build")
	fake.workflows = []models.WorkflowInstance{{WorkflowID: "wf-1", Name: "build", WorkflowSecurityID: "build", Status: "failed"}}
	wf := eventWorkflow("failed", "lint")
	wf.WorkflowSecurityID = "build"
	if err := updater.UpdateWorkflowStatus(context.Background(), wf, nil); err != nil {
		t.Fatal(err)
	}
	if got := client.contexts(); len(got) != 2 || got[0] != "lint" {
		t.Fatalf("contexts = %v, want [lint Reactorcide Checks]", got)
	}
}

func TestMarkAdvisoryRendersCommentEntries(t *testing.T) {
	updater := NewJobStatusUpdater()
	wf := &models.WorkflowInstance{Name: "lint", CommitSHA: "abcdef1234"}
	body := updater.renderWorkflowCommentBody(wf, nil, "<!-- m -->")
	if got := markAdvisory(body, requiredset.Required); got != body {
		t.Fatal("a required workflow body must not change")
	}
	advisory := markAdvisory(body, requiredset.Classification{State: requiredset.StateAdvisory})
	if !strings.HasPrefix(advisory, "## lint for commit `abcdef1`\n\n> **Advisory — not required.**") {
		t.Fatalf("advisory body = %q", advisory)
	}
	until := time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)
	quarantined := markAdvisory(body, requiredset.Classification{State: requiredset.StateQuarantined, QuarantinedUntil: &until, QuarantineReason: "flaky | runner"})
	if !strings.Contains(quarantined, "**Quarantined — not required.** The quarantine ends at 2026-12-01 10:00 UTC") ||
		!strings.Contains(quarantined, `Reason: flaky \| runner.`) {
		t.Fatalf("quarantined body = %q", quarantined)
	}
	if label := (requiredset.Classification{State: requiredset.StateAdvisory}).Label(); label != "advisory — not required" {
		t.Fatalf("label = %q", label)
	}
}
