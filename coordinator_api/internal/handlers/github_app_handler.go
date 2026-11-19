package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs/githubapp"
)

// GitHub App pool REST API (WP11, docs/github-app.md). Every route needs a
// global administrator: the admin API token, or a user with the admin role.
//
//	GET|POST   /api/v1/github-apps
//	GET        /api/v1/github-apps/recommended
//	GET        /api/v1/github-apps/account-orgs
//	PUT|DELETE /api/v1/github-apps/account-orgs/LOGIN
//	GET        /api/v1/github-apps/APP_ID
//	POST       /api/v1/github-apps/APP_ID/enable | /disable | /sync
//	GET        /api/v1/github-apps/APP_ID/installations
//
// The private key and the webhook secret are write-only. No answer has them.

// buildGitHubAppService makes the App pool and installs it for the VCS
// clients, the webhook handler, and the worker checkout path. It returns nil
// when the store or the master keys are missing.
func buildGitHubAppService(appStore store.Store, keys *secrets.MasterKeyManager, webhooks *WebhookHandler) *githubapp.Service {
	apps, ok := appStore.(store.GitHubAppStore)
	if !ok || keys == nil {
		return nil
	}
	service, err := githubapp.NewService(githubapp.Options{
		Store: apps, Crypto: keys, Projects: defaultImageRegistry{StoreRegistry: githubapp.StoreRegistry{Store: appStore, Apps: apps}, image: config.DefaultRunnerImage},
		APIBaseURL: config.GitHubAppAPIURL, AllowInsecureTransport: config.AllowInsecureTransport,
	})
	if err != nil {
		log.Printf("WARNING: GitHub Apps disabled: %v", err)
		return nil
	}
	vcs.SetAppInstallationSource(service)
	if webhooks != nil {
		webhooks.SetGitHubApps(service)
	}
	return service
}

// defaultImageRegistry gives a project that a GitHub App registers the
// default runner image of the deployment (REACTORCIDE_DEFAULT_RUNNER_IMAGE).
// Without it the project gets the image of the database default, which is
// not the image of the deployment. An empty setting keeps that default.
type defaultImageRegistry struct {
	githubapp.StoreRegistry
	image string
}

func (r defaultImageRegistry) CreateProject(ctx context.Context, project *models.Project) error {
	if project != nil && project.DefaultRunnerImage == "" && r.image != "" {
		project.DefaultRunnerImage = r.image
	}
	return r.StoreRegistry.CreateProject(ctx, project)
}

type githubAppRoutes struct {
	BaseHandler
	store   store.Store
	service *githubapp.Service
}

func newGitHubAppRoutes(appStore store.Store, service *githubapp.Service) *githubAppRoutes {
	return &githubAppRoutes{store: appStore, service: service}
}

// GitHubAppView is the REST form of an App.
type GitHubAppView struct {
	AppID               int64                    `json:"app_id"`
	Slug                string                   `json:"slug"`
	Name                string                   `json:"name"`
	ClientID            string                   `json:"client_id"`
	DefaultOrgID        *string                  `json:"default_org_id,omitempty"`
	OpenRegistration    bool                     `json:"open_registration"`
	Enabled             bool                     `json:"enabled"`
	RateLimit           models.RateLimitSnapshot `json:"rate_limit"`
	Headroom            float64                  `json:"headroom"`
	ActiveInstallations int                      `json:"active_installations"`
	Repositories        int                      `json:"repositories"`
	InstallURL          string                   `json:"install_url,omitempty"`
	Recommended         bool                     `json:"recommended"`
	CreatedAt           time.Time                `json:"created_at"`
	UpdatedAt           time.Time                `json:"updated_at"`
}

func githubAppView(summary githubapp.AppSummary) GitHubAppView {
	app := summary.App
	return GitHubAppView{AppID: app.AppID, Slug: app.Slug, Name: app.Name, ClientID: app.ClientID, DefaultOrgID: app.DefaultOrgID,
		OpenRegistration: app.OpenRegistration, Enabled: app.Enabled, RateLimit: app.RateLimit, Headroom: summary.Headroom.Headroom,
		ActiveInstallations: summary.Headroom.ActiveInstallations, Repositories: summary.Headroom.Repositories,
		InstallURL: summary.InstallURL, Recommended: summary.Recommended, CreatedAt: app.CreatedAt, UpdatedAt: app.UpdatedAt}
}

