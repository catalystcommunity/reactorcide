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

// TestRequiredSetUIOperations drives the CSIL required set operations through
// the router-mounted UI service with the admin token (no logins), as the
// project page does. These calls commit, so the test removes what it made.
func TestRequiredSetUIOperations(t *testing.T) {
	initTestDB()
	require.NoError(t, initErr)
	adminValue := uniqueName("required-set-admin-token")
	du := &DataUtils{db: testDB}
	adminToken, err := du.CreateAPIToken(DataSetup{"Name": "required-set-ui-admin", "TokenHash": checkauth.HashAPIToken(adminValue),
		"IsActive": true, "SubjectType": "instance_token", "AllOrganizations": true, "AllCapabilities": true})
	require.NoError(t, err)
	org, err := store.AppStore.(interface {
		GetDefaultOrganization(context.Context) (*models.Organization, error)
	}).GetDefaultOrganization(context.Background())
	require.NoError(t, err)
	project := &models.Project{Name: uniqueName("rs-ui"), RepoURL: "github.com/rs/" + uniqueName("ui"), OrgID: org.OrgID}
	require.NoError(t, store.AppStore.CreateProject(context.Background(), project))
	projectID := project.ProjectID
	require.NoError(t, testDB.Create(&models.WorkflowInstance{OrgID: org.OrgID, ProjectID: &projectID, Name: "build",
		WorkflowSecurityID: "build", Status: "success"}).Error)
	t.Cleanup(func() {
		testDB.Exec(`DELETE FROM workflow_instances WHERE project_id = ?`, projectID)
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

	_, svcErr := csilCall(t, mux, "ReactorcideUi", "get-required-set", csilapi.EncodeGetRequiredSetRequest,
		csilapi.DecodeGetRequiredSetResponse, csilapi.GetRequiredSetRequest{ProjectId: projectID}, "")
	require.NotNil(t, svcErr)
	assert.Equal(t, "unauthorized", svcErr.Code)

	got, svcErr := csilCall(t, mux, "ReactorcideUi", "get-required-set", csilapi.EncodeGetRequiredSetRequest,
		csilapi.DecodeGetRequiredSetResponse, csilapi.GetRequiredSetRequest{ProjectId: projectID}, session)
	require.Nil(t, svcErr)
	assert.False(t, got.Enabled)
	assert.True(t, got.CanManage)
	require.Len(t, got.Workflows, 1)
	assert.Equal(t, "required", got.Workflows[0].State)

	enabled, svcErr := csilCall(t, mux, "ReactorcideUi", "set-required-set-enabled", csilapi.EncodeSetRequiredSetEnabledRequest,
		csilapi.DecodeSetRequiredSetEnabledResponse, csilapi.SetRequiredSetEnabledRequest{ProjectId: projectID, Enabled: true}, session)
	require.Nil(t, svcErr)
	assert.Equal(t, []string{"build"}, enabled.Seeded)

	approve := true
	proposed, svcErr := csilCall(t, mux, "ReactorcideUi", "propose-required-set-change", csilapi.EncodeProposeRequiredSetChangeRequest,
		csilapi.DecodeProposeRequiredSetChangeResponse, csilapi.ProposeRequiredSetChangeRequest{ProjectId: projectID, Action: "promote",
			WorkflowSecurityId: "lint", Approve: &approve}, session)
	require.Nil(t, svcErr)
	assert.Equal(t, "approved", proposed.Proposal.State)

	quarantined, svcErr := csilCall(t, mux, "ReactorcideUi", "quarantine-required-workflow", csilapi.EncodeQuarantineRequiredWorkflowRequest,
		csilapi.DecodeQuarantineRequiredWorkflowResponse, csilapi.QuarantineRequiredWorkflowRequest{ProjectId: projectID,
			WorkflowSecurityId: "build", Until: "3d", Reason: "flaky"}, session)
	require.Nil(t, svcErr)
	require.NotNil(t, quarantined.Workflow.QuarantinedUntil)

	_, svcErr = csilCall(t, mux, "ReactorcideUi", "quarantine-required-workflow", csilapi.EncodeQuarantineRequiredWorkflowRequest,
		csilapi.DecodeQuarantineRequiredWorkflowResponse, csilapi.QuarantineRequiredWorkflowRequest{ProjectId: projectID,
			WorkflowSecurityId: "build", Until: "60d", Reason: "too long"}, session)
	require.NotNil(t, svcErr)
	assert.Equal(t, "invalid_argument", svcErr.Code)

	got, svcErr = csilCall(t, mux, "ReactorcideUi", "get-required-set", csilapi.EncodeGetRequiredSetRequest,
		csilapi.DecodeGetRequiredSetResponse, csilapi.GetRequiredSetRequest{ProjectId: projectID}, session)
	require.Nil(t, svcErr)
	states := map[string]string{}
	for _, row := range got.Workflows {
		states[row.WorkflowSecurityId] = row.State
	}
	assert.Equal(t, map[string]string{"build": "quarantined", "lint": "required"}, states)

	_, svcErr = csilCall(t, mux, "ReactorcideUi", "unquarantine-required-workflow", csilapi.EncodeUnquarantineRequiredWorkflowRequest,
		csilapi.DecodeUnquarantineRequiredWorkflowResponse, csilapi.UnquarantineRequiredWorkflowRequest{ProjectId: projectID, WorkflowSecurityId: "build"}, session)
	require.Nil(t, svcErr)

	demote, svcErr := csilCall(t, mux, "ReactorcideUi", "propose-required-set-change", csilapi.EncodeProposeRequiredSetChangeRequest,
		csilapi.DecodeProposeRequiredSetChangeResponse, csilapi.ProposeRequiredSetChangeRequest{ProjectId: projectID, Action: "demote",
			WorkflowSecurityId: "lint"}, session)
	require.Nil(t, svcErr)
	decided, svcErr := csilCall(t, mux, "ReactorcideUi", "decide-required-set-proposal", csilapi.EncodeDecideRequiredSetProposalRequest,
		csilapi.DecodeDecideRequiredSetProposalResponse, csilapi.DecideRequiredSetProposalRequest{ProjectId: projectID,
			ProposalId: demote.Proposal.ProposalId, Decision: "reject"}, session)
	require.Nil(t, svcErr)
	assert.Equal(t, "rejected", decided.Proposal.State)

	withApprovers, svcErr := csilCall(t, mux, "ReactorcideUi", "set-required-set-approvers", csilapi.EncodeSetRequiredSetApproversRequest,
		csilapi.DecodeGetRequiredSetResponse, csilapi.SetRequiredSetApproversRequest{ProjectId: projectID, Approvers: []string{"reactorcide_group:ci"},
			InheritRules: true}, session)
	require.Nil(t, svcErr)
	assert.Equal(t, []string{"reactorcide_group:ci"}, withApprovers.Approvers)
	assert.Equal(t, "setting", withApprovers.ApproversSource)
}
