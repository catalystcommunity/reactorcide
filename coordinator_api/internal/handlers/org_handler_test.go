package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

type organizationHandlerStore struct {
	*ProjectMockStore
	organizations map[string]*models.Organization
	deleted       [][2]string
}

func newOrganizationHandlerStore() *organizationHandlerStore {
	return &organizationHandlerStore{
		ProjectMockStore: &ProjectMockStore{},
		organizations: map[string]*models.Organization{
			"default": {OrgID: "00000000-0000-0000-0000-000000000001", Name: "default"},
			"linked":  {OrgID: "00000000-0000-0000-0000-000000000002", Name: "linked"},
		},
	}
}

func (s *organizationHandlerStore) CreateOrganization(context.Context, *models.Organization) error {
	return nil
}

func (s *organizationHandlerStore) GetOrganizationByName(_ context.Context, name string) (*models.Organization, error) {
	organization, ok := s.organizations[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *organization
	return &copy, nil
}

func (s *organizationHandlerStore) ListOrganizations(context.Context, int, int) ([]models.Organization, error) {
	return nil, nil
}

func (s *organizationHandlerStore) UpdateOrganization(context.Context, *models.Organization) error {
	return nil
}

func (s *organizationHandlerStore) DeleteOrganization(_ context.Context, orgID, replacementOrgID string) error {
	s.deleted = append(s.deleted, [2]string{orgID, replacementOrgID})
	return nil
}

func (s *organizationHandlerStore) SetDefaultOrganization(context.Context, string) error {
	return nil
}

func (s *organizationHandlerStore) GetDefaultOrganization(context.Context) (*models.Organization, error) {
	return s.GetOrganizationByName(context.Background(), "default")
}

func TestDeleteOrganizationAllowsAdminServiceToken(t *testing.T) {
	organizationStore := newOrganizationHandlerStore()
	handler := NewOrganizationHandler(organizationStore)
	principal := &checkauth.Principal{
		CredentialType:  "service_token",
		OrganizationIDs: []string{organizationStore.organizations["default"].OrgID, organizationStore.organizations["linked"].OrgID},
		Capabilities:    tokencaps.Set{tokencaps.OrganizationsManage: {}},
	}

	request := organizationDeleteRequestForTest(t, principal)
	response := httptest.NewRecorder()
	handler.Delete(response, request)

	require.Equal(t, http.StatusNoContent, response.Code)
	require.Equal(t, [][2]string{{organizationStore.organizations["default"].OrgID, organizationStore.organizations["linked"].OrgID}}, organizationStore.deleted)
}

func TestDeleteOrganizationAllowsUserWhoAdministersBothOrganizations(t *testing.T) {
	organizationStore := newOrganizationHandlerStore()
	userID := "00000000-0000-0000-0000-000000000003"
	for _, organization := range organizationStore.organizations {
		orgID := organization.OrgID
		organizationStore.RoleAssignments = append(organizationStore.RoleAssignments, models.RoleAssignment{
			PrincipalType: models.PrincipalTypeUser,
			PrincipalID:   userID,
			ScopeType:     models.ScopeTypeOrg,
			ScopeID:       &orgID,
			Role:          models.RoleAdmin,
		})
	}
	principal := &checkauth.Principal{
		CredentialType:  "user_token",
		UserID:          userID,
		OrganizationIDs: []string{organizationStore.organizations["default"].OrgID, organizationStore.organizations["linked"].OrgID},
		Capabilities:    tokencaps.Set{tokencaps.OrganizationsManage: {}},
	}
	request := organizationDeleteRequestForTest(t, principal)
	request = request.WithContext(checkauth.SetUserContext(request.Context(), &models.User{
		UserID: userID,
		Status: "active",
	}))
	response := httptest.NewRecorder()

	NewOrganizationHandler(organizationStore).Delete(response, request)

	require.Equal(t, http.StatusNoContent, response.Code)
	require.Len(t, organizationStore.deleted, 1)
}

func TestDeleteOrganizationRequiresReplacementAuthority(t *testing.T) {
	organizationStore := newOrganizationHandlerStore()
	principal := &checkauth.Principal{
		CredentialType:  "service_token",
		OrganizationIDs: []string{organizationStore.organizations["default"].OrgID},
		Capabilities:    tokencaps.Set{tokencaps.OrganizationsManage: {}},
	}
	request := organizationDeleteRequestForTest(t, principal)
	response := httptest.NewRecorder()

	NewOrganizationHandler(organizationStore).Delete(response, request)

	require.Equal(t, http.StatusForbidden, response.Code)
	require.Empty(t, organizationStore.deleted)
}

func organizationDeleteRequestForTest(t *testing.T, principal *checkauth.Principal) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/organizations/default",
		bytes.NewBufferString(`{"replacement":"linked","confirm":true}`))
	ctx := setIDContext(request.Context(), "organization_name", "default")
	ctx = checkauth.SetPrincipalContext(ctx, principal)
	return request.WithContext(ctx)
}
