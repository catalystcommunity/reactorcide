package handlers

import (
	"net/http"

	"github.com/catalystcommunity/reactorcide/webapp/internal/config"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient"
)

// NewRouter creates the HTTP handler with all routes
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	client := NewAPIClient()
	uiClients := uiclient.New(config.APIUrl)
	webHandler := NewWebHandler(client, uiClients)
	wsProxy := NewWSProxy()

	// Health check at root for k8s probes
	mux.HandleFunc("GET /", webHandler.HealthCheck)

	// Web UI routes under /app/. Every /app/ page route goes through
	// withSession so SessionInfo (cookie -> coordinator authenticate ->
	// capabilities) is resolved once per request and available both to the
	// handler and to the shared "head"/"foot" layout templates (nav bar
	// auth area).
	mux.HandleFunc("GET /app/", webHandler.withSession(webHandler.JobsList))
	mux.HandleFunc("GET /app/jobs", webHandler.withSession(webHandler.RedirectToAppRoot))
	mux.HandleFunc("GET /app/jobs/", webHandler.withSession(webHandler.RedirectToAppRoot))
	mux.HandleFunc("GET /app/workflows/{id}", webHandler.withSession(webHandler.WorkflowDetail))
	mux.HandleFunc("POST /app/workflows/{id}/cancel", webHandler.withSession(webHandler.WorkflowCancel))
	mux.HandleFunc("POST /app/workflows/{id}/retry", webHandler.withSession(webHandler.WorkflowRetry))
	mux.HandleFunc("POST /app/workflows/{id}/retry-unsuccessful", webHandler.withSession(webHandler.WorkflowRetryUnsuccessful))
	mux.HandleFunc("GET /app/jobs/{id}", webHandler.withSession(webHandler.JobDetail))
	mux.HandleFunc("GET /app/jobs/{id}/logs", webHandler.withSession(webHandler.JobLogs))
	mux.HandleFunc("POST /app/jobs/{id}/cancel", webHandler.withSession(webHandler.JobCancel))
	mux.HandleFunc("POST /app/jobs/{id}/kill", webHandler.withSession(webHandler.JobKill))
	mux.HandleFunc("POST /app/jobs/{id}/retry", webHandler.withSession(webHandler.JobRetry))

	// Projects (Task I: management UI). GET /app/projects/new must be
	// registered alongside GET /app/projects/{id} — Go 1.22's ServeMux
	// prefers the more specific literal segment ("new") over the wildcard
	// ("{id}") for the same path shape, so this is unambiguous.
	mux.HandleFunc("GET /app/projects", webHandler.withSession(webHandler.ProjectsList))
	mux.HandleFunc("GET /app/projects/new", webHandler.withSession(webHandler.ProjectNewForm))
	mux.HandleFunc("POST /app/projects", webHandler.withSession(webHandler.ProjectCreate))
	mux.HandleFunc("GET /app/projects/{id}", webHandler.withSession(webHandler.ProjectDetail))
	mux.HandleFunc("POST /app/projects/{id}/settings", webHandler.withSession(webHandler.ProjectSettingsUpdate))
	mux.HandleFunc("POST /app/projects/{id}/delete", webHandler.withSession(webHandler.ProjectDelete))
	mux.HandleFunc("POST /app/projects/{id}/webhook-secrets", webHandler.withSession(webHandler.WebhookSecretAdd))
	mux.HandleFunc("POST /app/projects/{id}/webhook-secrets/{sid}/deactivate", webHandler.withSession(webHandler.WebhookSecretDeactivate))
	mux.HandleFunc("POST /app/projects/{id}/webhook-secrets/{sid}/delete", webHandler.withSession(webHandler.WebhookSecretDelete))
	mux.HandleFunc("POST /app/projects/{id}/vcs-credentials", webHandler.withSession(webHandler.VcsCredentialAdd))
	mux.HandleFunc("POST /app/projects/{id}/vcs-credentials/{sid}/deactivate", webHandler.withSession(webHandler.VcsCredentialDeactivate))
	mux.HandleFunc("POST /app/projects/{id}/vcs-credentials/{sid}/delete", webHandler.withSession(webHandler.VcsCredentialDelete))

	// Groups & roles (org-scoped management).
	mux.HandleFunc("GET /app/org/groups", webHandler.withSession(webHandler.OrgGroupsPage))
	mux.HandleFunc("POST /app/org/groups", webHandler.withSession(webHandler.GroupCreate))
	mux.HandleFunc("POST /app/org/groups/{id}/delete", webHandler.withSession(webHandler.GroupDelete))
	mux.HandleFunc("POST /app/org/groups/{id}/members", webHandler.withSession(webHandler.GroupMemberAdd))
	mux.HandleFunc("POST /app/org/groups/{id}/members/remove", webHandler.withSession(webHandler.GroupMemberRemove))
	mux.HandleFunc("GET /app/org/roles", webHandler.withSession(webHandler.OrgRolesPage))
	mux.HandleFunc("POST /app/org/roles", webHandler.withSession(webHandler.RoleAssign))
	mux.HandleFunc("POST /app/org/roles/{id}/revoke", webHandler.withSession(webHandler.RoleRevoke))

	// Secrets & grants (org-scoped, write-only through the UI).
	mux.HandleFunc("GET /app/org/secrets", webHandler.withSession(webHandler.OrgSecretsPage))
	mux.HandleFunc("POST /app/org/secrets", webHandler.withSession(webHandler.SecretSet))
	mux.HandleFunc("POST /app/org/secrets/delete", webHandler.withSession(webHandler.SecretDelete))
	mux.HandleFunc("POST /app/org/secrets/grants", webHandler.withSession(webHandler.SecretGrantCreate))
	mux.HandleFunc("POST /app/org/secrets/grants/{id}", webHandler.withSession(webHandler.SecretGrantUpdate))
	mux.HandleFunc("POST /app/org/secrets/grants/{id}/delete", webHandler.withSession(webHandler.SecretGrantDelete))

	// Admin (global-admin only).
	mux.HandleFunc("GET /app/admin", webHandler.withSession(webHandler.AdminPage))
	mux.HandleFunc("POST /app/admin/global-settings", webHandler.withSession(webHandler.AdminGlobalSettingsUpdate))
	mux.HandleFunc("POST /app/admin/trusted-identities", webHandler.withSession(webHandler.TrustedIdentityAdd))
	mux.HandleFunc("POST /app/admin/trusted-identities/remove", webHandler.withSession(webHandler.TrustedIdentityRemove))
	mux.HandleFunc("POST /app/admin/trusted-domain-patterns", webHandler.withSession(webHandler.TrustedDomainPatternAdd))
	mux.HandleFunc("POST /app/admin/trusted-domain-patterns/{id}/remove", webHandler.withSession(webHandler.TrustedDomainPatternRemove))

	// Auth routes: login (identity-selector form -> begin-login ->
	// redirect), the LinkKeys/local-rp callback landing page
	// (complete-login), logout, and the one-time bootstrap-admin flow.
	mux.HandleFunc("GET /app/login", webHandler.withSession(webHandler.LoginPage))
	mux.HandleFunc("POST /app/login", webHandler.withSession(webHandler.LoginSubmit))
	mux.HandleFunc("GET /app/auth/callback", webHandler.withSession(webHandler.AuthCallback))
	mux.HandleFunc("POST /app/logout", webHandler.withSession(webHandler.Logout))
	mux.HandleFunc("GET /app/bootstrap", webHandler.withSession(webHandler.BootstrapPage))
	mux.HandleFunc("POST /app/bootstrap", webHandler.withSession(webHandler.BootstrapSubmit))

	// WebSocket streams. The browser connects here; we proxy to the
	// coordinator's WS endpoints using the server-side service token so
	// the token never reaches client JS.
	mux.HandleFunc("GET /app/ws/jobs", wsProxy.AllJobsStream)
	mux.HandleFunc("GET /app/ws/jobs/{id}", wsProxy.JobStream)

	return mux
}
