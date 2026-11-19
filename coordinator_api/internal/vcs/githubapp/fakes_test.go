package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// fakeCrypto "encrypts" by prefixing; enough to prove the service never
// stores plaintext under the plaintext column and decrypts with the key name.
type fakeCrypto struct{}

func (fakeCrypto) EncryptWithPrimary(plaintext []byte) (string, []byte, error) {
	return "k1", append([]byte("enc:"), plaintext...), nil
}

func (fakeCrypto) DecryptWithKey(name string, ciphertext []byte) ([]byte, error) {
	if name != "k1" || !strings.HasPrefix(string(ciphertext), "enc:") {
		return nil, fmt.Errorf("bad key")
	}
	return ciphertext[4:], nil
}

type fakeStore struct {
	mu            sync.Mutex
	apps          map[int64]*models.GitHubApp
	installations map[int64]*models.GitHubAppInstallation
	repos         map[string]*models.GitHubAppRepository
	accounts      map[string]string
	prs           map[string]int
	appRates      map[int64]models.RateLimitSnapshot
	instRates     map[int64]models.RateLimitSnapshot
}

var _ store.GitHubAppStore = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{apps: map[int64]*models.GitHubApp{}, installations: map[int64]*models.GitHubAppInstallation{},
		repos: map[string]*models.GitHubAppRepository{}, accounts: map[string]string{}, prs: map[string]int{},
		appRates: map[int64]models.RateLimitSnapshot{}, instRates: map[int64]models.RateLimitSnapshot{}}
}

func (f *fakeStore) CreateGitHubApp(_ context.Context, app *models.GitHubApp) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.apps[app.AppID]; ok {
		return store.ErrAlreadyExists
	}
	copy := *app
	f.apps[app.AppID] = &copy
	return nil
}

func (f *fakeStore) GetGitHubApp(_ context.Context, appID int64) (*models.GitHubApp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	app, ok := f.apps[appID]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *app
	return &copy, nil
}

func (f *fakeStore) ListGitHubApps(_ context.Context) ([]models.GitHubApp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.GitHubApp
	for _, app := range f.apps {
		out = append(out, *app)
	}
	return out, nil
}

func (f *fakeStore) UpdateGitHubApp(_ context.Context, app *models.GitHubApp) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.apps[app.AppID]; !ok {
		return store.ErrNotFound
	}
	copy := *app
	f.apps[app.AppID] = &copy
	return nil
}

func (f *fakeStore) SetGitHubAppEnabled(_ context.Context, appID int64, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	app, ok := f.apps[appID]
	if !ok {
		return store.ErrNotFound
	}
	app.Enabled = enabled
	return nil
}

func (f *fakeStore) UpsertGitHubAppInstallation(_ context.Context, installation *models.GitHubAppInstallation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	copy := *installation
	if existing, ok := f.installations[installation.InstallationID]; ok {
		copy.RateLimit = existing.RateLimit
	}
	f.installations[installation.InstallationID] = &copy
	return nil
}

