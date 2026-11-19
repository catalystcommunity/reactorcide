package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/transportsecurity"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/sirupsen/logrus"
)

// Crypto encrypts the App secrets with the master keys (internal/secrets).
type Crypto interface {
	EncryptWithPrimary(plaintext []byte) (keyName string, ciphertext []byte, err error)
	DecryptWithKey(keyName string, ciphertext []byte) ([]byte, error)
}

// Options configure a Service.
type Options struct {
	Store  store.GitHubAppStore
	Crypto Crypto
	// Projects registers projects for repositories (registration.go). nil
	// turns self-service registration off.
	Projects ProjectRegistry
	// APIBaseURL is the GitHub REST API. Empty: https://api.github.com.
	APIBaseURL string
	// AllowInsecureTransport is the development-only exception of
	// internal/transportsecurity (an httptest server in tests).
	AllowInsecureTransport bool
	// Transport is the base HTTP transport. nil: the default.
	Transport http.RoundTripper
	// RateLimitWriteInterval: how often a rate-limit snapshot of one target
	// is written. Zero: 30 seconds. Negative: after each response (tests).
	RateLimitWriteInterval time.Duration
	Now                    func() time.Time
	Logger                 *logrus.Logger
}

// Service is the App pool. It implements vcs.AppInstallationSource and
// vcs.InstallationTokenProvider.
type Service struct {
	store     store.GitHubAppStore
	crypto    Crypto
	projects  ProjectRegistry
	baseURL   string
	insecure  bool
	transport http.RoundTripper
	now       func() time.Time
	logger    *logrus.Logger
	tokens    *tokenCache
	runs      *checkRunCache
	rates     *rateLimitRecorder
}

var _ vcs.AppInstallationSource = (*Service)(nil)
var _ vcs.InstallationTokenProvider = (*Service)(nil)

// NewService validates the options and returns the pool.
func NewService(opts Options) (*Service, error) {
	if opts.Store == nil || opts.Crypto == nil {
		return nil, errors.New("a GitHub App store and master keys are required")
	}
	baseURL := strings.TrimSuffix(opts.APIBaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	if err := transportsecurity.ValidateURL(baseURL, opts.AllowInsecureTransport, "GitHub App API"); err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = logrus.New()
		logger.SetFormatter(&logrus.JSONFormatter{})
	}
	interval := opts.RateLimitWriteInterval
	if interval == 0 {
		interval = 30 * time.Second
	}
	return &Service{store: opts.Store, crypto: opts.Crypto, projects: opts.Projects, baseURL: baseURL,
		insecure: opts.AllowInsecureTransport, transport: opts.Transport, now: now, logger: logger,
		tokens: newTokenCache(), runs: newCheckRunCache(), rates: newRateLimitRecorder(opts.Store, interval)}, nil
}

// Store returns the App store of the pool.
func (s *Service) Store() store.GitHubAppStore { return s.store }

func (s *Service) httpClient(target rateLimitTarget) *http.Client {
	transport := &observingTransport{base: s.transport, target: target, record: s.rates.observe, nowFunc: s.now}
	return transportsecurity.HTTPClient(&http.Client{Transport: transport, Timeout: 30 * time.Second}, s.insecure, "GitHub App API")
}

func (s *Service) apiFor(target rateLimitTarget) *api {
	return &api{baseURL: s.baseURL, client: s.httpClient(target)}
}

// --- App registry -----------------------------------------------------

// RegisterInput is a new App or a credential replacement.
type RegisterInput struct {
	AppID         int64
	PrivateKeyPEM []byte
	WebhookSecret string
	Slug          string
	Name          string
	ClientID      string
	DefaultOrgID  *string
	// OpenRegistration enables the new projects that go into DefaultOrgID
	// (no account mapping). nil on a replacement keeps the current value;
	// nil on a new App is false.
	OpenRegistration *bool
	// Verify calls GET /app with the key and fills slug, name, and client
	// ID from GitHub.
	Verify bool
	// Replace updates a registered App (key or secret rotation).
	Replace bool
}

