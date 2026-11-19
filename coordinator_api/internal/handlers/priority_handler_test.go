package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/priority"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePriorityStore keeps lanes and group shifts in memory.
type fakePriorityStore struct {
	*fakeGroupStore
	lanes  map[string]models.PriorityLane
	jobs   map[string]*models.Job
	shifts map[string]int
}

func newFakePriorityStore() *fakePriorityStore {
	return &fakePriorityStore{
		fakeGroupStore: &fakeGroupStore{MockStore: &MockStore{}, members: map[string][]models.User{}, users: map[string]models.User{}},
		lanes:          map[string]models.PriorityLane{}, jobs: map[string]*models.Job{}, shifts: map[string]int{},
	}
}

func laneKey(projectID *string, name string) string {
	if projectID == nil {
		return "org/" + name
	}
	return *projectID + "/" + name
}

func (s *fakePriorityStore) ListPriorityLanes(_ context.Context, _ string, projectID *string) ([]models.PriorityLane, error) {
	out := []models.PriorityLane{}
	for _, lane := range s.lanes {
		if (projectID == nil) == (lane.ProjectID == nil) && (projectID == nil || *projectID == *lane.ProjectID) {
			out = append(out, lane)
		}
	}
	return out, nil
}
func (s *fakePriorityStore) GetPriorityLane(_ context.Context, _ string, projectID *string, name string) (*models.PriorityLane, error) {
	if lane, ok := s.lanes[laneKey(projectID, name)]; ok {
		return &lane, nil
	}
	return nil, store.ErrNotFound
}
func (s *fakePriorityStore) UpsertPriorityLane(_ context.Context, lane *models.PriorityLane) error {
	lane.LaneID = "lane-" + lane.Name
	s.lanes[laneKey(lane.ProjectID, lane.Name)] = *lane
	return nil
}
func (s *fakePriorityStore) DeletePriorityLane(_ context.Context, _ string, projectID *string, name string) error {
	if _, ok := s.lanes[laneKey(projectID, name)]; !ok {
		return store.ErrNotFound
	}
	delete(s.lanes, laneKey(projectID, name))
	return nil
}
func (s *fakePriorityStore) ListPrincipalGroupShifts(context.Context, string, string) ([]models.GroupPriorityShift, error) {
	return nil, nil
}
func (s *fakePriorityStore) ListGroupShifts(context.Context, string, []string) ([]models.GroupPriorityShift, error) {
	return nil, nil
}
func (s *fakePriorityStore) SetGroupPriorityShift(_ context.Context, groupID string, shift int) error {
	if err := priority.ValidateGroupShift(shift); err != nil {
		return store.ErrInvalidInput
	}
	s.shifts[groupID] = shift
	return nil
}
func (s *fakePriorityStore) GetProjectByID(_ context.Context, id string) (*models.Project, error) {
	if id != "proj-1" {
		return nil, store.ErrNotFound
	}
	return &models.Project{ProjectID: "proj-1", OrgID: "org-1", Name: "app"}, nil
}
func (s *fakePriorityStore) GetJobByID(_ context.Context, id string) (*models.Job, error) {
	if job, ok := s.jobs[id]; ok {
		return job, nil
	}
	return nil, store.ErrNotFound
}

func priorityRequest(t *testing.T, h *PriorityHandler, principal *checkauth.Principal, method, scope, ref, rest string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	if body == nil {
		data = nil
	}
	req := httptest.NewRequest(method, "/api/v1/"+scope+"/"+ref+"/"+rest, bytes.NewReader(data))
	if principal != nil {
		req = req.WithContext(checkauth.SetPrincipalContext(req.Context(), principal))
	}
	w := httptest.NewRecorder()
	if scope == "organizations" {
		h.ServeOrganization(w, req, ref, rest)
	} else {
		h.ServeProject(w, req, ref, rest)
	}
	return w
}

var priorityAdmin = &checkauth.Principal{CredentialType: "instance_token", OrganizationIDs: []string{"org-1"}, AllCapabilities: true}

