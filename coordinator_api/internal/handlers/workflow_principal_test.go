package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

type principalWorkflowStore struct {
	*MockStore
	summary *models.WorkflowSummary
}

func (s *principalWorkflowStore) ListWorkflowSummaries(context.Context, map[string]interface{}, int, int) ([]models.WorkflowSummary, error) {
	return []models.WorkflowSummary{*s.summary}, nil
}

func (s *principalWorkflowStore) GetWorkflowSummary(context.Context, string) (*models.WorkflowSummary, error) {
	copy := *s.summary
	return &copy, nil
}

func workflowPrincipalRequest(principal *checkauth.Principal) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/workflow-1", nil)
	ctx := context.WithValue(req.Context(), GetContextKey("workflow_id"), "workflow-1")
	if principal != nil {
		ctx = checkauth.SetPrincipalContext(ctx, principal)
	}
	return req.WithContext(ctx)
}

func TestWorkflowHandlerGetWorkflowAllowsUserlessTokenPrincipals(t *testing.T) {
	tests := []struct {
		name      string
		principal *checkauth.Principal
		wantCode  int
	}{
		{
			name: "instance token with global authority",
			principal: &checkauth.Principal{CredentialType: "instance_token", AllOrganizations: true,
				AllCapabilities: true},
			wantCode: http.StatusOK,
		},
		{
			name: "service token with matching organization",
			principal: &checkauth.Principal{CredentialType: "service_token", OrganizationIDs: []string{"org-1"},
				Capabilities: tokencaps.Set{tokencaps.WorkflowsRead: {}}},
			wantCode: http.StatusOK,
		},
		{
			name: "service token for another organization",
			principal: &checkauth.Principal{CredentialType: "service_token", OrganizationIDs: []string{"org-2"},
				Capabilities: tokencaps.Set{tokencaps.WorkflowsRead: {}}},
			wantCode: http.StatusForbidden,
		},
		{
			name: "service token without workflow read",
			principal: &checkauth.Principal{CredentialType: "service_token", OrganizationIDs: []string{"org-1"},
				Capabilities: tokencaps.Set{tokencaps.JobsRead: {}}},
			wantCode: http.StatusForbidden,
		},
		{
			name: "job token",
			principal: &checkauth.Principal{CredentialType: "job_token", OrganizationIDs: []string{"org-1"},
				Capabilities: tokencaps.Set{tokencaps.WorkflowsRead: {}}},
			wantCode: http.StatusForbidden,
		},
		{
			name: "user token without a resolved user",
			principal: &checkauth.Principal{CredentialType: "user_token", OrganizationIDs: []string{"org-1"},
				Capabilities: tokencaps.Set{tokencaps.WorkflowsRead: {}}},
			wantCode: http.StatusForbidden,
		},
		{name: "anonymous", wantCode: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &principalWorkflowStore{MockStore: &MockStore{}, summary: &models.WorkflowSummary{
				WorkflowID: "workflow-1", OrgID: "org-1", Status: "success",
			}}
			handler := NewWorkflowHandler(store)
			recorder := httptest.NewRecorder()
			handler.GetWorkflow(recorder, workflowPrincipalRequest(tt.principal))
			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
		})
	}
}

func TestWorkflowHandlerControlUsesOrganizationAndCapability(t *testing.T) {
	handler := NewWorkflowHandler(&MockStore{})
	workflow := &models.WorkflowInstance{WorkflowID: "workflow-1", OrgID: "org-1"}

	tests := []struct {
		name      string
		principal *checkauth.Principal
		want      bool
	}{
		{
			name: "instance token",
			principal: &checkauth.Principal{CredentialType: "instance_token", AllOrganizations: true,
				AllCapabilities: true},
			want: true,
		},
		{
			name: "scoped service token",
			principal: &checkauth.Principal{CredentialType: "service_token", OwnerOrgID: "org-1",
				Capabilities: tokencaps.Set{tokencaps.WorkflowsControl: {}}},
			want: true,
		},
		{
			name: "missing control capability",
			principal: &checkauth.Principal{CredentialType: "service_token", OwnerOrgID: "org-1",
				Capabilities: tokencaps.Set{tokencaps.WorkflowsRead: {}}},
		},
		{
			name: "wrong organization",
			principal: &checkauth.Principal{CredentialType: "service_token", OwnerOrgID: "org-2",
				Capabilities: tokencaps.Set{tokencaps.WorkflowsControl: {}}},
		},
		{
			name: "job token",
			principal: &checkauth.Principal{CredentialType: "job_token", OwnerOrgID: "org-1",
				Capabilities: tokencaps.Set{tokencaps.WorkflowsControl: {}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			ctx = checkauth.SetPrincipalContext(ctx, tt.principal)
			if got := handler.canSubjectControlWorkflow(ctx, nil, workflow); got != tt.want {
				t.Fatalf("canSubjectControlWorkflow() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWorkflowSummaryFromLooseJobKeepsOrganization(t *testing.T) {
	summary := workflowSummaryFromLooseJob(&models.Job{JobID: "job-1", OrgID: "org-1", UserID: "legacy-user"})
	if summary.OrgID != "org-1" {
		t.Fatalf("OrgID = %q, want org-1", summary.OrgID)
	}
}
