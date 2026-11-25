package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestSecretsOrgResolutionForInstanceToken checks that a token for all
// organizations with no owner organization uses the default organization
// when the request has no ?org=, and the named organization when it has one.
func TestSecretsOrgResolutionForInstanceToken(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		keyManager := createTestMasterKeyManager(t)
		handler := handlers.NewSecretsHandler(store.AppStore, keyManager)
		user := setupSecretsTestUser(t, tx, keyManager)

		orgs, ok := store.AppStore.(interface {
			GetOrganizationByID(context.Context, string) (*models.Organization, error)
			SetDefaultOrganization(context.Context, string) error
		})
		require.True(t, ok)
		org, err := orgs.GetOrganizationByID(ctx, user.UserID)
		require.NoError(t, err)

		body, _ := json.Marshal(handlers.SecretValueRequest{Value: "org-value"})
		setReq := httptest.NewRequest(http.MethodPut, "/api/v1/secrets/value?path=app&key=TOKEN", bytes.NewReader(body))
		setReq = setReq.WithContext(checkauth.SetUserContext(ctx, user))
		setW := httptest.NewRecorder()
		handler.SetSecret(setW, setReq)
		require.Equal(t, http.StatusOK, setW.Code, setW.Body.String())

		instanceCtx := checkauth.SetPrincipalContext(ctx, &checkauth.Principal{
			CredentialType: "instance_token", AllOrganizations: true, AllCapabilities: true,
		})
		get := func(query string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/secrets/value?"+query, nil).WithContext(instanceCtx)
			w := httptest.NewRecorder()
			handler.GetSecret(w, req)
			return w
		}
		valueOf := func(w *httptest.ResponseRecorder) string {
			var resp handlers.SecretValueResponse
			require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
			return resp.Value
		}

		require.NoError(t, orgs.SetDefaultOrganization(ctx, org.OrgID))
		w := get("path=app&key=TOKEN")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "org-value", valueOf(w))

		w = get(url.Values{"path": {"app"}, "key": {"TOKEN"}, "org": {org.Name}}.Encode())
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "org-value", valueOf(w))

		w = get("path=app&key=TOKEN&org=no-such-org-xyz")
		assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())

		require.NoError(t, tx.Where("key = ?", models.GlobalSettingDefaultOrgID).Delete(&models.GlobalSetting{}).Error)
		w = get("path=app&key=TOKEN")
		assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		var resp handlers.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "organization_required", resp.Error)
	})
}
