package worker

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// advisoryShedStore is the store surface skipAdvisoryWhenShedding reads.
type advisoryShedStore struct {
	store.Store
	level   string
	enabled bool
	entries []models.RequiredWorkflow
	event   models.CIEvent
}

func (s *advisoryShedStore) GetShedState(context.Context, string) (models.ShedState, error) {
	return models.EffectiveShedState(s.level, s.level, nil), nil
}
func (s *advisoryShedStore) EffectiveDraftPRs(context.Context, string) (string, error) {
	return "run", nil
}
func (s *advisoryShedStore) SetOrganizationDraftPRs(context.Context, string, string) error {
	return nil
}
func (s *advisoryShedStore) SetGlobalShedLevel(context.Context, string) error { return nil }
func (s *advisoryShedStore) SetOrganizationShed(context.Context, string, string, *models.ShedActionsConfig, bool) error {
	return nil
}

func (s *advisoryShedStore) GetCIEvent(context.Context, string) (*models.CIEvent, error) {
	copied := s.event
	return &copied, nil
}

type advisoryShedFixture struct {
	*advisoryShedStore
	store.CIEventStore
	store.RequiredSetStore
}

func (f advisoryShedFixture) GetCIEvent(ctx context.Context, id string) (*models.CIEvent, error) {
	return f.advisoryShedStore.GetCIEvent(ctx, id)
}
func (f advisoryShedFixture) GetProjectRequiredSetSettings(_ context.Context, projectID string) (*models.ProjectRequiredSetSettings, error) {
	return &models.ProjectRequiredSetSettings{ProjectID: projectID, Enabled: f.enabled}, nil
}
func (f advisoryShedFixture) ListRequiredWorkflows(context.Context, string) ([]models.RequiredWorkflow, error) {
	return f.entries, nil
}

func newAdvisoryShedProcessor(level string, enabled bool, required ...string) (*TriggerProcessor, *models.Job) {
	projectID, eventID := "project-1", "event-1"
	inner := &advisoryShedStore{level: level, enabled: enabled,
		event: models.CIEvent{EventID: eventID, ProjectID: &projectID, Branch: "main"}}
	for _, id := range required {
		inner.entries = append(inner.entries, models.RequiredWorkflow{WorkflowSecurityID: id})
	}
	fixture := advisoryShedFixture{advisoryShedStore: inner}
	tp := &TriggerProcessor{store: fixture}
	return tp, &models.Job{JobID: "eval", OrgID: "org-1", EventID: &eventID, ProjectID: &projectID}
}

func batchIDs(batches []triggerWorkflowSpec) []string {
	ids := []string{}
	for _, batch := range batches {
		ids = append(ids, workflowSecurityIDOf(batch))
	}
	return ids
}

func TestSkipAdvisoryWhenShedding(t *testing.T) {
	batches := func() []triggerWorkflowSpec {
		return []triggerWorkflowSpec{{ID: "build", Name: "build"}, {ID: "lint", Name: "lint"}}
	}
	ctx := context.Background()

	tp, job := newAdvisoryShedProcessor(models.ShedLevelCritical, true, "build")
	notes := map[string]models.DecisionReason{}
	kept := tp.skipAdvisoryWhenShedding(ctx, job, batches(), notes)
	if got := batchIDs(kept); len(got) != 1 || got[0] != "build" {
		t.Fatalf("critical with the set on kept %v, want [build]", got)
	}
	note, ok := notes[advisorySkipNotePrefix+"lint"]
	if !ok || note.Kind != models.SkipReasonShed || note.Detail == "" {
		t.Fatalf("the skipped advisory workflow needs a shed reason: %+v", notes)
	}

	tp, job = newAdvisoryShedProcessor(models.ShedLevelNormal, true, "build")
	if got := batchIDs(tp.skipAdvisoryWhenShedding(ctx, job, batches(), map[string]models.DecisionReason{})); len(got) != 2 {
		t.Fatalf("normal level kept %v, want both", got)
	}

	tp, job = newAdvisoryShedProcessor(models.ShedLevelCritical, false)
	if got := batchIDs(tp.skipAdvisoryWhenShedding(ctx, job, batches(), map[string]models.DecisionReason{})); len(got) != 2 {
		t.Fatalf("set off kept %v, want both (every workflow is required)", got)
	}
}
