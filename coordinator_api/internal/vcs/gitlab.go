package vcs

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
)

// GitLabClient implements VCS client for GitLab
type GitLabClient struct {
	config Config
	client *http.Client
	logger *logrus.Logger
}

// NewGitLabClient creates a new GitLab VCS client
func NewGitLabClient(config Config) (*GitLabClient, error) {
	if config.BaseURL == "" {
		config.BaseURL = "https://gitlab.com/api/v4"
	}

	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	return &GitLabClient{
		config: config,
		client: &http.Client{},
		logger: logger,
	}, nil
}

// GetProvider returns the provider type
func (c *GitLabClient) GetProvider() Provider {
	return GitLab
}

// ParseWebhook parses a GitLab webhook event
func (c *GitLabClient) ParseWebhook(r *http.Request) (*WebhookEvent, error) {
	eventType := r.Header.Get("X-Gitlab-Event")
	if eventType == "" {
		return nil, ErrMissingEventHeader
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("reading request body: %w", err)
	}

	event := &WebhookEvent{
		Provider:   GitLab,
		EventType:  eventType,
		RawPayload: body,
	}

	// Parse based on event type
	switch eventType {
	case "Merge Request Hook":
		if err := c.parseMergeRequestEvent(body, event); err != nil {
			return nil, fmt.Errorf("parsing merge request event: %w", err)
		}
	case "Push Hook":
		if err := c.parsePushEvent(body, event); err != nil {
			return nil, fmt.Errorf("parsing push event: %w", err)
		}
	case "System Hook":
		// System hook for GitLab instance events
		c.logger.Info("Received GitLab system hook event")
	default:
		c.logger.WithField("event_type", eventType).Warn("Unsupported GitLab event type")
	}

	return event, nil
}

// ValidateWebhook validates GitLab webhook token
func (c *GitLabClient) ValidateWebhook(r *http.Request, secret string) error {
	if secret == "" {
		return nil // No validation if secret not configured
	}

	token := r.Header.Get("X-Gitlab-Token")
	if token == "" {
		return ErrMissingSignature
	}

	if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
		return ErrInvalidSignature
	}

	return nil
}

