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

// TestEventSubscriptionUIOperations drives the CSIL subscription operations
// with the admin API token (no logins), as the Event subscriptions page and
// an automation would. These calls commit, so the test removes what it made.
func TestEventSubscriptionUIOperations(t *testing.T) {
	initTestDB()
	require.NoError(t, initErr)
	adminValue := uniqueName("subs-admin-token")
	du := &DataUtils{db: testDB}
	adminToken, err := du.CreateAPIToken(DataSetup{"Name": "subs-ui-admin", "TokenHash": checkauth.HashAPIToken(adminValue),
		"IsActive": true, "SubjectType": "instance_token", "AllOrganizations": true, "AllCapabilities": true})
	require.NoError(t, err)
	var subscriptionID string
	t.Cleanup(func() {
		testDB.Exec(`DELETE FROM event_subscriptions WHERE subscription_id::text = ?`, subscriptionID)
		testDB.Exec(`DELETE FROM api_tokens WHERE token_id = ?`, adminToken.TokenID)
		testDB.Exec(`DELETE FROM principals WHERE principal_id = ?`, adminToken.TokenID)
		testDB.Exec(`DELETE FROM users WHERE user_id = ?`, adminToken.UserID)
	})
	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	mux := handlers.GetAppMux()
	org, err := store.AppStore.(interface {
		GetDefaultOrganization(context.Context) (*models.Organization, error)
	}).GetDefaultOrganization(context.Background())
	require.NoError(t, err)

	_, svcErr := csilCall(t, mux, "ReactorcideUi", "list-event-subscriptions", csilapi.EncodeListEventSubscriptionsRequest,
		csilapi.DecodeListEventSubscriptionsResponse, csilapi.ListEventSubscriptionsRequest{OrgId: org.OrgID}, "")
	require.NotNil(t, svcErr)
	assert.Equal(t, "unauthorized", svcErr.Code)

	_, svcErr = csilCall(t, mux, "ReactorcideUi", "create-event-subscription", csilapi.EncodeCreateEventSubscriptionRequest,
		csilapi.DecodeCreateEventSubscriptionResponse, csilapi.CreateEventSubscriptionRequest{OrgId: org.OrgID,
			Name: uniqueName("lo"), Scope: "org", WebhookUrl: "https://127.0.0.1/hook"}, adminValue)
	require.NotNil(t, svcErr)
	assert.Equal(t, "invalid_argument", svcErr.Code, "a loopback destination is refused")

	name := uniqueName("subs")
	created, svcErr := csilCall(t, mux, "ReactorcideUi", "create-event-subscription", csilapi.EncodeCreateEventSubscriptionRequest,
		csilapi.DecodeCreateEventSubscriptionResponse, csilapi.CreateEventSubscriptionRequest{OrgId: org.OrgID,
			Name: name, Scope: "org", WebhookUrl: "https://hooks.example.com/rc", EventTypes: []string{"workflow.completed"},
			Results: []string{"failed"}}, adminValue)
	require.Nil(t, svcErr, "%+v", svcErr)
	subscriptionID = created.Subscription.SubscriptionId
	assert.NotEmpty(t, created.SigningSecret)
	assert.Equal(t, adminToken.TokenID, created.Subscription.OwnerPrincipalId, "the admin token's service principal owns it")

	updated, svcErr := csilCall(t, mux, "ReactorcideUi", "update-event-subscription", csilapi.EncodeUpdateEventSubscriptionRequest,
		csilapi.DecodeUpdateEventSubscriptionResponse, csilapi.UpdateEventSubscriptionRequest{SubscriptionId: subscriptionID,
			ClearFilters: []string{"results"}, RotateSecret: boolPtrTest(true)}, adminValue)
	require.Nil(t, svcErr, "%+v", svcErr)
	assert.Empty(t, updated.Subscription.Results)
	require.NotNil(t, updated.SigningSecret)
	assert.NotEqual(t, created.SigningSecret, *updated.SigningSecret)

	deliveries, svcErr := csilCall(t, mux, "ReactorcideUi", "list-event-deliveries", csilapi.EncodeListEventDeliveriesRequest,
		csilapi.DecodeListEventDeliveriesResponse, csilapi.ListEventDeliveriesRequest{SubscriptionId: subscriptionID}, adminValue)
	require.Nil(t, svcErr)
	assert.Empty(t, deliveries.Deliveries)

	_, svcErr = csilCall(t, mux, "ReactorcideUi", "replay-event-delivery", csilapi.EncodeReplayEventDeliveryRequest,
		csilapi.DecodeReplayEventDeliveryResponse, csilapi.ReplayEventDeliveryRequest{SubscriptionId: subscriptionID}, adminValue)
	require.NotNil(t, svcErr)
	assert.Equal(t, "invalid_argument", svcErr.Code)

	_, svcErr = csilCall(t, mux, "ReactorcideUi", "delete-event-subscription", csilapi.EncodeDeleteEventSubscriptionRequest,
		csilapi.DecodeDeleteEventSubscriptionResponse, csilapi.DeleteEventSubscriptionRequest{SubscriptionId: subscriptionID}, adminValue)
	require.Nil(t, svcErr)
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "get-event-subscription", csilapi.EncodeGetEventSubscriptionRequest,
		csilapi.DecodeGetEventSubscriptionResponse, csilapi.GetEventSubscriptionRequest{SubscriptionId: subscriptionID}, adminValue)
	require.NotNil(t, svcErr)
	assert.Equal(t, "not_found", svcErr.Code)
}

func boolPtrTest(v bool) *bool { return &v }
