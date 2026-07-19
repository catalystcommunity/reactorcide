package uiapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	rpctransport "github.com/catalystcommunity/csilgen/transports/go"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
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
// ReactorcideUi implementations. Pass NewStubAuth()/NewStubUi() to mount a
// handler whose every op returns ServiceError{code:"unimplemented"} until
// the real implementations (Task G) are wired in.
func NewHandler(auth csilapi.ReactorcideAuth, ui csilapi.ReactorcideUi) *Handler {
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
		"list-projects":                 wrapOp(csilapi.DecodeListProjectsRequest, csilapi.EncodeListProjectsResponse, "ListProjectsResponse", ui.ListProjects),
		"get-project":                   wrapOp(csilapi.DecodeGetProjectRequest, csilapi.EncodeGetProjectResponse, "GetProjectResponse", ui.GetProject),
		"create-project":                wrapOp(csilapi.DecodeCreateProjectRequest, csilapi.EncodeCreateProjectResponse, "CreateProjectResponse", ui.CreateProject),
		"update-project":                wrapOp(csilapi.DecodeUpdateProjectRequest, csilapi.EncodeUpdateProjectResponse, "UpdateProjectResponse", ui.UpdateProject),
		"delete-project":                wrapOp(csilapi.DecodeDeleteProjectRequest, csilapi.EncodeDeleteProjectResponse, "DeleteProjectResponse", ui.DeleteProject),
		"list-groups":                   wrapOp(csilapi.DecodeListGroupsRequest, csilapi.EncodeListGroupsResponse, "ListGroupsResponse", ui.ListGroups),
		"create-group":                  wrapOp(csilapi.DecodeCreateGroupRequest, csilapi.EncodeCreateGroupResponse, "CreateGroupResponse", ui.CreateGroup),
		"update-group":                  wrapOp(csilapi.DecodeUpdateGroupRequest, csilapi.EncodeUpdateGroupResponse, "UpdateGroupResponse", ui.UpdateGroup),
		"delete-group":                  wrapOp(csilapi.DecodeDeleteGroupRequest, csilapi.EncodeDeleteGroupResponse, "DeleteGroupResponse", ui.DeleteGroup),
		"add-group-member":              wrapOp(csilapi.DecodeAddGroupMemberRequest, csilapi.EncodeAddGroupMemberResponse, "AddGroupMemberResponse", ui.AddGroupMember),
		"remove-group-member":           wrapOp(csilapi.DecodeRemoveGroupMemberRequest, csilapi.EncodeRemoveGroupMemberResponse, "RemoveGroupMemberResponse", ui.RemoveGroupMember),
		"list-group-members":            wrapOp(csilapi.DecodeListGroupMembersRequest, csilapi.EncodeListGroupMembersResponse, "ListGroupMembersResponse", ui.ListGroupMembers),
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
		"list-trusted-identities":       wrapOp(csilapi.DecodeListTrustedIdentitiesRequest, csilapi.EncodeListTrustedIdentitiesResponse, "ListTrustedIdentitiesResponse", ui.ListTrustedIdentities),
		"add-trusted-identity":          wrapOp(csilapi.DecodeAddTrustedIdentityRequest, csilapi.EncodeAddTrustedIdentityResponse, "AddTrustedIdentityResponse", ui.AddTrustedIdentity),
		"remove-trusted-identity":       wrapOp(csilapi.DecodeRemoveTrustedIdentityRequest, csilapi.EncodeRemoveTrustedIdentityResponse, "RemoveTrustedIdentityResponse", ui.RemoveTrustedIdentity),
		"list-trusted-domain-patterns":  wrapOp(csilapi.DecodeListTrustedDomainPatternsRequest, csilapi.EncodeListTrustedDomainPatternsResponse, "ListTrustedDomainPatternsResponse", ui.ListTrustedDomainPatterns),
		"add-trusted-domain-pattern":    wrapOp(csilapi.DecodeAddTrustedDomainPatternRequest, csilapi.EncodeAddTrustedDomainPatternResponse, "AddTrustedDomainPatternResponse", ui.AddTrustedDomainPattern),
		"remove-trusted-domain-pattern": wrapOp(csilapi.DecodeRemoveTrustedDomainPatternRequest, csilapi.EncodeRemoveTrustedDomainPatternResponse, "RemoveTrustedDomainPatternResponse", ui.RemoveTrustedDomainPattern),
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
