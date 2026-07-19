package handlers

// Shared helpers for Task I's management UI handlers (projects, groups,
// roles, secrets, admin, job/workflow actions). Kept in one file since every
// management page follows the same shape: gate on a capability, call a
// uiClients.Ui/Auth op with h.authContext(r), then either redirect on
// success or map a *uiclient.ServiceCallError onto a rendered response.
//
// Query-param flash convention: GET pages read ?msg=<text> (success) and
// ?err=<text> (failure) and render them next to the relevant form/section.
// Both are always passed through html/template, which auto-escapes, so no
// manual HTML-escaping is needed here to satisfy AGENTS.md/UI_AUTH_PLAN.md's
// "html-escape everything" instruction for redirect-carried messages.

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
	"github.com/sirupsen/logrus"
)

// redirectFlash redirects to target with a ?msg= (isErr=false) or ?err=
// (isErr=true) query parameter carrying msg, preserving any existing query
// string on target.
func (h *WebHandler) redirectFlash(w http.ResponseWriter, r *http.Request, target, msg string, isErr bool) {
	if msg != "" {
		v := url.Values{}
		if isErr {
			v.Set("err", msg)
		} else {
			v.Set("msg", msg)
		}
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target = target + sep + v.Encode()
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// flashFromQuery reads the ?msg=/?err= flash query params a redirectFlash
// (or job/workflow action) call left on this request's URL.
func flashFromQuery(r *http.Request) (msg, errMsg string) {
	return r.URL.Query().Get("msg"), r.URL.Query().Get("err")
}

// redirectToLoginWithMessage sends an unauthorized/login_disabled caller to
// the login page with a friendly, non-sensitive reason.
func (h *WebHandler) redirectToLoginWithMessage(w http.ResponseWriter, r *http.Request, msg string) {
	v := url.Values{}
	if msg != "" {
		v.Set("error", msg)
	}
	target := "/app/login"
	if len(v) > 0 {
		target += "?" + v.Encode()
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// serviceErrorDetail classifies a uiClients call error into an HTTP status,
// a safe-to-display message, and whether it should send the browser to the
// login page rather than showing status/message directly. Every
// ReactorcideAuth/ReactorcideUi ServiceError code (unauthorized, forbidden,
// not_found, invalid_argument, login_disabled, conflict, internal,
// unimplemented, ...) is covered by the default branch when it isn't one of
// the specially-handled ones.
func serviceErrorDetail(err error) (status int, message string, toLogin bool) {
	var svcErr *uiclient.ServiceCallError
	if errors.As(err, &svcErr) {
		switch svcErr.Code {
		case "unauthorized", "login_disabled":
			return http.StatusUnauthorized, svcErr.Message, true
		case "forbidden":
			return http.StatusForbidden, svcErr.Message, false
		case "not_found":
			return http.StatusNotFound, svcErr.Message, false
		default:
			// invalid_argument, conflict, internal, unimplemented, and any
			// future code: safe to show verbatim (fixed reason strings, no
			// secret material), rendered as a form-level error rather than
			// a whole-page failure.
			return http.StatusBadRequest, svcErr.Message, false
		}
	}
	logrus.WithError(err).Warn("uiclient: management call failed")
	return http.StatusBadGateway, "request failed", false
}

// renderServiceError handles a uiClients call error for a GET (page-load)
// path: unauthorized/login_disabled sends the browser to /app/login,
// forbidden/not_found render the matching error page, everything else
// renders a generic error page (there is no form to flash the message next
// to on a page-load failure).
func (h *WebHandler) renderServiceError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg, toLogin := serviceErrorDetail(err)
	if toLogin {
		h.redirectToLoginWithMessage(w, r, msg)
		return
	}
	h.renderError(w, r, status, msg, nil)
}

// handleFormServiceError handles a uiClients call error for a POST (form
// submission) path: unauthorized/login_disabled sends the browser to
// /app/login, forbidden/not_found render the matching error page (deliverable
// #9), and everything else (invalid_argument, conflict, internal,
// unimplemented, transport failures) redirects back to backTo with the
// message as a ?err= flash so the originating page can render it next to the
// form (deliverable #8/#9's "invalid_argument must render the message next
// to the form").
func (h *WebHandler) handleFormServiceError(w http.ResponseWriter, r *http.Request, err error, backTo string) {
	status, msg, toLogin := serviceErrorDetail(err)
	if toLogin {
		h.redirectToLoginWithMessage(w, r, msg)
		return
	}
	switch status {
	case http.StatusForbidden:
		h.renderError(w, r, http.StatusForbidden, "Forbidden: "+msg, nil)
	case http.StatusNotFound:
		h.renderError(w, r, http.StatusNotFound, "Not found: "+msg, nil)
	default:
		h.redirectFlash(w, r, backTo, msg, true)
	}
}

// capabilitiesForOrg fetches get-capabilities scoped to orgID, for
// organization-scoped management pages (groups, roles, secrets). Unlike
// capabilitiesForProject (job/workflow cancel/kill hints), org-scoped
// capabilities matter for whole-page gating here, so a lookup failure is
// still treated as "no capabilities" (safe default).
func (h *WebHandler) capabilitiesForOrg(r *http.Request, orgID string) csilapi.GetCapabilitiesResponse {
	if h.uiClients == nil || orgID == "" {
		return csilapi.GetCapabilitiesResponse{}
	}
	req := csilapi.GetCapabilitiesRequest{OrgId: &orgID}
	caps, err := h.uiClients.Ui.GetCapabilities(h.authContext(r), req)
	if err != nil {
		logrus.WithError(err).Debug("uiclient: get-capabilities (org-scoped) failed")
		return csilapi.GetCapabilitiesResponse{}
	}
	return caps
}

// resolveOrgID picks the org a groups/roles/secrets page operates on: an
// explicit ?org_id= query override (used by the org selector global admins
// see), otherwise the caller's own org (recall: "user_id IS the org id
// everywhere", so a non-global-admin's own org is their own user id). A
// global admin with no explicit selection gets "" back; callers should list
// orgs and default to the first one themselves.
func (h *WebHandler) resolveOrgID(r *http.Request, si SessionInfo) string {
	if v := strings.TrimSpace(r.URL.Query().Get("org_id")); v != "" {
		return v
	}
	if !si.IsGlobalAdmin {
		return si.UserID
	}
	return ""
}

// listOrgsForSelector lists orgs for the org selector shown to global admins
// on groups/roles/secrets pages. Errors are logged and treated as "no orgs"
// (the page still renders; the selector is just empty).
func (h *WebHandler) listOrgsForSelector(r *http.Request) []csilapi.OrgSummary {
	if h.uiClients == nil {
		return nil
	}
	resp, err := h.uiClients.Ui.ListOrgs(h.authContext(r), csilapi.ListOrgsRequest{})
	if err != nil {
		logrus.WithError(err).Debug("uiclient: list-orgs failed")
		return nil
	}
	return resp.Orgs
}

// --- form parsing helpers ---

// formTrim reads a form field and trims surrounding whitespace.
func formTrim(r *http.Request, name string) string {
	return strings.TrimSpace(r.FormValue(name))
}

// formOptionalPtr reads a form field, returning nil when it is empty (for
// optional *string request fields) rather than a pointer to "".
func formOptionalPtr(r *http.Request, name string) *string {
	v := strings.TrimSpace(r.FormValue(name))
	if v == "" {
		return nil
	}
	return &v
}

// formCheckbox reports whether a checkbox-style form field was checked. An
// HTML checkbox only appears in the posted form at all when checked, so
// FormValue returning "" (unset) and "on" (checked, the default value
// browsers send) are the only cases that matter here.
func formCheckbox(r *http.Request, name string) bool {
	v := r.FormValue(name)
	return v == "on" || v == "true" || v == "1"
}

// formStringList parses a comma-separated form field into a trimmed,
// empty-entry-filtered []string, or nil if the field is blank (so the
// generated request's omitempty leaves the corresponding field untouched
// rather than sending an explicit empty list).
func formStringList(r *http.Request, name string) []string {
	raw := r.FormValue(name)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// joinStringList renders a []string back into the same comma-separated form
// formStringList parses, for pre-filling an edit form.
func joinStringList(v []string) string {
	return strings.Join(v, ", ")
}
