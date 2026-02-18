package vcs

import "strings"

// EventType represents a generic, VCS-agnostic event type
type EventType string

const (
	EventPush               EventType = "push"
	EventPullRequestOpened  EventType = "pull_request_opened"
	EventPullRequestUpdated EventType = "pull_request_updated"
	EventPullRequestMerged  EventType = "pull_request_merged"
	EventPullRequestClosed  EventType = "pull_request_closed"
	EventTagCreated         EventType = "tag_created"
	EventPing               EventType = "ping"
	EventUnknown            EventType = ""
)

// GenericEventFromGitHub translates a GitHub webhook event into a generic EventType.
func GenericEventFromGitHub(eventType, action string, pr *PullRequestInfo, push *PushInfo) EventType {
	switch eventType {
	case "ping":
		return EventPing

	case "push":
		if push == nil {
			return EventUnknown
		}
		if strings.HasPrefix(push.Ref, "refs/tags/") {
			return EventTagCreated
		}
		if strings.HasPrefix(push.Ref, "refs/heads/") {
			return EventPush
		}
		return EventUnknown

	case "pull_request":
		switch action {
		case "opened", "reopened":
			return EventPullRequestOpened
		case "synchronize":
			return EventPullRequestUpdated
		case "closed":
			if pr != nil && pr.Merged {
				return EventPullRequestMerged
			}
			return EventPullRequestClosed
		default:
			return EventUnknown
		}

	default:
		return EventUnknown
	}
}
