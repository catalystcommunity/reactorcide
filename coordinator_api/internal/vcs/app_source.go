package vcs

import (
	"context"
	"strings"
	"sync"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// GitHub App integration points (WP11, docs/github-app.md). The App pool
// lives in internal/vcs/githubapp. Callers in internal/vcs, internal/handlers,
// and internal/workerapi use only these interfaces, so a deployment with no
// App keeps the token path and the Commit Status API unchanged.

// AppInstallationSource gives installation-token credentials for repositories
// that an App installation holds.
type AppInstallationSource interface {
	// ClientForRepo returns a client that authenticates with an installation
	// token of the installation that holds repoFullName ("owner/name"). ok
	// is false when no active installation holds the repository; the caller
	// then uses its token path.
	ClientForRepo(ctx context.Context, provider Provider, repoFullName string) (client Client, ok bool, err error)
	// CheckoutToken returns an installation token for a git checkout of the
	// repository. The token can only read the contents of that one
	// repository. It is a system credential: it goes only into the lease
	// vcs_auth, never into a job environment.
	//
	// projectID is the project of the job. The source gives a token only
	// when the binding of the repository belongs to that project. A job can
	// name any source URL, so a repository of another project (or another
	// organization) never gets a token of its installation: ok is false and
	// the caller uses the token path of its own project.
	CheckoutToken(ctx context.Context, provider Provider, repoFullName, projectID string) (token string, ok bool, err error)
}

// InstallationTokenProvider gives an installation token for an account (a
// GitHub organization login), for reads that are not about one repository,
// for example team membership (WP8). ok is false when no active installation
// of an enabled App is on the account.
//
// orgID is the organization that asks. The provider serves only an
// installation that the organization is entitled to: an installation that
// holds a repository of a project of the organization, or any installation
// on an account in the VCS owner allowlist of the organization. ok is false
// for every other installation.
type InstallationTokenProvider interface {
	InstallationTokenForAccount(ctx context.Context, provider Provider, accountLogin, orgID string) (token string, ok bool, err error)
	// ClientForAccount returns a client with that token.
	ClientForAccount(ctx context.Context, provider Provider, accountLogin, orgID string) (client Client, ok bool, err error)
}

// CheckRunAnnotation is one line annotation of a check run.
type CheckRunAnnotation struct {
	Path      string
	StartLine int
	EndLine   int
	// Level is "notice", "warning", or "failure".
	Level   string
	Title   string
	Message string
}

// CheckRunAction is a button on a check run (a requested_action).
type CheckRunAction struct {
	Label       string
	Description string
	Identifier  string
}

// CheckRunUpdate is the full state of one named check run on one commit.
type CheckRunUpdate struct {
	Name    string
	HeadSHA string
	// State is the Reactorcide state; the publisher maps it to the GitHub
	// status and conclusion.
	State       StatusState
	DetailsURL  string
	ExternalID  string
	Title       string
	Summary     string
	Text        string
	Annotations []CheckRunAnnotation
	Actions     []CheckRunAction
}

// CheckRunPublisher is implemented by a client that can post check runs (a
// GitHub App installation client). PublishEventChecks and the reviews status
// use it when the client has it, and the Commit Status API when not.
type CheckRunPublisher interface {
	PublishCheckRun(ctx context.Context, repo string, update CheckRunUpdate) error
}

// RerunActionIdentifier is the requested_action identifier of the "Re-run"
// button of "Reactorcide Checks".
const RerunActionIdentifier = "rerun"

// CheckRunContexts are the status contexts that an App client posts as check
// runs. Other contexts (a workflow status.context) stay commit statuses.
var CheckRunContexts = map[string]bool{
	models.CIEventChecksContext: true,
	"Reactorcide Reviews":       true,
}

// GitHubRepoFullName returns "owner/name" of a canonical GitHub project
// repository URL ("github.com/owner/name"), or "" for another host.
func GitHubRepoFullName(repoURL string) string {
	normalized := NormalizeRepoURL(repoURL)
	rest, ok := strings.CutPrefix(strings.ToLower(normalized), "github.com/")
	if !ok || strings.Count(rest, "/") != 1 {
		return ""
	}
	// Keep the case of the URL: GitHub names are case-insensitive, and the
	// API accepts either.
	return normalized[len("github.com/"):]
}

// appClientForProject returns the App installation client of a GitHub
// project, or nil.
func appClientForProject(ctx context.Context, source AppInstallationSource, project *models.Project, provider Provider) Client {
	if source == nil || project == nil || provider != GitHub {
		return nil
	}
	repo := GitHubRepoFullName(project.RepoURL)
	if repo == "" {
		return nil
	}
	client, ok, err := source.ClientForRepo(ctx, provider, repo)
	if err != nil || !ok {
		return nil
	}
	return client
}

// AppClientForProject is appClientForProject for other packages.
func AppClientForProject(ctx context.Context, source AppInstallationSource, project *models.Project, provider Provider) Client {
	return appClientForProject(ctx, source, project, provider)
}

var appSourceState struct {
	mu     sync.RWMutex
	source AppInstallationSource
	tokens InstallationTokenProvider
}

// SetAppInstallationSource installs the App pool of the process. A process
// with no App (nil) keeps the token path everywhere.
func SetAppInstallationSource(source AppInstallationSource) {
	appSourceState.mu.Lock()
	defer appSourceState.mu.Unlock()
	appSourceState.source = source
	appSourceState.tokens, _ = source.(InstallationTokenProvider)
}

// AppSource returns the App pool of the process, or nil.
func AppSource() AppInstallationSource {
	appSourceState.mu.RLock()
	defer appSourceState.mu.RUnlock()
	return appSourceState.source
}

// AppTokenProvider returns the account token provider of the App pool, or
// nil. Team reads (WP8) can use it when no project credential reads a team.
func AppTokenProvider() InstallationTokenProvider {
	appSourceState.mu.RLock()
	defer appSourceState.mu.RUnlock()
	return appSourceState.tokens
}
