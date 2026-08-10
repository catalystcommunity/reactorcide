package handlers

import (
	"context"
	"net/http"
	"strconv"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

type auditStore interface {
	GetOrganizationByName(context.Context, string) (*models.Organization, error)
	ListAuditEvents(context.Context, string, int) ([]models.AuditEvent, error)
}

type AuditHandler struct {
	BaseHandler
	store    auditStore
	appStore store.Store
}

func NewAuditHandler(appStore store.Store) *AuditHandler {
	value, _ := appStore.(auditStore)
	return &AuditHandler{store: value, appStore: appStore}
}

func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	principal := checkauth.GetPrincipalFromContext(r.Context())
	if h.store == nil || principal == nil || !principal.HasCapability(tokencaps.AuditRead) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	org, err := h.store.GetOrganizationByName(r.Context(), r.URL.Query().Get("org"))
	if err != nil {
		h.respondWithError(w, http.StatusBadRequest, err)
		return
	}
	if !principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.AuditRead) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := h.store.ListAuditEvents(r.Context(), org.OrgID, limit)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"organization": org.Name, "events": events})
}
