package oidcissuer

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeFactsStore struct {
	event *models.CIEvent
}

func (f *fakeFactsStore) GetOrganizationByID(_ context.Context, id string) (*models.Organization, error) {
	return &models.Organization{OrgID: id, Name: "acme"}, nil
}
func (f *fakeFactsStore) GetProjectByID(_ context.Context, id string) (*models.Project, error) {
	return &models.Project{ProjectID: id, Name: "app"}, nil
}
func (f *fakeFactsStore) GetWorkflowInstance(context.Context, string) (*models.WorkflowInstance, error) {
	return nil, nil
}
func (f *fakeFactsStore) GetCIEvent(context.Context, string) (*models.CIEvent, error) {
	return f.event, nil
}
func (f *fakeFactsStore) GetPrincipal(context.Context, string) (*models.Principal, error) {
	return nil, nil
}

func factsFor(t *testing.T, event *models.CIEvent, job *models.Job) JobFacts {
	t.Helper()
	if job == nil {
		job = &models.Job{JobID: "j1", OrgID: "o1"}
	}
	if event != nil {
		id := "e1"
		job.EventID = &id
	}
	facts, err := LoadJobFacts(context.Background(), &fakeFactsStore{event: event}, job)
	require.NoError(t, err)
	return facts
}

// A manual eval submit states its branch and event type. Its token never
// has the ref, the subject, or the event type of a push to that branch.
func TestLoadJobFacts_ManualSubmitCannotForgeRef(t *testing.T) {
	forged := &models.CIEvent{EventID: "e1", Source: models.CIEventSourceManual, EventType: "push", Branch: "main"}
	facts := factsFor(t, forged, nil)
	assert.Equal(t, EventTypeManual, facts.EventType, "a stored push type of a manual event is ignored")
	assert.Equal(t, "", facts.Ref)
	assert.Equal(t, "main", facts.SubmittedRef)
	claims := Claims(facts, nil)
	assert.Equal(t, "manual", claims["event_type"])
	assert.Equal(t, "", claims["ref"])
	assert.Equal(t, "main", claims["submitted_ref"])
	assert.NotContains(t, claims["sub"], "refs/heads/main")

	stated := &models.CIEvent{EventID: "e1", Source: models.CIEventSourceManual, EventType: "manual", Ref: "refs/heads/main"}
	facts = factsFor(t, stated, nil)
	assert.Equal(t, "", facts.Ref)
	assert.Equal(t, "refs/heads/main", facts.SubmittedRef)
	assert.NotContains(t, Subject(facts), "refs/heads/main")
}

// A manual run that the coordinator resolved from the VCS keeps its real ref
// but has the event type "manual".
func TestLoadJobFacts_CoordinatorManualRun(t *testing.T) {
	run := &models.CIEvent{EventID: "e1", Source: models.CIEventSourceManual, Provider: "github", EventType: "push",
		Ref: "refs/heads/main", Branch: "main", RefClasses: []string{"manual"}}
	facts := factsFor(t, run, nil)
	assert.Equal(t, EventTypeManual, facts.EventType)
	assert.Equal(t, "refs/heads/main", facts.Ref)
	assert.Equal(t, "", facts.SubmittedRef)
	assert.Equal(t, []string{"manual"}, facts.RefClasses)

	pr := 4
	prRun := &models.CIEvent{EventID: "e1", Source: models.CIEventSourceManual, Provider: "github",
		EventType: "pull_request_updated", PRNumber: &pr, Ref: "main", Branch: "main"}
	facts = factsFor(t, prRun, nil)
	assert.Equal(t, "refs/pull/4/head", facts.Ref)
	assert.Equal(t, EventTypeManual, facts.EventType)
}

// An API submit with no event never gets a ref claim, also when its source
// ref is "refs/heads/main".
func TestLoadJobFacts_APISubmitRefIsOnlySubmitted(t *testing.T) {
	ref := "refs/heads/main"
	facts := factsFor(t, nil, &models.Job{JobID: "j1", OrgID: "o1", SourceRef: &ref})
	assert.Equal(t, EventTypeAPI, facts.EventType)
	assert.Equal(t, "", facts.Ref)
	assert.Equal(t, "refs/heads/main", facts.SubmittedRef)
}

func TestLoadJobFacts_WebhookPushKeepsRef(t *testing.T) {
	push := &models.CIEvent{EventID: "e1", Source: models.CIEventSourceWebhook, Provider: "github", EventType: "push", Branch: "main"}
	facts := factsFor(t, push, nil)
	assert.Equal(t, "push", facts.EventType)
	assert.Equal(t, "refs/heads/main", facts.Ref)
}
