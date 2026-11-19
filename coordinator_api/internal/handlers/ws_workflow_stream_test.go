package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/gorilla/websocket"
)

type workflowStreamStore struct {
	MockStore
	wf *models.WorkflowInstance
}

func (s *workflowStreamStore) GetWorkflowInstance(_ context.Context, id string) (*models.WorkflowInstance, error) {
	if s.wf != nil && s.wf.WorkflowID == id {
		return s.wf, nil
	}
	return nil, store.ErrNotFound
}

func streamWorkflowRequest(handler *WSHandler, principal *checkauth.Principal, workflowID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/stream/"+workflowID, nil)
	ctx := setIDContext(checkauth.SetPrincipalContext(req.Context(), principal), "workflow_id", workflowID)
	recorder := httptest.NewRecorder()
	handler.StreamWorkflow(recorder, req.WithContext(ctx))
	return recorder
}

func TestStreamWorkflowRefusesJobTokensAndOtherOrgs(t *testing.T) {
	bus := pubsub.NewBus(nil, 1)
	t.Cleanup(bus.Close)
	handler := NewWSHandler(bus, &workflowStreamStore{wf: &models.WorkflowInstance{WorkflowID: "wf-1", OrgID: "org-1", Status: "running"}})

	job := &checkauth.Principal{CredentialType: "job_token", OrganizationIDs: []string{"org-1"}, AllCapabilities: true}
	if code := streamWorkflowRequest(handler, job, "wf-1").Code; code != http.StatusForbidden {
		t.Fatalf("job token: status = %d, want 403", code)
	}
	other := &checkauth.Principal{CredentialType: "service_token", OrganizationIDs: []string{"org-2"},
		Capabilities: tokencaps.Set{tokencaps.WorkflowsRead: {}}}
	if code := streamWorkflowRequest(handler, other, "wf-1").Code; code != http.StatusNotFound {
		t.Fatalf("other org: status = %d, want 404", code)
	}
	noCap := &checkauth.Principal{CredentialType: "service_token", OrganizationIDs: []string{"org-1"},
		Capabilities: tokencaps.Set{tokencaps.JobsRead: {}}}
	if code := streamWorkflowRequest(handler, noCap, "wf-1").Code; code != http.StatusNotFound {
		t.Fatalf("no workflows:read: status = %d, want 404", code)
	}
}

func TestStreamWorkflowSendsTheCurrentStatusToAServiceToken(t *testing.T) {
	bus := pubsub.NewBus(nil, 4)
	t.Cleanup(bus.Close)
	handler := NewWSHandler(bus, &workflowStreamStore{wf: &models.WorkflowInstance{WorkflowID: "wf-1", OrgID: "org-1", Status: "running"}})
	principal := &checkauth.Principal{CredentialType: "service_token", OrganizationIDs: []string{"org-1"},
		Capabilities: tokencaps.Set{tokencaps.WorkflowsRead: {}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := setIDContext(checkauth.SetPrincipalContext(r.Context(), principal), "workflow_id", "wf-1")
		handler.StreamWorkflow(w, r.WithContext(ctx))
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_, first, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(first), `"workflow_id":"wf-1"`) || !strings.Contains(string(first), `"running"`) {
		t.Fatalf("first frame = %s, err %v", first, err)
	}
}
