package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/backlog"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/stretchr/testify/require"
)

type backlogMockStore struct {
	*MockStore
}

func (backlogMockStore) ListQueues(context.Context, int, int) ([]models.Queue, error) {
	return []models.Queue{
		{QueueID: "q-a", QueueUUID: "uuid-a", DisplayName: "a", OrgID: "org-a", Characteristics: characteristics.Characteristics{"os": characteristics.StringValue("linux")}},
		{QueueID: "q-b", QueueUUID: "uuid-b", DisplayName: "b", OrgID: "org-b", Characteristics: characteristics.Characteristics{"os": characteristics.StringValue("linux")}},
	}, nil
}

func (backlogMockStore) ListWaitingJobGroups(context.Context) ([]models.WaitingJobGroup, error) {
	return []models.WaitingJobGroup{
		{QueueName: "uuid-a", ResourceCPURequest: "1", ResourceMemoryLimit: "1Gi", Count: 2, OldestCreatedAt: time.Now().Add(-time.Minute)},
		{QueueName: "uuid-b", ResourceCPURequest: "2", ResourceMemoryLimit: "2Gi", Count: 3, OldestCreatedAt: time.Now().Add(-time.Hour)},
	}, nil
}

func (backlogMockStore) GetOrganizationByName(_ context.Context, name string) (*models.Organization, error) {
	if name == "alpha" {
		return &models.Organization{OrgID: "org-a", Name: "alpha"}, nil
	}
	return nil, store.ErrNotFound
}

func backlogRequest(t *testing.T, h *BacklogHandler, query string, principal *checkauth.Principal) (int, backlog.Snapshot) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/backlog"+query, nil)
	if principal != nil {
		req = req.WithContext(checkauth.SetPrincipalContext(req.Context(), principal))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var snapshot backlog.Snapshot
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snapshot))
	}
	return rec.Code, snapshot
}

func TestBacklogHandlerAuthorization(t *testing.T) {
	h := NewBacklogHandler(backlogMockStore{MockStore: &MockStore{}})
	readCaps, err := tokencaps.New(tokencaps.WorkersRead)
	require.NoError(t, err)
	jobsCaps, err := tokencaps.New(tokencaps.JobsRead)
	require.NoError(t, err)

	global := &checkauth.Principal{CredentialType: "instance_token", AllOrganizations: true, Capabilities: readCaps}
	code, snapshot := backlogRequest(t, h, "", global)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, snapshot.Queues, 2)
	require.Equal(t, int64(5), snapshot.Totals.Count)

	orgToken := &checkauth.Principal{CredentialType: "api_token", OrganizationIDs: []string{"org-a"}, Capabilities: readCaps}
	code, _ = backlogRequest(t, h, "", orgToken)
	require.Equal(t, http.StatusForbidden, code, "an organization token must not read the global backlog")
	code, snapshot = backlogRequest(t, h, "?org=alpha", orgToken)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, snapshot.Queues, 1)
	require.Equal(t, "uuid-a", snapshot.Queues[0].QueueUUID)
	require.Equal(t, int64(2000), snapshot.Queues[0].CPURequestMillicores)

	wrongCap := &checkauth.Principal{CredentialType: "instance_token", AllOrganizations: true, Capabilities: jobsCaps}
	code, _ = backlogRequest(t, h, "", wrongCap)
	require.Equal(t, http.StatusForbidden, code)

	code, _ = backlogRequest(t, h, "", nil)
	require.Equal(t, http.StatusUnauthorized, code)
	code, _ = backlogRequest(t, h, "?org=missing", global)
	require.Equal(t, http.StatusNotFound, code)
}

func TestBacklogHandlerWithoutBacklogStore(t *testing.T) {
	h := NewBacklogHandler(&MockStore{})
	readCaps, _ := tokencaps.New(tokencaps.WorkersRead)
	code, _ := backlogRequest(t, h, "", &checkauth.Principal{CredentialType: "instance_token", AllOrganizations: true, Capabilities: readCaps})
	require.Equal(t, http.StatusServiceUnavailable, code)
}

type suggestionMockStore struct {
	*MockStore
	runs   []models.JobRunWithSummary
	filter store.JobRunHistoryFilter
}

func (s *suggestionMockStore) ListJobRunsWithSummaries(_ context.Context, filter store.JobRunHistoryFilter) ([]models.JobRunWithSummary, error) {
	s.filter = filter
	return s.runs, nil
}

func (s *suggestionMockStore) UpsertJobResourceSummary(context.Context, *models.JobResourceSummary) error {
	return nil
}

func TestGetJobResourceSuggestion(t *testing.T) {
	projectID := "project-1"
	job := &models.Job{JobID: "job-1", OrgID: "org-a", UserID: "org-a", ProjectID: &projectID, Name: "build"}
	mock := &suggestionMockStore{MockStore: &MockStore{GetJobByIDFunc: func(context.Context, string) (*models.Job, error) { return job, nil }}}
	for i := 0; i < 6; i++ {
		cpu, memory := int64(800), int64(1024*1024*1024)
		mock.runs = append(mock.runs, models.JobRunWithSummary{
			Job:     models.Job{Status: "completed"},
			Summary: &models.JobResourceSummary{SummaryVersion: 1 << 20, SampleCount: 10, CPUP95Millicores: &cpu, MemoryMaxBytes: &memory},
		})
	}
	handler := NewJobHandler(mock, nil)
	readCaps, _ := tokencaps.New(tokencaps.JobsRead)
	request := func(query string, principal *checkauth.Principal) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-1/resource-suggestion"+query, nil)
		ctx := setIDContext(req.Context(), "job_id", "job-1")
		if principal != nil {
			ctx = checkauth.SetPrincipalContext(ctx, principal)
		}
		rec := httptest.NewRecorder()
		handler.GetJobResourceSuggestion(rec, req.WithContext(ctx))
		return rec
	}

	rec := request("?limit=20", &checkauth.Principal{CredentialType: "api_token", OrganizationIDs: []string{"org-a"}, Capabilities: readCaps})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "build", body["name"])
	require.Equal(t, true, body["sufficient"])
	require.Equal(t, "1", body["cpu_request"]) // 800m * 1.2 = 960m -> 1000m
	require.Equal(t, "1280Mi", body["memory_limit"])
	require.Equal(t, 20, mock.filter.Limit)
	require.Equal(t, "org-a", mock.filter.OrgID)

	require.Equal(t, http.StatusForbidden, request("", &checkauth.Principal{CredentialType: "api_token", OrganizationIDs: []string{"org-b"}, Capabilities: readCaps}).Code)
	require.Equal(t, http.StatusBadRequest, request("?limit=1", &checkauth.Principal{CredentialType: "api_token", OrganizationIDs: []string{"org-a"}, Capabilities: readCaps}).Code)
}