func TestPriorityHandler_ListShowsBuiltinLanes(t *testing.T) {
	h := NewPriorityHandler(newFakePriorityStore())
	w := priorityRequest(t, h, priorityAdmin, http.MethodGet, "organizations", "catalyst", "priority-lanes", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var response PriorityLanesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	assert.Empty(t, response.Lanes)
	assert.Len(t, response.Effective, len(priority.Builtins()))
	assert.Equal(t, priority.MaxGroupShift, response.Bounds.MaxGroupShift)
}

func TestPriorityHandler_SetBaseOnlyKeepsBuiltinMatch(t *testing.T) {
	fake := newFakePriorityStore()
	h := NewPriorityHandler(fake)
	w := priorityRequest(t, h, priorityAdmin, http.MethodPut, "organizations", "catalyst", "priority-lanes/pr-ready", map[string]any{"base": -50})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	lane := fake.lanes["org/pr-ready"]
	assert.Equal(t, -50, lane.Base)
	assert.Equal(t, []string{"pr"}, []string(lane.ClassesAny), "the built-in match stays")
	require.NotNil(t, lane.Draft)
	assert.False(t, *lane.Draft)
	assert.Equal(t, 400, lane.Position)

	// A project lane with the same name keeps the organization lane's match.
	fake.lanes["org/hotfix"] = models.PriorityLane{Name: "hotfix", Base: 200, Position: 5, ClassesAny: []string{"hotfix"}, Enabled: true}
	w = priorityRequest(t, h, priorityAdmin, http.MethodPut, "projects", "proj-1", "priority-lanes/hotfix", map[string]any{"base": 150})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []string{"hotfix"}, []string(fake.lanes["proj-1/hotfix"].ClassesAny))
	assert.Equal(t, 5, fake.lanes["proj-1/hotfix"].Position)

	w = priorityRequest(t, h, priorityAdmin, http.MethodGet, "projects", "proj-1", "priority-lanes", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var response PriorityLanesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Len(t, response.Lanes, 1)
	assert.NotEmpty(t, response.Inherited)
	assert.Equal(t, "hotfix", response.Effective[0].Name)
	assert.Equal(t, priority.ScopeProject, response.Effective[0].Scope)
	assert.Equal(t, priority.ScopeOrg, response.Effective[0].Replaces)
}

func TestPriorityHandler_SetValidates(t *testing.T) {
	h := NewPriorityHandler(newFakePriorityStore())
	cases := []map[string]any{
		{},           // no base
		{"base": 10}, // a new lane needs a match
		{"base": 9000, "match": map[string]any{}},
		{"base": 1, "match": map[string]any{"job_kind": "deploy"}},
		{"base": 1, "match": map[string]any{"event_types": []string{"comment"}}},
	}
	for _, body := range cases {
		w := priorityRequest(t, h, priorityAdmin, http.MethodPut, "organizations", "catalyst", "priority-lanes/new-lane", body)
		assert.Equal(t, http.StatusBadRequest, w.Code, "%v: %s", body, w.Body.String())
	}
	w := priorityRequest(t, h, priorityAdmin, http.MethodPut, "organizations", "catalyst", "priority-lanes/Bad", map[string]any{"base": 1, "match": map[string]any{}})
	assert.Equal(t, http.StatusBadRequest, w.Code)
	w = priorityRequest(t, h, priorityAdmin, http.MethodPut, "organizations", "catalyst", "priority-lanes/all", map[string]any{"base": 1, "match": map[string]any{}})
	assert.Equal(t, http.StatusOK, w.Code, "an empty match is every job")
}

func TestPriorityHandler_DeleteAndAuthority(t *testing.T) {
	fake := newFakePriorityStore()
	h := NewPriorityHandler(fake)
	require.Equal(t, http.StatusOK, priorityRequest(t, h, priorityAdmin, http.MethodPut, "organizations", "catalyst", "priority-lanes/schedule", map[string]any{"base": -350}).Code)
	assert.Equal(t, http.StatusNoContent, priorityRequest(t, h, priorityAdmin, http.MethodDelete, "organizations", "catalyst", "priority-lanes/schedule", nil).Code)
	assert.Equal(t, http.StatusNotFound, priorityRequest(t, h, priorityAdmin, http.MethodDelete, "organizations", "catalyst", "priority-lanes/schedule", nil).Code)

	otherOrg := &checkauth.Principal{CredentialType: "instance_token", OrganizationIDs: []string{"org-2"}, AllCapabilities: true}
	assert.Equal(t, http.StatusForbidden, priorityRequest(t, h, otherOrg, http.MethodGet, "organizations", "catalyst", "priority-lanes", nil).Code)
	assert.Equal(t, http.StatusForbidden, priorityRequest(t, h, nil, http.MethodGet, "organizations", "catalyst", "priority-lanes", nil).Code)
	reader := &checkauth.Principal{CredentialType: "service_token", OrganizationIDs: []string{"org-1"}, Capabilities: tokencaps.Set{tokencaps.ProjectsRead: {}}}
	assert.Equal(t, http.StatusOK, priorityRequest(t, h, reader, http.MethodGet, "organizations", "catalyst", "priority-lanes", nil).Code)
	assert.Equal(t, http.StatusForbidden, priorityRequest(t, h, reader, http.MethodPut, "organizations", "catalyst", "priority-lanes/x", map[string]any{"base": 1, "match": map[string]any{}}).Code)
}

func TestPriorityHandler_Explain(t *testing.T) {
	fake := newFakePriorityStore()
	h := NewPriorityHandler(fake)
	w := priorityRequest(t, h, priorityAdmin, http.MethodPost, "organizations", "catalyst", "priority/explain",
		map[string]any{"ref_classes": []string{"pr"}, "draft": true, "requested": 3})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var explanation priority.Explanation
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &explanation))
	assert.Equal(t, "pr-draft", explanation.Decision.Lane)
	assert.Equal(t, -197, explanation.Decision.Final)

	job := &models.Job{JobID: "job-1", OrgID: "org-1", RefClasses: []string{"default"}}
	priority.Compute(priority.Effective(nil, nil), priority.GroupShifts{}, priority.Facts{RefClasses: []string{"default"}}).Apply(job)
	fake.jobs["job-1"] = job
	fake.jobs["job-other"] = &models.Job{JobID: "job-other", OrgID: "org-2"}
	w = priorityRequest(t, h, priorityAdmin, http.MethodPost, "organizations", "catalyst", "priority/explain", map[string]any{"job_id": "job-1"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &explanation))
	require.NotNil(t, explanation.Stored)
	assert.Equal(t, "default-branch", explanation.Stored.Lane)
	w = priorityRequest(t, h, priorityAdmin, http.MethodPost, "organizations", "catalyst", "priority/explain", map[string]any{"job_id": "job-other"})
	assert.Equal(t, http.StatusNotFound, w.Code, "a job of another organization")
	w = priorityRequest(t, h, priorityAdmin, http.MethodPost, "organizations", "catalyst", "priority/explain", map[string]any{"job_kind": "deploy"})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGroupHandler_SetPriorityShift(t *testing.T) {
	fake := newFakePriorityStore()
	fake.groups = []models.Group{{GroupID: "group-1", OrgID: "org-1", Name: "release"}}
	h := NewGroupHandler(fake)
	w := groupRequest(t, h, priorityAdmin, http.MethodPut, "catalyst", "release/priority-shift", map[string]int{"shift": 20})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, 20, fake.shifts["group-1"])
	assert.Contains(t, w.Body.String(), `"priority_shift":20`)
	assert.Equal(t, http.StatusBadRequest, groupRequest(t, h, priorityAdmin, http.MethodPut, "catalyst", "release/priority-shift", map[string]int{"shift": 51}).Code)
	assert.Equal(t, http.StatusBadRequest, groupRequest(t, h, priorityAdmin, http.MethodPut, "catalyst", "release/priority-shift", map[string]string{}).Code)
	assert.Equal(t, http.StatusNotFound, groupRequest(t, h, priorityAdmin, http.MethodPut, "catalyst", "missing/priority-shift", map[string]int{"shift": 1}).Code)

	fake.groups[0].PriorityShift = 20
	w = groupRequest(t, h, priorityAdmin, http.MethodGet, "catalyst", "", nil)
	assert.Contains(t, w.Body.String(), `"priority_shift":20`)
}
