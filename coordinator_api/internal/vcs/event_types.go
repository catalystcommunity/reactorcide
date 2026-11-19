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
	// EventDirectlySubmitted marks jobs submitted directly through the API/CLI
	// rather than by a VCS webhook. Such jobs have no VCS provider integration,
	// so they never post commit statuses or PR comments; the type exists to keep
	// their workflow comment marker distinct from real VCS events.
	EventDirectlySubmitted EventType = "directly_submitted"
	EventUnknown           EventType = ""
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
		case "ready_for_review":
			// A draft became ready. It is an update of the pull request with
			// draft=false, so every workflow that runs on
			// pull_request_updated runs, with no new event name in workflow
			// files. converted_to_draft starts nothing: the head did not
			// change, and the work of the head already ran or was skipped.
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
