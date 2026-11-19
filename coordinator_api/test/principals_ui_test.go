package test

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrincipalsUIOperations drives the CSIL principal operations through the
// router-mounted UI service, as the Principals page does.
func TestPrincipalsUIOperations(t *testing.T) {
	// The no-login path: the admin API token authenticates the CSIL call.
	// These calls commit, so the test removes what it made.
	initTestDB()
	require.NoError(t, initErr)
	adminValue := uniqueName("principals-admin-token")
	du := &DataUtils{db: testDB}
	adminToken, err := du.CreateAPIToken(DataSetup{"Name": "principals-ui-admin", "TokenHash": checkauth.HashAPIToken(adminValue),
		"IsActive": true, "SubjectType": "instance_token", "AllOrganizations": true, "AllCapabilities": true})
	require.NoError(t, err)
	session := adminValue
	var principalID string
	t.Cleanup(func() {
		testDB.Exec(`DELETE FROM api_tokens WHERE token_id = ? OR principal_id = ? OR principal_id = ?`, adminToken.TokenID, principalID, adminToken.TokenID)
		testDB.Exec(`DELETE FROM principals WHERE principal_id IN (?, ?)`, principalID, adminToken.TokenID)
		testDB.Exec(`DELETE FROM users WHERE user_id = ?`, adminToken.UserID)
	})
	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	mux := handlers.GetAppMux()
	org, err := store.AppStore.(interface {
		GetDefaultOrganization(context.Context) (*models.Organization, error)
	}).GetDefaultOrganization(context.Background())
	require.NoError(t, err)

	// Anonymous callers get nothing.
	_, svcErr := csilCall(t, mux, "ReactorcideUi", "list-principals", csilapi.EncodeListPrincipalsRequest,
		csilapi.DecodeListPrincipalsResponse, csilapi.ListPrincipalsRequest{OrgId: org.OrgID}, "")
	require.NotNil(t, svcErr)
	assert.Equal(t, "unauthorized", svcErr.Code)

	name := uniqueName("ui-bot")
	created, svcErr := csilCall(t, mux, "ReactorcideUi", "create-service-principal", csilapi.EncodeCreateServicePrincipalRequest,
		csilapi.DecodeCreateServicePrincipalResponse, csilapi.CreateServicePrincipalRequest{OrgId: org.OrgID, Name: name}, session)
	require.Nil(t, svcErr)
	principalID = created.Principal.Principal.PrincipalId
	assert.Equal(t, models.PrincipalKindService, created.Principal.Principal.Kind)
	assert.Contains(t, created.Principal.Subjects, "service:"+name)

	listed, svcErr := csilCall(t, mux, "ReactorcideUi", "list-principals", csilapi.EncodeListPrincipalsRequest,
		csilapi.DecodeListPrincipalsResponse, csilapi.ListPrincipalsRequest{OrgId: org.OrgID}, session)
	require.Nil(t, svcErr)
	found := false
	for _, p := range listed.Principals {
		found = found || p.PrincipalId == principalID
	}
	assert.True(t, found)

	linked, svcErr := csilCall(t, mux, "ReactorcideUi", "link-principal-identity", csilapi.EncodePrincipalIdentityRequest,
		csilapi.DecodePrincipalIdentityResponse, csilapi.PrincipalIdentityRequest{OrgId: org.OrgID, PrincipalId: principalID,
			IdentityType: "vcsstar", IdentityValue: name + "@vcsstar.example"}, session)
	require.Nil(t, svcErr)
	assert.Contains(t, linked.Principal.Subjects, "identity:vcsstar:"+name+"@vcsstar.example")

	resolved, svcErr := csilCall(t, mux, "ReactorcideUi", "resolve-principal", csilapi.EncodeResolvePrincipalRequest,
		csilapi.DecodeResolvePrincipalResponse, csilapi.ResolvePrincipalRequest{OrgId: org.OrgID, IdentityType: "vcsstar",
			IdentityValue: name + "@vcsstar.example"}, session)
	require.Nil(t, svcErr)
	assert.Equal(t, principalID, resolved.Principal.Principal.PrincipalId)

	issued, svcErr := csilCall(t, mux, "ReactorcideUi", "issue-principal-token", csilapi.EncodeIssuePrincipalTokenRequest,
		csilapi.DecodeIssuePrincipalTokenResponse, csilapi.IssuePrincipalTokenRequest{OrgId: org.OrgID, PrincipalId: principalID,
			Name: "ui", Capabilities: []string{"jobs:read"}}, session)
	require.Nil(t, svcErr)
	require.NotEmpty(t, issued.Token)

	detail, svcErr := csilCall(t, mux, "ReactorcideUi", "get-principal", csilapi.EncodeGetPrincipalRequest,
		csilapi.DecodeGetPrincipalResponse, csilapi.GetPrincipalRequest{OrgId: org.OrgID, PrincipalId: principalID}, session)
	require.Nil(t, svcErr)
	require.Len(t, detail.Principal.Tokens, 1)
	assert.True(t, detail.Principal.Tokens[0].IsActive)

	// The token of a principal with no roles cannot list the principals.
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "list-principals", csilapi.EncodeListPrincipalsRequest,
		csilapi.DecodeListPrincipalsResponse, csilapi.ListPrincipalsRequest{OrgId: org.OrgID}, issued.Token)
	require.NotNil(t, svcErr)

	// No-login instance (owner decision B): a token issued with no
	// capability list gets all capabilities and all organizations, and roles
	// do not narrow it. It lists the principals with no role.
	origMode := config.UIAuthMode
	config.UIAuthMode = config.UIAuthModeNone
	wide, svcErr := csilCall(t, mux, "ReactorcideUi", "issue-principal-token", csilapi.EncodeIssuePrincipalTokenRequest,
		csilapi.DecodeIssuePrincipalTokenResponse, csilapi.IssuePrincipalTokenRequest{OrgId: org.OrgID, PrincipalId: principalID,
			Name: "ui-wide"}, session)
	require.Nil(t, svcErr)
	assert.Equal(t, []string{tokencaps.All}, wide.Info.Capabilities)
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "list-principals", csilapi.EncodeListPrincipalsRequest,
		csilapi.DecodeListPrincipalsResponse, csilapi.ListPrincipalsRequest{OrgId: org.OrgID}, wide.Token)
	require.Nil(t, svcErr)
	// With logins enabled, the same request gives a role-narrowed token.
	config.UIAuthMode = config.UIAuthModeLocalRP
	narrowDefault, svcErr := csilCall(t, mux, "ReactorcideUi", "issue-principal-token", csilapi.EncodeIssuePrincipalTokenRequest,
		csilapi.DecodeIssuePrincipalTokenResponse, csilapi.IssuePrincipalTokenRequest{OrgId: org.OrgID, PrincipalId: principalID,
			Name: "ui-login"}, session)
	config.UIAuthMode = origMode
	require.Nil(t, svcErr)
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "list-principals", csilapi.EncodeListPrincipalsRequest,
		csilapi.DecodeListPrincipalsResponse, csilapi.ListPrincipalsRequest{OrgId: org.OrgID}, narrowDefault.Token)
	require.NotNil(t, svcErr)

	_, svcErr = csilCall(t, mux, "ReactorcideUi", "revoke-principal-token", csilapi.EncodeRevokePrincipalTokenRequest,
		csilapi.DecodeRevokePrincipalTokenResponse, csilapi.RevokePrincipalTokenRequest{OrgId: org.OrgID, PrincipalId: principalID,
			TokenId: issued.Info.TokenId}, session)
	require.Nil(t, svcErr)
	disabled, svcErr := csilCall(t, mux, "ReactorcideUi", "set-principal-status", csilapi.EncodeSetPrincipalStatusRequest,
		csilapi.DecodeSetPrincipalStatusResponse, csilapi.SetPrincipalStatusRequest{OrgId: org.OrgID, PrincipalId: principalID,
			Status: models.PrincipalStatusDisabled}, session)
	require.Nil(t, svcErr)
	assert.Equal(t, models.PrincipalStatusDisabled, disabled.Principal.Principal.Status)

	// The system principal belongs to no organization.
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "get-principal", csilapi.EncodeGetPrincipalRequest,
		csilapi.DecodeGetPrincipalResponse, csilapi.GetPrincipalRequest{OrgId: org.OrgID, PrincipalId: models.SystemPrincipalID}, session)
	require.NotNil(t, svcErr)
	assert.Equal(t, "not_found", svcErr.Code)
}

