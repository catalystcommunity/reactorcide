package vcs

import (
	"context"
	"net/http"
)

// Provider represents a VCS provider type
type Provider string

const (
	GitHub Provider = "github"
	GitLab Provider = "gitlab"
)

// WebhookEvent represents a parsed webhook event from a VCS provider
type WebhookEvent struct {
	Provider     Provider
	EventType    string // raw event type from the VCS provider (e.g., "pull_request", "push")
	GenericEvent EventType // VCS-agnostic event type (e.g., EventPullRequestOpened)
	Repository   RepositoryInfo
	PullRequest  *PullRequestInfo
	Push         *PushInfo
	RawPayload   []byte
}

// RepositoryInfo contains repository information
type RepositoryInfo struct {
	FullName  string // e.g., "owner/repo"
	CloneURL  string
	SSHURL    string
	HTMLURL   string
	DefaultBranch string
}

// PullRequestInfo contains pull request information
type PullRequestInfo struct {
	Number      int
	Title       string
	Description string
	State       string // open, closed
	Merged      bool
	HeadSHA     string
	HeadRef     string // branch name
	BaseSHA     string
	BaseRef     string // target branch
	Action      string // opened, closed, synchronize, etc.
	HTMLURL     string
	AuthorLogin string
	AuthorEmail string
}

// PushInfo contains push event information
type PushInfo struct {
	Ref        string   // e.g., "refs/heads/main"
	Before     string   // previous commit SHA
	After      string   // new commit SHA
	Created    bool
	Deleted    bool
	Forced     bool
	Compare    string   // URL to compare changes
	Commits    []Commit
	Pusher     string
	PusherEmail string
}

// Commit represents a commit in a push event
type Commit struct {
	ID        string
	Message   string
	Author    string
	AuthorEmail string
	Timestamp string
	URL       string
	Added     []string
	Modified  []string
	Removed   []string
}

// StatusUpdate represents a commit status update
type StatusUpdate struct {
	SHA         string
	State       StatusState
	TargetURL   string
	Description string
	Context     string // e.g., "continuous-integration/reactorcide"
}

// StatusState represents the state of a commit status
type StatusState string

const (
	StatusPending StatusState = "pending"
	StatusRunning StatusState = "running"
	StatusSuccess StatusState = "success"
	StatusFailure StatusState = "failure"
	StatusError   StatusState = "error"
	StatusCancelled StatusState = "cancelled"
)

// WebhookHandler processes webhook events from VCS providers
type WebhookHandler interface {
	// ParseWebhook parses an incoming webhook request
	ParseWebhook(r *http.Request) (*WebhookEvent, error)

	// ValidateWebhook validates the webhook signature/secret
	ValidateWebhook(r *http.Request, secret string) error
}

// StatusUpdater updates commit/PR statuses in the VCS
type StatusUpdater interface {
	// UpdateCommitStatus updates the status of a commit
	UpdateCommitStatus(ctx context.Context, repo string, update StatusUpdate) error

	// UpdatePRComment adds or updates a comment on a pull request
	UpdatePRComment(ctx context.Context, repo string, prNumber int, comment string) error

	// GetPRInfo gets information about a pull request
	GetPRInfo(ctx context.Context, repo string, prNumber int) (*PullRequestInfo, error)
}

// Client combines webhook handling and status updating
type Client interface {
	WebhookHandler
	StatusUpdater

	// GetProvider returns the provider type
	GetProvider() Provider
}

// Config holds VCS configuration
type Config struct {
	Provider     Provider
	Token        string // API token for status updates
	WebhookSecret string // Secret for webhook validation
	BaseURL      string // Base URL for Enterprise instances (optional)
}

// NewClient creates a new VCS client based on the provider
func NewClient(config Config) (Client, error) {
	switch config.Provider {
	case GitHub:
		return NewGitHubClient(config)
	case GitLab:
		return NewGitLabClient(config)
	default:
		return nil, ErrUnsupportedProvider
	}
}