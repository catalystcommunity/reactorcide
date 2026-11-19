package uiapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	rpctransport "github.com/catalystcommunity/csilgen/transports/go"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	workercsilapi "github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

const (
	// RpcPath is the canonical CSIL-RPC envelope-in-body mount point
	// (csil-rpc-transport.md §2.1). Exported so the router mounts *Handler
	// at the same path this package documents itself against.
	RpcPath = "/csil/v1/rpc"
	// maxEnvelopeBytes guards against unbounded request bodies before any
	// CBOR decoding happens (csil-transport-conventions.md's max-frame guard
	// default is 16 MiB; the HTTP envelope-in-body profile applies the same
	// bound to the whole POST body).
	maxEnvelopeBytes = 16 * 1024 * 1024
)

// Transport status codes for the failure modes this dispatcher can hit
// before/around a routed call (a successful call instead goes through
// rpctransport.NewRpcResponseOk, which sets its own StatusOk).
// statusMalformedRequest and statusUnknownRoute reuse the shared
// csil-transport-conventions.md registry codes 1 and 2 (malformed-envelope,
// unknown-service-or-op) via the rpctransport package; statusHandlerError
// maps a routed op returning a non-ServiceError error to the registry's
// "internal" code (6) — the previous hand-rolled dispatcher used an ad hoc,
// non-registry code 3 here (which collides with the registry's
// "unauthenticated"), so this is a deliberate, behavior-equivalent
// correction rather than a preserved wire value (no test or peer depends on
// the exact non-zero code, only on it being non-zero).
var (
	statusMalformedRequest = rpctransport.StatusMalformedEnvelope
	statusUnknownRoute     = rpctransport.StatusUnknownServiceOrOp
	statusHandlerError     = rpctransport.StatusInternal
)

// opFunc decodes an operation's request payload, invokes the bound
// implementation method, and encodes the result. A non-nil returned error is
// always a transport-level failure (statusHandlerError); an application-level
// failure returned by the implementation as a *ServiceErr is translated to
// the "ServiceError" response variant (status 0) inside wrapOp, never
// reaching the caller of opFunc as an error.
type opFunc func(ctx context.Context, payload []byte) (variant string, data []byte, err error)

// Handler is an http.Handler that dispatches CSIL-RPC envelope-in-body
// requests (POST /csil/v1/rpc, csil-rpc-transport.md §2.1) to the
// ReactorcideAuth / ReactorcideUi service implementations it was
// constructed with. It decodes the request envelope, routes on
// (service, op) to the matching generated service interface method, encodes
// the success/ServiceError response arm, and exposes the envelope's "auth"
// field to implementations via WithAuthToken/AuthTokenFromContext.
type Handler struct {
	ops map[string]map[string]opFunc
}

// NewHandler builds the dispatcher for the given ReactorcideAuth /
// ReactorcideUi implementations, with no ReactorcideWorker service mounted
// (a request for service "ReactorcideWorker" resolves as an unknown route).
// Pass NewStubAuth()/NewStubUi() to mount a handler whose every op returns
// ServiceError{code:"unimplemented"} until the real implementations are
// wired in. Existing callers (router.go's non-worker paths, this
// package's own dispatcher tests) keep this two-argument shape; production
// wiring that also mounts the worker protocol uses NewHandlerWithWorker.
func NewHandler(auth csilapi.ReactorcideAuth, ui csilapi.ReactorcideUi) *Handler {
	return NewHandlerWithWorker(auth, ui, nil)
}

