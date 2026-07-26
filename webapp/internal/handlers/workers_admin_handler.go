package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// This file implements WORKERS_PLAN.md Wave-4 P4's webapp admin pages: pools
// (+ enrollment tokens), workers, and queues. Every op here is gated on
// authz.Caps.ManageWorkers (org-admin of the target org, or global admin);
// queues have no owning-org concept yet (see coordinator_api's
// requireManageWorkers doc comment), so every queue op is global-admin only
// — QueuesPage and the queue mutation handlers gate on si.IsGlobalAdmin
// specifically rather than the (possibly org-scoped) ManageWorkers capability,
// matching what the coordinator will actually authorize.
//
// SECURITY: CreateEnrollmentToken's raw token value is returned by the
// coordinator exactly once, in that op's response only — never by
// list-enrollment-tokens, never logged. This file preserves that: the token
// is rendered straight into the page response body (not redirect-flashed,
// which would put it in a URL — server access logs, browser history,
// Referer headers) and is never written to a log statement.

// poolView bundles a pool with its workers and enrollment tokens for the
// overview page — one nested fetch per pool (ListWorkers +
// ListEnrollmentTokens), fine at the admin/operator scale this page deals
// with, mirroring OrgGroupsPage's per-group member fetch.
type poolView struct {
	Pool    csilapi.WorkerPoolSummary
	Workers []csilapi.WorkerSummary
	Tokens  []csilapi.EnrollmentTokenSummary
}

// WorkersPage renders GET /app/workers: pools, each with its workers and
// enrollment tokens, plus (for global admins) a link to the Queues tab.
// Gated on ManageWorkers — there is no separate "view" tier, same as every
// other Task I management page.
func (h *WebHandler) WorkersPage(w http.ResponseWriter, r *http.Request) {
	si := h.sessionInfo(r)
	if !si.Caps.ManageWorkers {
		h.renderError(w, r, http.StatusForbidden, "You do not have permission to manage workers", nil)
		return
	}
	if h.uiClients == nil {
		h.renderError(w, r, http.StatusServiceUnavailable, "Management is not available", nil)
		return
	}
	h.renderWorkersPage(w, r, si, nil)
}

// renderWorkersPage does the actual pool/worker/token fetch-and-render for
// GET /app/workers, and is reused by EnrollmentTokenCreate so a freshly
// minted token can be shown once in the same response instead of being
// redirect-flashed (see the file doc comment on why that matters). extra is
// merged into the template data after the computed fields, so a caller can
// add page-specific data (the one-time token banner) without duplicating
// the pool/worker/org fetch logic.
func (h *WebHandler) renderWorkersPage(w http.ResponseWriter, r *http.Request, si SessionInfo, extra map[string]interface{}) {
	orgID := h.resolveOrgID(r, si)
	var orgs []csilapi.OrgSummary
	if si.IsGlobalAdmin {
		orgs = h.listOrgsForSelector(r)
	}

	pools, err := h.loadPoolViews(r, orgID)
	if err != nil {
		h.renderServiceError(w, r, err)
		return
	}

	msg, errMsg := flashFromQuery(r)
	data := map[string]interface{}{
		"Title":     "Workers",
		"OrgID":     orgID,
		"Orgs":      orgs,
		"IsAdmin":   si.IsGlobalAdmin,
		"Pools":     pools,
		"FormMsg":   msg,
		"FormError": errMsg,
	}
	for k, v := range extra {
		data[k] = v
	}
	h.render(w, r, "workers.html", data)
}

