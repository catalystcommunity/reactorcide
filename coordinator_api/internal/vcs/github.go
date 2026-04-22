package vcs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/sirupsen/logrus"
)

// GitHubClient implements VCS client for GitHub
type GitHubClient struct {
	config Config
	client *http.Client
	logger *logrus.Logger
}

// NewGitHubClient creates a new GitHub VCS client
func NewGitHubClient(config Config) (*GitHubClient, error) {
	if config.BaseURL == "" {
		config.BaseURL = "https://api.github.com"
	}

	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	return &GitHubClient{
		config: config,
		client: &http.Client{},
		logger: logger,
	}, nil
}

// GetProvider returns the provider type
func (c *GitHubClient) GetProvider() Provider {
	return GitHub
}

// ParseWebhook parses a GitHub webhook event
func (c *GitHubClient) ParseWebhook(r *http.Request) (*WebhookEvent, error) {
	eventType := r.Header.Get("X-GitHub-Event")
	if eventType == "" {
		return nil, ErrMissingEventHeader
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("reading request body: %w", err)
	}

	// Handle form-encoded webhooks (Content-Type: application/x-www-form-urlencoded).
	// GitHub sends JSON inside a "payload" form field when configured for form encoding.
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, fmt.Errorf("parsing form-encoded body: %w", err)
		}
		payload := form.Get("payload")
		if payload == "" {
			return nil, fmt.Errorf("missing payload field in form-encoded webhook")
		}
		body = []byte(payload)
	}

	event := &WebhookEvent{
		Provider:   GitHub,
		EventType:  eventType,
		RawPayload: body,
	}

	// Parse based on event type
	switch eventType {
	case "pull_request":
		if err := c.parsePullRequestEvent(body, event); err != nil {
			return nil, fmt.Errorf("parsing pull request event: %w", err)
		}
	case "push":
		if err := c.parsePushEvent(body, event); err != nil {
			return nil, fmt.Errorf("parsing push event: %w", err)
		}
	case "ping":
		// Ping event for webhook setup verification
		c.logger.Info("Received GitHub ping event")
	default:
		c.logger.WithField("event_type", eventType).Warn("Unsupported GitHub event type")
	}

	// Translate raw GitHub event into a generic, VCS-agnostic event type
	var action string
	if event.PullRequest != nil {
		action = event.PullRequest.Action
	}
	event.GenericEvent = GenericEventFromGitHub(eventType, action, event.PullRequest, event.Push)

	return event, nil
}

// ValidateWebhook validates GitHub webhook signature
func (c *GitHubClient) ValidateWebhook(r *http.Request, secret string) error {
	if secret == "" {
		return nil // No validation if secret not configured
	}

	signature := r.Header.Get("X-Hub-Signature-256")
	if signature == "" {
		return ErrMissingSignature
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("reading request body: %w", err)
	}

	// Compute expected signature
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expectedSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(signature), []byte(expectedSig)) {
		return ErrInvalidSignature
	}

	return nil
}

// UpdateCommitStatus updates the status of a commit on GitHub
func (c *GitHubClient) UpdateCommitStatus(ctx context.Context, repo string, update StatusUpdate) error {
	// Map our status to GitHub status
	githubState := c.mapStatusState(update.State)

	payload := map[string]interface{}{
		"state":       githubState,
		"target_url":  update.TargetURL,
		"description": update.Description,
		"context":     update.Context,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling status payload: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/statuses/%s", c.config.BaseURL, repo, update.SHA)
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "token "+c.config.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github.v3+json")

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
		"state":  githubState,
		"context": update.Context,
	}).Info("Updated GitHub commit status")

	return nil
}

// UpdatePRComment adds or updates a comment on a GitHub pull request
func (c *GitHubClient) UpdatePRComment(ctx context.Context, repo string, prNumber int, comment string) error {
	payload := map[string]interface{}{
		"body": comment,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling comment payload: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/issues/%d/comments", c.config.BaseURL, repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "token "+c.config.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github.v3+json")

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
		"pr_number": prNumber,
	}).Info("Added comment to GitHub PR")

	return nil
}