// UpdateCommitStatus updates the status of a commit on GitLab
func (c *GitLabClient) UpdateCommitStatus(ctx context.Context, repo string, update StatusUpdate) error {
	// GitLab uses project ID or URL-encoded path
	projectPath := strings.ReplaceAll(repo, "/", "%2F")

	// Map our status to GitLab status
	gitlabState := c.mapStatusState(update.State)

	payload := map[string]interface{}{
		"state":       gitlabState,
		"target_url":  update.TargetURL,
		"description": update.Description,
		"name":        update.Context,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling status payload: %w", err)
	}

	url := fmt.Sprintf("%s/projects/%s/statuses/%s", c.config.BaseURL, projectPath, update.SHA)
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("PRIVATE-TOKEN", c.config.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	c.logger.WithFields(logrus.Fields{
		"repo":   repo,
		"sha":    update.SHA,
		"state":  gitlabState,
		"context": update.Context,
	}).Info("Updated GitLab commit status")

	return nil
}

// UpdatePRComment adds or updates a comment on a GitLab merge request
func (c *GitLabClient) UpdatePRComment(ctx context.Context, repo string, prNumber int, comment string) error {
	// GitLab uses project ID or URL-encoded path
	projectPath := strings.ReplaceAll(repo, "/", "%2F")

	payload := map[string]interface{}{
		"body": comment,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling comment payload: %w", err)
	}

	url := fmt.Sprintf("%s/projects/%s/merge_requests/%d/notes", c.config.BaseURL, projectPath, prNumber)
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("PRIVATE-TOKEN", c.config.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	c.logger.WithFields(logrus.Fields{
		"repo":      repo,
		"mr_number": prNumber,
	}).Info("Added comment to GitLab MR")

	return nil
}

// GetPRInfo gets information about a GitLab merge request
func (c *GitLabClient) GetPRInfo(ctx context.Context, repo string, prNumber int) (*PullRequestInfo, error) {
	// GitLab uses project ID or URL-encoded path
	projectPath := strings.ReplaceAll(repo, "/", "%2F")

	url := fmt.Sprintf("%s/projects/%s/merge_requests/%d", c.config.BaseURL, projectPath, prNumber)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("PRIVATE-TOKEN", c.config.Token)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var mr gitlabMergeRequest
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return c.convertMRInfo(mr), nil
}

// parseMergeRequestEvent parses a GitLab merge request event
func (c *GitLabClient) parseMergeRequestEvent(body []byte, event *WebhookEvent) error {
	var payload gitlabMergeRequestEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}

	event.Repository = RepositoryInfo{
		FullName:      payload.Project.PathWithNamespace,
		CloneURL:      payload.Project.HTTPUrl,
		SSHURL:        payload.Project.SSHUrl,
		HTMLURL:       payload.Project.WebURL,
		DefaultBranch: payload.Project.DefaultBranch,
	}

	// Map GitLab MR state to our state
	state := payload.ObjectAttributes.State
	if payload.ObjectAttributes.MergeStatus == "merged" {
		state = "merged"
	}

	// Map GitLab action to GitHub-like action
	action := payload.ObjectAttributes.Action
	if action == "" {
		action = mapGitLabAction(payload.ObjectAttributes.State, payload.ObjectAttributes.OldRev != "")
	}

	event.PullRequest = &PullRequestInfo{
		Number:      payload.ObjectAttributes.IID,
		Title:       payload.ObjectAttributes.Title,
		Description: payload.ObjectAttributes.Description,
		State:       state,
		HeadSHA:     payload.ObjectAttributes.LastCommit.ID,
		HeadRef:     payload.ObjectAttributes.SourceBranch,
		BaseSHA:     "", // Not provided in webhook
		BaseRef:     payload.ObjectAttributes.TargetBranch,
		Action:      action,
		HTMLURL:     payload.ObjectAttributes.URL,
		AuthorLogin: payload.User.Username,
		AuthorEmail: payload.User.Email,
	}

	return nil
}

// parsePushEvent parses a GitLab push event
func (c *GitLabClient) parsePushEvent(body []byte, event *WebhookEvent) error {
	var payload gitlabPushEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}

	event.Repository = RepositoryInfo{
		FullName:      payload.Project.PathWithNamespace,
		CloneURL:      payload.Project.HTTPUrl,
		SSHURL:        payload.Project.SSHUrl,
		HTMLURL:       payload.Project.WebURL,
		DefaultBranch: payload.Project.DefaultBranch,
	}

	commits := make([]Commit, len(payload.Commits))
	for i, c := range payload.Commits {
		commits[i] = Commit{
			ID:          c.ID,
			Message:     c.Message,
			Author:      c.Author.Name,
			AuthorEmail: c.Author.Email,
			Timestamp:   c.Timestamp,
			URL:         c.URL,
			Added:       c.Added,
			Modified:    c.Modified,
			Removed:     c.Removed,
		}
	}

	// Extract branch name from ref
	ref := payload.Ref
	_ = strings.TrimPrefix(ref, "refs/heads/")

	event.Push = &PushInfo{
		Ref:         payload.Ref,
		Before:      payload.Before,
		After:       payload.After,
		Created:     payload.Before == "0000000000000000000000000000000000000000",
		Deleted:     payload.After == "0000000000000000000000000000000000000000",
		Forced:      false, // Not provided by GitLab
		Compare:     "",    // Not provided by GitLab
		Commits:     commits,
		Pusher:      payload.UserName,
		PusherEmail: payload.UserEmail,
	}

	return nil
}

// mapStatusState maps our status state to GitLab's
func (c *GitLabClient) mapStatusState(state StatusState) string {
	switch state {
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusSuccess:
		return "success"
	case StatusFailure:
		return "failed"
	case StatusError:
		return "failed"
	case StatusCancelled:
		return "canceled"
	default:
		return "failed"
	}
}