func (f *fakeStore) GetGitHubAppInstallation(_ context.Context, id int64) (*models.GitHubAppInstallation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	installation, ok := f.installations[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *installation
	return &copy, nil
}

func (f *fakeStore) ListGitHubAppInstallations(_ context.Context, appID int64) ([]models.GitHubAppInstallation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.GitHubAppInstallation
	for _, installation := range f.installations {
		if appID > 0 && installation.AppID != appID {
			continue
		}
		copy := *installation
		for _, repo := range f.repos {
			if repo.InstallationID == installation.InstallationID && repo.RemovedAt == nil {
				copy.RepositoryCount++
			}
		}
		out = append(out, copy)
	}
	return out, nil
}

func (f *fakeStore) SetGitHubAppInstallationSuspended(_ context.Context, id int64, suspended bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	installation, ok := f.installations[id]
	if !ok {
		return store.ErrNotFound
	}
	installation.Suspended = suspended
	return nil
}

func (f *fakeStore) MarkGitHubAppInstallationDeleted(_ context.Context, id int64, at time.Time) ([]models.GitHubAppRepository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	installation, ok := f.installations[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	installation.DeletedAt = &at
	var removed []models.GitHubAppRepository
	for _, repo := range f.repos {
		if repo.InstallationID == id && repo.RemovedAt == nil {
			repo.RemovedAt = &at
			removed = append(removed, *repo)
		}
	}
	return removed, nil
}

func (f *fakeStore) activeLocked(repo *models.GitHubAppRepository) bool {
	if repo.RemovedAt != nil {
		return false
	}
	installation, ok := f.installations[repo.InstallationID]
	if !ok || !installation.Active() {
		return false
	}
	app, ok := f.apps[installation.AppID]
	return ok && app.Enabled
}

func (f *fakeStore) BindGitHubAppRepository(_ context.Context, repo *models.GitHubAppRepository) (models.GitHubAppRepository, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := models.NormalizeGitHubRepoFullName(repo.RepoFullName)
	existing, ok := f.repos[name]
	if !ok {
		copy := *repo
		copy.RepoFullName = name
		f.repos[name] = &copy
		return copy, true, nil
	}
	if existing.InstallationID != repo.InstallationID && f.activeLocked(existing) {
		return *existing, false, nil
	}
	existing.InstallationID = repo.InstallationID
	existing.Private = repo.Private
	existing.RemovedAt = nil
	return *existing, true, nil
}

func (f *fakeStore) SetGitHubAppRepositoryProject(_ context.Context, name, projectID string, auto bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	repo, ok := f.repos[models.NormalizeGitHubRepoFullName(name)]
	if !ok {
		return store.ErrNotFound
	}
	id := projectID
	repo.ProjectID, repo.AutoRegistered, repo.DisabledByRemoval = &id, auto, false
	return nil
}

func (f *fakeStore) SetGitHubAppRepositoryPending(_ context.Context, name string, pending bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	repo, ok := f.repos[models.NormalizeGitHubRepoFullName(name)]
	if !ok {
		return store.ErrNotFound
	}
	repo.PendingApproval = pending
	return nil
}

func (f *fakeStore) RemoveGitHubAppRepository(_ context.Context, installationID int64, name string, disabled bool, at time.Time) (models.GitHubAppRepository, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	repo, ok := f.repos[models.NormalizeGitHubRepoFullName(name)]
	if !ok || repo.InstallationID != installationID {
		return models.GitHubAppRepository{}, false, nil
	}
	repo.RemovedAt, repo.DisabledByRemoval = &at, disabled
	return *repo, true, nil
}

func (f *fakeStore) ListGitHubAppRepositories(_ context.Context, installationID int64) ([]models.GitHubAppRepository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.GitHubAppRepository
	for _, repo := range f.repos {
		if repo.InstallationID == installationID && repo.RemovedAt == nil {
			out = append(out, *repo)
		}
	}
	return out, nil
}

func (f *fakeStore) ResolveGitHubAppBinding(_ context.Context, name string) (*models.GitHubAppBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	repo, ok := f.repos[models.NormalizeGitHubRepoFullName(name)]
	if !ok || !f.activeLocked(repo) {
		return nil, nil
	}
	installation := f.installations[repo.InstallationID]
	return &models.GitHubAppBinding{App: *f.apps[installation.AppID], Installation: *installation, Repository: *repo}, nil
}

func (f *fakeStore) ListActiveGitHubAppInstallationsForAccount(_ context.Context, login string) ([]models.GitHubAppInstallation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.GitHubAppInstallation
	for _, installation := range f.installations {
		app, ok := f.apps[installation.AppID]
		if strings.EqualFold(installation.AccountLogin, login) && installation.Active() && ok && app.Enabled {
			out = append(out, *installation)
		}
	}
	return out, nil
}

func (f *fakeStore) RecordGitHubAppRateLimit(_ context.Context, appID int64, s models.RateLimitSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appRates[appID] = s
	if app, ok := f.apps[appID]; ok {
		app.RateLimit = s
	}
	return nil
}

func (f *fakeStore) RecordGitHubInstallationRateLimit(_ context.Context, id int64, s models.RateLimitSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instRates[id] = s
	if installation, ok := f.installations[id]; ok {
		installation.RateLimit = s
	}
	return nil
}

func (f *fakeStore) LatestPRNumberForCommit(_ context.Context, projectID, sha string) (int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.prs[projectID+"/"+sha]
	return n, ok, nil
}

func (f *fakeStore) SetGitHubAccountOrg(_ context.Context, login, orgID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[strings.ToLower(login)] = orgID
	return nil
}

func (f *fakeStore) DeleteGitHubAccountOrg(_ context.Context, login string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.accounts[strings.ToLower(login)]
	delete(f.accounts, strings.ToLower(login))
	return ok, nil
}

func (f *fakeStore) GetGitHubAccountOrg(_ context.Context, login string) (*models.GitHubAccountOrg, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	org, ok := f.accounts[strings.ToLower(login)]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &models.GitHubAccountOrg{AccountLogin: strings.ToLower(login), OrgID: org}, nil
}

func (f *fakeStore) ListGitHubAccountOrgs(_ context.Context) ([]models.GitHubAccountOrg, error) {
	return nil, nil
}

// fakeRegistry is an in-memory ProjectRegistry.
type fakeRegistry struct {
	mu         sync.Mutex
	projects   map[string]*models.Project
	defaultOrg string
	accounts   *fakeStore
	next       int
	// allowlists are the VCS owner allowlists, by organization.
	allowlists map[string][]string
}

func (r *fakeRegistry) ProjectOrgID(_ context.Context, projectID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, project := range r.projects {
		if project.ProjectID == projectID {
			return project.OwnershipOrgID(), nil
		}
	}
	return "", nil
}

func (r *fakeRegistry) VCSOwnerAllowlist(_ context.Context, orgID string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allowlists[orgID], nil
}

func newFakeRegistry(apps *fakeStore) *fakeRegistry {
	return &fakeRegistry{projects: map[string]*models.Project{}, defaultOrg: "00000000-0000-0000-0000-00000000d0d0", accounts: apps}
}

func (r *fakeRegistry) FindProject(_ context.Context, url string) (*models.Project, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	project, ok := r.projects[url]
	if !ok {
		return nil, nil
	}
	copy := *project
	return &copy, nil
}

func (r *fakeRegistry) CreateProject(_ context.Context, project *models.Project) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	project.ProjectID = fmt.Sprintf("00000000-0000-0000-0000-%012d", r.next)
	copy := *project
	r.projects[project.RepoURL] = &copy
	return nil
}

func (r *fakeRegistry) SetProjectEnabled(_ context.Context, project *models.Project, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projects[project.RepoURL].Enabled = enabled
	project.Enabled = enabled
	return nil
}

func (r *fakeRegistry) SetProjectRepositoryID(_ context.Context, project *models.Project, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value := id
	r.projects[project.RepoURL].VCSRepositoryID = &value
	project.VCSRepositoryID = &value
	return nil
}

func (r *fakeRegistry) OrganizationFor(ctx context.Context, app *models.GitHubApp, login string) (string, OrgSource, error) {
	if mapping, err := r.accounts.GetGitHubAccountOrg(ctx, login); err == nil {
		return mapping.OrgID, OrgSourceAccountMapping, nil
	}
	if app != nil && app.DefaultOrgID != nil {
		return *app.DefaultOrgID, OrgSourceAppDefault, nil
	}
	return r.defaultOrg, OrgSourceDeploymentDefault, nil
}

func (r *fakeRegistry) project(url string) *models.Project {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.projects[url]
}

// testKey is an RSA key in GitHub's PKCS#1 PEM form.
func testKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// fakeGitHub is an httptest GitHub API.
type fakeGitHub struct {
	t           *testing.T
	server      *httptest.Server
	mu          sync.Mutex
	tokenCalls  int
	tokenBodies []tokenRequest
	tokenTTL    time.Duration
	now         func() time.Time
	auth        []string
	requests    []recorded
	checkRuns   []map[string]any
	nextRunID   int64
	listRuns    []checkRunRecord
	repos       map[int64][]Repository
	installs    []InstallationInfo
	remaining   int
}

type recorded struct {
	Method, Path string
	Body         map[string]any
}

func newFakeGitHub(t *testing.T, now func() time.Time) *fakeGitHub {
	f := &fakeGitHub{t: t, tokenTTL: time.Hour, now: now, nextRunID: 100, repos: map[int64][]Repository{}, remaining: 4999}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	f.requests = append(f.requests, recorded{Method: r.Method, Path: r.URL.Path, Body: decoded})
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", fmt.Sprint(f.remaining))
	w.Header().Set("X-RateLimit-Used", fmt.Sprint(5000-f.remaining))
	w.Header().Set("X-RateLimit-Reset", fmt.Sprint(f.now().Add(30*time.Minute).Unix()))
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/app":
		_ = json.NewEncoder(w).Encode(AppInfo{ID: 42, Slug: "reactorcide-a", Name: "Reactorcide A", ClientID: "Iv1.abc"})
	case r.URL.Path == "/app/installations":
		_ = json.NewEncoder(w).Encode(f.installs)
	case strings.HasSuffix(r.URL.Path, "/access_tokens"):
		f.tokenCalls++
		var request tokenRequest
		_ = json.Unmarshal(body, &request)
		f.tokenBodies = append(f.tokenBodies, request)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(InstallationToken{Token: fmt.Sprintf("ghs_token_%d", f.tokenCalls), ExpiresAt: f.now().Add(f.tokenTTL)})
	case r.URL.Path == "/installation/repositories":
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "token ")
		var repos []Repository
		for _, list := range f.repos {
			if token != "" {
				repos = append(repos, list...)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"repositories": repos})
	case strings.Contains(r.URL.Path, "/check-runs") && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": f.listRuns})
	case strings.HasSuffix(r.URL.Path, "/check-runs") && r.Method == http.MethodPost:
		f.nextRunID++
		f.checkRuns = append(f.checkRuns, decoded)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": f.nextRunID, "status": decoded["status"]})
	case strings.Contains(r.URL.Path, "/check-runs/") && r.Method == http.MethodPatch:
		f.checkRuns = append(f.checkRuns, decoded)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": f.nextRunID, "status": decoded["status"]})
	case strings.Contains(r.URL.Path, "/statuses/"):
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}
}

func (f *fakeGitHub) snapshot() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.requests...)
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newTestService registers App 42 and returns the pool, its store, and the
// fake API.
func newTestService(t *testing.T) (*Service, *fakeStore, *fakeRegistry, *fakeGitHub, *testClock) {
	t.Helper()
	clock := &testClock{now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	api := newFakeGitHub(t, clock.Now)
	apps := newFakeStore()
	registry := newFakeRegistry(apps)
	service, err := NewService(Options{Store: apps, Crypto: fakeCrypto{}, Projects: registry, APIBaseURL: api.server.URL,
		AllowInsecureTransport: true, Now: clock.Now, RateLimitWriteInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	_, pemBytes := testKey(t)
	if _, err := service.RegisterApp(context.Background(), RegisterInput{AppID: 42, PrivateKeyPEM: pemBytes,
		WebhookSecret: "hook-secret", Verify: true}); err != nil {
		t.Fatal(err)
	}
	return service, apps, registry, api, clock
}
