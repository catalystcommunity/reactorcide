package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type auditingProjectStore struct {
	*ProjectMockStore
	events []models.AuditEvent
}

func (s *auditingProjectStore) AppendAuditEvent(_ context.Context, event *models.AuditEvent) error {
	s.events = append(s.events, *event)
	return nil
}

func (s *auditingProjectStore) find(action string) *models.AuditEvent {
	for i := range s.events {
		if s.events[i].Action == action {
			return &s.events[i]
		}
	}
	return nil
}

// A REST change of the project event settings is in the audit log, as an
// organization change is.
func TestProjectEventSettingsAreAudited(t *testing.T) {
	projectID := uuid.New().String()
	project := testProject(projectID)
	mock := &ProjectMockStore{GetProjectByIDFunc: func(context.Context, string) (*models.Project, error) {
		p := *project
		return &p, nil
	}}
	st := &auditingProjectStore{ProjectMockStore: mock}
	handler := NewProjectHandler(st)

	body := `{"dynamic_jobs":"deny","duplicate_events":"skip","draft_prs":"skip","infra_retries":2,"egress_control":true,"description":"x"}`
	req := withProjectID(withUser(httptest.NewRequest(http.MethodPut, "/api/v1/projects/"+projectID, bytes.NewBufferString(body))), projectID)
	w := httptest.NewRecorder()
	handler.UpdateProject(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	event := st.find("project.event_settings")
	require.NotNil(t, event, "an event settings audit event")
	for _, key := range []string{"dynamic_jobs", "duplicate_events", "draft_prs", "infra_retries", "egress_control"} {
		assert.Contains(t, event.Details, key)
	}
	assert.Equal(t, "deny", event.Details["dynamic_jobs"].(map[string]any)["to"])

	// No change: no event.
	st.events = nil
	req = withProjectID(withUser(httptest.NewRequest(http.MethodPut, "/api/v1/projects/"+projectID,
		bytes.NewBufferString(`{"description":"y"}`))), projectID)
	w = httptest.NewRecorder()
	handler.UpdateProject(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Nil(t, st.find("project.event_settings"))
}

// projects create accepts the event settings.
func TestCreateProjectWithEventSettings(t *testing.T) {
	var created *models.Project
	mock := &ProjectMockStore{CreateProjectFunc: func(_ context.Context, project *models.Project) error {
		project.ProjectID = uuid.New().String()
		created = project
		return nil
	}}
	st := &auditingProjectStore{ProjectMockStore: mock}
	handler := NewProjectHandler(st)
	body := `{"name":"app","repo_url":"github.com/org/app","dynamic_jobs":"deny","duplicate_events":"skip","draft_prs":"run","infra_retries":0,"egress_control":true}`
	w := httptest.NewRecorder()
	handler.CreateProject(w, withUser(httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewBufferString(body))))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.NotNil(t, created)
	assert.Equal(t, "deny", created.DynamicJobs)
	assert.Equal(t, "skip", created.DuplicateEvents)
	assert.Equal(t, "run", created.DraftPRs)
	require.NotNil(t, created.InfraRetries)
	assert.Equal(t, 0, *created.InfraRetries)
	assert.True(t, created.EgressControl)
	event := st.find("project.create")
	require.NotNil(t, event)
	assert.Equal(t, "deny", event.Details["dynamic_jobs"])

	w = httptest.NewRecorder()
	handler.CreateProject(w, withUser(httptest.NewRequest(http.MethodPost, "/api/v1/projects",
		bytes.NewBufferString(`{"name":"bad","repo_url":"github.com/org/bad","dynamic_jobs":"maybe"}`))))
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}