// NewHandlerWithWorker builds the dispatcher for the given ReactorcideAuth /
// ReactorcideUi implementations, additionally mounting a ReactorcideWorker
// implementation on the SAME /csil/v1/rpc envelope-in-body endpoint --
// CSIL-RPC routes on the envelope's own `service` field, so one HTTP handler
// serves every service sharing this transport. worker may be nil (no
// "ReactorcideWorker" ops registered; a request naming that service resolves
// as an unknown route, matching pre-worker-protocol behavior exactly).
//
// worker's ServiceError arm is encoded via THIS package's
// csilapi.EncodeServiceError (through wrapOp's *ServiceErr recognition,
// shared across every service this dispatcher serves) rather than
// workerapi/csilapi's own generated encoder for the same-shaped type --
// reactorcide-worker.csil's ServiceError is a structurally identical
// {code: text, message: text} record, so the wire bytes are identical
// either way; internal/workerapi implementations return uiapi.NewServiceError
// values for exactly this reason (see internal/workerapi/deps.go).
func NewHandlerWithWorker(auth csilapi.ReactorcideAuth, ui csilapi.ReactorcideUi, worker workercsilapi.ReactorcideWorker) *Handler {
	h := &Handler{ops: map[string]map[string]opFunc{}}

	h.ops["ReactorcideAuth"] = map[string]opFunc{
		"get-auth-config": wrapOp(csilapi.DecodeGetAuthConfigRequest, csilapi.EncodeGetAuthConfigResponse, "GetAuthConfigResponse", auth.GetAuthConfig),
		"begin-login":     wrapOp(csilapi.DecodeBeginLoginRequest, csilapi.EncodeBeginLoginResponse, "BeginLoginResponse", auth.BeginLogin),
		"complete-login":  wrapOp(csilapi.DecodeCompleteLoginRequest, csilapi.EncodeCompleteLoginResponse, "CompleteLoginResponse", auth.CompleteLogin),
		"authenticate":    wrapOp(csilapi.DecodeAuthenticateRequest, csilapi.EncodeAuthenticateResponse, "AuthenticateResponse", auth.Authenticate),
		"logout":          wrapOp(csilapi.DecodeLogoutRequest, csilapi.EncodeLogoutResponse, "LogoutResponse", auth.Logout),
		"bootstrap-admin": wrapOp(csilapi.DecodeBootstrapAdminRequest, csilapi.EncodeBootstrapAdminResponse, "BootstrapAdminResponse", auth.BootstrapAdmin),
	}
	h.ops["ReactorcideUi"] = map[string]opFunc{
		"get-capabilities":              wrapOp(csilapi.DecodeGetCapabilitiesRequest, csilapi.EncodeGetCapabilitiesResponse, "GetCapabilitiesResponse", ui.GetCapabilities),
		"list-orgs":                     wrapOp(csilapi.DecodeListOrgsRequest, csilapi.EncodeListOrgsResponse, "ListOrgsResponse", ui.ListOrgs),
		"create-org":                    wrapOp(csilapi.DecodeCreateOrgRequest, csilapi.EncodeCreateOrgResponse, "CreateOrgResponse", ui.CreateOrg),
		"update-org":                    wrapOp(csilapi.DecodeUpdateOrgRequest, csilapi.EncodeUpdateOrgResponse, "UpdateOrgResponse", ui.UpdateOrg),
		"set-default-org":               wrapOp(csilapi.DecodeSetDefaultOrgRequest, csilapi.EncodeSetDefaultOrgResponse, "SetDefaultOrgResponse", ui.SetDefaultOrg),
		"delete-org":                    wrapOp(csilapi.DecodeDeleteOrgRequest, csilapi.EncodeDeleteOrgResponse, "DeleteOrgResponse", ui.DeleteOrg),
		"list-execution-profiles":       wrapOp(csilapi.DecodeListExecutionProfilesRequest, csilapi.EncodeListExecutionProfilesResponse, "ListExecutionProfilesResponse", ui.ListExecutionProfiles),
		"put-execution-profile":         wrapOp(csilapi.DecodePutExecutionProfileRequest, csilapi.EncodePutExecutionProfileResponse, "PutExecutionProfileResponse", ui.PutExecutionProfile),
		"delete-execution-profile":      wrapOp(csilapi.DecodeDeleteExecutionProfileRequest, csilapi.EncodeDeleteExecutionProfileResponse, "DeleteExecutionProfileResponse", ui.DeleteExecutionProfile),
		"list-worker-classes":           wrapOp(csilapi.DecodeListWorkerClassesRequest, csilapi.EncodeListWorkerClassesResponse, "ListWorkerClassesResponse", ui.ListWorkerClasses),
		"put-worker-class":              wrapOp(csilapi.DecodePutWorkerClassRequest, csilapi.EncodePutWorkerClassResponse, "PutWorkerClassResponse", ui.PutWorkerClass),
		"delete-worker-class":           wrapOp(csilapi.DecodeDeleteWorkerClassRequest, csilapi.EncodeDeleteWorkerClassResponse, "DeleteWorkerClassResponse", ui.DeleteWorkerClass),
		"set-worker-class-pool":         wrapOp(csilapi.DecodeSetWorkerClassPoolRequest, csilapi.EncodeSetWorkerClassPoolResponse, "SetWorkerClassPoolResponse", ui.SetWorkerClassPool),
		"pr-command":                    wrapOp(csilapi.DecodePrCommandRequest, csilapi.EncodePrCommandResponse, "PrCommandResponse", ui.PrCommand),
		"get-pr-approvals":              wrapOp(csilapi.DecodeGetPrApprovalsRequest, csilapi.EncodeGetPrApprovalsResponse, "GetPrApprovalsResponse", ui.GetPrApprovals),
		"get-ci-trust-settings":         wrapOp(csilapi.DecodeGetCiTrustSettingsRequest, csilapi.EncodeGetCiTrustSettingsResponse, "GetCiTrustSettingsResponse", ui.GetCiTrustSettings),
		"put-ci-trust-settings":         wrapOp(csilapi.DecodePutCiTrustSettingsRequest, csilapi.EncodePutCiTrustSettingsResponse, "PutCiTrustSettingsResponse", ui.PutCiTrustSettings),
		"list-projects":                 wrapOp(csilapi.DecodeListProjectsRequest, csilapi.EncodeListProjectsResponse, "ListProjectsResponse", ui.ListProjects),
		"get-project":                   wrapOp(csilapi.DecodeGetProjectRequest, csilapi.EncodeGetProjectResponse, "GetProjectResponse", ui.GetProject),
		"create-project":                wrapOp(csilapi.DecodeCreateProjectRequest, csilapi.EncodeCreateProjectResponse, "CreateProjectResponse", ui.CreateProject),
		"update-project":                wrapOp(csilapi.DecodeUpdateProjectRequest, csilapi.EncodeUpdateProjectResponse, "UpdateProjectResponse", ui.UpdateProject),
		"delete-project":                wrapOp(csilapi.DecodeDeleteProjectRequest, csilapi.EncodeDeleteProjectResponse, "DeleteProjectResponse", ui.DeleteProject),
		"get-ci-policy":                 wrapOp(csilapi.DecodeGetCiPolicyRequest, csilapi.EncodeGetCiPolicyResponse, "GetCiPolicyResponse", ui.GetCiPolicy),
		"put-ci-policy":                 wrapOp(csilapi.DecodePutCiPolicyRequest, csilapi.EncodePutCiPolicyResponse, "PutCiPolicyResponse", ui.PutCiPolicy),
		"delete-ci-policy":              wrapOp(csilapi.DecodeDeleteCiPolicyRequest, csilapi.EncodeDeleteCiPolicyResponse, "DeleteCiPolicyResponse", ui.DeleteCiPolicy),
		"list-groups":                   wrapOp(csilapi.DecodeListGroupsRequest, csilapi.EncodeListGroupsResponse, "ListGroupsResponse", ui.ListGroups),
		"create-group":                  wrapOp(csilapi.DecodeCreateGroupRequest, csilapi.EncodeCreateGroupResponse, "CreateGroupResponse", ui.CreateGroup),
		"update-group":                  wrapOp(csilapi.DecodeUpdateGroupRequest, csilapi.EncodeUpdateGroupResponse, "UpdateGroupResponse", ui.UpdateGroup),
		"delete-group":                  wrapOp(csilapi.DecodeDeleteGroupRequest, csilapi.EncodeDeleteGroupResponse, "DeleteGroupResponse", ui.DeleteGroup),
		"add-group-member":              wrapOp(csilapi.DecodeAddGroupMemberRequest, csilapi.EncodeAddGroupMemberResponse, "AddGroupMemberResponse", ui.AddGroupMember),
		"remove-group-member":           wrapOp(csilapi.DecodeRemoveGroupMemberRequest, csilapi.EncodeRemoveGroupMemberResponse, "RemoveGroupMemberResponse", ui.RemoveGroupMember),
		"list-group-members":            wrapOp(csilapi.DecodeListGroupMembersRequest, csilapi.EncodeListGroupMembersResponse, "ListGroupMembersResponse", ui.ListGroupMembers),
		"list-group-member-details":     wrapOp(csilapi.DecodeListGroupMemberDetailsRequest, csilapi.EncodeListGroupMemberDetailsResponse, "ListGroupMemberDetailsResponse", ui.ListGroupMemberDetails),
		"list-group-vcs-links":          wrapOp(csilapi.DecodeListGroupVcsLinksRequest, csilapi.EncodeListGroupVcsLinksResponse, "ListGroupVcsLinksResponse", ui.ListGroupVcsLinks),
		"link-group-vcs-team":           wrapOp(csilapi.DecodeLinkGroupVcsTeamRequest, csilapi.EncodeLinkGroupVcsTeamResponse, "LinkGroupVcsTeamResponse", ui.LinkGroupVcsTeam),
		"unlink-group-vcs-team":         wrapOp(csilapi.DecodeUnlinkGroupVcsTeamRequest, csilapi.EncodeUnlinkGroupVcsTeamResponse, "UnlinkGroupVcsTeamResponse", ui.UnlinkGroupVcsTeam),
		"sync-group-vcs-teams":          wrapOp(csilapi.DecodeSyncGroupVcsTeamsRequest, csilapi.EncodeSyncGroupVcsTeamsResponse, "SyncGroupVcsTeamsResponse", ui.SyncGroupVcsTeams),
		"list-users":                    wrapOp(csilapi.DecodeListUsersRequest, csilapi.EncodeListUsersResponse, "ListUsersResponse", ui.ListUsers),
		"list-role-assignments":         wrapOp(csilapi.DecodeListRoleAssignmentsRequest, csilapi.EncodeListRoleAssignmentsResponse, "ListRoleAssignmentsResponse", ui.ListRoleAssignments),
		"assign-role":                   wrapOp(csilapi.DecodeAssignRoleRequest, csilapi.EncodeAssignRoleResponse, "AssignRoleResponse", ui.AssignRole),
		"revoke-role":                   wrapOp(csilapi.DecodeRevokeRoleRequest, csilapi.EncodeRevokeRoleResponse, "RevokeRoleResponse", ui.RevokeRole),
		"list-webhook-secrets":          wrapOp(csilapi.DecodeListWebhookSecretsRequest, csilapi.EncodeListWebhookSecretsResponse, "ListWebhookSecretsResponse", ui.ListWebhookSecrets),
		"add-webhook-secret":            wrapOp(csilapi.DecodeAddWebhookSecretRequest, csilapi.EncodeAddWebhookSecretResponse, "AddWebhookSecretResponse", ui.AddWebhookSecret),
		"deactivate-webhook-secret":     wrapOp(csilapi.DecodeDeactivateWebhookSecretRequest, csilapi.EncodeDeactivateWebhookSecretResponse, "DeactivateWebhookSecretResponse", ui.DeactivateWebhookSecret),
		"delete-webhook-secret":         wrapOp(csilapi.DecodeDeleteWebhookSecretRequest, csilapi.EncodeDeleteWebhookSecretResponse, "DeleteWebhookSecretResponse", ui.DeleteWebhookSecret),
		"list-vcs-credentials":          wrapOp(csilapi.DecodeListVcsCredentialsRequest, csilapi.EncodeListVcsCredentialsResponse, "ListVcsCredentialsResponse", ui.ListVcsCredentials),
		"add-vcs-credential":            wrapOp(csilapi.DecodeAddVcsCredentialRequest, csilapi.EncodeAddVcsCredentialResponse, "AddVcsCredentialResponse", ui.AddVcsCredential),
		"deactivate-vcs-credential":     wrapOp(csilapi.DecodeDeactivateVcsCredentialRequest, csilapi.EncodeDeactivateVcsCredentialResponse, "DeactivateVcsCredentialResponse", ui.DeactivateVcsCredential),
		"delete-vcs-credential":         wrapOp(csilapi.DecodeDeleteVcsCredentialRequest, csilapi.EncodeDeleteVcsCredentialResponse, "DeleteVcsCredentialResponse", ui.DeleteVcsCredential),
		"set-secret":                    wrapOp(csilapi.DecodeSetSecretRequest, csilapi.EncodeSetSecretResponse, "SetSecretResponse", ui.SetSecret),
		"delete-secret":                 wrapOp(csilapi.DecodeDeleteSecretRequest, csilapi.EncodeDeleteSecretResponse, "DeleteSecretResponse", ui.DeleteSecret),
		"list-secret-paths":             wrapOp(csilapi.DecodeListSecretPathsRequest, csilapi.EncodeListSecretPathsResponse, "ListSecretPathsResponse", ui.ListSecretPaths),
		"list-secret-grants":            wrapOp(csilapi.DecodeListSecretGrantsRequest, csilapi.EncodeListSecretGrantsResponse, "ListSecretGrantsResponse", ui.ListSecretGrants),
		"create-secret-grant":           wrapOp(csilapi.DecodeCreateSecretGrantRequest, csilapi.EncodeCreateSecretGrantResponse, "CreateSecretGrantResponse", ui.CreateSecretGrant),
		"update-secret-grant":           wrapOp(csilapi.DecodeUpdateSecretGrantRequest, csilapi.EncodeUpdateSecretGrantResponse, "UpdateSecretGrantResponse", ui.UpdateSecretGrant),
		"delete-secret-grant":           wrapOp(csilapi.DecodeDeleteSecretGrantRequest, csilapi.EncodeDeleteSecretGrantResponse, "DeleteSecretGrantResponse", ui.DeleteSecretGrant),
		"cancel-job":                    wrapOp(csilapi.DecodeCancelJobRequest, csilapi.EncodeCancelJobResponse, "CancelJobResponse", ui.CancelJob),
		"kill-job":                      wrapOp(csilapi.DecodeKillJobRequest, csilapi.EncodeKillJobResponse, "KillJobResponse", ui.KillJob),
		"cancel-workflow":               wrapOp(csilapi.DecodeCancelWorkflowRequest, csilapi.EncodeCancelWorkflowResponse, "CancelWorkflowResponse", ui.CancelWorkflow),
		"retry-job":                     wrapOp(csilapi.DecodeRetryJobRequest, csilapi.EncodeRetryJobResponse, "RetryJobResponse", ui.RetryJob),
		"retry-workflow":                wrapOp(csilapi.DecodeRetryWorkflowRequest, csilapi.EncodeRetryWorkflowResponse, "RetryWorkflowResponse", ui.RetryWorkflow),
		"retry-unsuccessful-jobs":       wrapOp(csilapi.DecodeRetryUnsuccessfulJobsRequest, csilapi.EncodeRetryUnsuccessfulJobsResponse, "RetryUnsuccessfulJobsResponse", ui.RetryUnsuccessfulJobs),
		"get-global-settings":           wrapOp(csilapi.DecodeGetGlobalSettingsRequest, csilapi.EncodeGetGlobalSettingsResponse, "GetGlobalSettingsResponse", ui.GetGlobalSettings),
		"update-global-settings":        wrapOp(csilapi.DecodeUpdateGlobalSettingsRequest, csilapi.EncodeUpdateGlobalSettingsResponse, "UpdateGlobalSettingsResponse", ui.UpdateGlobalSettings),
		"get-shed-state":                wrapOp(csilapi.DecodeGetShedStateRequest, csilapi.EncodeGetShedStateResponse, "GetShedStateResponse", ui.GetShedState),
		"set-shed-level":                wrapOp(csilapi.DecodeSetShedLevelRequest, csilapi.EncodeSetShedLevelResponse, "SetShedLevelResponse", ui.SetShedLevel),
		"list-trusted-identities":       wrapOp(csilapi.DecodeListTrustedIdentitiesRequest, csilapi.EncodeListTrustedIdentitiesResponse, "ListTrustedIdentitiesResponse", ui.ListTrustedIdentities),
		"add-trusted-identity":          wrapOp(csilapi.DecodeAddTrustedIdentityRequest, csilapi.EncodeAddTrustedIdentityResponse, "AddTrustedIdentityResponse", ui.AddTrustedIdentity),
		"remove-trusted-identity":       wrapOp(csilapi.DecodeRemoveTrustedIdentityRequest, csilapi.EncodeRemoveTrustedIdentityResponse, "RemoveTrustedIdentityResponse", ui.RemoveTrustedIdentity),
		"list-trusted-domain-patterns":  wrapOp(csilapi.DecodeListTrustedDomainPatternsRequest, csilapi.EncodeListTrustedDomainPatternsResponse, "ListTrustedDomainPatternsResponse", ui.ListTrustedDomainPatterns),
		"add-trusted-domain-pattern":    wrapOp(csilapi.DecodeAddTrustedDomainPatternRequest, csilapi.EncodeAddTrustedDomainPatternResponse, "AddTrustedDomainPatternResponse", ui.AddTrustedDomainPattern),
		"remove-trusted-domain-pattern": wrapOp(csilapi.DecodeRemoveTrustedDomainPatternRequest, csilapi.EncodeRemoveTrustedDomainPatternResponse, "RemoveTrustedDomainPatternResponse", ui.RemoveTrustedDomainPattern),
		"list-queues":                   wrapOp(csilapi.DecodeListQueuesRequest, csilapi.EncodeListQueuesResponse, "ListQueuesResponse", ui.ListQueues),
		"create-queue":                  wrapOp(csilapi.DecodeCreateQueueRequest, csilapi.EncodeCreateQueueResponse, "CreateQueueResponse", ui.CreateQueue),
		"rename-queue":                  wrapOp(csilapi.DecodeRenameQueueRequest, csilapi.EncodeRenameQueueResponse, "RenameQueueResponse", ui.RenameQueue),
		"delete-queue":                  wrapOp(csilapi.DecodeDeleteQueueRequest, csilapi.EncodeDeleteQueueResponse, "DeleteQueueResponse", ui.DeleteQueue),
		"list-workers":                  wrapOp(csilapi.DecodeListWorkersRequest, csilapi.EncodeListWorkersResponse, "ListWorkersResponse", ui.ListWorkers),
		"set-worker-status":             wrapOp(csilapi.DecodeSetWorkerStatusRequest, csilapi.EncodeSetWorkerStatusResponse, "SetWorkerStatusResponse", ui.SetWorkerStatus),
		"drain-worker":                  wrapOp(csilapi.DecodeDrainWorkerRequest, csilapi.EncodeDrainWorkerResponse, "DrainWorkerResponse", ui.DrainWorker),
		"list-pools":                    wrapOp(csilapi.DecodeListPoolsRequest, csilapi.EncodeListPoolsResponse, "ListPoolsResponse", ui.ListPools),
		"create-pool":                   wrapOp(csilapi.DecodeCreatePoolRequest, csilapi.EncodeCreatePoolResponse, "CreatePoolResponse", ui.CreatePool),
		"update-pool":                   wrapOp(csilapi.DecodeUpdatePoolRequest, csilapi.EncodeUpdatePoolResponse, "UpdatePoolResponse", ui.UpdatePool),
		"delete-pool":                   wrapOp(csilapi.DecodeDeletePoolRequest, csilapi.EncodeDeletePoolResponse, "DeletePoolResponse", ui.DeletePool),
		"create-enrollment-token":       wrapOp(csilapi.DecodeCreateEnrollmentTokenRequest, csilapi.EncodeCreateEnrollmentTokenResponse, "CreateEnrollmentTokenResponse", ui.CreateEnrollmentToken),
		"list-enrollment-tokens":        wrapOp(csilapi.DecodeListEnrollmentTokensRequest, csilapi.EncodeListEnrollmentTokensResponse, "ListEnrollmentTokensResponse", ui.ListEnrollmentTokens),
		"deactivate-enrollment-token":   wrapOp(csilapi.DecodeDeactivateEnrollmentTokenRequest, csilapi.EncodeDeactivateEnrollmentTokenResponse, "DeactivateEnrollmentTokenResponse", ui.DeactivateEnrollmentToken),
		"get-job-metrics":               wrapOp(csilapi.DecodeGetJobMetricsRequest, csilapi.EncodeGetJobMetricsResponse, "GetJobMetricsResponse", ui.GetJobMetrics),
		"get-job-logs":                  wrapOp(csilapi.DecodeGetJobLogsRequest, csilapi.EncodeGetJobLogsResponse, "GetJobLogsResponse", ui.GetJobLogs),
		"get-job-resource-history":      wrapOp(csilapi.DecodeGetJobResourceHistoryRequest, csilapi.EncodeGetJobResourceHistoryResponse, "GetJobResourceHistoryResponse", ui.GetJobResourceHistory),
		"get-pipeline-dashboard":        wrapOp(csilapi.DecodeGetPipelineDashboardRequest, csilapi.EncodeGetPipelineDashboardResponse, "GetPipelineDashboardResponse", ui.GetPipelineDashboard),
		"get-infra-dashboard":           wrapOp(csilapi.DecodeGetInfraDashboardRequest, csilapi.EncodeGetInfraDashboardResponse, "GetInfraDashboardResponse", ui.GetInfraDashboard),
		"get-usage-dashboard":           wrapOp(csilapi.DecodeGetUsageDashboardRequest, csilapi.EncodeGetUsageDashboardResponse, "GetUsageDashboardResponse", ui.GetUsageDashboard),
		"list-schedules":                wrapOp(csilapi.DecodeListSchedulesRequest, csilapi.EncodeListSchedulesResponse, "ListSchedulesResponse", ui.ListSchedules),
		"create-schedule":               wrapOp(csilapi.DecodeCreateScheduleRequest, csilapi.EncodeCreateScheduleResponse, "CreateScheduleResponse", ui.CreateSchedule),
		"update-schedule":               wrapOp(csilapi.DecodeUpdateScheduleRequest, csilapi.EncodeUpdateScheduleResponse, "UpdateScheduleResponse", ui.UpdateSchedule),
		"delete-schedule":               wrapOp(csilapi.DecodeDeleteScheduleRequest, csilapi.EncodeDeleteScheduleResponse, "DeleteScheduleResponse", ui.DeleteSchedule),
		"list-schedule-fires":           wrapOp(csilapi.DecodeListScheduleFiresRequest, csilapi.EncodeListScheduleFiresResponse, "ListScheduleFiresResponse", ui.ListScheduleFires),
		"list-event-subscriptions":      wrapOp(csilapi.DecodeListEventSubscriptionsRequest, csilapi.EncodeListEventSubscriptionsResponse, "ListEventSubscriptionsResponse", ui.ListEventSubscriptions),
		"get-event-subscription":        wrapOp(csilapi.DecodeGetEventSubscriptionRequest, csilapi.EncodeGetEventSubscriptionResponse, "GetEventSubscriptionResponse", ui.GetEventSubscription),
		"create-event-subscription":     wrapOp(csilapi.DecodeCreateEventSubscriptionRequest, csilapi.EncodeCreateEventSubscriptionResponse, "CreateEventSubscriptionResponse", ui.CreateEventSubscription),
		"update-event-subscription":     wrapOp(csilapi.DecodeUpdateEventSubscriptionRequest, csilapi.EncodeUpdateEventSubscriptionResponse, "UpdateEventSubscriptionResponse", ui.UpdateEventSubscription),
		"delete-event-subscription":     wrapOp(csilapi.DecodeDeleteEventSubscriptionRequest, csilapi.EncodeDeleteEventSubscriptionResponse, "DeleteEventSubscriptionResponse", ui.DeleteEventSubscription),
		"list-event-deliveries":         wrapOp(csilapi.DecodeListEventDeliveriesRequest, csilapi.EncodeListEventDeliveriesResponse, "ListEventDeliveriesResponse", ui.ListEventDeliveries),
		"get-event-delivery":            wrapOp(csilapi.DecodeGetEventDeliveryRequest, csilapi.EncodeGetEventDeliveryResponse, "GetEventDeliveryResponse", ui.GetEventDelivery),
		"replay-event-delivery":         wrapOp(csilapi.DecodeReplayEventDeliveryRequest, csilapi.EncodeReplayEventDeliveryResponse, "ReplayEventDeliveryResponse", ui.ReplayEventDelivery),
		"test-event-subscription":       wrapOp(csilapi.DecodeTestEventSubscriptionRequest, csilapi.EncodeTestEventSubscriptionResponse, "TestEventSubscriptionResponse", ui.TestEventSubscription),
		"list-jobs":                     wrapOp(csilapi.DecodeListJobsRequest, csilapi.EncodeListJobsResponse, "ListJobsResponse", ui.ListJobs),
		"get-job":                       wrapOp(csilapi.DecodeGetJobRequest, csilapi.EncodeGetJobResponse, "GetJobResponse", ui.GetJob),
		"list-workflows":                wrapOp(csilapi.DecodeListWorkflowsRequest, csilapi.EncodeListWorkflowsResponse, "ListWorkflowsResponse", ui.ListWorkflows),
		"get-workflow":                  wrapOp(csilapi.DecodeGetWorkflowRequest, csilapi.EncodeGetWorkflowResponse, "GetWorkflowResponse", ui.GetWorkflow),
		"get-event":                     wrapOp(csilapi.DecodeGetEventRequest, csilapi.EncodeGetEventResponse, "GetEventResponse", ui.GetEvent),
		"describe-form-metadata":        wrapOp(csilapi.DecodeDescribeFormMetadataRequest, csilapi.EncodeDescribeFormMetadataResponse, "DescribeFormMetadataResponse", ui.DescribeFormMetadata),
		// WP2 principals.
		"list-principals":           wrapOp(csilapi.DecodeListPrincipalsRequest, csilapi.EncodeListPrincipalsResponse, "ListPrincipalsResponse", ui.ListPrincipals),
		"get-principal":             wrapOp(csilapi.DecodeGetPrincipalRequest, csilapi.EncodeGetPrincipalResponse, "GetPrincipalResponse", ui.GetPrincipal),
		"create-service-principal":  wrapOp(csilapi.DecodeCreateServicePrincipalRequest, csilapi.EncodeCreateServicePrincipalResponse, "CreateServicePrincipalResponse", ui.CreateServicePrincipal),
		"set-principal-status":      wrapOp(csilapi.DecodeSetPrincipalStatusRequest, csilapi.EncodeSetPrincipalStatusResponse, "SetPrincipalStatusResponse", ui.SetPrincipalStatus),
		"link-principal-identity":   wrapOp(csilapi.DecodePrincipalIdentityRequest, csilapi.EncodePrincipalIdentityResponse, "PrincipalIdentityResponse", ui.LinkPrincipalIdentity),
		"unlink-principal-identity": wrapOp(csilapi.DecodePrincipalIdentityRequest, csilapi.EncodePrincipalIdentityResponse, "PrincipalIdentityResponse", ui.UnlinkPrincipalIdentity),
		"set-principal-group":       wrapOp(csilapi.DecodeSetPrincipalGroupRequest, csilapi.EncodeSetPrincipalGroupResponse, "SetPrincipalGroupResponse", ui.SetPrincipalGroup),
		"issue-principal-token":     wrapOp(csilapi.DecodeIssuePrincipalTokenRequest, csilapi.EncodeIssuePrincipalTokenResponse, "IssuePrincipalTokenResponse", ui.IssuePrincipalToken),
		"revoke-principal-token":    wrapOp(csilapi.DecodeRevokePrincipalTokenRequest, csilapi.EncodeRevokePrincipalTokenResponse, "RevokePrincipalTokenResponse", ui.RevokePrincipalToken),
		"resolve-principal":         wrapOp(csilapi.DecodeResolvePrincipalRequest, csilapi.EncodeResolvePrincipalResponse, "ResolvePrincipalResponse", ui.ResolvePrincipal),
		"list-federation-issuers":   wrapOp(csilapi.DecodeListFederationIssuersRequest, csilapi.EncodeListFederationIssuersResponse, "ListFederationIssuersResponse", ui.ListFederationIssuers),
		"save-federation-issuer":    wrapOp(csilapi.DecodeSaveFederationIssuerRequest, csilapi.EncodeSaveFederationIssuerResponse, "SaveFederationIssuerResponse", ui.SaveFederationIssuer),
		"delete-federation-issuer":  wrapOp(csilapi.DecodeDeleteFederationIssuerRequest, csilapi.EncodeDeleteFederationIssuerResponse, "DeleteFederationIssuerResponse", ui.DeleteFederationIssuer),
		"save-federation-mapping":   wrapOp(csilapi.DecodeSaveFederationMappingRequest, csilapi.EncodeSaveFederationMappingResponse, "SaveFederationMappingResponse", ui.SaveFederationMapping),
		"delete-federation-mapping": wrapOp(csilapi.DecodeDeleteFederationMappingRequest, csilapi.EncodeDeleteFederationMappingResponse, "DeleteFederationMappingResponse", ui.DeleteFederationMapping),
		"list-github-apps":          wrapOp(csilapi.DecodeListGitHubAppsRequest, csilapi.EncodeListGitHubAppsResponse, "ListGitHubAppsResponse", ui.ListGithubApps),
		"set-github-app-enabled":    wrapOp(csilapi.DecodeSetGitHubAppEnabledRequest, csilapi.EncodeSetGitHubAppEnabledResponse, "SetGitHubAppEnabledResponse", ui.SetGithubAppEnabled),
		"sync-github-app":           wrapOp(csilapi.DecodeSyncGitHubAppRequest, csilapi.EncodeSyncGitHubAppResponse, "SyncGitHubAppResponse", ui.SyncGithubApp),
		// WP3 ref classes.
		// WP10 reporting and ownership.
		"get-reporting-settings":    wrapOp(csilapi.DecodeGetReportingSettingsRequest, csilapi.EncodeGetReportingSettingsResponse, "GetReportingSettingsResponse", ui.GetReportingSettings),
		"update-reporting-settings": wrapOp(csilapi.DecodeUpdateReportingSettingsRequest, csilapi.EncodeGetReportingSettingsResponse, "GetReportingSettingsResponse", ui.UpdateReportingSettings),
		"get-agreement-report":      wrapOp(csilapi.DecodeGetAgreementReportRequest, csilapi.EncodeGetAgreementReportResponse, "GetAgreementReportResponse", ui.GetAgreementReport),
		"list-path-acceptances":     wrapOp(csilapi.DecodeListPathAcceptancesRequest, csilapi.EncodeListPathAcceptancesResponse, "ListPathAcceptancesResponse", ui.ListPathAcceptances),
		"request-path-acceptance":   wrapOp(csilapi.DecodeRequestPathAcceptanceRequest, csilapi.EncodePathAcceptanceResponse, "PathAcceptanceResponse", ui.RequestPathAcceptance),
		"decide-path-acceptance":    wrapOp(csilapi.DecodeDecidePathAcceptanceRequest, csilapi.EncodePathAcceptanceResponse, "PathAcceptanceResponse", ui.DecidePathAcceptance),
		// WP15 artifacts, test results, test quarantine, provenance.
		"list-job-artifacts":             wrapOp(csilapi.DecodeListJobArtifactsRequest, csilapi.EncodeListJobArtifactsResponse, "ListJobArtifactsResponse", ui.ListJobArtifacts),
		"read-job-artifact":              wrapOp(csilapi.DecodeReadJobArtifactRequest, csilapi.EncodeReadJobArtifactResponse, "ReadJobArtifactResponse", ui.ReadJobArtifact),
		"get-job-test-results":           wrapOp(csilapi.DecodeGetJobTestResultsRequest, csilapi.EncodeGetJobTestResultsResponse, "GetJobTestResultsResponse", ui.GetJobTestResults),
		"list-flaky-tests":               wrapOp(csilapi.DecodeListFlakyTestsRequest, csilapi.EncodeListFlakyTestsResponse, "ListFlakyTestsResponse", ui.ListFlakyTests),
		"list-test-quarantines":          wrapOp(csilapi.DecodeListTestQuarantinesRequest, csilapi.EncodeListTestQuarantinesResponse, "ListTestQuarantinesResponse", ui.ListTestQuarantines),
		"quarantine-test":                wrapOp(csilapi.DecodeQuarantineTestRequest, csilapi.EncodeQuarantineTestResponse, "QuarantineTestResponse", ui.QuarantineTest),
		"unquarantine-test":              wrapOp(csilapi.DecodeUnquarantineTestRequest, csilapi.EncodeUnquarantineTestResponse, "UnquarantineTestResponse", ui.UnquarantineTest),
		"get-job-provenance":             wrapOp(csilapi.DecodeGetJobProvenanceRequest, csilapi.EncodeGetJobProvenanceResponse, "GetJobProvenanceResponse", ui.GetJobProvenance),
		"get-required-set":               wrapOp(csilapi.DecodeGetRequiredSetRequest, csilapi.EncodeGetRequiredSetResponse, "GetRequiredSetResponse", ui.GetRequiredSet),
		"set-required-set-enabled":       wrapOp(csilapi.DecodeSetRequiredSetEnabledRequest, csilapi.EncodeSetRequiredSetEnabledResponse, "SetRequiredSetEnabledResponse", ui.SetRequiredSetEnabled),
		"set-required-set-approvers":     wrapOp(csilapi.DecodeSetRequiredSetApproversRequest, csilapi.EncodeGetRequiredSetResponse, "GetRequiredSetResponse", ui.SetRequiredSetApprovers),
		"propose-required-set-change":    wrapOp(csilapi.DecodeProposeRequiredSetChangeRequest, csilapi.EncodeProposeRequiredSetChangeResponse, "ProposeRequiredSetChangeResponse", ui.ProposeRequiredSetChange),
		"decide-required-set-proposal":   wrapOp(csilapi.DecodeDecideRequiredSetProposalRequest, csilapi.EncodeDecideRequiredSetProposalResponse, "DecideRequiredSetProposalResponse", ui.DecideRequiredSetProposal),
		"quarantine-required-workflow":   wrapOp(csilapi.DecodeQuarantineRequiredWorkflowRequest, csilapi.EncodeQuarantineRequiredWorkflowResponse, "QuarantineRequiredWorkflowResponse", ui.QuarantineRequiredWorkflow),
		"unquarantine-required-workflow": wrapOp(csilapi.DecodeUnquarantineRequiredWorkflowRequest, csilapi.EncodeUnquarantineRequiredWorkflowResponse, "UnquarantineRequiredWorkflowResponse", ui.UnquarantineRequiredWorkflow),
		"list-priority-lanes":            wrapOp(csilapi.DecodeListPriorityLanesRequest, csilapi.EncodeListPriorityLanesResponse, "ListPriorityLanesResponse", ui.ListPriorityLanes),
		"put-priority-lane":              wrapOp(csilapi.DecodePutPriorityLaneRequest, csilapi.EncodePutPriorityLaneResponse, "PutPriorityLaneResponse", ui.PutPriorityLane),
		"delete-priority-lane":           wrapOp(csilapi.DecodeDeletePriorityLaneRequest, csilapi.EncodeDeletePriorityLaneResponse, "DeletePriorityLaneResponse", ui.DeletePriorityLane),
		"explain-priority":               wrapOp(csilapi.DecodeExplainPriorityRequest, csilapi.EncodeExplainPriorityResponse, "ExplainPriorityResponse", ui.ExplainPriority),
		"set-group-priority-shift":       wrapOp(csilapi.DecodeSetGroupPriorityShiftRequest, csilapi.EncodeSetGroupPriorityShiftResponse, "SetGroupPriorityShiftResponse", ui.SetGroupPriorityShift),
		"list-ref-class-rules":           wrapOp(csilapi.DecodeListRefClassRulesRequest, csilapi.EncodeListRefClassRulesResponse, "ListRefClassRulesResponse", ui.ListRefClassRules),
		"put-ref-class-rule":             wrapOp(csilapi.DecodePutRefClassRuleRequest, csilapi.EncodePutRefClassRuleResponse, "PutRefClassRuleResponse", ui.PutRefClassRule),
		"delete-ref-class-rule":          wrapOp(csilapi.DecodeDeleteRefClassRuleRequest, csilapi.EncodeDeleteRefClassRuleResponse, "DeleteRefClassRuleResponse", ui.DeleteRefClassRule),
		"explain-ref-classes":            wrapOp(csilapi.DecodeExplainRefClassesRequest, csilapi.EncodeExplainRefClassesResponse, "ExplainRefClassesResponse", ui.ExplainRefClasses),
	}
	// WP17 diagnosis entries from principals (docs/diagnosis.md).
	h.ops["ReactorcideUi"]["list-diagnoses"] = wrapOp(csilapi.DecodeListDiagnosesRequest, csilapi.EncodeListDiagnosesResponse, "ListDiagnosesResponse", ui.ListDiagnoses)
	h.ops["ReactorcideUi"]["put-diagnosis"] = wrapOp(csilapi.DecodePutDiagnosisRequest, csilapi.EncodePutDiagnosisResponse, "PutDiagnosisResponse", ui.PutDiagnosis)
	h.ops["ReactorcideUi"]["set-shed-actions"] = wrapOp(csilapi.DecodeSetShedActionsRequest, csilapi.EncodeSetShedLevelResponse, "SetShedLevelResponse", ui.SetShedActions)
	h.ops["ReactorcideUi"]["get-org-reporting-settings"] = wrapOp(csilapi.DecodeGetOrgReportingSettingsRequest, csilapi.EncodeGetOrgReportingSettingsResponse, "GetOrgReportingSettingsResponse", ui.GetOrgReportingSettings)
	h.ops["ReactorcideUi"]["put-org-reporting-settings"] = wrapOp(csilapi.DecodePutOrgReportingSettingsRequest, csilapi.EncodeGetOrgReportingSettingsResponse, "GetOrgReportingSettingsResponse", ui.PutOrgReportingSettings)
	h.ops["ReactorcideUi"]["get-required-set-org-settings"] = wrapOp(csilapi.DecodeGetRequiredSetOrgSettingsRequest, csilapi.EncodeGetRequiredSetOrgSettingsResponse, "GetRequiredSetOrgSettingsResponse", ui.GetRequiredSetOrgSettings)
	h.ops["ReactorcideUi"]["put-required-set-org-settings"] = wrapOp(csilapi.DecodePutRequiredSetOrgSettingsRequest, csilapi.EncodeGetRequiredSetOrgSettingsResponse, "GetRequiredSetOrgSettingsResponse", ui.PutRequiredSetOrgSettings)
	h.ops["ReactorcideUi"]["get-vcs-owner-allowlist"] = wrapOp(csilapi.DecodeGetVcsOwnerAllowlistRequest, csilapi.EncodeVcsOwnerAllowlistResponse, "VcsOwnerAllowlistResponse", ui.GetVcsOwnerAllowlist)
	h.ops["ReactorcideUi"]["put-vcs-owner-allowlist"] = wrapOp(csilapi.DecodePutVcsOwnerAllowlistRequest, csilapi.EncodeVcsOwnerAllowlistResponse, "VcsOwnerAllowlistResponse", ui.PutVcsOwnerAllowlist)
	h.ops["ReactorcideUi"]["get-oidc-settings"] = wrapOp(csilapi.DecodeGetOidcSettingsRequest, csilapi.EncodeOidcSettingsResponse, "OidcSettingsResponse", ui.GetOidcSettings)
	h.ops["ReactorcideUi"]["put-oidc-settings"] = wrapOp(csilapi.DecodePutOidcSettingsRequest, csilapi.EncodeOidcSettingsResponse, "OidcSettingsResponse", ui.PutOidcSettings)
	h.ops["ReactorcideUi"]["list-oidc-keys"] = wrapOp(csilapi.DecodeListOidcKeysRequest, csilapi.EncodeListOidcKeysResponse, "ListOidcKeysResponse", ui.ListOidcKeys)
	h.ops["ReactorcideUi"]["rotate-oidc-key"] = wrapOp(csilapi.DecodeRotateOidcKeyRequest, csilapi.EncodeListOidcKeysResponse, "ListOidcKeysResponse", ui.RotateOidcKey)
	h.ops["ReactorcideUi"]["retire-oidc-key"] = wrapOp(csilapi.DecodeRetireOidcKeyRequest, csilapi.EncodeListOidcKeysResponse, "ListOidcKeysResponse", ui.RetireOidcKey)
	h.ops["ReactorcideUi"]["list-alert-rules"] = wrapOp(csilapi.DecodeListAlertRulesRequest, csilapi.EncodeListAlertRulesResponse, "ListAlertRulesResponse", ui.ListAlertRules)
	h.ops["ReactorcideUi"]["create-alert-rule"] = wrapOp(csilapi.DecodeCreateAlertRuleRequest, csilapi.EncodeAlertRuleResponse, "AlertRuleResponse", ui.CreateAlertRule)
	h.ops["ReactorcideUi"]["update-alert-rule"] = wrapOp(csilapi.DecodeUpdateAlertRuleRequest, csilapi.EncodeAlertRuleResponse, "AlertRuleResponse", ui.UpdateAlertRule)
	h.ops["ReactorcideUi"]["delete-alert-rule"] = wrapOp(csilapi.DecodeDeleteAlertRuleRequest, csilapi.EncodeDeleteAlertRuleResponse, "DeleteAlertRuleResponse", ui.DeleteAlertRule)
	h.ops["ReactorcideUi"]["delete-diagnosis"] = wrapOp(csilapi.DecodeDeleteDiagnosisRequest, csilapi.EncodeDeleteDiagnosisResponse, "DeleteDiagnosisResponse", ui.DeleteDiagnosis)
	// WP5 re-run with shifts and runs on any ref.
	h.ops["ReactorcideUi"]["rerun-workflow"] = wrapOp(csilapi.DecodeRerunWorkflowRequest, csilapi.EncodeRerunWorkflowResponse, "RerunWorkflowResponse", ui.RerunWorkflow)
	h.ops["ReactorcideUi"]["start-run"] = wrapOp(csilapi.DecodeStartRunRequest, csilapi.EncodeStartRunResponse, "StartRunResponse", ui.StartRun)
	// WP14 environment defaults, image policy, and reserved job links
	// (docs/builds-and-caches.md).
	h.ops["ReactorcideUi"]["list-env-defaults"] = wrapOp(csilapi.DecodeListEnvDefaultsRequest, csilapi.EncodeListEnvDefaultsResponse, "ListEnvDefaultsResponse", ui.ListEnvDefaults)
	h.ops["ReactorcideUi"]["put-env-default"] = wrapOp(csilapi.DecodePutEnvDefaultRequest, csilapi.EncodePutEnvDefaultResponse, "PutEnvDefaultResponse", ui.PutEnvDefault)
	h.ops["ReactorcideUi"]["delete-env-default"] = wrapOp(csilapi.DecodeDeleteEnvDefaultRequest, csilapi.EncodeDeleteEnvDefaultResponse, "DeleteEnvDefaultResponse", ui.DeleteEnvDefault)
	h.ops["ReactorcideUi"]["get-image-policy"] = wrapOp(csilapi.DecodeGetImagePolicyRequest, csilapi.EncodeGetImagePolicyResponse, "GetImagePolicyResponse", ui.GetImagePolicy)
	h.ops["ReactorcideUi"]["put-image-policy"] = wrapOp(csilapi.DecodePutImagePolicyRequest, csilapi.EncodePutImagePolicyResponse, "PutImagePolicyResponse", ui.PutImagePolicy)
	h.ops["ReactorcideUi"]["list-image-aliases"] = wrapOp(csilapi.DecodeListImageAliasesRequest, csilapi.EncodeListImageAliasesResponse, "ListImageAliasesResponse", ui.ListImageAliases)
	h.ops["ReactorcideUi"]["get-image-alias"] = wrapOp(csilapi.DecodeGetImageAliasRequest, csilapi.EncodeGetImageAliasResponse, "GetImageAliasResponse", ui.GetImageAlias)
	h.ops["ReactorcideUi"]["set-image-alias"] = wrapOp(csilapi.DecodeSetImageAliasRequest, csilapi.EncodeSetImageAliasResponse, "SetImageAliasResponse", ui.SetImageAlias)
	h.ops["ReactorcideUi"]["delete-image-alias"] = wrapOp(csilapi.DecodeDeleteImageAliasRequest, csilapi.EncodeDeleteImageAliasResponse, "DeleteImageAliasResponse", ui.DeleteImageAlias)
	h.ops["ReactorcideUi"]["get-image-rollout-status"] = wrapOp(csilapi.DecodeGetImageRolloutStatusRequest, csilapi.EncodeGetImageRolloutStatusResponse, "GetImageRolloutStatusResponse", ui.GetImageRolloutStatus)
	h.ops["ReactorcideUi"]["get-worker-image-state"] = wrapOp(csilapi.DecodeGetWorkerImageStateRequest, csilapi.EncodeGetWorkerImageStateResponse, "GetWorkerImageStateResponse", ui.GetWorkerImageState)
	h.ops["ReactorcideUi"]["list-job-links"] = wrapOp(csilapi.DecodeListJobLinksRequest, csilapi.EncodeListJobLinksResponse, "ListJobLinksResponse", ui.ListJobLinks)
	if worker != nil {
		h.ops["ReactorcideWorker"] = map[string]opFunc{
			"register":            wrapOp(workercsilapi.DecodeRegisterRequest, workercsilapi.EncodeRegisterResponse, "RegisterResponse", worker.Register),
			"request-job":         wrapOp(workercsilapi.DecodeRequestJobRequest, workercsilapi.EncodeRequestJobResponse, "RequestJobResponse", worker.RequestJob),
			"heartbeat":           wrapOp(workercsilapi.DecodeHeartbeatRequest, workercsilapi.EncodeHeartbeatResponse, "HeartbeatResponse", worker.Heartbeat),
			"append-logs":         wrapOp(workercsilapi.DecodeAppendLogsRequest, workercsilapi.EncodeAppendLogsResponse, "AppendLogsResponse", worker.AppendLogs),
			"append-log-batch":    wrapOp(workercsilapi.DecodeAppendLogBatchRequest, workercsilapi.EncodeAppendLogBatchResponse, "AppendLogBatchResponse", worker.AppendLogBatch),
			"append-metric-batch": wrapOp(workercsilapi.DecodeAppendMetricBatchRequest, workercsilapi.EncodeAppendMetricBatchResponse, "AppendMetricBatchResponse", worker.AppendMetricBatch),
			"report-result":       wrapOp(workercsilapi.DecodeReportResultRequest, workercsilapi.EncodeReportResultResponse, "ReportResultResponse", worker.ReportResult),
			"upload-artifact":     wrapOp(workercsilapi.DecodeUploadArtifactRequest, workercsilapi.EncodeUploadArtifactResponse, "UploadArtifactResponse", worker.UploadArtifact),
		}
	}
	return h
}

