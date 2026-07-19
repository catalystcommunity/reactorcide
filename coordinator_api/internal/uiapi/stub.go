// Package uiapi implements the coordinator-side CSIL-RPC dispatcher for the
// ReactorcideAuth and ReactorcideUi services (coordinator_api/csil/reactorcide-ui.csil).
//
// StubAuth and StubUi are placeholder implementations of the generated
// csilapi.ReactorcideAuth / csilapi.ReactorcideUi interfaces: every op
// returns ServiceError{code:"unimplemented"}. They let the coordinator mount
// POST /csil/v1/rpc and build/serve before Task G lands the real
// implementations against auth/authz/store/rotation/cancel primitives.
package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// StubAuth is a ReactorcideAuth implementation where every op returns
// ServiceError{code:"unimplemented"}.
type StubAuth struct{}

// NewStubAuth returns a StubAuth.
func NewStubAuth() *StubAuth { return &StubAuth{} }

var _ csilapi.ReactorcideAuth = (*StubAuth)(nil)

func (StubAuth) GetAuthConfig(ctx context.Context, req csilapi.GetAuthConfigRequest) (csilapi.GetAuthConfigResponse, error) {
	return csilapi.GetAuthConfigResponse{}, ErrUnimplemented("ReactorcideAuth/get-auth-config")
}

func (StubAuth) BeginLogin(ctx context.Context, req csilapi.BeginLoginRequest) (csilapi.BeginLoginResponse, error) {
	return csilapi.BeginLoginResponse{}, ErrUnimplemented("ReactorcideAuth/begin-login")
}

func (StubAuth) CompleteLogin(ctx context.Context, req csilapi.CompleteLoginRequest) (csilapi.CompleteLoginResponse, error) {
	return csilapi.CompleteLoginResponse{}, ErrUnimplemented("ReactorcideAuth/complete-login")
}

func (StubAuth) Authenticate(ctx context.Context, req csilapi.AuthenticateRequest) (csilapi.AuthenticateResponse, error) {
	return csilapi.AuthenticateResponse{}, ErrUnimplemented("ReactorcideAuth/authenticate")
}

func (StubAuth) Logout(ctx context.Context, req csilapi.LogoutRequest) (csilapi.LogoutResponse, error) {
	return csilapi.LogoutResponse{}, ErrUnimplemented("ReactorcideAuth/logout")
}

func (StubAuth) BootstrapAdmin(ctx context.Context, req csilapi.BootstrapAdminRequest) (csilapi.BootstrapAdminResponse, error) {
	return csilapi.BootstrapAdminResponse{}, ErrUnimplemented("ReactorcideAuth/bootstrap-admin")
}

// StubUi is a ReactorcideUi implementation where every op returns
// ServiceError{code:"unimplemented"}.
type StubUi struct{}

// NewStubUi returns a StubUi.
func NewStubUi() *StubUi { return &StubUi{} }

var _ csilapi.ReactorcideUi = (*StubUi)(nil)

func (StubUi) GetCapabilities(ctx context.Context, req csilapi.GetCapabilitiesRequest) (csilapi.GetCapabilitiesResponse, error) {
	return csilapi.GetCapabilitiesResponse{}, ErrUnimplemented("ReactorcideUi/get-capabilities")
}

func (StubUi) ListOrgs(ctx context.Context, req csilapi.ListOrgsRequest) (csilapi.ListOrgsResponse, error) {
	return csilapi.ListOrgsResponse{}, ErrUnimplemented("ReactorcideUi/list-orgs")
}

func (StubUi) ListProjects(ctx context.Context, req csilapi.ListProjectsRequest) (csilapi.ListProjectsResponse, error) {
	return csilapi.ListProjectsResponse{}, ErrUnimplemented("ReactorcideUi/list-projects")
}

func (StubUi) GetProject(ctx context.Context, req csilapi.GetProjectRequest) (csilapi.GetProjectResponse, error) {
	return csilapi.GetProjectResponse{}, ErrUnimplemented("ReactorcideUi/get-project")
}

