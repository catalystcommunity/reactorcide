package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultAPIBaseURL is the GitHub REST API.
const DefaultAPIBaseURL = "https://api.github.com"

// AppInfo is the GET /app response.
type AppInfo struct {
	ID       int64  `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	ClientID string `json:"client_id"`
}

// Account is the account of an installation.
type Account struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// InstallationInfo is one installation as the API and webhooks give it.
type InstallationInfo struct {
	ID                  int64             `json:"id"`
	AppID               int64             `json:"app_id"`
	Account             Account           `json:"account"`
	RepositorySelection string            `json:"repository_selection"`
	Permissions         map[string]string `json:"permissions"`
	SuspendedAt         *time.Time        `json:"suspended_at"`
}

// Repository is one repository of an installation.
type Repository struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
}

// InstallationToken is a POST /app/installations/{id}/access_tokens response.
type InstallationToken struct {
	Token       string            `json:"token"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Permissions map[string]string `json:"permissions"`
}

// tokenRequest narrows an installation token.
type tokenRequest struct {
	Repositories []string          `json:"repositories,omitempty"`
	Permissions  map[string]string `json:"permissions,omitempty"`
}

// apiError is a GitHub response with an unexpected status. It holds no
// request credentials.
type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("GitHub returned status %d: %s", e.Status, e.Body)
}

// api calls the GitHub REST API with one authorization value.
type api struct {
	baseURL string
	client  *http.Client
}

func (a *api) do(ctx context.Context, method, path, authorization string, body, out any) (http.Header, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	endpoint := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		endpoint = strings.TrimSuffix(a.baseURL, "/") + path
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return resp.Header, &apiError{Status: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.Header, fmt.Errorf("decoding response: %w", err)
		}
	}
	return resp.Header, nil
}

func bearer(jwt string) string { return "Bearer " + jwt }
func tokenAuth(token string) string {
	return "token " + token
}

func (a *api) getApp(ctx context.Context, appJWT string) (AppInfo, error) {
	var info AppInfo
	_, err := a.do(ctx, http.MethodGet, "/app", bearer(appJWT), nil, &info)
	return info, err
}

func (a *api) listInstallations(ctx context.Context, appJWT string) ([]InstallationInfo, error) {
	var all []InstallationInfo
	for page := 1; page <= 100; page++ {
		var batch []InstallationInfo
		if _, err := a.do(ctx, http.MethodGet, fmt.Sprintf("/app/installations?per_page=100&page=%d", page), bearer(appJWT), nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}

func (a *api) createInstallationToken(ctx context.Context, appJWT string, installationID int64, request tokenRequest) (InstallationToken, error) {
	var token InstallationToken
	_, err := a.do(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", installationID), bearer(appJWT), request, &token)
	if err == nil && token.Token == "" {
		err = fmt.Errorf("GitHub returned no installation token")
	}
	return token, err
}

func (a *api) listInstallationRepositories(ctx context.Context, token string) ([]Repository, error) {
	var all []Repository
	for page := 1; page <= 1000; page++ {
		var batch struct {
			Repositories []Repository `json:"repositories"`
		}
		if _, err := a.do(ctx, http.MethodGet, fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), tokenAuth(token), nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch.Repositories...)
		if len(batch.Repositories) < 100 {
			break
		}
	}
	return all, nil
}