// RegisterApp stores an App. The key and secret are encrypted. They are not
// in the returned record or in any error.
func (s *Service) RegisterApp(ctx context.Context, in RegisterInput) (*models.GitHubApp, error) {
	if in.AppID <= 0 {
		return nil, fmt.Errorf("app_id must be a positive number")
	}
	if strings.TrimSpace(in.WebhookSecret) == "" {
		return nil, fmt.Errorf("a webhook secret is required")
	}
	key, err := ParsePrivateKey(in.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	app := &models.GitHubApp{AppID: in.AppID, Slug: in.Slug, Name: in.Name, ClientID: in.ClientID,
		DefaultOrgID: in.DefaultOrgID, Enabled: true}
	if in.OpenRegistration != nil {
		app.OpenRegistration = *in.OpenRegistration
	}
	if in.Verify {
		appJWT, err := AppJWT(in.AppID, key, s.now())
		if err != nil {
			return nil, err
		}
		info, err := s.apiFor(rateLimitTarget{appID: in.AppID}).getApp(ctx, appJWT)
		if err != nil {
			return nil, fmt.Errorf("GitHub did not accept the App key: %w", err)
		}
		if info.ID != 0 && info.ID != in.AppID {
			return nil, fmt.Errorf("the key belongs to App %d, not to App %d", info.ID, in.AppID)
		}
		app.Slug, app.Name, app.ClientID = firstNonEmpty(in.Slug, info.Slug), firstNonEmpty(in.Name, info.Name), firstNonEmpty(in.ClientID, info.ClientID)
	}
	if app.PrivateKeyKeyName, app.PrivateKeyCiphertext, err = s.crypto.EncryptWithPrimary(in.PrivateKeyPEM); err != nil {
		return nil, fmt.Errorf("encrypting the private key: %w", err)
	}
	if app.WebhookSecretKeyName, app.WebhookSecretCiphertext, err = s.crypto.EncryptWithPrimary([]byte(strings.TrimSpace(in.WebhookSecret))); err != nil {
		return nil, fmt.Errorf("encrypting the webhook secret: %w", err)
	}
	if in.Replace {
		existing, err := s.store.GetGitHubApp(ctx, in.AppID)
		if err != nil {
			return nil, err
		}
		app.Enabled = existing.Enabled
		if in.DefaultOrgID == nil {
			app.DefaultOrgID = existing.DefaultOrgID
		}
		if in.OpenRegistration == nil {
			app.OpenRegistration = existing.OpenRegistration
		}
		app.Slug, app.Name, app.ClientID = firstNonEmpty(app.Slug, existing.Slug), firstNonEmpty(app.Name, existing.Name), firstNonEmpty(app.ClientID, existing.ClientID)
		if err := s.store.UpdateGitHubApp(ctx, app); err != nil {
			return nil, err
		}
		s.tokens.forgetAll()
		return s.store.GetGitHubApp(ctx, in.AppID)
	}
	if err := s.store.CreateGitHubApp(ctx, app); err != nil {
		return nil, err
	}
	return s.store.GetGitHubApp(ctx, in.AppID)
}

// SetAppEnabled enables or disables an App. A disabled App gives no tokens:
// its projects use their token path and Commit Status again.
func (s *Service) SetAppEnabled(ctx context.Context, appID int64, enabled bool) error {
	if err := s.store.SetGitHubAppEnabled(ctx, appID, enabled); err != nil {
		return err
	}
	s.tokens.forgetAll()
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// WebhookSecret returns the webhook secret of an enabled App. ok is false
// for an unknown or disabled App.
func (s *Service) WebhookSecret(ctx context.Context, appID int64) (secret string, ok bool, err error) {
	app, err := s.store.GetGitHubApp(ctx, appID)
	if errors.Is(err, store.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !app.Enabled {
		return "", false, nil
	}
	plaintext, err := s.crypto.DecryptWithKey(app.WebhookSecretKeyName, app.WebhookSecretCiphertext)
	if err != nil {
		return "", false, fmt.Errorf("decrypting the webhook secret of App %d: %w", appID, err)
	}
	return string(plaintext), true, nil
}

func (s *Service) appJWT(app *models.GitHubApp) (string, error) {
	pemBytes, err := s.crypto.DecryptWithKey(app.PrivateKeyKeyName, app.PrivateKeyCiphertext)
	if err != nil {
		return "", fmt.Errorf("decrypting the private key of App %d: %w", app.AppID, err)
	}
	key, err := ParsePrivateKey(pemBytes)
	if err != nil {
		return "", err
	}
	return AppJWT(app.AppID, key, s.now())
}

// --- Installation tokens ----------------------------------------------

// installationToken returns a cached or new token of the installation.
// checkoutRepo, when set, narrows the token to that one repository with
// read-only contents.
func (s *Service) installationToken(ctx context.Context, app *models.GitHubApp, installation *models.GitHubAppInstallation, checkoutRepo string) (string, error) {
	key := tokenKey{installationID: installation.InstallationID, scope: "system"}
	wanted := SystemPermissions
	request := tokenRequest{}
	if checkoutRepo != "" {
		key.scope = "checkout:" + strings.ToLower(checkoutRepo)
		wanted = CheckoutPermissions
		if _, name, ok := strings.Cut(checkoutRepo, "/"); ok && name != "" {
			request.Repositories = []string{name}
		}
	}
	request.Permissions = narrowPermissions(wanted, installation.Permissions)
	return s.tokens.getOrFetch(ctx, key, s.now, func(ctx context.Context) (string, time.Time, error) {
		appJWT, err := s.appJWT(app)
		if err != nil {
			return "", time.Time{}, err
		}
		token, err := s.apiFor(rateLimitTarget{appID: app.AppID}).createInstallationToken(ctx, appJWT, installation.InstallationID, request)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("creating an installation token for installation %d: %w", installation.InstallationID, err)
		}
		expiresAt := token.ExpiresAt
		if expiresAt.IsZero() {
			// GitHub tokens live one hour.
			expiresAt = s.now().Add(time.Hour)
		}
		return token.Token, expiresAt, nil
	})
}

func (s *Service) newAppClient(app *models.GitHubApp, installation *models.GitHubAppInstallation, token string) (*AppClient, error) {
	target := rateLimitTarget{installationID: installation.InstallationID}
	base, err := vcs.NewGitHubClient(vcs.Config{Provider: vcs.GitHub, Token: token, BaseURL: s.baseURL,
		AllowInsecureTransport: s.insecure,
		Transport:              &observingTransport{base: s.transport, target: target, record: s.rates.observe, nowFunc: s.now}})
	if err != nil {
		return nil, err
	}
	return &AppClient{GitHubClient: base, appID: app.AppID, installationID: installation.InstallationID, token: token,
		api: s.apiFor(target), runs: s.runs, now: s.now}, nil
}

// ClientForRepo implements vcs.AppInstallationSource.
func (s *Service) ClientForRepo(ctx context.Context, provider vcs.Provider, repoFullName string) (vcs.Client, bool, error) {
	if provider != vcs.GitHub {
		return nil, false, nil
	}
	binding, err := s.store.ResolveGitHubAppBinding(ctx, repoFullName)
	if err != nil || binding == nil {
		return nil, false, err
	}
	token, err := s.installationToken(ctx, &binding.App, &binding.Installation, "")
	if err != nil {
		s.logger.WithError(err).WithField("repo", repoFullName).Warn("Could not get a GitHub App installation token")
		return nil, false, err
	}
	client, err := s.newAppClient(&binding.App, &binding.Installation, token)
	if err != nil {
		return nil, false, err
	}
	return client, true, nil
}

// CheckoutToken implements vcs.AppInstallationSource.
// The binding must belong to projectID: a job names its source URL, so
// without this check any job could get a token for a private repository of
// another organization that an installation holds.
func (s *Service) CheckoutToken(ctx context.Context, provider vcs.Provider, repoFullName, projectID string) (string, bool, error) {
	if provider != vcs.GitHub || strings.TrimSpace(projectID) == "" {
		return "", false, nil
	}
	binding, err := s.store.ResolveGitHubAppBinding(ctx, repoFullName)
	if err != nil || binding == nil {
		return "", false, err
	}
	if binding.Repository.ProjectID == nil || *binding.Repository.ProjectID != projectID {
		s.logger.WithFields(logrus.Fields{"repo": repoFullName, "project_id": projectID}).
			Warn("GitHub App checkout token refused: the repository binding belongs to another project")
		return "", false, nil
	}
	token, err := s.installationToken(ctx, &binding.App, &binding.Installation, repoFullName)
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}

// AccountEntitlements says which GitHub accounts an organization can use
// installation tokens of. StoreRegistry implements it.
type AccountEntitlements interface {
	// ProjectOrgID returns the owning organization of a project, or "".
	ProjectOrgID(ctx context.Context, projectID string) (string, error)
	// VCSOwnerAllowlist returns the VCS owner allowlist of an organization.
	VCSOwnerAllowlist(ctx context.Context, orgID string) ([]string, error)
}

// entitledInstallation reports whether orgID can use the installation: the
// account is in the VCS owner allowlist of the organization, or the
// installation holds a repository of a project of the organization.
func (s *Service) entitledInstallation(ctx context.Context, entitlements AccountEntitlements, orgID string, allowlisted bool, installation models.GitHubAppInstallation) (bool, error) {
	if allowlisted {
		return true, nil
	}
	repos, err := s.store.ListGitHubAppRepositories(ctx, installation.InstallationID)
	if err != nil {
		return false, err
	}
	for _, repo := range repos {
		if repo.ProjectID == nil {
			continue
		}
		owner, err := entitlements.ProjectOrgID(ctx, *repo.ProjectID)
		if err != nil {
			return false, err
		}
		if owner != "" && owner == orgID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) accountInstallation(ctx context.Context, accountLogin, orgID string) (*models.GitHubApp, *models.GitHubAppInstallation, error) {
	entitlements, ok := s.projects.(AccountEntitlements)
	if !ok || strings.TrimSpace(orgID) == "" || strings.TrimSpace(accountLogin) == "" {
		// No way to check the entitlement: serve nothing (fail closed).
		return nil, nil, nil
	}
	installations, err := s.store.ListActiveGitHubAppInstallationsForAccount(ctx, accountLogin)
	if err != nil || len(installations) == 0 {
		return nil, nil, err
	}
	allowlist, err := entitlements.VCSOwnerAllowlist(ctx, orgID)
	if err != nil {
		return nil, nil, err
	}
	allowlisted := false
	for _, owner := range allowlist {
		if strings.EqualFold(strings.TrimSpace(owner), accountLogin) {
			allowlisted = true
			break
		}
	}
	var chosen *models.GitHubAppInstallation
	for i := range installations {
		candidate := installations[i]
		entitled, err := s.entitledInstallation(ctx, entitlements, orgID, allowlisted, candidate)
		if err != nil {
			return nil, nil, err
		}
		if !entitled {
			continue
		}
		// Prefer an installation that granted members (team reads).
		if chosen == nil || (chosen.Permissions["members"] == "" && candidate.Permissions["members"] != "") {
			chosen = &candidate
		}
	}
	if chosen == nil {
		s.logger.WithFields(logrus.Fields{"account": accountLogin, "org_id": orgID}).
			Info("No GitHub App installation on the account that the organization is entitled to")
		return nil, nil, nil
	}
	app, err := s.store.GetGitHubApp(ctx, chosen.AppID)
	if err != nil {
		return nil, nil, err
	}
	return app, chosen, nil
}

// InstallationTokenForAccount implements vcs.InstallationTokenProvider.
func (s *Service) InstallationTokenForAccount(ctx context.Context, provider vcs.Provider, accountLogin, orgID string) (string, bool, error) {
	if provider != vcs.GitHub {
		return "", false, nil
	}
	app, installation, err := s.accountInstallation(ctx, accountLogin, orgID)
	if err != nil || installation == nil {
		return "", false, err
	}
	token, err := s.installationToken(ctx, app, installation, "")
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}

// ClientForAccount implements vcs.InstallationTokenProvider.
func (s *Service) ClientForAccount(ctx context.Context, provider vcs.Provider, accountLogin, orgID string) (vcs.Client, bool, error) {
	if provider != vcs.GitHub {
		return nil, false, nil
	}
	app, installation, err := s.accountInstallation(ctx, accountLogin, orgID)
	if err != nil || installation == nil {
		return nil, false, err
	}
	token, err := s.installationToken(ctx, app, installation, "")
	if err != nil {
		return nil, false, err
	}
	client, err := s.newAppClient(app, installation, token)
	if err != nil {
		return nil, false, err
	}
	return client, true, nil
}

// --- Headroom ---------------------------------------------------------

// AppSummary is an App with its headroom, for the REST API, the CLI, and
// the UI.
type AppSummary struct {
	App         models.GitHubApp `json:"app"`
	Headroom    AppHeadroom      `json:"headroom"`
	InstallURL  string           `json:"install_url,omitempty"`
	Recommended bool             `json:"recommended"`
}

// Summaries returns every App with its headroom. Recommended marks the App
// that new installations should use (SelectApp).
func (s *Service) Summaries(ctx context.Context) ([]AppSummary, error) {
	apps, err := s.store.ListGitHubApps(ctx)
	if err != nil {
		return nil, err
	}
	installations, err := s.store.ListGitHubAppInstallations(ctx, 0)
	if err != nil {
		return nil, err
	}
	now := s.now()
	headroom := ComputeHeadroom(apps, installations, now)
	selected, _, ok := SelectApp(apps, installations, now)
	out := make([]AppSummary, 0, len(apps))
	for _, app := range apps {
		out = append(out, AppSummary{App: app, Headroom: headroom[app.AppID], InstallURL: InstallURL(app),
			Recommended: ok && app.AppID == selected.AppID})
	}
	return out, nil
}

// Recommended returns the App that new repository registrations should use.
func (s *Service) Recommended(ctx context.Context) (*AppSummary, error) {
	summaries, err := s.Summaries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range summaries {
		if summaries[i].Recommended {
			return &summaries[i], nil
		}
	}
	return nil, store.ErrNotFound
}