func (StubUi) CreateProject(ctx context.Context, req csilapi.CreateProjectRequest) (csilapi.CreateProjectResponse, error) {
	return csilapi.CreateProjectResponse{}, ErrUnimplemented("ReactorcideUi/create-project")
}

func (StubUi) UpdateProject(ctx context.Context, req csilapi.UpdateProjectRequest) (csilapi.UpdateProjectResponse, error) {
	return csilapi.UpdateProjectResponse{}, ErrUnimplemented("ReactorcideUi/update-project")
}

func (StubUi) DeleteProject(ctx context.Context, req csilapi.DeleteProjectRequest) (csilapi.DeleteProjectResponse, error) {
	return csilapi.DeleteProjectResponse{}, ErrUnimplemented("ReactorcideUi/delete-project")
}

func (StubUi) ListGroups(ctx context.Context, req csilapi.ListGroupsRequest) (csilapi.ListGroupsResponse, error) {
	return csilapi.ListGroupsResponse{}, ErrUnimplemented("ReactorcideUi/list-groups")
}

func (StubUi) CreateGroup(ctx context.Context, req csilapi.CreateGroupRequest) (csilapi.CreateGroupResponse, error) {
	return csilapi.CreateGroupResponse{}, ErrUnimplemented("ReactorcideUi/create-group")
}

func (StubUi) UpdateGroup(ctx context.Context, req csilapi.UpdateGroupRequest) (csilapi.UpdateGroupResponse, error) {
	return csilapi.UpdateGroupResponse{}, ErrUnimplemented("ReactorcideUi/update-group")
}

func (StubUi) DeleteGroup(ctx context.Context, req csilapi.DeleteGroupRequest) (csilapi.DeleteGroupResponse, error) {
	return csilapi.DeleteGroupResponse{}, ErrUnimplemented("ReactorcideUi/delete-group")
}

func (StubUi) AddGroupMember(ctx context.Context, req csilapi.AddGroupMemberRequest) (csilapi.AddGroupMemberResponse, error) {
	return csilapi.AddGroupMemberResponse{}, ErrUnimplemented("ReactorcideUi/add-group-member")
}

func (StubUi) RemoveGroupMember(ctx context.Context, req csilapi.RemoveGroupMemberRequest) (csilapi.RemoveGroupMemberResponse, error) {
	return csilapi.RemoveGroupMemberResponse{}, ErrUnimplemented("ReactorcideUi/remove-group-member")
}

func (StubUi) ListGroupMembers(ctx context.Context, req csilapi.ListGroupMembersRequest) (csilapi.ListGroupMembersResponse, error) {
	return csilapi.ListGroupMembersResponse{}, ErrUnimplemented("ReactorcideUi/list-group-members")
}

func (StubUi) ListRoleAssignments(ctx context.Context, req csilapi.ListRoleAssignmentsRequest) (csilapi.ListRoleAssignmentsResponse, error) {
	return csilapi.ListRoleAssignmentsResponse{}, ErrUnimplemented("ReactorcideUi/list-role-assignments")
}

func (StubUi) AssignRole(ctx context.Context, req csilapi.AssignRoleRequest) (csilapi.AssignRoleResponse, error) {
	return csilapi.AssignRoleResponse{}, ErrUnimplemented("ReactorcideUi/assign-role")
}

func (StubUi) RevokeRole(ctx context.Context, req csilapi.RevokeRoleRequest) (csilapi.RevokeRoleResponse, error) {
	return csilapi.RevokeRoleResponse{}, ErrUnimplemented("ReactorcideUi/revoke-role")
}

func (StubUi) ListWebhookSecrets(ctx context.Context, req csilapi.ListWebhookSecretsRequest) (csilapi.ListWebhookSecretsResponse, error) {
	return csilapi.ListWebhookSecretsResponse{}, ErrUnimplemented("ReactorcideUi/list-webhook-secrets")
}

func (StubUi) AddWebhookSecret(ctx context.Context, req csilapi.AddWebhookSecretRequest) (csilapi.AddWebhookSecretResponse, error) {
	return csilapi.AddWebhookSecretResponse{}, ErrUnimplemented("ReactorcideUi/add-webhook-secret")
}