// mapGitLabAction maps GitLab MR states to GitHub-like actions
func mapGitLabAction(state string, hasUpdate bool) string {
	switch state {
	case "opened":
		if hasUpdate {
			return "synchronize"
		}
		return "opened"
	case "closed":
		return "closed"
	case "merged":
		return "closed" // with merged flag
	default:
		return state
	}
}

// convertMRInfo converts GitLab MR to our format
func (c *GitLabClient) convertMRInfo(mr gitlabMergeRequest) *PullRequestInfo {
	state := mr.State
	if mr.MergeStatus == "merged" {
		state = "merged"
	}

	return &PullRequestInfo{
		Number:      mr.IID,
		Title:       mr.Title,
		Description: mr.Description,
		State:       state,
		HeadSHA:     mr.SHA,
		HeadRef:     mr.SourceBranch,
		BaseSHA:     mr.DiffRefs.BaseSHA,
		BaseRef:     mr.TargetBranch,
		HTMLURL:     mr.WebURL,
		AuthorLogin: mr.Author.Username,
		AuthorEmail: "", // Not provided in API response
	}
}

// GitLab API structures
type gitlabMergeRequestEvent struct {
	ObjectKind       string                  `json:"object_kind"`
	User             gitlabUser              `json:"user"`
	Project          gitlabProject           `json:"project"`
	ObjectAttributes gitlabMergeRequestAttrs `json:"object_attributes"`
}

type gitlabMergeRequestAttrs struct {
	ID           int                 `json:"id"`
	IID          int                 `json:"iid"`
	Title        string              `json:"title"`
	Description  string              `json:"description"`
	State        string              `json:"state"`
	MergeStatus  string              `json:"merge_status"`
	SourceBranch string              `json:"source_branch"`
	TargetBranch string              `json:"target_branch"`
	LastCommit   gitlabCommit        `json:"last_commit"`
	URL          string              `json:"url"`
	Action       string              `json:"action"`
	OldRev       string              `json:"oldrev"`
}

type gitlabMergeRequest struct {
	ID           int              `json:"id"`
	IID          int              `json:"iid"`
	Title        string           `json:"title"`
	Description  string           `json:"description"`
	State        string           `json:"state"`
	MergeStatus  string           `json:"merge_status"`
	SHA          string           `json:"sha"`
	SourceBranch string           `json:"source_branch"`
	TargetBranch string           `json:"target_branch"`
	WebURL       string           `json:"web_url"`
	Author       gitlabUser       `json:"author"`
	DiffRefs     gitlabDiffRefs   `json:"diff_refs"`
}

type gitlabDiffRefs struct {
	BaseSHA string `json:"base_sha"`
	HeadSHA string `json:"head_sha"`
	StartSHA string `json:"start_sha"`
}

type gitlabUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

type gitlabProject struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	WebURL            string `json:"web_url"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	HTTPUrl           string `json:"http_url"`
	SSHUrl            string `json:"ssh_url"`
}

type gitlabPushEvent struct {
	ObjectKind   string         `json:"object_kind"`
	Before       string         `json:"before"`
	After        string         `json:"after"`
	Ref          string         `json:"ref"`
	CheckoutSHA  string         `json:"checkout_sha"`
	UserID       int            `json:"user_id"`
	UserName     string         `json:"user_name"`
	UserUsername string         `json:"user_username"`
	UserEmail    string         `json:"user_email"`
	UserAvatar   string         `json:"user_avatar"`
	ProjectID    int            `json:"project_id"`
	Project      gitlabProject  `json:"project"`
	Commits      []gitlabCommit `json:"commits"`
	TotalCommits int            `json:"total_commits_count"`
}

type gitlabCommit struct {
	ID        string       `json:"id"`
	Message   string       `json:"message"`
	Timestamp string       `json:"timestamp"`
	URL       string       `json:"url"`
	Author    gitlabAuthor `json:"author"`
	Added     []string     `json:"added"`
	Modified  []string     `json:"modified"`
	Removed   []string     `json:"removed"`
}

type gitlabAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}