// TestPrincipalsUILinkScope: an org admin links identities only to service
// principals of the org. The subjects of a human apply in every org, so a
// human needs a global admin (here the admin token).
func TestPrincipalsUILinkScope(t *testing.T) {
	initTestDB()
	require.NoError(t, initErr)
	ctx := context.Background()
	du := &DataUtils{db: testDB}
	adminValue := uniqueName("link-scope-admin-token")
	adminToken, err := du.CreateAPIToken(DataSetup{"Name": "link-scope-admin", "TokenHash": checkauth.HashAPIToken(adminValue),
		"IsActive": true, "SubjectType": "instance_token", "AllOrganizations": true, "AllCapabilities": true})
	require.NoError(t, err)
	orgAdmin, err := du.CreateUser(DataSetup{})
	require.NoError(t, err)
	victim, err := du.CreateUser(DataSetup{})
	require.NoError(t, err)
	org, err := store.AppStore.(interface {
		GetDefaultOrganization(context.Context) (*models.Organization, error)
	}).GetDefaultOrganization(ctx)
	require.NoError(t, err)
	roleStore := store.AppStore.(interface {
		CreateRoleAssignment(context.Context, *models.RoleAssignment) error
	})
	orgID := org.OrgID
	require.NoError(t, roleStore.CreateRoleAssignment(ctx, &models.RoleAssignment{PrincipalType: models.PrincipalTypeUser,
		PrincipalID: orgAdmin.UserID, ScopeType: models.ScopeTypeOrg, ScopeID: &orgID, Role: models.RoleAdmin}))
	require.NoError(t, roleStore.CreateRoleAssignment(ctx, &models.RoleAssignment{PrincipalType: models.PrincipalTypeUser,
		PrincipalID: victim.UserID, ScopeType: models.ScopeTypeOrg, ScopeID: &orgID, Role: models.RoleMember}))
	orgAdminValue := uniqueName("link-scope-org-admin")
	orgAdminToken, err := du.CreateAPIToken(DataSetup{"Name": "link-scope-org-admin", "TokenHash": checkauth.HashAPIToken(orgAdminValue),
		"IsActive": true, "SubjectType": "user_token", "UserID": orgAdmin.UserID, "AllOrganizations": true, "AllCapabilities": true})
	require.NoError(t, err)
	login := uniqueName("victim-login")
	var serviceID string
	t.Cleanup(func() {
		testDB.Exec(`DELETE FROM principal_identities WHERE identity_value LIKE ?`, "%"+login+"%")
		testDB.Exec(`DELETE FROM role_assignments WHERE principal_id IN (?, ?)`, orgAdmin.UserID, victim.UserID)
		testDB.Exec(`DELETE FROM api_tokens WHERE token_id IN (?, ?)`, adminToken.TokenID, orgAdminToken.TokenID)
		testDB.Exec(`DELETE FROM principals WHERE principal_id IN (?, ?)`, serviceID, adminToken.TokenID)
		testDB.Exec(`DELETE FROM users WHERE user_id IN (?, ?, ?)`, orgAdmin.UserID, victim.UserID, adminToken.UserID)
	})
	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	mux := handlers.GetAppMux()

	created, svcErr := csilCall(t, mux, "ReactorcideUi", "create-service-principal", csilapi.EncodeCreateServicePrincipalRequest,
		csilapi.DecodeCreateServicePrincipalResponse, csilapi.CreateServicePrincipalRequest{OrgId: org.OrgID, Name: uniqueName("scope-bot")}, orgAdminValue)
	require.Nil(t, svcErr)
	serviceID = created.Principal.Principal.PrincipalId

	link := func(principalID, value, session string) *ServiceErrorView {
		_, svcErr := csilCall(t, mux, "ReactorcideUi", "link-principal-identity", csilapi.EncodePrincipalIdentityRequest,
			csilapi.DecodePrincipalIdentityResponse, csilapi.PrincipalIdentityRequest{OrgId: org.OrgID, PrincipalId: principalID,
				IdentityType: "vcs", IdentityValue: value}, session)
		if svcErr == nil {
			return nil
		}
		return &ServiceErrorView{Code: svcErr.Code}
	}
	// The org admin links to a service principal of the org.
	assert.Nil(t, link(serviceID, "github/"+login+"-bot", orgAdminValue))
	// The org admin cannot link to a human.
	refused := link(victim.UserID, "github/"+login, orgAdminValue)
	require.NotNil(t, refused, "an org admin must not link an identity to a human")
	assert.Equal(t, "forbidden", refused.Code)
	// The admin token (no-login global admin) can.
	assert.Nil(t, link(victim.UserID, "github/"+login, adminValue))
}

// ServiceErrorView is the code of a CSIL service error.
type ServiceErrorView struct{ Code string }