// wrapOp binds a single operation's codec functions and implementation
// method into an opFunc. Req/Resp are always the generated named record
// types (never inferred), so decode/encode are exactly the generated
// Decode<Type>/Encode<Type> functions and call is exactly the bound
// interface method value (e.g. auth.GetAuthConfig) — their signatures match
// this generic's parameters without any adapter code.
func wrapOp[Req any, Resp any](
	decode func([]byte) (Req, error),
	encode func(Resp) []byte,
	variant string,
	call func(context.Context, Req) (Resp, error),
) opFunc {
	return func(ctx context.Context, payload []byte) (string, []byte, error) {
		req, err := decode(payload)
		if err != nil {
			return "", nil, fmt.Errorf("decode %s request: %w", variant, err)
		}
		resp, err := call(ctx, req)
		if err != nil {
			var svcErr *ServiceErr
			if errors.As(err, &svcErr) {
				return "ServiceError", csilapi.EncodeServiceError(svcErr.ServiceError), nil
			}
			return "", nil, err
		}
		return variant, encode(resp), nil
	}
}

// ServeHTTP implements http.Handler for POST /csil/v1/rpc (envelope-in-body
// profile). The HTTP status is always 200 when a CsilRpcResponse is
// returned, per csil-rpc-transport.md §2.1 — outcome (success, ServiceError,
// or transport failure) rides the envelope's own status/variant/error
// fields, never the HTTP status line.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxEnvelopeBytes+1))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if len(body) > maxEnvelopeBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	req, err := rpctransport.DecodeRpcRequest(body)
	if err != nil {
		h.writeTransportError(w, nil, statusMalformedRequest, err.Error())
		return
	}

	ctx := r.Context()
	if req.Auth != nil {
		ctx = WithAuthToken(ctx, *req.Auth)
	}

	svcOps, ok := h.ops[req.Service]
	if !ok {
		h.writeTransportError(w, req.ID, statusUnknownRoute, fmt.Sprintf("unknown service %q", req.Service))
		return
	}
	op, ok := svcOps[req.Op]
	if !ok {
		h.writeTransportError(w, req.ID, statusUnknownRoute, fmt.Sprintf("unknown op %q on service %q", req.Op, req.Service))
		return
	}

	variant, data, err := op(ctx, req.Payload)
	if err != nil {
		h.writeTransportError(w, req.ID, statusHandlerError, err.Error())
		return
	}

	h.writeResponse(w, rpctransport.NewRpcResponseOk(variant, data).WithID(req.ID))
}

func (h *Handler) writeTransportError(w http.ResponseWriter, id *uint64, status rpctransport.Status, msg string) {
	h.writeResponse(w, rpctransport.NewRpcResponseTransportError(status, msg).WithID(id))
}

func (h *Handler) writeResponse(w http.ResponseWriter, resp rpctransport.RpcResponse) {
	// Encode never actually fails (it only builds and serializes a CBOR
	// value from already-valid Go values), so a returned error here would
	// indicate a bug in the transport module, not a runtime condition to
	// recover from.
	body, err := resp.Encode()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to encode response envelope: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/cbor")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