func (StubUi) DeactivateWebhookSecret(ctx context.Context, req csilapi.DeactivateWebhookSecretRequest) (csilapi.DeactivateWebhookSecretResponse, error) {
	return csilapi.DeactivateWebhookSecretResponse{}, ErrUnimplemented("ReactorcideUi/deactivate-webhook-secret")
}

func (StubUi) DeleteWebhookSecret(ctx context.Context, req csilapi.DeleteWebhookSecretRequest) (csilapi.DeleteWebhookSecretResponse, error) {
	return csilapi.DeleteWebhookSecretResponse{}, ErrUnimplemented("ReactorcideUi/delete-webhook-secret")
}

func (StubUi) ListVcsCredentials(ctx context.Context, req csilapi.ListVcsCredentialsRequest) (csilapi.ListVcsCredentialsResponse, error) {
	return csilapi.ListVcsCredentialsResponse{}, ErrUnimplemented("ReactorcideUi/list-vcs-credentials")
}

func (StubUi) AddVcsCredential(ctx context.Context, req csilapi.AddVcsCredentialRequest) (csilapi.AddVcsCredentialResponse, error) {
	return csilapi.AddVcsCredentialResponse{}, ErrUnimplemented("ReactorcideUi/add-vcs-credential")
}

func (StubUi) DeactivateVcsCredential(ctx context.Context, req csilapi.DeactivateVcsCredentialRequest) (csilapi.DeactivateVcsCredentialResponse, error) {
	return csilapi.DeactivateVcsCredentialResponse{}, ErrUnimplemented("ReactorcideUi/deactivate-vcs-credential")
}

func (StubUi) DeleteVcsCredential(ctx context.Context, req csilapi.DeleteVcsCredentialRequest) (csilapi.DeleteVcsCredentialResponse, error) {
	return csilapi.DeleteVcsCredentialResponse{}, ErrUnimplemented("ReactorcideUi/delete-vcs-credential")
}

func (StubUi) SetSecret(ctx context.Context, req csilapi.SetSecretRequest) (csilapi.SetSecretResponse, error) {
	return csilapi.SetSecretResponse{}, ErrUnimplemented("ReactorcideUi/set-secret")
}

func (StubUi) DeleteSecret(ctx context.Context, req csilapi.DeleteSecretRequest) (csilapi.DeleteSecretResponse, error) {
	return csilapi.DeleteSecretResponse{}, ErrUnimplemented("ReactorcideUi/delete-secret")
}

func (StubUi) ListSecretPaths(ctx context.Context, req csilapi.ListSecretPathsRequest) (csilapi.ListSecretPathsResponse, error) {
	return csilapi.ListSecretPathsResponse{}, ErrUnimplemented("ReactorcideUi/list-secret-paths")
}

func (StubUi) ListSecretGrants(ctx context.Context, req csilapi.ListSecretGrantsRequest) (csilapi.ListSecretGrantsResponse, error) {
	return csilapi.ListSecretGrantsResponse{}, ErrUnimplemented("ReactorcideUi/list-secret-grants")
}

func (StubUi) CreateSecretGrant(ctx context.Context, req csilapi.CreateSecretGrantRequest) (csilapi.CreateSecretGrantResponse, error) {
	return csilapi.CreateSecretGrantResponse{}, ErrUnimplemented("ReactorcideUi/create-secret-grant")
}

func (StubUi) UpdateSecretGrant(ctx context.Context, req csilapi.UpdateSecretGrantRequest) (csilapi.UpdateSecretGrantResponse, error) {
	return csilapi.UpdateSecretGrantResponse{}, ErrUnimplemented("ReactorcideUi/update-secret-grant")
}

func (StubUi) DeleteSecretGrant(ctx context.Context, req csilapi.DeleteSecretGrantRequest) (csilapi.DeleteSecretGrantResponse, error) {
	return csilapi.DeleteSecretGrantResponse{}, ErrUnimplemented("ReactorcideUi/delete-secret-grant")
}

func (StubUi) CancelJob(ctx context.Context, req csilapi.CancelJobRequest) (csilapi.CancelJobResponse, error) {
	return csilapi.CancelJobResponse{}, ErrUnimplemented("ReactorcideUi/cancel-job")
}