// UpsertPRCommentByMarker scans existing PR comments for one containing the
// given marker string. If found, PATCHes its body; otherwise POSTs a new
// comment. Callers are expected to embed the marker in body so subsequent
// calls locate the same comment.
func (c *GitHubClient) UpsertPRCommentByMarker(ctx context.Context, repo string, prNumber int, marker, body string) error {
	listURL := fmt.Sprintf("%s/repos/%s/issues/%d/comments?per_page=100", c.config.BaseURL, repo, prNumber)

	existingID, err := c.findCommentIDByMarker(ctx, listURL, marker)
	if err != nil {
		return fmt.Errorf("searching for existing comment: %w", err)
	}

	if existingID != 0 {
		return c.patchIssueComment(ctx, repo, existingID, body)
	}
	return c.UpdatePRComment(ctx, repo, prNumber, body)
}

// findCommentIDByMarker walks paginated issue-comment results and returns
// the ID of the first comment whose body contains marker, or 0 if none found.
func (c *GitHubClient) findCommentIDByMarker(ctx context.Context, startURL, marker string) (int64, error) {
	next := startURL
	for next != "" {
		req, err := http.NewRequestWithContext(ctx, "GET", next, nil)
		if err != nil {
			return 0, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Authorization", "token "+c.config.Token)
		req.Header.Set("Accept", "application/vnd.github.v3+json")

		resp, err := c.client.Do(req)
		if err != nil {
			return 0, fmt.Errorf("sending request: %w", err)
		}

		var comments []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&comments); err != nil {
			resp.Body.Close()
			return 0, fmt.Errorf("decoding comments: %w", err)
		}
		next = parseGitHubNextLink(resp.Header.Get("Link"))
		resp.Body.Close()

		for _, cm := range comments {
			if strings.Contains(cm.Body, marker) {
				return cm.ID, nil
			}
		}
	}
	return 0, nil
}

