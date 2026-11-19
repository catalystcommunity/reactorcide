package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcsteams"
)

// teamSyncStore serves the projects of an org for the team sync.
type teamSyncStore struct {
	store.GroupVCSStore
	projects []models.Project
}

func (s *teamSyncStore) ListOrgProjectsForTeamSync(context.Context, string) ([]models.Project, error) {
	return s.projects, nil
}

// allowlistWebhookStore adds the VCS owner allowlist to the webhook store.
type allowlistWebhookStore struct {
	*WebhookMockStore
	allow []string
}

func (s *allowlistWebhookStore) GetOrganizationVCSOwnerAllowlist(context.Context, string) ([]string, error) {
	return s.allow, nil
}

// globalTeamClient is the global VCS client of the coordinator.
type globalTeamClient struct{ vcs.Client }

func (globalTeamClient) GetTeam(context.Context, string, string) (*vcs.Team, error) {
	return &vcs.Team{}, nil
}
func (globalTeamClient) ListTeamMembers(context.Context, string, string) ([]string, error) {
	return nil, nil
}

// The group VCS sync of a tenant must not read another tenant's teams with
// the global credential: only an owner on the allowlist (set by a global
// admin) may use it.
func TestTeamClientResolverNeedsAllowlistForGlobalCredential(t *testing.T) {
	st := &allowlistWebhookStore{WebhookMockStore: &WebhookMockStore{}}
	handler := NewWebhookHandler(st, corndogs.NewMockClient())
	handler.AddVCSClient(vcs.GitHub, globalTeamClient{})
	sync := &teamSyncStore{projects: []models.Project{{ProjectID: "p1", Name: "app", RepoURL: "github.com/victim/app", OrgID: "org-1"}}}
	resolve := handler.teamClientResolver(sync)

	if _, err := resolve(context.Background(), "org-1", "github", "victim"); !errors.Is(err, vcsteams.ErrNoCredential) {
		t.Fatalf("the global credential must not read an owner that is not on the allowlist: %v", err)
	}
	st.allow = []string{"victim"}
	lister, err := resolve(context.Background(), "org-1", "github", "victim")
	if err != nil || lister == nil {
		t.Fatalf("an allowlisted owner may use the global credential: %v", err)
	}
}

// appTeamSource is a GitHub App pool that serves one account.
type appTeamSource struct {
	account, orgID string
	client         vcs.Client
}

func (appTeamSource) ClientForRepo(context.Context, vcs.Provider, string) (vcs.Client, bool, error) {
	return nil, false, nil
}
func (appTeamSource) CheckoutToken(context.Context, vcs.Provider, string, string) (string, bool, error) {
	return "", false, nil
}
func (s appTeamSource) InstallationTokenForAccount(_ context.Context, _ vcs.Provider, account, orgID string) (string, bool, error) {
	return "fake-installation-token", account == s.account && orgID == s.orgID, nil
}
func (s appTeamSource) ClientForAccount(_ context.Context, _ vcs.Provider, account, orgID string) (vcs.Client, bool, error) {
	if account != s.account || orgID != s.orgID {
		return nil, false, nil
	}
	return s.client, true, nil
}

// appTeamClient is the client of an App installation.
type appTeamClient struct{ globalTeamClient }

// The team sync prefers an App installation that the organization is
// entitled to, before project credentials and the global credential.
func TestTeamClientResolverPrefersAppInstallation(t *testing.T) {
	st := &allowlistWebhookStore{WebhookMockStore: &WebhookMockStore{}}
	handler := NewWebhookHandler(st, corndogs.NewMockClient())
	handler.AddVCSClient(vcs.GitHub, globalTeamClient{})
	app := appTeamClient{}
	vcs.SetAppInstallationSource(appTeamSource{account: "acme", orgID: "org-1", client: app})
	t.Cleanup(func() { vcs.SetAppInstallationSource(nil) })
	sync := &teamSyncStore{projects: []models.Project{{ProjectID: "p1", Name: "app", RepoURL: "github.com/acme/app", OrgID: "org-1"},
		{ProjectID: "p2", Name: "x", RepoURL: "github.com/other/x", OrgID: "org-1"}}}
	resolve := handler.teamClientResolver(sync)

	lister, err := resolve(context.Background(), "org-1", "github", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lister.(appTeamClient); !ok {
		t.Fatalf("lister = %T, want the App installation client", lister)
	}
	// Another account: no entitled installation, and no allowlist entry.
	if _, err := resolve(context.Background(), "org-1", "github", "other"); !errors.Is(err, vcsteams.ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
}