func (StubUi) KillJob(ctx context.Context, req csilapi.KillJobRequest) (csilapi.KillJobResponse, error) {
	return csilapi.KillJobResponse{}, ErrUnimplemented("ReactorcideUi/kill-job")
}

func (StubUi) CancelWorkflow(ctx context.Context, req csilapi.CancelWorkflowRequest) (csilapi.CancelWorkflowResponse, error) {
	return csilapi.CancelWorkflowResponse{}, ErrUnimplemented("ReactorcideUi/cancel-workflow")
}

func (StubUi) RetryJob(ctx context.Context, req csilapi.RetryJobRequest) (csilapi.RetryJobResponse, error) {
	return csilapi.RetryJobResponse{}, ErrUnimplemented("ReactorcideUi/retry-job")
}

func (StubUi) RetryWorkflow(ctx context.Context, req csilapi.RetryWorkflowRequest) (csilapi.RetryWorkflowResponse, error) {
	return csilapi.RetryWorkflowResponse{}, ErrUnimplemented("ReactorcideUi/retry-workflow")
}

func (StubUi) RetryUnsuccessfulJobs(ctx context.Context, req csilapi.RetryUnsuccessfulJobsRequest) (csilapi.RetryUnsuccessfulJobsResponse, error) {
	return csilapi.RetryUnsuccessfulJobsResponse{}, ErrUnimplemented("ReactorcideUi/retry-unsuccessful-jobs")
}

func (StubUi) GetGlobalSettings(ctx context.Context, req csilapi.GetGlobalSettingsRequest) (csilapi.GetGlobalSettingsResponse, error) {
	return csilapi.GetGlobalSettingsResponse{}, ErrUnimplemented("ReactorcideUi/get-global-settings")
}

func (StubUi) UpdateGlobalSettings(ctx context.Context, req csilapi.UpdateGlobalSettingsRequest) (csilapi.UpdateGlobalSettingsResponse, error) {
	return csilapi.UpdateGlobalSettingsResponse{}, ErrUnimplemented("ReactorcideUi/update-global-settings")
}

func (StubUi) ListTrustedIdentities(ctx context.Context, req csilapi.ListTrustedIdentitiesRequest) (csilapi.ListTrustedIdentitiesResponse, error) {
	return csilapi.ListTrustedIdentitiesResponse{}, ErrUnimplemented("ReactorcideUi/list-trusted-identities")
}

func (StubUi) AddTrustedIdentity(ctx context.Context, req csilapi.AddTrustedIdentityRequest) (csilapi.AddTrustedIdentityResponse, error) {
	return csilapi.AddTrustedIdentityResponse{}, ErrUnimplemented("ReactorcideUi/add-trusted-identity")
}

func (StubUi) RemoveTrustedIdentity(ctx context.Context, req csilapi.RemoveTrustedIdentityRequest) (csilapi.RemoveTrustedIdentityResponse, error) {
	return csilapi.RemoveTrustedIdentityResponse{}, ErrUnimplemented("ReactorcideUi/remove-trusted-identity")
}

func (StubUi) ListTrustedDomainPatterns(ctx context.Context, req csilapi.ListTrustedDomainPatternsRequest) (csilapi.ListTrustedDomainPatternsResponse, error) {
	return csilapi.ListTrustedDomainPatternsResponse{}, ErrUnimplemented("ReactorcideUi/list-trusted-domain-patterns")
}

func (StubUi) AddTrustedDomainPattern(ctx context.Context, req csilapi.AddTrustedDomainPatternRequest) (csilapi.AddTrustedDomainPatternResponse, error) {
	return csilapi.AddTrustedDomainPatternResponse{}, ErrUnimplemented("ReactorcideUi/add-trusted-domain-pattern")
}

func (StubUi) RemoveTrustedDomainPattern(ctx context.Context, req csilapi.RemoveTrustedDomainPatternRequest) (csilapi.RemoveTrustedDomainPatternResponse, error) {
	return csilapi.RemoveTrustedDomainPatternResponse{}, ErrUnimplemented("ReactorcideUi/remove-trusted-domain-pattern")
}
