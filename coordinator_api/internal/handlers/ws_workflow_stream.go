package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	storemodels "github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/gorilla/websocket"
)

// StreamWorkflow streams the live events of one workflow (workflow_update,
// workflow_node_update, and job events of its jobs) to a pull consumer:
// GET /api/v1/workflows/stream/{workflow_id} (WebSocket).
//
// A service, instance, or user token needs workflows:read for the
// organization of the workflow; a service principal token intersects it
// with its roles. A job token is refused. The first frame is the current
// workflow status. The stream is best effort (LISTEN/NOTIFY); a consumer
// that must not miss an event subscribes to outbound events instead.
func (h *WSHandler) StreamWorkflow(w http.ResponseWriter, r *http.Request) {
	workflowID := GetIDFromContext(r, "workflow_id")
	if workflowID == "" {
		http.Error(w, "missing workflow id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	principal := checkauth.GetPrincipalFromContext(ctx)
	user := checkauth.GetUserFromContext(ctx)
	if principal == nil && user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if principal != nil && principal.CredentialType == "job_token" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	workflows, ok := h.store.(interface {
		GetWorkflowInstance(context.Context, string) (*storemodels.WorkflowInstance, error)
	})
	if !ok {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	wf, err := workflows.GetWorkflowInstance(ctx, workflowID)
	if err != nil || wf == nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	allowed := false
	if principal != nil {
		allowed = principal.HasCapability(tokencaps.WorkflowsRead) &&
			principalAllowsOrg(ctx, h.store, wf.OwnershipOrgID(), tokencaps.WorkflowsRead)
	} else if h.visibility != nil {
		ok, viewErr := h.visibility.CanViewWorkflowInstance(ctx, authz.IdentityFromUser(user), wf)
		allowed = viewErr == nil && ok
	}
	if !allowed {
		// Not found, not forbidden: no existence oracle.
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.WithError(err).Warn("WS upgrade failed")
		return
	}
	defer ws.Close()

	sub := h.bus.Subscribe(func(evt pubsub.Event) bool { return evt.WorkflowID == workflowID })
	defer h.bus.Unsubscribe(sub)

	initial := pubsub.Event{Type: pubsub.EventWorkflowUpdate, WorkflowID: wf.WorkflowID, Status: wf.Status,
		UpdatedAt: wf.UpdatedAt.UTC().Format(time.RFC3339Nano), OwnerUserID: wf.OwnershipOrgID()}
	if wf.ProjectID != nil {
		initial.ProjectID = *wf.ProjectID
	}
	if payload, err := pubsub.EncodeEvent(initial); err == nil {
		ws.SetWriteDeadline(time.Now().Add(wsWriteWait))
		_ = ws.WriteMessage(websocket.TextMessage, payload)
	}
	h.runStream(ctx, ws, sub, "")
}
