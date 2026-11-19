package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeGroupStore struct {
	*MockStore
	groups  []models.Group
	members map[string][]models.User
	users   map[string]models.User
}

func (s *fakeGroupStore) GetOrganizationByName(_ context.Context, name string) (*models.Organization, error) {
	if name != "catalyst" {
		return nil, store.ErrNotFound
	}
	return &models.Organization{OrgID: "org-1"}, nil
}

func (s *fakeGroupStore) GetUserByUsername(_ context.Context, username string) (*models.User, error) {
	user, ok := s.users[username]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &user, nil
}

func (s *fakeGroupStore) CreateGroup(_ context.Context, group *models.Group) error {
	group.GroupID = "group-1"
	s.groups = append(s.groups, *group)
	return nil
}

func (s *fakeGroupStore) ListGroupsByOrg(_ context.Context, orgID string) ([]models.Group, error) {
	out := []models.Group{}
	for _, group := range s.groups {
		if group.OrgID == orgID {
			out = append(out, group)
		}
	}
	return out, nil
}

func (s *fakeGroupStore) AddGroupMember(_ context.Context, groupID, userID string) error {
	for _, user := range s.users {
		if user.UserID == userID {
			s.members[groupID] = append(s.members[groupID], user)
		}
	}
	return nil
}

func (s *fakeGroupStore) RemoveGroupMember(_ context.Context, groupID, userID string) error {
	kept := []models.User{}
	for _, user := range s.members[groupID] {
		if user.UserID != userID {
			kept = append(kept, user)
		}
	}
	if len(kept) == len(s.members[groupID]) {
		return store.ErrNotFound
	}
	s.members[groupID] = kept
	return nil
}

func (s *fakeGroupStore) ListGroupMembers(_ context.Context, groupID string) ([]models.User, error) {
	return s.members[groupID], nil
}

func groupRequest(t *testing.T, h *GroupHandler, principal *checkauth.Principal, method, org, rest string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/api/v1/organizations/"+org+"/groups/"+rest, reader)
	if principal != nil {
		req = req.WithContext(checkauth.SetPrincipalContext(req.Context(), principal))
	}
	w := httptest.NewRecorder()
	h.ServeGroups(w, req, org, rest)
	return w
}

func TestGroupHandler_CreateAddListRemove(t *testing.T) {
	fake := &fakeGroupStore{MockStore: &MockStore{}, members: map[string][]models.User{},
		users: map[string]models.User{"todpunk": {UserID: "user-1", Username: "todpunk"}}}
	h := NewGroupHandler(fake)
	admin := &checkauth.Principal{CredentialType: "instance_token", OrganizationIDs: []string{"org-1"}, AllCapabilities: true}

	w := groupRequest(t, h, admin, http.MethodPost, "catalyst", "", map[string]string{"name": "linkkeys-trust-maintainers"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	w = groupRequest(t, h, admin, http.MethodPost, "catalyst", "", map[string]string{"name": "linkkeys-trust-maintainers"})
	assert.Equal(t, http.StatusConflict, w.Code, "a second group with the same name")
	w = groupRequest(t, h, admin, http.MethodPost, "catalyst", "", map[string]string{"name": "Bad Name"})
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = groupRequest(t, h, admin, http.MethodPost, "catalyst", "linkkeys-trust-maintainers/members", map[string]string{"username": "todpunk"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	w = groupRequest(t, h, admin, http.MethodGet, "catalyst", "linkkeys-trust-maintainers/members", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"username":"todpunk"`)

	w = groupRequest(t, h, admin, http.MethodDelete, "catalyst", "linkkeys-trust-maintainers/members/todpunk", nil)
	assert.Equal(t, http.StatusNoContent, w.Code)
	w = groupRequest(t, h, admin, http.MethodGet, "catalyst", "missing/members", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGroupHandler_RequiresOrganizationAuthority(t *testing.T) {
	fake := &fakeGroupStore{MockStore: &MockStore{}, members: map[string][]models.User{}, users: map[string]models.User{}}
	h := NewGroupHandler(fake)
	otherOrg := &checkauth.Principal{CredentialType: "instance_token", OrganizationIDs: []string{"org-2"}, AllCapabilities: true}
	assert.Equal(t, http.StatusForbidden, groupRequest(t, h, otherOrg, http.MethodGet, "catalyst", "", nil).Code)
	assert.Equal(t, http.StatusForbidden, groupRequest(t, h, nil, http.MethodGet, "catalyst", "", nil).Code)
}

// TestGroupHandler_ReadNeedsOrganizationsRead: a GET accepts
// organizations:read; a change needs organizations:manage.
func TestGroupHandler_ReadNeedsOrganizationsRead(t *testing.T) {
	fake := &fakeGroupStore{MockStore: &MockStore{}, members: map[string][]models.User{}, users: map[string]models.User{}}
	h := NewGroupHandler(fake)
	readSet, _ := tokencaps.New(tokencaps.OrganizationsRead)
	reader := &checkauth.Principal{CredentialType: "instance_token", OrganizationIDs: []string{"org-1"}, Capabilities: readSet}
	assert.Equal(t, http.StatusOK, groupRequest(t, h, reader, http.MethodGet, "catalyst", "", nil).Code)
	assert.Equal(t, http.StatusForbidden, groupRequest(t, h, reader, http.MethodPost, "catalyst", "", map[string]string{"name": "new"}).Code)
	jobsSet, _ := tokencaps.New(tokencaps.JobsRead)
	none := &checkauth.Principal{CredentialType: "instance_token", OrganizationIDs: []string{"org-1"}, Capabilities: jobsSet}
	assert.Equal(t, http.StatusForbidden, groupRequest(t, h, none, http.MethodGet, "catalyst", "", nil).Code)
}