// patchIssueComment replaces the body of an existing issue comment.
func (c *GitHubClient) patchIssueComment(ctx context.Context, repo string, commentID int64, body string) error {
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return fmt.Errorf("marshaling comment payload: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/issues/comments/%d", c.config.BaseURL, repo, commentID)
	req, err := http.NewRequestWithContext(ctx, "PATCH", url, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "token "+c.config.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// parseGitHubNextLink extracts the URL for rel="next" from a Link header.
func parseGitHubNextLink(header string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		sections := strings.Split(part, ";")
		if len(sections) < 2 {
			continue
		}
		urlPart := strings.Trim(strings.TrimSpace(sections[0]), "<>")
		for _, s := range sections[1:] {
			if strings.TrimSpace(s) == `rel="next"` {
				return urlPart
			}
		}
	}
	return ""
}

// GetPRInfo gets information about a GitHub pull request
func (c *GitHubClient) GetPRInfo(ctx context.Context, repo string, prNumber int) (*PullRequestInfo, error) {
	url := fmt.Sprintf("%s/repos/%s/pulls/%d", c.config.BaseURL, repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "token "+c.config.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var pr githubPullRequest
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return c.convertPRInfo(pr), nil
}

// parsePullRequestEvent parses a GitHub pull request event
func (c *GitHubClient) parsePullRequestEvent(body []byte, event *WebhookEvent) error {
	var payload githubPullRequestEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}

	event.Repository = RepositoryInfo{
		FullName:      payload.Repository.FullName,
		CloneURL:      payload.Repository.CloneURL,
		SSHURL:        payload.Repository.SSHURL,
		HTMLURL:       payload.Repository.HTMLURL,
		DefaultBranch: payload.Repository.DefaultBranch,
	}

	event.PullRequest = &PullRequestInfo{
		Number:      payload.Number,
		Title:       payload.PullRequest.Title,
		Description: payload.PullRequest.Body,
		State:       payload.PullRequest.State,
		Merged:      payload.PullRequest.Merged,
		HeadSHA:     payload.PullRequest.Head.SHA,
		HeadRef:     payload.PullRequest.Head.Ref,
		BaseSHA:     payload.PullRequest.Base.SHA,
		BaseRef:     payload.PullRequest.Base.Ref,
		Action:      payload.Action,
		HTMLURL:     payload.PullRequest.HTMLURL,
		AuthorLogin: payload.PullRequest.User.Login,
		AuthorEmail: "", // Not provided in webhook
	}

	return nil
}

// parsePushEvent parses a GitHub push event
func (c *GitHubClient) parsePushEvent(body []byte, event *WebhookEvent) error {
	var payload githubPushEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}

	event.Repository = RepositoryInfo{
		FullName:      payload.Repository.FullName,
		CloneURL:      payload.Repository.CloneURL,
		SSHURL:        payload.Repository.SSHURL,
		HTMLURL:       payload.Repository.HTMLURL,
		DefaultBranch: payload.Repository.DefaultBranch,
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

	event.Push = &PushInfo{
		Ref:         payload.Ref,
		Before:      payload.Before,
		After:       payload.After,
		Created:     payload.Created,
		Deleted:     payload.Deleted,
		Forced:      payload.Forced,
		Compare:     payload.Compare,
		Commits:     commits,
		Pusher:      payload.Pusher.Name,
		PusherEmail: payload.Pusher.Email,
	}

	return nil
}

// mapStatusState maps our status state to GitHub's
func (c *GitHubClient) mapStatusState(state StatusState) string {
	switch state {
	case StatusPending, StatusRunning:
		return "pending"
	case StatusSuccess:
		return "success"
	case StatusFailure:
		return "failure"
	case StatusError, StatusCancelled:
		return "error"
	default:
		return "error"
	}
}

// convertPRInfo converts GitHub PR to our format
func (c *GitHubClient) convertPRInfo(pr githubPullRequest) *PullRequestInfo {
	return &PullRequestInfo{
		Number:      pr.Number,
		Title:       pr.Title,
		Description: pr.Body,
		State:       pr.State,
		Merged:      pr.Merged,
		HeadSHA:     pr.Head.SHA,
		HeadRef:     pr.Head.Ref,
		BaseSHA:     pr.Base.SHA,
		BaseRef:     pr.Base.Ref,
		HTMLURL:     pr.HTMLURL,
		AuthorLogin: pr.User.Login,
	}
}

// GitHub API structures
type githubPullRequestEvent struct {
	Action      string              `json:"action"`
	Number      int                 `json:"number"`
	PullRequest githubPullRequest   `json:"pull_request"`
	Repository  githubRepository    `json:"repository"`
}

type githubPullRequest struct {
	Number  int              `json:"number"`
	Title   string           `json:"title"`
	Body    string           `json:"body"`
	State   string           `json:"state"`
	Merged  bool             `json:"merged"`
	HTMLURL string           `json:"html_url"`
	Head    githubRef        `json:"head"`
	Base    githubRef        `json:"base"`
	User    githubUser       `json:"user"`
}

type githubRef struct {
	Ref  string           `json:"ref"`
	SHA  string           `json:"sha"`
	Repo githubRepository `json:"repo"`
}

type githubUser struct {
	Login string `json:"login"`
	Email string `json:"email"`
}

type githubRepository struct {
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	SSHURL        string `json:"ssh_url"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
}

type githubPushEvent struct {
	Ref        string           `json:"ref"`
	Before     string           `json:"before"`
	After      string           `json:"after"`
	Created    bool             `json:"created"`
	Deleted    bool             `json:"deleted"`
	Forced     bool             `json:"forced"`
	Compare    string           `json:"compare"`
	Commits    []githubCommit   `json:"commits"`
	Repository githubRepository `json:"repository"`
	Pusher     githubAuthor     `json:"pusher"`
}

type githubCommit struct {
	ID        string       `json:"id"`
	Message   string       `json:"message"`
	Timestamp string       `json:"timestamp"`
	URL       string       `json:"url"`
	Author    githubAuthor `json:"author"`
	Added     []string     `json:"added"`
	Modified  []string     `json:"modified"`
	Removed   []string     `json:"removed"`
}

type githubAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}