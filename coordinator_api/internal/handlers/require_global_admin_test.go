package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/stretchr/testify/require"
)

// The master key routes accept the admin token with no user (they needed a
// user with the admin role), and still refuse an organization token.
func TestRequireGlobalAdmin(t *testing.T) {
	allCaps, err := tokencaps.New(tokencaps.SecretsManage)
	require.NoError(t, err)
	cases := []struct {
		name      string
		principal *checkauth.Principal
		user      *models.User
		want      int
	}{
		{"admin token", &checkauth.Principal{CredentialType: "instance_token", AllOrganizations: true, AllCapabilities: true}, nil, http.StatusOK},
		{"organization token", &checkauth.Principal{CredentialType: "service_token", OwnerOrgID: "org-1", OrganizationIDs: []string{"org-1"}, Capabilities: allCaps}, nil, http.StatusForbidden},
		{"admin user token", &checkauth.Principal{CredentialType: "user_token", UserID: "u1", Capabilities: allCaps}, &models.User{UserID: "u1", Roles: []string{"admin"}}, http.StatusOK},
		{"admin user token without capability", &checkauth.Principal{CredentialType: "user_token", UserID: "u1"}, &models.User{UserID: "u1", Roles: []string{"admin"}}, http.StatusForbidden},
		{"plain user", nil, &models.User{UserID: "u2"}, http.StatusForbidden},
		{"nobody", nil, nil, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/secrets/master-keys", nil)
			ctx := req.Context()
			if tc.principal != nil {
				ctx = checkauth.SetPrincipalContext(ctx, tc.principal)
			}
			if tc.user != nil {
				ctx = checkauth.SetUserContext(ctx, tc.user)
			}
			w := httptest.NewRecorder()
			requireGlobalAdmin(&ProjectMockStore{}, tokencaps.SecretsManage)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(w, req.WithContext(ctx))
			require.Equal(t, tc.want, w.Code)
		})
	}
}