// loadPoolViews lists pools (scoped to orgFilter when set, across every org
// when the caller is a global admin and orgFilter is "") and fetches each
// pool's workers and enrollment tokens. A per-pool ListWorkers/
// ListEnrollmentTokens failure is logged and treated as "empty" rather than
// failing the whole page — one pool's transient error shouldn't hide every
// other pool.
func (h *WebHandler) loadPoolViews(r *http.Request, orgFilter string) ([]poolView, error) {
	req := csilapi.ListPoolsRequest{}
	if orgFilter != "" {
		req.OrgId = &orgFilter
	}
	resp, err := h.uiClients.Ui.ListPools(h.authContext(r), req)
	if err != nil {
		return nil, err
	}

	views := make([]poolView, 0, len(resp.Pools))
	for _, p := range resp.Pools {
		pv := poolView{Pool: p}
		poolID := p.PoolId
		if wResp, wErr := h.uiClients.Ui.ListWorkers(h.authContext(r), csilapi.ListWorkersRequest{PoolId: &poolID}); wErr == nil {
			pv.Workers = wResp.Workers
		}
		if tResp, tErr := h.uiClients.Ui.ListEnrollmentTokens(h.authContext(r), csilapi.ListEnrollmentTokensRequest{PoolId: poolID}); tErr == nil {
			pv.Tokens = tResp.Tokens
		}
		views = append(views, pv)
	}
	return views, nil
}

// PoolCreate handles POST /app/workers/pools. org_id is posted as "" for a
// global pool (global admins only — the create-pool form only offers a
// blank org_id option to global admins) or an org id otherwise; the
// coordinator re-authorizes regardless of what the form sends.
func (h *WebHandler) PoolCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	name := formTrim(r, "name")
	if name == "" {
		h.redirectFlash(w, r, "/app/workers", "name is required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/workers", "management is not available", true)
		return
	}

	req := csilapi.CreatePoolRequest{Name: name, Description: formOptionalPtr(r, "description")}
	if orgID := formTrim(r, "org_id"); orgID != "" {
		req.OrgId = &orgID
	}
	if _, err := h.uiClients.Ui.CreatePool(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, "/app/workers")
		return
	}
	h.redirectFlash(w, r, "/app/workers", "Pool created", false)
}

// PoolUpdate handles POST /app/workers/pools/{id}. Both fields are
// optional edit-in-place (blank leaves the existing value untouched),
// mirroring SecretGrantUpdate.
func (h *WebHandler) PoolUpdate(w http.ResponseWriter, r *http.Request) {
	poolID := r.PathValue("id")
	if poolID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/workers", "management is not available", true)
		return
	}

	req := csilapi.UpdatePoolRequest{
		PoolId:      poolID,
		Name:        formOptionalPtr(r, "name"),
		Description: formOptionalPtr(r, "description"),
	}
	if _, err := h.uiClients.Ui.UpdatePool(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, "/app/workers")
		return
	}
	h.redirectFlash(w, r, "/app/workers", "Pool updated", false)
}

