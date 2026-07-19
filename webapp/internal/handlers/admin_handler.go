package handlers

import (
	"net/http"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// AdminPage renders GET /app/admin: global settings, trusted identities, and
// trusted domain patterns. Global-admin only — hidden from the nav entirely
// for anyone else, and a direct hit renders 403 (the coordinator's
// get-global-settings/list-trusted-* ops are global-admin-only with no
// partial view for anyone else, so there is nothing else this page could
// show an incapable caller).
func (h *WebHandler) AdminPage(w http.ResponseWriter, r *http.Request) {
	si := h.sessionInfo(r)
	if !si.IsGlobalAdmin {
		h.renderError(w, r, http.StatusForbidden, "Admin access is required", nil)
		return
	}
	if h.uiClients == nil {
		h.renderError(w, r, http.StatusServiceUnavailable, "Management is not available", nil)
		return
	}

	settings, err := h.uiClients.Ui.GetGlobalSettings(h.authContext(r), csilapi.GetGlobalSettingsRequest{})
	if err != nil {
		h.renderServiceError(w, r, err)
		return
	}
	identities, err := h.uiClients.Ui.ListTrustedIdentities(h.authContext(r), csilapi.ListTrustedIdentitiesRequest{})
	if err != nil {
		h.renderServiceError(w, r, err)
		return
	}
	patterns, err := h.uiClients.Ui.ListTrustedDomainPatterns(h.authContext(r), csilapi.ListTrustedDomainPatternsRequest{})
	if err != nil {
		h.renderServiceError(w, r, err)
		return
	}

	msg, errMsg := flashFromQuery(r)
	data := map[string]interface{}{
		"Title":             "Admin",
		"Settings":          settings,
		"TrustedIdentities": identities.Identities,
		"TrustedPatterns":   patterns.Patterns,
		"FormMsg":           msg,
		"FormError":         errMsg,
	}
	h.render(w, r, "admin.html", data)
}

// AdminGlobalSettingsUpdate handles POST /app/admin/global-settings.
func (h *WebHandler) AdminGlobalSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/admin", "management is not available", true)
		return
	}
	newProjectsPrivate := formCheckbox(r, "new_projects_private")
	req := csilapi.UpdateGlobalSettingsRequest{NewProjectsPrivate: &newProjectsPrivate}
	if _, err := h.uiClients.Ui.UpdateGlobalSettings(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, "/app/admin")
		return
	}
	h.redirectFlash(w, r, "/app/admin", "Global settings updated", false)
}

// TrustedIdentityAdd handles POST /app/admin/trusted-identities.
func (h *WebHandler) TrustedIdentityAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	domain := formTrim(r, "domain")
	if domain == "" {
		h.redirectFlash(w, r, "/app/admin", "domain is required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/admin", "management is not available", true)
		return
	}
	req := csilapi.AddTrustedIdentityRequest{Domain: domain, Handle: formOptionalPtr(r, "handle")}
	if _, err := h.uiClients.Ui.AddTrustedIdentity(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, "/app/admin")
		return
	}
	h.redirectFlash(w, r, "/app/admin", "Trusted identity added", false)
}

// TrustedIdentityRemove handles POST /app/admin/trusted-identities/remove.
// Trusted identities are addressed by (domain, handle), not a synthetic ID,
// so the remove form posts both fields.
func (h *WebHandler) TrustedIdentityRemove(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	domain := formTrim(r, "domain")
	if domain == "" {
		h.redirectFlash(w, r, "/app/admin", "domain is required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/admin", "management is not available", true)
		return
	}
	req := csilapi.RemoveTrustedIdentityRequest{Domain: domain, Handle: formOptionalPtr(r, "handle")}
	if _, err := h.uiClients.Ui.RemoveTrustedIdentity(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, "/app/admin")
		return
	}
	h.redirectFlash(w, r, "/app/admin", "Trusted identity removed", false)
}

// TrustedDomainPatternAdd handles POST /app/admin/trusted-domain-patterns.
// The client-side regex sanity check (new RegExp() in admin.html) is a UX
// convenience only; auth.ValidateDomainPattern on the coordinator is
// authoritative (RE2 syntax, not JS regex syntax — they usually agree for
// the simple domain patterns this is meant for, but the coordinator's
// invalid_argument response is what actually gates persistence).
func (h *WebHandler) TrustedDomainPatternAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	pattern := formTrim(r, "pattern")
	if pattern == "" {
		h.redirectFlash(w, r, "/app/admin", "pattern is required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/admin", "management is not available", true)
		return
	}
	req := csilapi.AddTrustedDomainPatternRequest{Pattern: pattern, Description: formOptionalPtr(r, "description")}
	if _, err := h.uiClients.Ui.AddTrustedDomainPattern(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, "/app/admin")
		return
	}
	h.redirectFlash(w, r, "/app/admin", "Trusted domain pattern added", false)
}

// TrustedDomainPatternRemove handles POST
// /app/admin/trusted-domain-patterns/{id}/remove.
func (h *WebHandler) TrustedDomainPatternRemove(w http.ResponseWriter, r *http.Request) {
	patternID := r.PathValue("id")
	if patternID == "" {
		http.NotFound(w, r)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/admin", "management is not available", true)
		return
	}
	req := csilapi.RemoveTrustedDomainPatternRequest{PatternId: patternID}
	if _, err := h.uiClients.Ui.RemoveTrustedDomainPattern(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, "/app/admin")
		return
	}
	h.redirectFlash(w, r, "/app/admin", "Trusted domain pattern removed", false)
}
