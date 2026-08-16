package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/gorilla/websocket"
)

func TestWSHandlerAllowsUserlessInstanceToken(t *testing.T) {
	bus := pubsub.NewBus(nil, 1)
	t.Cleanup(bus.Close)
	handler := NewWSHandler(bus, &MockStore{})
	principal := &checkauth.Principal{CredentialType: "instance_token", AllOrganizations: true, AllCapabilities: true}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := checkauth.SetPrincipalContext(r.Context(), principal)
		handler.StreamAllJobs(w, r.WithContext(ctx))
	}))
	t.Cleanup(server.Close)

	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("WebSocket dial failed with status %d: %v", status, err)
	}
	conn.Close()
}

func TestWSHandlerRejectsTokenWithoutJobsRead(t *testing.T) {
	bus := pubsub.NewBus(nil, 1)
	t.Cleanup(bus.Close)
	handler := NewWSHandler(bus, &MockStore{})
	principal := &checkauth.Principal{CredentialType: "service_token", OwnerOrgID: "org-1",
		Capabilities: tokencaps.Set{tokencaps.WorkflowsRead: {}}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stream", nil)
	req = req.WithContext(checkauth.SetPrincipalContext(req.Context(), principal))
	recorder := httptest.NewRecorder()

	handler.StreamAllJobs(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestWSHandlerTokenVisibilityUsesOrganizationScope(t *testing.T) {
	bus := pubsub.NewBus(nil, 1)
	t.Cleanup(bus.Close)
	handler := NewWSHandler(bus, &MockStore{})
	job := &models.Job{JobID: "job-1", OrgID: "org-1"}

	tests := []struct {
		name      string
		principal *checkauth.Principal
		want      bool
	}{
		{
			name: "matching service token",
			principal: &checkauth.Principal{CredentialType: "service_token", OrganizationIDs: []string{"org-1"},
				Capabilities: tokencaps.Set{tokencaps.JobsRead: {}}},
			want: true,
		},
		{
			name: "wrong organization",
			principal: &checkauth.Principal{CredentialType: "service_token", OrganizationIDs: []string{"org-2"},
				Capabilities: tokencaps.Set{tokencaps.JobsRead: {}}},
		},
		{
			name: "missing capability",
			principal: &checkauth.Principal{CredentialType: "service_token", OrganizationIDs: []string{"org-1"},
				Capabilities: tokencaps.Set{tokencaps.WorkflowsRead: {}}},
		},
		{
			name: "job token",
			principal: &checkauth.Principal{CredentialType: "job_token", OrganizationIDs: []string{"org-1"},
				Capabilities: tokencaps.Set{tokencaps.JobsRead: {}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := checkauth.SetPrincipalContext(context.Background(), tt.principal)
			if got := handler.canViewJob(ctx, job); got != tt.want {
				t.Fatalf("canViewJob() = %v, want %v", got, tt.want)
			}
		})
	}
}
