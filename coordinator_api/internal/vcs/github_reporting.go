package vcs

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/reporting"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// GitHub reads for the agreement report and the code owners import
// (docs/reporting-and-ownership.md). They only read; they never write.

var (
	_ CommitResultsReader  = (*GitHubClient)(nil)
	_ reporting.FileReader = (*GitHubClient)(nil)
)

// GetFileContent reads one file at a commit through the contents API.
// found is false when the commit has no such file.
func (c *GitHubClient) GetFileContent(ctx context.Context, repo, filePath, ref string) ([]byte, bool, error) {
	escaped := []string{}
	for _, part := range strings.Split(strings.Trim(filePath, "/"), "/") {
		escaped = append(escaped, url.PathEscape(part))
	}
	endpoint := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", c.config.BaseURL, repo, strings.Join(escaped, "/"), url.QueryEscape(ref))
	var file struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		Size     int    `json:"size"`
	}
	status, err := c.getJSON(ctx, endpoint, &file)
	if err != nil {
		return nil, false, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("reading %s: GitHub returned status %d", filePath, status)
	}
	if file.Type != "" && file.Type != "file" {
		return nil, false, nil
	}
	if file.Encoding != "base64" {
		return nil, false, fmt.Errorf("reading %s: the file is too large for the contents API (%d bytes)", filePath, file.Size)
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", filePath, err)
	}
	return content, true, nil
}

// ListCommitCIResults reads the commit statuses (the combined status) and
// the check runs (the latest run of each name) of a commit.
func (c *GitHubClient) ListCommitCIResults(ctx context.Context, repo, sha string) ([]models.AgreementOtherResult, error) {
	results := []models.AgreementOtherResult{}
	for page := 1; page <= 10; page++ {
		var combined struct {
			Statuses []struct {
				Context   string `json:"context"`
				State     string `json:"state"`
				TargetURL string `json:"target_url"`
			} `json:"statuses"`
			TotalCount int `json:"total_count"`
		}
		endpoint := fmt.Sprintf("%s/repos/%s/commits/%s/status?per_page=100&page=%d", c.config.BaseURL, repo, url.PathEscape(sha), page)
		status, err := c.getJSON(ctx, endpoint, &combined)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound {
			break
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("reading commit statuses: GitHub returned status %d", status)
		}
		for _, item := range combined.Statuses {
			results = append(results, models.AgreementOtherResult{Name: item.Context, Kind: reporting.OtherKindStatus,
				State: reporting.NormalizeStatusState(item.State), Raw: item.State, TargetURL: item.TargetURL})
		}
		if len(combined.Statuses) < 100 {
			break
		}
	}
	for page := 1; page <= 10; page++ {
		var runs struct {
			CheckRuns []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HTMLURL    string `json:"html_url"`
			} `json:"check_runs"`
		}
		endpoint := fmt.Sprintf("%s/repos/%s/commits/%s/check-runs?filter=latest&per_page=100&page=%d", c.config.BaseURL, repo, url.PathEscape(sha), page)
		status, err := c.getJSON(ctx, endpoint, &runs)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound || status == http.StatusForbidden {
			// A credential without the checks permission sees no check
			// runs. The commit statuses still count.
			break
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("reading check runs: GitHub returned status %d", status)
		}
		for _, run := range runs.CheckRuns {
			raw := run.Status
			if run.Conclusion != "" {
				raw = run.Conclusion
			}
			results = append(results, models.AgreementOtherResult{Name: run.Name, Kind: reporting.OtherKindCheck,
				State: reporting.NormalizeCheckState(run.Status, run.Conclusion), Raw: raw, TargetURL: run.HTMLURL})
		}
		if len(runs.CheckRuns) < 100 {
			break
		}
	}
	return results, nil
}
