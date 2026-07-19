package handlers

import (
	"net/http"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// WebhookSecretAdd handles POST /app/projects/{id}/webhook-secrets. The
// secret value is form-only: it flows straight into the CSIL call and is
// never echoed back in a redirect, a rendered form, or a log line.
func (h *WebHandler) WebhookSecretAdd(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	backTo := "/app/projects/" + projectID

	provider := formTrim(r, "provider")
	name := formTrim(r, "name")
	value := r.FormValue("value")
	if provider == "" || name == "" || value == "" {
		h.redirectFlash(w, r, backTo, "provider, name, and secret value are required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}

	req := csilapi.AddWebhookSecretRequest{ProjectId: projectID, Provider: provider, Name: name, Value: value}
	if _, err := h.uiClients.Ui.AddWebhookSecret(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Webhook secret added", false)
}

// WebhookSecretDeactivate handles POST
// /app/projects/{id}/webhook-secrets/{sid}/deactivate.
func (h *WebHandler) WebhookSecretDeactivate(w http.ResponseWriter, r *http.Request) {
	projectID, secretID := r.PathValue("id"), r.PathValue("sid")
	if projectID == "" || secretID == "" {
		http.NotFound(w, r)
		return
	}
	backTo := "/app/projects/" + projectID
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.DeactivateWebhookSecret(h.authContext(r), csilapi.DeactivateWebhookSecretRequest{Id: secretID}); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Webhook secret deactivated", false)
}

// WebhookSecretDelete handles POST
// /app/projects/{id}/webhook-secrets/{sid}/delete.
func (h *WebHandler) WebhookSecretDelete(w http.ResponseWriter, r *http.Request) {
	projectID, secretID := r.PathValue("id"), r.PathValue("sid")
	if projectID == "" || secretID == "" {
		http.NotFound(w, r)
		return
	}
	backTo := "/app/projects/" + projectID
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.DeleteWebhookSecret(h.authContext(r), csilapi.DeleteWebhookSecretRequest{Id: secretID}); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Webhook secret deleted", false)
}

// VcsCredentialAdd handles POST /app/projects/{id}/vcs-credentials.
func (h *WebHandler) VcsCredentialAdd(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	backTo := "/app/projects/" + projectID

	provider := formTrim(r, "provider")
	name := formTrim(r, "name")
	value := r.FormValue("value")
	if provider == "" || name == "" || value == "" {
		h.redirectFlash(w, r, backTo, "provider, name, and credential value are required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}

	req := csilapi.AddVcsCredentialRequest{ProjectId: projectID, Provider: provider, Name: name, Value: value}
	if _, err := h.uiClients.Ui.AddVcsCredential(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "VCS credential added", false)
}

// VcsCredentialDeactivate handles POST
// /app/projects/{id}/vcs-credentials/{sid}/deactivate.
func (h *WebHandler) VcsCredentialDeactivate(w http.ResponseWriter, r *http.Request) {
	projectID, credID := r.PathValue("id"), r.PathValue("sid")
	if projectID == "" || credID == "" {
		http.NotFound(w, r)
		return
	}
	backTo := "/app/projects/" + projectID
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.DeactivateVcsCredential(h.authContext(r), csilapi.DeactivateVcsCredentialRequest{Id: credID}); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "VCS credential deactivated", false)
}

// VcsCredentialDelete handles POST
// /app/projects/{id}/vcs-credentials/{sid}/delete, mirroring
// WebhookSecretDelete.
func (h *WebHandler) VcsCredentialDelete(w http.ResponseWriter, r *http.Request) {
	projectID, credID := r.PathValue("id"), r.PathValue("sid")
	if projectID == "" || credID == "" {
		http.NotFound(w, r)
		return
	}
	backTo := "/app/projects/" + projectID
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.DeleteVcsCredential(h.authContext(r), csilapi.DeleteVcsCredentialRequest{Id: credID}); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "VCS credential deleted", false)
}
