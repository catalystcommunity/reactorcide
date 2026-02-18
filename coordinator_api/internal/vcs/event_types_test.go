package vcs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenericEventFromGitHub(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		action    string
		pr        *PullRequestInfo
		push      *PushInfo
		want      EventType
	}{
		// Push events
		{
			name:      "push to branch",
			eventType: "push",
			push:      &PushInfo{Ref: "refs/heads/main"},
			want:      EventPush,
		},
		{
			name:      "push to feature branch",
			eventType: "push",
			push:      &PushInfo{Ref: "refs/heads/feature/my-feature"},
			want:      EventPush,
		},
		{
			name:      "tag push",
			eventType: "push",
			push:      &PushInfo{Ref: "refs/tags/v1.0.0"},
			want:      EventTagCreated,
		},
		{
			name:      "tag push with nested name",
			eventType: "push",
			push:      &PushInfo{Ref: "refs/tags/release/v2.0"},
			want:      EventTagCreated,
		},
		{
			name:      "push with unknown ref",
			eventType: "push",
			push:      &PushInfo{Ref: "refs/other/something"},
			want:      EventUnknown,
		},
		{
			name:      "push with nil push info",
			eventType: "push",
			push:      nil,
			want:      EventUnknown,
		},

		// Pull request events
		{
			name:      "PR opened",
			eventType: "pull_request",
			action:    "opened",
			pr:        &PullRequestInfo{Action: "opened"},
			want:      EventPullRequestOpened,
		},
		{
			name:      "PR reopened maps to opened",
			eventType: "pull_request",
			action:    "reopened",
			pr:        &PullRequestInfo{Action: "reopened"},
			want:      EventPullRequestOpened,
		},
		{
			name:      "PR synchronize maps to updated",
			eventType: "pull_request",
			action:    "synchronize",
			pr:        &PullRequestInfo{Action: "synchronize"},
			want:      EventPullRequestUpdated,
		},
		{
			name:      "PR closed and merged",
			eventType: "pull_request",
			action:    "closed",
			pr:        &PullRequestInfo{Action: "closed", Merged: true},
			want:      EventPullRequestMerged,
		},
		{
			name:      "PR closed without merge",
			eventType: "pull_request",
			action:    "closed",
			pr:        &PullRequestInfo{Action: "closed", Merged: false},
			want:      EventPullRequestClosed,
		},
		{
			name:      "PR closed with nil PR info",
			eventType: "pull_request",
			action:    "closed",
			pr:        nil,
			want:      EventPullRequestClosed,
		},
		{
			name:      "PR unknown action",
			eventType: "pull_request",
			action:    "labeled",
			pr:        &PullRequestInfo{Action: "labeled"},
			want:      EventUnknown,
		},
		{
			name:      "PR assigned action",
			eventType: "pull_request",
			action:    "assigned",
			pr:        &PullRequestInfo{Action: "assigned"},
			want:      EventUnknown,
		},

		// Ping event
		{
			name:      "ping event",
			eventType: "ping",
			want:      EventPing,
		},
		// Unknown event types
		{
			name:      "issues event",
			eventType: "issues",
			want:      EventUnknown,
		},
		{
			name:      "empty event type",
			eventType: "",
			want:      EventUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenericEventFromGitHub(tt.eventType, tt.action, tt.pr, tt.push)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEventTypeConstants(t *testing.T) {
	// Verify the string values of constants are as expected
	assert.Equal(t, EventType("push"), EventPush)
	assert.Equal(t, EventType("pull_request_opened"), EventPullRequestOpened)
	assert.Equal(t, EventType("pull_request_updated"), EventPullRequestUpdated)
	assert.Equal(t, EventType("pull_request_merged"), EventPullRequestMerged)
	assert.Equal(t, EventType("pull_request_closed"), EventPullRequestClosed)
	assert.Equal(t, EventType("tag_created"), EventTagCreated)
	assert.Equal(t, EventType(""), EventUnknown)
}