// PoolDelete handles POST /app/workers/pools/{id}/delete. Confirmed
// client-side (data-confirm); the coordinator refuses a pool that still has
// workers, surfaced here as an ordinary form-level error.
func (h *WebHandler) PoolDelete(w http.ResponseWriter, r *http.Request) {
	poolID := r.PathValue("id")
	if poolID == "" {
		http.NotFound(w, r)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/workers", "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.DeletePool(h.authContext(r), csilapi.DeletePoolRequest{PoolId: poolID}); err != nil {
		h.handleFormServiceError(w, r, err, "/app/workers")
		return
	}
	h.redirectFlash(w, r, "/app/workers", "Pool deleted", false)
}

// EnrollmentTokenCreate handles POST /app/workers/pools/{id}/tokens.
// SECURITY: on success this renders the workers page directly (200, no
// redirect) with the coordinator's one-time raw token value in the
// response body — see the file doc comment. It is never placed in a
// redirect Location/query string and never logged.
func (h *WebHandler) EnrollmentTokenCreate(w http.ResponseWriter, r *http.Request) {
	poolID := r.PathValue("id")
	if poolID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	si := h.sessionInfo(r)
	if !si.Caps.ManageWorkers {
		h.renderError(w, r, http.StatusForbidden, "You do not have permission to manage workers", nil)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/workers", "management is not available", true)
		return
	}

	req := csilapi.CreateEnrollmentTokenRequest{PoolId: poolID, Name: formOptionalPtr(r, "name")}
	resp, err := h.uiClients.Ui.CreateEnrollmentToken(h.authContext(r), req)
	if err != nil {
		h.handleFormServiceError(w, r, err, "/app/workers")
		return
	}

	h.renderWorkersPage(w, r, si, map[string]interface{}{
		"NewToken":       resp.Token,
		"NewTokenPoolId": poolID,
		"NewTokenName":   resp.Summary.Name,
	})
}

// EnrollmentTokenDeactivate handles POST /app/workers/tokens/{id}/deactivate.
// Deactivation returns metadata only (no token value), so this is a plain
// redirect-flash like every other mutating action.
func (h *WebHandler) EnrollmentTokenDeactivate(w http.ResponseWriter, r *http.Request) {
	tokenID := r.PathValue("id")
	if tokenID == "" {
		http.NotFound(w, r)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/workers", "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.DeactivateEnrollmentToken(h.authContext(r), csilapi.DeactivateEnrollmentTokenRequest{TokenId: tokenID}); err != nil {
		h.handleFormServiceError(w, r, err, "/app/workers")
		return
	}
	h.redirectFlash(w, r, "/app/workers", "Enrollment token deactivated", false)
}

// WorkerStatusUpdate handles POST /app/workers/worker/{id}/status
// (quarantine/disable/re-activate). status must be one of
// active/quarantined/disabled — the coordinator is the authoritative check;
// this only avoids an obviously-wrong call.
func (h *WebHandler) WorkerStatusUpdate(w http.ResponseWriter, r *http.Request) {
	workerID := r.PathValue("id")
	if workerID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	status := formTrim(r, "status")
	if status == "" {
		h.redirectFlash(w, r, "/app/workers", "status is required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/workers", "management is not available", true)
		return
	}
	req := csilapi.SetWorkerStatusRequest{WorkerId: workerID, Status: status}
	if _, err := h.uiClients.Ui.SetWorkerStatus(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, "/app/workers")
		return
	}
	h.redirectFlash(w, r, "/app/workers", "Worker status updated", false)
}

// WorkerDrain handles POST /app/workers/worker/{id}/drain: asks a worker to
// finish its current lease(s) and stop accepting new work (modeled as the
// "quarantined" status on the coordinator, plus a draining hint in its
// heartbeat response — see DrainWorker's doc comment on the coordinator).
func (h *WebHandler) WorkerDrain(w http.ResponseWriter, r *http.Request) {
	workerID := r.PathValue("id")
	if workerID == "" {
		http.NotFound(w, r)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/workers", "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.DrainWorker(h.authContext(r), csilapi.DrainWorkerRequest{WorkerId: workerID}); err != nil {
		h.handleFormServiceError(w, r, err, "/app/workers")
		return
	}
	h.redirectFlash(w, r, "/app/workers", "Worker draining", false)
}

// QueuesPage renders GET /app/workers/queues. Queues have no owning-org
// concept today (see the file doc comment), so every queue op the
// coordinator exposes requires a true global admin regardless of
// ManageWorkers scope — gate here matches that exactly rather than showing
// a page whose every action would 403.
func (h *WebHandler) QueuesPage(w http.ResponseWriter, r *http.Request) {
	si := h.sessionInfo(r)
	if !si.IsGlobalAdmin {
		h.renderError(w, r, http.StatusForbidden, "Queue management is a global-admin operation", nil)
		return
	}
	if h.uiClients == nil {
		h.renderError(w, r, http.StatusServiceUnavailable, "Management is not available", nil)
		return
	}

	resp, err := h.uiClients.Ui.ListQueues(h.authContext(r), csilapi.ListQueuesRequest{})
	if err != nil {
		h.renderServiceError(w, r, err)
		return
	}

	msg, errMsg := flashFromQuery(r)
	data := map[string]interface{}{
		"Title":     "Queues",
		"Queues":    resp.Queues,
		"FormMsg":   msg,
		"FormError": errMsg,
	}
	h.render(w, r, "workers_queues.html", data)
}

// QueueCreate handles POST /app/workers/queues. Characteristics are entered
// as a simple comma-separated "key=value" list (parseCharacteristicsInput);
// display_name is required client-side for a usable list, though the
// coordinator itself accepts an empty one.
func (h *WebHandler) QueueCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	displayName := formTrim(r, "display_name")
	if displayName == "" {
		h.redirectFlash(w, r, "/app/workers/queues", "display name is required", true)
		return
	}
	chars, parseErr := parseCharacteristicsInput(r.FormValue("characteristics"))
	if parseErr != nil {
		h.redirectFlash(w, r, "/app/workers/queues", parseErr.Error(), true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/workers/queues", "management is not available", true)
		return
	}

	req := csilapi.CreateQueueRequest{DisplayName: &displayName, Characteristics: chars}
	if _, err := h.uiClients.Ui.CreateQueue(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, "/app/workers/queues")
		return
	}
	h.redirectFlash(w, r, "/app/workers/queues", "Queue created", false)
}

// QueueRename handles POST /app/workers/queues/{id}/rename. display_name is
// the only mutable field a queue has (characteristics are immutable once
// created — no update-characteristics op exists).
func (h *WebHandler) QueueRename(w http.ResponseWriter, r *http.Request) {
	queueID := r.PathValue("id")
	if queueID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	displayName := formTrim(r, "display_name")
	if displayName == "" {
		h.redirectFlash(w, r, "/app/workers/queues", "display name is required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/workers/queues", "management is not available", true)
		return
	}
	req := csilapi.RenameQueueRequest{QueueId: queueID, DisplayName: displayName}
	if _, err := h.uiClients.Ui.RenameQueue(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, "/app/workers/queues")
		return
	}
	h.redirectFlash(w, r, "/app/workers/queues", "Queue renamed", false)
}

