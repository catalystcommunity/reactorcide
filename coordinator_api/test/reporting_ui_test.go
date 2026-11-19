package test

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReportingUIOperations drives the CSIL reporting operations through the
// router-mounted UI service with the admin token (no logins). These calls
// commit, so the test removes what it made.
func TestReportingUIOperations(t *testing.T) {
	initTestDB()
	require.NoError(t, initErr)
	adminValue := uniqueName("reporting-admin-token")
	du := &DataUtils{db: testDB}
	adminToken, err := du.CreateAPIToken(DataSetup{"Name": "reporting-ui-admin", "TokenHash": checkauth.HashAPIToken(adminValue),
		"IsActive": true, "SubjectType": "instance_token", "AllOrganizations": true, "AllCapabilities": true})
	require.NoError(t, err)
	org, err := store.AppStore.(interface {
		GetDefaultOrganization(context.Context) (*models.Organization, error)
	}).GetDefaultOrganization(context.Background())
	require.NoError(t, err)
	project := &models.Project{Name: uniqueName("rp-ui"), RepoURL: "github.com/rp/" + uniqueName("ui"), OrgID: org.OrgID}
	require.NoError(t, store.AppStore.CreateProject(context.Background(), project))
	projectID := project.ProjectID
	t.Cleanup(func() {
		testDB.Exec(`DELETE FROM path_acceptances WHERE project_id = ?`, projectID)
		testDB.Exec(`DELETE FROM project_reporting_settings WHERE project_id = ?`, projectID)
		testDB.Exec(`DELETE FROM audit_events WHERE details->>'project_id' = ?`, projectID)
		testDB.Exec(`DELETE FROM projects WHERE project_id = ?`, projectID)
		testDB.Exec(`DELETE FROM api_tokens WHERE token_id = ?`, adminToken.TokenID)
		testDB.Exec(`DELETE FROM principals WHERE principal_id = ?`, adminToken.TokenID)
		testDB.Exec(`DELETE FROM users WHERE user_id = ?`, adminToken.UserID)
	})
	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	mux := handlers.GetAppMux()
	session := adminValue

	_, svcErr := csilCall(t, mux, "ReactorcideUi", "get-reporting-settings", csilapi.EncodeGetReportingSettingsRequest,
		csilapi.DecodeGetReportingSettingsResponse, csilapi.GetReportingSettingsRequest{ProjectId: projectID}, "")
	require.NotNil(t, svcErr)
	assert.Equal(t, "unauthorized", svcErr.Code)

	got, svcErr := csilCall(t, mux, "ReactorcideUi", "get-reporting-settings", csilapi.EncodeGetReportingSettingsRequest,
		csilapi.DecodeGetReportingSettingsResponse, csilapi.GetReportingSettingsRequest{ProjectId: projectID}, session)
	require.Nil(t, svcErr)
	assert.Equal(t, "full", got.Effective.ReportMode)
	assert.True(t, got.CanManageOwnership)

	mode, acceptance := "statuses_only", "warn"
	got, svcErr = csilCall(t, mux, "ReactorcideUi", "update-reporting-settings", csilapi.EncodeUpdateReportingSettingsRequest,
		csilapi.DecodeGetReportingSettingsResponse, csilapi.UpdateReportingSettingsRequest{ProjectId: projectID, ReportMode: &mode,
			PathAcceptance: &acceptance, AgreementContexts: []string{"ci/build"}}, session)
	require.Nil(t, svcErr)
	assert.Equal(t, "statuses_only", got.Effective.ReportMode)
	assert.Equal(t, "project", got.Effective.ReportModeSource)
	assert.Equal(t, []string{"ci/build"}, got.ProjectAgreementContexts)

	report, svcErr := csilCall(t, mux, "ReactorcideUi", "get-agreement-report", csilapi.EncodeGetAgreementReportRequest,
		csilapi.DecodeGetAgreementReportResponse, csilapi.GetAgreementReportRequest{ProjectId: projectID}, session)
	require.Nil(t, svcErr)
	assert.Equal(t, []string{"ci/build"}, report.Contexts)
	assert.Nil(t, report.RateBasisPoints)

	created, svcErr := csilCall(t, mux, "ReactorcideUi", "request-path-acceptance", csilapi.EncodeRequestPathAcceptanceRequest,
		csilapi.DecodePathAcceptanceResponse, csilapi.RequestPathAcceptanceRequest{ProjectId: projectID, WorkflowSecurityId: "build",
			PathPattern: "/payments/", OwnerSubject: "reactorcide_group:payments"}, session)
	require.Nil(t, svcErr)
	assert.Equal(t, "requested", created.Acceptance.State)
	decided, svcErr := csilCall(t, mux, "ReactorcideUi", "decide-path-acceptance", csilapi.EncodeDecidePathAcceptanceRequest,
		csilapi.DecodePathAcceptanceResponse, csilapi.DecidePathAcceptanceRequest{ProjectId: projectID,
			AcceptanceId: created.Acceptance.AcceptanceId, Decision: "accept"}, session)
	require.Nil(t, svcErr)
	assert.Equal(t, "accepted", decided.Acceptance.State)
	list, svcErr := csilCall(t, mux, "ReactorcideUi", "list-path-acceptances", csilapi.EncodeListPathAcceptancesRequest,
		csilapi.DecodeListPathAcceptancesResponse, csilapi.ListPathAcceptancesRequest{ProjectId: projectID}, session)
	require.Nil(t, svcErr)
	assert.Equal(t, "warn", list.PathAcceptance)
	require.Len(t, list.Rows, 1)
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "decide-path-acceptance", csilapi.EncodeDecidePathAcceptanceRequest,
		csilapi.DecodePathAcceptanceResponse, csilapi.DecidePathAcceptanceRequest{ProjectId: projectID,
			AcceptanceId: created.Acceptance.AcceptanceId, Decision: "explode"}, session)
	require.NotNil(t, svcErr)
	assert.Equal(t, "invalid_argument", svcErr.Code)
}
