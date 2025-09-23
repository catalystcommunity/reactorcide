package vcs

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubClient_ParseWebhook(t *testing.T) {
	client, err := NewGitHubClient(Config{Provider: GitHub})
	require.NoError(t, err)

	tests := []struct {
		name        string
		eventType   string
		payload     string
		wantErr     bool
		checkResult func(t *testing.T, event *WebhookEvent)
	}{
		{
			name:      "pull_request_opened",
			eventType: "pull_request",
			payload: `{
				"action": "opened",
				"number": 123,
				"pull_request": {
					"number": 123,
					"title": "Test PR",
					"body": "Test description",
					"state": "open",
					"html_url": "https://github.com/test/repo/pull/123",
					"head": {
						"ref": "feature-branch",
						"sha": "abc123"
					},
					"base": {
						"ref": "main",
						"sha": "def456"
					},
					"user": {
						"login": "testuser"
					}
				},
				"repository": {
					"full_name": "test/repo",
					"clone_url": "https://github.com/test/repo.git",
					"ssh_url": "git@github.com:test/repo.git",
					"html_url": "https://github.com/test/repo",
					"default_branch": "main"
				}
			}`,
			wantErr: false,
			checkResult: func(t *testing.T, event *WebhookEvent) {
				assert.Equal(t, GitHub, event.Provider)
				assert.Equal(t, "pull_request", event.EventType)
				assert.NotNil(t, event.PullRequest)
				assert.Equal(t, 123, event.PullRequest.Number)
				assert.Equal(t, "Test PR", event.PullRequest.Title)
				assert.Equal(t, "opened", event.PullRequest.Action)
				assert.Equal(t, "abc123", event.PullRequest.HeadSHA)
				assert.Equal(t, "test/repo", event.Repository.FullName)
			},
		},
		{
			name:      "push_event",
			eventType: "push",
			payload: `{
				"ref": "refs/heads/main",
				"before": "000000",
				"after": "abc123",
				"created": false,
				"deleted": false,
				"forced": false,
				"compare": "https://github.com/test/repo/compare/000000...abc123",
				"commits": [
					{
						"id": "abc123",
						"message": "Test commit",
						"timestamp": "2024-01-01T12:00:00Z",
						"url": "https://github.com/test/repo/commit/abc123",
						"author": {
							"name": "Test User",
							"email": "test@example.com"
						},
						"added": ["file1.txt"],
						"modified": ["file2.txt"],
						"removed": []
					}
				],
				"repository": {
					"full_name": "test/repo",
					"clone_url": "https://github.com/test/repo.git",
					"ssh_url": "git@github.com:test/repo.git",
					"html_url": "https://github.com/test/repo",
					"default_branch": "main"
				},
				"pusher": {
					"name": "testuser",
					"email": "test@example.com"
				}
			}`,
			wantErr: false,
			checkResult: func(t *testing.T, event *WebhookEvent) {
				assert.Equal(t, GitHub, event.Provider)
				assert.Equal(t, "push", event.EventType)
				assert.NotNil(t, event.Push)
				assert.Equal(t, "refs/heads/main", event.Push.Ref)
				assert.Equal(t, "abc123", event.Push.After)
				assert.Len(t, event.Push.Commits, 1)
				assert.Equal(t, "Test commit", event.Push.Commits[0].Message)
			},
		},
		{
			name:      "ping_event",
			eventType: "ping",
			payload:   `{"zen": "Design for failure."}`,
			wantErr:   false,
			checkResult: func(t *testing.T, event *WebhookEvent) {
				assert.Equal(t, GitHub, event.Provider)
				assert.Equal(t, "ping", event.EventType)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/webhook", bytes.NewBufferString(tt.payload))
			req.Header.Set("X-GitHub-Event", tt.eventType)

			event, err := client.ParseWebhook(req)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, event)
				}
			}
		})
	}
}

func TestGitHubClient_ValidateWebhook(t *testing.T) {
	client, err := NewGitHubClient(Config{
		Provider:      GitHub,
		WebhookSecret: "test-secret",
	})
	require.NoError(t, err)

	tests := []struct {
		name      string
		body      string
		signature string
		secret    string
		wantErr   bool
	}{
		{
			name:      "valid_signature",
			body:      `{"test": "data"}`,
			signature: "sha256=1b2c16b75bd2a870c114153ccda5bcfca63314bc722fa160d690de133ccbb9db",
			secret:    "test-secret",
			wantErr:   false,
		},
		{
			name:      "invalid_signature",
			body:      `{"test": "data"}`,
			signature: "sha256=invalid",
			secret:    "test-secret",
			wantErr:   true,
		},
		{
			name:      "missing_signature",
			body:      `{"test": "data"}`,
			signature: "",
			secret:    "test-secret",
			wantErr:   true,
		},
		{
			name:      "no_secret_configured",
			body:      `{"test": "data"}`,
			signature: "",
			secret:    "",
			wantErr:   false, // No validation if secret not configured
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/webhook", bytes.NewBufferString(tt.body))
			if tt.signature != "" {
				req.Header.Set("X-Hub-Signature-256", tt.signature)
			}

			err := client.ValidateWebhook(req, tt.secret)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGitHubClient_UpdateCommitStatus(t *testing.T) {
	// Create a test server to mock GitHub API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/repos/test/repo/statuses/abc123", r.URL.Path)
		assert.Equal(t, "token test-token", r.Header.Get("Authorization"))

		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id": 1, "state": "success"}`))
	}))
	defer server.Close()

	client, err := NewGitHubClient(Config{
		Provider: GitHub,
		Token:    "test-token",
		BaseURL:  server.URL,
	})
	require.NoError(t, err)

	update := StatusUpdate{
		SHA:         "abc123",
		State:       StatusSuccess,
		TargetURL:   "https://ci.example.com/job/123",
		Description: "Test passed",
		Context:     "continuous-integration/test",
	}

	err = client.UpdateCommitStatus(context.Background(), "test/repo", update)
	assert.NoError(t, err)
}

func TestGitHubClient_MapStatusState(t *testing.T) {
	client, err := NewGitHubClient(Config{Provider: GitHub})
	require.NoError(t, err)

	tests := []struct {
		input    StatusState
		expected string
	}{
		{StatusPending, "pending"},
		{StatusRunning, "pending"},
		{StatusSuccess, "success"},
		{StatusFailure, "failure"},
		{StatusError, "error"},
		{StatusCancelled, "error"},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			result := client.mapStatusState(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}