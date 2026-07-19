package handlers

import (
	"net/http"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// OrgSecretsPage renders GET /app/org/secrets: gated on ManageSecrets.
// Secrets are write-only through this UI (list-secret-paths returns paths
// and key names only, never values — see UI_AUTH_PLAN.md's "Secrets are
// write-only through the UI" architecture note), plus the secret grants
// management section.
func (h *WebHandler) OrgSecretsPage(w http.ResponseWriter, r *http.Request) {
	si := h.sessionInfo(r)
	orgID := h.resolveOrgID(r, si)
	var orgs []csilapi.OrgSummary
	if si.IsGlobalAdmin {
		orgs = h.listOrgsForSelector(r)
		if orgID == "" && len(orgs) > 0 {
			orgID = orgs[0].OrgId
		}
	}

	caps := h.capabilitiesForOrg(r, orgID)
	if !caps.ManageSecrets {
		h.renderError(w, r, http.StatusForbidden, "You do not have permission to manage secrets for this org", nil)
		return
	}

	msg, errMsg := flashFromQuery(r)
	data := map[string]interface{}{
		"Title":     "Secrets",
		"OrgID":     orgID,
		"Orgs":      orgs,
		"IsAdmin":   si.IsGlobalAdmin,
		"FormMsg":   msg,
		"FormError": errMsg,
	}

	if orgID != "" && h.uiClients != nil {
		paths, err := h.uiClients.Ui.ListSecretPaths(h.authContext(r), csilapi.ListSecretPathsRequest{OrgId: orgID})
		if err != nil {
			h.renderServiceError(w, r, err)
			return
		}
		data["Paths"] = paths.Paths

		grants, err := h.uiClients.Ui.ListSecretGrants(h.authContext(r), csilapi.ListSecretGrantsRequest{OrgId: orgID})
		if err != nil {
			h.renderServiceError(w, r, err)
			return
		}
		data["Grants"] = grants.Grants

		projects, err := h.uiClients.Ui.ListProjects(h.authContext(r), csilapi.ListProjectsRequest{OrgId: &orgID})
		if err == nil {
			data["Projects"] = projects.Projects
		}
	}

	h.render(w, r, "org_secrets.html", data)
}

// SecretSet handles POST /app/org/secrets. The value is form-only: it flows
// straight into SetSecret and is never echoed back anywhere.
func (h *WebHandler) SecretSet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	orgID := formTrim(r, "org_id")
	backTo := orgPageBackTo("/app/org/secrets", orgID)

	path := formTrim(r, "path")
	key := formTrim(r, "key")
	value := r.FormValue("value")
	if orgID == "" || path == "" || key == "" || value == "" {
		h.redirectFlash(w, r, backTo, "org, path, key, and value are required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}

	req := csilapi.SetSecretRequest{OrgId: orgID, Path: path, Key: key, Value: value}
	if _, err := h.uiClients.Ui.SetSecret(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Secret set", false)
}

// SecretDelete handles POST /app/org/secrets/delete. Secrets have no ID —
// they're addressed by (org, path, key) — so the delete form posts those
// three fields rather than a path segment.
func (h *WebHandler) SecretDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	orgID := formTrim(r, "org_id")
	backTo := orgPageBackTo("/app/org/secrets", orgID)

	path := formTrim(r, "path")
	key := formTrim(r, "key")
	if orgID == "" || path == "" || key == "" {
		h.redirectFlash(w, r, backTo, "org, path, and key are required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}

	req := csilapi.DeleteSecretRequest{OrgId: orgID, Path: path, Key: key}
	if _, err := h.uiClients.Ui.DeleteSecret(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Secret deleted", false)
}

// SecretGrantCreate handles POST /app/org/secrets/grants.
func (h *WebHandler) SecretGrantCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	orgID := formTrim(r, "org_id")
	backTo := orgPageBackTo("/app/org/secrets", orgID)

	name := formTrim(r, "name")
	pathMatch := formTrim(r, "secret_path_match")
	pathPattern := formTrim(r, "secret_path_pattern")
	if orgID == "" || name == "" || pathMatch == "" {
		h.redirectFlash(w, r, backTo, "org, name, and secret path match are required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}

	req := csilapi.CreateSecretGrantRequest{
		OrgId:             orgID,
		ProjectId:         formOptionalPtr(r, "project_id"),
		Name:              name,
		SecretPathMatch:   pathMatch,
		SecretPathPattern: pathPattern,
		JobNameMatch:      formOptionalPtr(r, "job_name_match"),
		JobNamePattern:    formOptionalPtr(r, "job_name_pattern"),
		Description:       formOptionalPtr(r, "description"),
	}
	if _, err := h.uiClients.Ui.CreateSecretGrant(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Secret grant created", false)
}

// SecretGrantUpdate handles POST /app/org/secrets/grants/{id}. Every field
// is optional (edit-in-place); blank inputs on the edit form leave the
// corresponding field untouched.
func (h *WebHandler) SecretGrantUpdate(w http.ResponseWriter, r *http.Request) {
	grantID := r.PathValue("id")
	if grantID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	backTo := orgPageBackTo("/app/org/secrets", formTrim(r, "org_id"))
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}

	req := csilapi.UpdateSecretGrantRequest{
		GrantId:           grantID,
		Name:              formOptionalPtr(r, "name"),
		SecretPathMatch:   formOptionalPtr(r, "secret_path_match"),
		SecretPathPattern: formOptionalPtr(r, "secret_path_pattern"),
		JobNameMatch:      formOptionalPtr(r, "job_name_match"),
		JobNamePattern:    formOptionalPtr(r, "job_name_pattern"),
		Description:       formOptionalPtr(r, "description"),
	}
	if _, err := h.uiClients.Ui.UpdateSecretGrant(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Secret grant updated", false)
}

// SecretGrantDelete handles POST /app/org/secrets/grants/{id}/delete.
func (h *WebHandler) SecretGrantDelete(w http.ResponseWriter, r *http.Request) {
	grantID := r.PathValue("id")
	if grantID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	backTo := orgPageBackTo("/app/org/secrets", formTrim(r, "org_id"))
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.DeleteSecretGrant(h.authContext(r), csilapi.DeleteSecretGrantRequest{GrantId: grantID}); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Secret grant deleted", false)
}
