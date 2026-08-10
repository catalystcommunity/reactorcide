package vcs

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGitHubParseIssueCommentApprovalEvent(t *testing.T) {
	client, err := NewGitHubClient(Config{})
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"action":"created","issue":{"number":17,"pull_request":{"url":"https://api.github.test/pulls/17"}},"comment":{"body":"/reactorcide approve backend-tests pr-untrusted rev"},"repository":{"full_name":"acme/repo","clone_url":"https://github.com/acme/repo.git"},"sender":{"login":"alice"}}`
	req := &http.Request{Header: http.Header{"X-Github-Event": []string{"issue_comment"}}, Body: io.NopCloser(strings.NewReader(payload))}
	event, err := client.ParseWebhook(req)
	if err != nil {
		t.Fatal(err)
	}
	if event.IssueComment == nil || event.IssueComment.IssueNumber != 17 || !event.IssueComment.IsPullRequest || event.SenderLogin != "alice" || event.Repository.FullName != "acme/repo" {
		t.Fatalf("unexpected event: %#v", event)
	}
}
