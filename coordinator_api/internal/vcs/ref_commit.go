package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// RefResolver resolves a branch or a tag to its commit, and finds the default
// branch of a repository. A manual run on a ref uses it: the coordinator, not
// the caller, decides the commit that runs.
type RefResolver interface {
	BranchHeadResolver
	// GetTagCommit returns the commit a tag points at (an annotated tag is
	// followed to its commit).
	GetTagCommit(ctx context.Context, repo, tag string) (string, error)
	// GetDefaultBranch returns the name of the default branch.
	GetDefaultBranch(ctx context.Context, repo string) (string, error)
}

// ErrTagNotFound is returned when the repository has no such tag.
var ErrTagNotFound = errors.New("tag not found")

// GetTagCommit returns the commit of a tag on GitHub. The commits endpoint
// takes "tags/NAME" and follows an annotated tag.
func (c *GitHubClient) GetTagCommit(ctx context.Context, repo, tag string) (string, error) {
	var body struct {
		SHA string `json:"sha"`
	}
	endpoint := fmt.Sprintf("%s/repos/%s/commits/tags/%s", c.config.BaseURL, escapePathSegments(repo), escapePathSegments(tag))
	status, err := c.getJSON(ctx, endpoint, &body)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound || status == http.StatusUnprocessableEntity {
		return "", fmt.Errorf("%w: %s on %s", ErrTagNotFound, tag, repo)
	}
	if status != http.StatusOK || body.SHA == "" {
		return "", fmt.Errorf("GitHub returned status %d and no commit for tag %s", status, tag)
	}
	return body.SHA, nil
}

// GetDefaultBranch returns the default branch of a GitHub repository.
func (c *GitHubClient) GetDefaultBranch(ctx context.Context, repo string) (string, error) {
	var body struct {
		DefaultBranch string `json:"default_branch"`
	}
	endpoint := fmt.Sprintf("%s/repos/%s", c.config.BaseURL, escapePathSegments(repo))
	status, err := c.getJSON(ctx, endpoint, &body)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK || body.DefaultBranch == "" {
		return "", fmt.Errorf("GitHub returned status %d and no default branch for %s", status, repo)
	}
	return body.DefaultBranch, nil
}

func (c *GitLabClient) getJSON(ctx context.Context, endpoint string, dst any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.config.Token)
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return resp.StatusCode, fmt.Errorf("decoding response: %w", err)
	}
	return resp.StatusCode, nil
}

// GetTagCommit returns the commit of a tag on GitLab.
func (c *GitLabClient) GetTagCommit(ctx context.Context, repo, tag string) (string, error) {
	var body struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	endpoint := fmt.Sprintf("%s/projects/%s/repository/tags/%s", c.config.BaseURL, url.PathEscape(repo), url.PathEscape(tag))
	status, err := c.getJSON(ctx, endpoint, &body)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return "", fmt.Errorf("%w: %s on %s", ErrTagNotFound, tag, repo)
	}
	if status != http.StatusOK || body.Commit.ID == "" {
		return "", fmt.Errorf("GitLab returned status %d and no commit for tag %s", status, tag)
	}
	return body.Commit.ID, nil
}

// GetDefaultBranch returns the default branch of a GitLab project.
func (c *GitLabClient) GetDefaultBranch(ctx context.Context, repo string) (string, error) {
	var body struct {
		DefaultBranch string `json:"default_branch"`
	}
	endpoint := fmt.Sprintf("%s/projects/%s", c.config.BaseURL, url.PathEscape(repo))
	status, err := c.getJSON(ctx, endpoint, &body)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK || body.DefaultBranch == "" {
		return "", fmt.Errorf("GitLab returned status %d and no default branch for %s", status, repo)
	}
	return body.DefaultBranch, nil
}