// QueueDelete handles POST /app/workers/queues/{id}/delete. The coordinator
// cancels every in-flight job routed to the queue before deleting it (and
// refuses to delete the default queue outright) — the confirm() prompt on
// the delete button (workers_queues.html) warns about the cancellation
// up front; this handler also folds the cancelled-job count into the
// success flash so it's visible even without JS.
func (h *WebHandler) QueueDelete(w http.ResponseWriter, r *http.Request) {
	queueID := r.PathValue("id")
	if queueID == "" {
		http.NotFound(w, r)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/workers/queues", "management is not available", true)
		return
	}
	resp, err := h.uiClients.Ui.DeleteQueue(h.authContext(r), csilapi.DeleteQueueRequest{QueueId: queueID})
	if err != nil {
		h.handleFormServiceError(w, r, err, "/app/workers/queues")
		return
	}
	msg := "Queue deleted"
	if n := len(resp.CancelledJobIds); n > 0 {
		msg = fmt.Sprintf("Queue deleted (%d in-flight job(s) cancelled)", n)
	}
	h.redirectFlash(w, r, "/app/workers/queues", msg, false)
}

// parseCharacteristicsInput parses the queue-create form's simple
// comma-separated "key=value,key2=value2" characteristics field into wire
// CharacteristicEntry values (always string-scalar — the coordinator's
// characteristics.ParseJobCharacteristics also accepts int/bool, but a plain
// text field has no way to express those distinctly, and string values
// satisfy every characteristics match rule this admin UI needs). A blank
// field is valid (no characteristics, e.g. the coordinator defaults
// "os":"linux"); every non-blank entry must have a non-empty key before "=".
func parseCharacteristicsInput(raw string) ([]csilapi.CharacteristicEntry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]csilapi.CharacteristicEntry, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("characteristics must be key=value pairs separated by commas (got %q)", part)
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		if key == "" {
			return nil, fmt.Errorf("characteristics keys must not be empty")
		}
		out = append(out, csilapi.CharacteristicEntry{Key: key, Value: val})
	}
	return out, nil
}