// GitHubInstallationView is an installation with its repositories.
type GitHubInstallationView struct {
	models.GitHubAppInstallation
	Headroom     float64                      `json:"headroom"`
	Repositories []models.GitHubAppRepository `json:"repositories"`
}

type githubAppRegisterRequest struct {
	AppID         int64  `json:"app_id"`
	PrivateKey    string `json:"private_key"`
	WebhookSecret string `json:"webhook_secret"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	ClientID      string `json:"client_id"`
	// DefaultOrg is the organization (name or ID) of new projects when no
	// account mapping applies. Empty: the default organization.
	DefaultOrg string `json:"default_org"`
	// OpenRegistration enables new projects that go into DefaultOrg (no
	// account mapping). Without it such a project is created disabled and
	// waits for approval. Omitted on a replacement: unchanged.
	OpenRegistration *bool `json:"open_registration"`
	// Verify (default true) checks the key with GitHub (GET /app).
	Verify *bool `json:"verify"`
	// Replace updates a registered App (key or secret rotation).
	Replace bool `json:"replace"`
}

type githubAccountOrgRequest struct {
	Org string `json:"org"`
}

// isGlobalAdmin: the admin API token (a global principal with the
// capability), or a user with the admin role.
func (g *githubAppRoutes) isGlobalAdmin(ctx context.Context, capability string) bool {
	if principal := checkauth.GetPrincipalFromContext(ctx); principal != nil {
		return principalAllowsGlobal(ctx, g.store, capability)
	}
	user := checkauth.GetUserFromContext(ctx)
	if user == nil {
		return false
	}
	for _, role := range user.Roles {
		if role == "admin" {
			return true
		}
	}
	return false
}

func (g *githubAppRoutes) mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		g.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
	case errors.Is(err, store.ErrAlreadyExists):
		g.respondWithJSON(w, http.StatusConflict, ErrorResponse{Error: "conflict", Message: err.Error()})
	case errors.Is(err, store.ErrInvalidInput):
		g.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: err.Error()})
	default:
		g.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "github_app_error", Message: err.Error()})
	}
}

// serve handles /api/v1/github-apps and everything under it.
func (g *githubAppRoutes) serve(w http.ResponseWriter, r *http.Request) {
	if g.service == nil {
		g.respondWithJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "unavailable",
			Message: "GitHub Apps need the master keys and a database store"})
		return
	}
	capability := tokencaps.OrganizationsRead
	if r.Method != http.MethodGet {
		capability = tokencaps.OrganizationsManage
	}
	if !g.isGlobalAdmin(r.Context(), capability) {
		g.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/github-apps"), "/")
	parts := []string{}
	if rest != "" {
		parts = strings.Split(rest, "/")
	}
	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		g.list(w, r)
	case len(parts) == 0 && r.Method == http.MethodPost:
		g.register(w, r)
	case len(parts) == 1 && parts[0] == "recommended" && r.Method == http.MethodGet:
		g.recommended(w, r)
	case len(parts) == 1 && parts[0] == "account-orgs" && r.Method == http.MethodGet:
		g.listAccountOrgs(w, r)
	case len(parts) == 2 && parts[0] == "account-orgs" && r.Method == http.MethodPut:
		g.setAccountOrg(w, r, parts[1])
	case len(parts) == 2 && parts[0] == "account-orgs" && r.Method == http.MethodDelete:
		g.deleteAccountOrg(w, r, parts[1])
	default:
		if len(parts) == 0 {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		appID, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || appID <= 0 {
			g.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
			return
		}
		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			g.show(w, r, appID)
		case len(parts) == 2 && (parts[1] == "enable" || parts[1] == "disable") && r.Method == http.MethodPost:
			g.setEnabled(w, r, appID, parts[1] == "enable")
		case len(parts) == 2 && parts[1] == "sync" && r.Method == http.MethodPost:
			g.sync(w, r, appID)
		case len(parts) == 2 && parts[1] == "installations" && r.Method == http.MethodGet:
			g.installations(w, r, appID)
		default:
			g.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		}
	}
}

func (g *githubAppRoutes) list(w http.ResponseWriter, r *http.Request) {
	summaries, err := g.service.Summaries(r.Context())
	if err != nil {
		g.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	views := make([]GitHubAppView, 0, len(summaries))
	for _, summary := range summaries {
		views = append(views, githubAppView(summary))
	}
	g.respondWithJSON(w, http.StatusOK, map[string]any{"apps": views})
}

func (g *githubAppRoutes) findSummary(ctx context.Context, appID int64) (*githubapp.AppSummary, error) {
	summaries, err := g.service.Summaries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range summaries {
		if summaries[i].App.AppID == appID {
			return &summaries[i], nil
		}
	}
	return nil, store.ErrNotFound
}

func (g *githubAppRoutes) show(w http.ResponseWriter, r *http.Request, appID int64) {
	summary, err := g.findSummary(r.Context(), appID)
	if err != nil {
		g.mapError(w, err)
		return
	}
	g.respondWithJSON(w, http.StatusOK, githubAppView(*summary))
}

func (g *githubAppRoutes) recommended(w http.ResponseWriter, r *http.Request) {
	summary, err := g.service.Recommended(r.Context())
	if err != nil {
		g.mapError(w, err)
		return
	}
	g.respondWithJSON(w, http.StatusOK, githubAppView(*summary))
}

type orgByNameOrID interface {
	GetOrganizationByName(ctx context.Context, name string) (*models.Organization, error)
	GetOrganizationByID(ctx context.Context, orgID string) (*models.Organization, error)
}

func (g *githubAppRoutes) resolveOrg(ctx context.Context, ref string) (*models.Organization, error) {
	orgs, ok := g.store.(orgByNameOrID)
	if !ok {
		return nil, errors.New("organizations are not available")
	}
	if org, err := orgs.GetOrganizationByName(ctx, ref); err == nil && org != nil {
		return org, nil
	}
	org, err := orgs.GetOrganizationByID(ctx, ref)
	if err != nil || org == nil {
		return nil, errors.New("organization " + strconv.Quote(ref) + " not found")
	}
	return org, nil
}

func (g *githubAppRoutes) register(w http.ResponseWriter, r *http.Request) {
	var req githubAppRegisterRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		g.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	input := githubapp.RegisterInput{AppID: req.AppID, PrivateKeyPEM: []byte(req.PrivateKey), WebhookSecret: req.WebhookSecret,
		Slug: req.Slug, Name: req.Name, ClientID: req.ClientID, Verify: req.Verify == nil || *req.Verify, Replace: req.Replace,
		OpenRegistration: req.OpenRegistration}
	if req.OpenRegistration != nil && *req.OpenRegistration && req.DefaultOrg == "" && !req.Replace {
		g.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input",
			Message: "open_registration needs default_org: open registration enables projects only in an explicit organization"})
		return
	}
	if req.DefaultOrg != "" {
		org, err := g.resolveOrg(r.Context(), req.DefaultOrg)
		if err != nil {
			g.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: err.Error()})
			return
		}
		input.DefaultOrgID = &org.OrgID
	}
	app, err := g.service.RegisterApp(r.Context(), input)
	if err != nil {
		g.mapError(w, err)
		return
	}
	action := "github_app.registered"
	status := http.StatusCreated
	if req.Replace {
		action, status = "github_app.credentials_replaced", http.StatusOK
	}
	audit.Record(r.Context(), g.store, "", action, "github_app", strconv.FormatInt(app.AppID, 10),
		models.JSONB{"app_id": app.AppID, "slug": app.Slug, "verified": input.Verify, "open_registration": app.OpenRegistration})
	summary, err := g.findSummary(r.Context(), app.AppID)
	if err != nil {
		g.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	g.respondWithJSON(w, status, githubAppView(*summary))
}

func (g *githubAppRoutes) setEnabled(w http.ResponseWriter, r *http.Request, appID int64, enabled bool) {
	if err := g.service.SetAppEnabled(r.Context(), appID, enabled); err != nil {
		g.mapError(w, err)
		return
	}
	action := "github_app.disabled"
	if enabled {
		action = "github_app.enabled"
	}
	audit.Record(r.Context(), g.store, "", action, "github_app", strconv.FormatInt(appID, 10), models.JSONB{"app_id": appID})
	g.show(w, r, appID)
}

func (g *githubAppRoutes) sync(w http.ResponseWriter, r *http.Request, appID int64) {
	result, err := g.service.SyncApp(r.Context(), appID)
	if err != nil {
		g.mapError(w, err)
		return
	}
	g.respondWithJSON(w, http.StatusOK, result)
}

func (g *githubAppRoutes) installations(w http.ResponseWriter, r *http.Request, appID int64) {
	views, err := githubInstallationViews(r.Context(), g.service.Store(), appID, time.Now())
	if err != nil {
		g.mapError(w, err)
		return
	}
	g.respondWithJSON(w, http.StatusOK, map[string]any{"installations": views})
}

// githubInstallationViews lists the installations of one App (or of all
// Apps for appID 0) with their repositories.
func githubInstallationViews(ctx context.Context, apps store.GitHubAppStore, appID int64, now time.Time) ([]GitHubInstallationView, error) {
	if appID > 0 {
		if _, err := apps.GetGitHubApp(ctx, appID); err != nil {
			return nil, err
		}
	}
	installations, err := apps.ListGitHubAppInstallations(ctx, appID)
	if err != nil {
		return nil, err
	}
	views := make([]GitHubInstallationView, 0, len(installations))
	for _, installation := range installations {
		repos, err := apps.ListGitHubAppRepositories(ctx, installation.InstallationID)
		if err != nil {
			return nil, err
		}
		if repos == nil {
			repos = []models.GitHubAppRepository{}
		}
		views = append(views, GitHubInstallationView{GitHubAppInstallation: installation,
			Headroom: installation.RateLimit.Headroom(now), Repositories: repos})
	}
	return views, nil
}

func (g *githubAppRoutes) listAccountOrgs(w http.ResponseWriter, r *http.Request) {
	rows, err := g.service.Store().ListGitHubAccountOrgs(r.Context())
	if err != nil {
		g.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	if rows == nil {
		rows = []models.GitHubAccountOrg{}
	}
	g.respondWithJSON(w, http.StatusOK, map[string]any{"account_orgs": rows})
}

func (g *githubAppRoutes) setAccountOrg(w http.ResponseWriter, r *http.Request, login string) {
	var req githubAccountOrgRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Org) == "" || strings.TrimSpace(login) == "" {
		g.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	org, err := g.resolveOrg(r.Context(), req.Org)
	if err != nil {
		g.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: err.Error()})
		return
	}
	if err := g.service.Store().SetGitHubAccountOrg(r.Context(), login, org.OrgID); err != nil {
		g.mapError(w, err)
		return
	}
	audit.Record(r.Context(), g.store, org.OrgID, "github_app.account_mapped", "organization", org.OrgID,
		models.JSONB{"account": strings.ToLower(login)})
	row, err := g.service.Store().GetGitHubAccountOrg(r.Context(), login)
	if err != nil {
		g.mapError(w, err)
		return
	}
	g.respondWithJSON(w, http.StatusOK, row)
}

func (g *githubAppRoutes) deleteAccountOrg(w http.ResponseWriter, r *http.Request, login string) {
	deleted, err := g.service.Store().DeleteGitHubAccountOrg(r.Context(), login)
	if err != nil {
		g.mapError(w, err)
		return
	}
	if !deleted {
		g.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	audit.Record(r.Context(), g.store, "", "github_app.account_unmapped", "github_account", strings.ToLower(login),
		models.JSONB{"account": strings.ToLower(login)})
	w.WriteHeader(http.StatusNoContent)
}
