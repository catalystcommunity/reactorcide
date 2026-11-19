package test

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/diagnosis"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDiagnosisUIOperations drives put-diagnosis and list-diagnoses with the
// admin API token (no logins), and the failure class filter of
// create-event-subscription. These calls commit, so the test removes what
// it made.
func TestDiagnosisUIOperations(t *testing.T) {
	initTestDB()
	require.NoError(t, initErr)
	diagnosis.SetRateLimiter(nil)
	adminValue := uniqueName("diag-admin-token")
	du := &DataUtils{db: testDB}
	adminToken, err := du.CreateAPIToken(DataSetup{"Name": "diag-ui-admin", "TokenHash": checkauth.HashAPIToken(adminValue),
		"IsActive": true, "SubjectType": "instance_token", "AllOrganizations": true, "AllCapabilities": true})
	require.NoError(t, err)
	job, err := du.CreateJob(DataSetup{"Name": "diag-ui-build", "SourceType": "git", "JobCommand": "false", "Status": "failed"})
	require.NoError(t, err)
	var subscriptionID string
	t.Cleanup(func() {
		testDB.Exec(`DELETE FROM event_subscriptions WHERE subscription_id::text = ?`, subscriptionID)
		testDB.Exec(`DELETE FROM diagnosis_reports WHERE job_id = ?`, job.JobID)
		testDB.Exec(`DELETE FROM jobs WHERE job_id = ?`, job.JobID)
		testDB.Exec(`DELETE FROM users WHERE user_id = ?`, job.UserID)
		testDB.Exec(`DELETE FROM api_tokens WHERE token_id = ?`, adminToken.TokenID)
		testDB.Exec(`DELETE FROM principals WHERE principal_id = ?`, adminToken.TokenID)
		testDB.Exec(`DELETE FROM users WHERE user_id = ?`, adminToken.UserID)
	})
	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	mux := handlers.GetAppMux()

	jobID := job.JobID
	request := csilapi.PutDiagnosisRequest{JobId: &jobID, Title: "Out of memory",
		Body: ptrText("cc @team <b>see</b>"), Confidence: ptrText("medium"),
		Links:           []csilapi.DiagnosisLinkEntry{{Name: "log", Url: "https://example.com/log"}},
		SuggestedShifts: &csilapi.RunShifts{Memory: ptrText("8Gi")}}
	_, svcErr := csilCall(t, mux, "ReactorcideUi", "put-diagnosis", csilapi.EncodePutDiagnosisRequest,
		csilapi.DecodePutDiagnosisResponse, request, "")
	require.NotNil(t, svcErr)
	assert.Equal(t, "unauthorized", svcErr.Code, "an anonymous caller never posts")

	put, svcErr := csilCall(t, mux, "ReactorcideUi", "put-diagnosis", csilapi.EncodePutDiagnosisRequest,
		csilapi.DecodePutDiagnosisResponse, request, adminValue)
	require.Nil(t, svcErr, "%+v", svcErr)
	assert.True(t, put.Created)
	assert.Equal(t, "memory=8Gi", put.Diagnosis.SuggestedShiftsText)
	assert.Equal(t, "cc @team <b>see</b>", put.Diagnosis.Body, "the stored body is text; the UI shows it as text")
	assert.Equal(t, "service", put.Diagnosis.Author.Kind)

	request.Title = "Out of memory (replaced)"
	again, svcErr := csilCall(t, mux, "ReactorcideUi", "put-diagnosis", csilapi.EncodePutDiagnosisRequest,
		csilapi.DecodePutDiagnosisResponse, request, adminValue)
	require.Nil(t, svcErr, "%+v", svcErr)
	assert.False(t, again.Created)
	assert.Equal(t, int64(2), again.Diagnosis.Revision)

	bad := request
	bad.Confidence = ptrText("certain")
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "put-diagnosis", csilapi.EncodePutDiagnosisRequest,
		csilapi.DecodePutDiagnosisResponse, bad, adminValue)
	require.NotNil(t, svcErr)
	assert.Equal(t, "invalid_argument", svcErr.Code)

	listed, svcErr := csilCall(t, mux, "ReactorcideUi", "list-diagnoses", csilapi.EncodeListDiagnosesRequest,
		csilapi.DecodeListDiagnosesResponse, csilapi.ListDiagnosesRequest{JobId: &jobID}, adminValue)
	require.Nil(t, svcErr, "%+v", svcErr)
	require.Len(t, listed.Diagnoses, 1)
	assert.Equal(t, "Out of memory (replaced)", listed.Diagnoses[0].Title)

	created, svcErr := csilCall(t, mux, "ReactorcideUi", "create-event-subscription", csilapi.EncodeCreateEventSubscriptionRequest,
		csilapi.DecodeCreateEventSubscriptionResponse, csilapi.CreateEventSubscriptionRequest{OrgId: job.OrgID,
			Name: uniqueName("diag"), Scope: "org", WebhookUrl: "https://hooks.example.com/diag",
			EventTypes: []string{"node.post_complete"}, FailureClasses: []string{"timeout", "job", "oom"}}, adminValue)
	require.Nil(t, svcErr, "%+v", svcErr)
	subscriptionID = created.Subscription.SubscriptionId
	assert.Equal(t, []string{"job", "oom", "timeout"}, created.Subscription.FailureClasses)
}

func ptrText(s string) *string { return &s }
