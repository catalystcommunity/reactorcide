package vcs

import (
	"context"
	"net/http"
)

type organizationContextKey struct{}

// WithOrganization binds secret resolution to one organization.
func WithOrganization(ctx context.Context, orgID string) context.Context {
	return context.WithValue(ctx, organizationContextKey{}, orgID)
}

// OrganizationFromContext returns the organization for a scoped VCS secret.
func OrganizationFromContext(ctx context.Context) (string, bool) {
	orgID, ok := ctx.Value(organizationContextKey{}).(string)
	return orgID, ok && orgID != ""
}

// Provider represents a VCS provider type
type Provider string

const (
	GitHub Provider = "github"
	GitLab Provider = "gitlab"
)

// WebhookEvent represents a parsed webhook event from a VCS provider
type WebhookEvent struct {
	Provider     Provider
	EventType    string    // raw event type from the VCS provider (e.g., "pull_request", "push")
	GenericEvent EventType // VCS-agnostic event type (e.g., EventPullRequestOpened)
	Repository   RepositoryInfo
	PullRequest  *PullRequestInfo
	Push         *PushInfo
	IssueComment *IssueCommentInfo
	RawPayload   []byte
	SenderLogin  string // actor that delivered the current head update
}

// IssueCommentInfo contains a comment that can carry a CI approval command.
type IssueCommentInfo struct {
	Action        string
	IssueNumber   int
	Body          string
	IsPullRequest bool
}

// ActorFacts keeps the PR author, head-update actor, and repository relation
// separate. Admission policy uses HeadUpdateActor for actor matching.
type ActorFacts struct {
	PRAuthor        string
	HeadUpdateActor string
	HeadRepository  string
	HeadRelation    string // same or fork
}

// ActorSubjectResolver resolves provider-verified policy subjects for one
// repository actor. Implementations must fail closed when a fact cannot be
// verified.
type ActorSubjectResolver interface {
	ResolveActorSubjects(ctx context.Context, repo, username string) ([]string, error)
}

type CIPolicyViolation struct {
	Path, WorkflowID, Actor, Rule, BaseSHA, HeadSHA string
}

func (e WebhookEvent) ActorFacts() ActorFacts {
	facts := ActorFacts{HeadUpdateActor: e.SenderLogin, HeadRepository: e.Repository.FullName, HeadRelation: "same"}
	if e.PullRequest != nil {
		facts.PRAuthor = e.PullRequest.AuthorLogin
		if e.PullRequest.HeadRepository != nil {
			facts.HeadRepository = e.PullRequest.HeadRepository.FullName
			facts.HeadRelation = "fork"
		}
	}
	return facts
}

// RepositoryInfo contains repository information
type RepositoryInfo struct {
	FullName      string // e.g., "owner/repo"
	CloneURL      string
	SSHURL        string
	HTMLURL       string
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
	MergeSHA    string // commit created on the target branch after a merge
	HeadRef     string // branch name
	BaseSHA     string
	BaseRef     string // target branch
	Action      string // opened, closed, synchronize, etc.
	HTMLURL     string
	AuthorLogin string
	AuthorEmail string

	// HeadRepository is set only for cross-repository PRs (forks).
	// When nil, the PR's head branch lives on the same repository as Repository
	// in the enclosing WebhookEvent. When non-nil, this is the fork where the
	// branch actually lives — clone from HeadRepository.CloneURL to reach
	// HeadRef.
	HeadRepository *RepositoryInfo
}

// PushInfo contains push event information
type PushInfo struct {
	Ref         string // e.g., "refs/heads/main"
	Before      string // previous commit SHA
	After       string // new commit SHA
	Created     bool
	Deleted     bool
	Forced      bool
	Compare     string // URL to compare changes
	Commits     []Commit
	Pusher      string
	PusherEmail string
}

// Commit represents a commit in a push event
type Commit struct {
	ID          string
	Message     string
	Author      string
	AuthorEmail string
	Timestamp   string
	URL         string
	Added       []string
	Modified    []string
	Removed     []string
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
	StatusPending   StatusState = "pending"
	StatusRunning   StatusState = "running"
	StatusSuccess   StatusState = "success"
	StatusFailure   StatusState = "failure"
	StatusError     StatusState = "error"
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

	// UpdatePRComment creates a new comment on a pull request. Prefer
	// UpsertPRCommentByMarker for any comment we expect to update later.
	UpdatePRComment(ctx context.Context, repo string, prNumber int, comment string) error

	// UpsertPRCommentByMarker locates an existing comment on the PR whose
	// body contains marker and replaces it with body; if no such comment
	// exists, posts a new one. The marker should be an HTML comment like
	// "<!-- reactorcide:pr-status:abc123 -->" embedded in body so both the
	// lookup and the future update find the same comment.
	UpsertPRCommentByMarker(ctx context.Context, repo string, prNumber int, marker, body string) error

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
	Provider Provider
	Token    string // API token for status updates
	BaseURL  string // Base URL for Enterprise instances (optional)
}
