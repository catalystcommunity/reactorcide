package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// BranchHeadResolver returns the commit a branch points at. A scheduled build
// uses it to pick the commit it runs on.
type BranchHeadResolver interface {
	GetBranchHead(ctx context.Context, repo, branch string) (string, error)
}

// ErrBranchNotFound is returned when the repository has no such branch.
var ErrBranchNotFound = errors.New("branch not found")

// escapePathSegments escapes each "/"-separated segment, keeping the
// separators. GitHub takes a branch with slashes as raw path segments.
func escapePathSegments(value string) string {
	parts := strings.Split(value, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// GetBranchHead returns the head commit SHA of a branch on GitHub.
func (c *GitHubClient) GetBranchHead(ctx context.Context, repo, branch string) (string, error) {
	var body struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	endpoint := fmt.Sprintf("%s/repos/%s/branches/%s", c.config.BaseURL, escapePathSegments(repo), escapePathSegments(branch))
	status, err := c.getJSON(ctx, endpoint, &body)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return "", fmt.Errorf("%w: %s on %s", ErrBranchNotFound, branch, repo)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("GitHub returned status %d for branch %s", status, branch)
	}
	if body.Commit.SHA == "" {
		return "", fmt.Errorf("GitHub returned no commit for branch %s", branch)
	}
	return body.Commit.SHA, nil
}

// GetBranchHead returns the head commit SHA of a branch on GitLab.
func (c *GitLabClient) GetBranchHead(ctx context.Context, repo, branch string) (string, error) {
	endpoint := fmt.Sprintf("%s/projects/%s/repository/branches/%s",
		c.config.BaseURL, url.PathEscape(repo), url.PathEscape(branch))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.config.Token)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("%w: %s on %s", ErrBranchNotFound, branch, repo)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitLab returned status %d for branch %s", resp.StatusCode, branch)
	}
	var body struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}
	if body.Commit.ID == "" {
		return "", fmt.Errorf("GitLab returned no commit for branch %s", branch)
	}
	return body.Commit.ID, nil
}
