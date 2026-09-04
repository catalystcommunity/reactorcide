package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// DescribeFormMetadata serves the enumerated values a form control binds to.
//
// Before this op, the only way for the UI to offer valid event types was to
// copy the list out of internal/vcs/event_types.go into the client. A copied
// enum drifts the moment either side changes, and the New Project form's
// "allowed event types" field was a free-text box precisely because nothing
// served the list. Deriving every choice below from the same Go constants the
// rest of the coordinator validates against means the form cannot offer a
// value the server will reject, and cannot omit one the server accepts.
//
// This op is deliberately open to anonymous callers: every value it returns is
// a static vocabulary of the software itself, not deployment data.
//
// Worker classes, execution profiles and queue names are deliberately absent.
// They are org-scoped and privileged -- ListWorkerClasses and
// ListExecutionProfiles both go through requireCIOrgAdmin, and queue listing
// is global-admin only. Serving them from an anonymous op would leak
// deployment configuration. A form that needs one of them calls the op that
// already guards it.
func (s *UiService) DescribeFormMetadata(_ context.Context, _ csilapi.DescribeFormMetadataRequest) (csilapi.DescribeFormMetadataResponse, error) {
	return csilapi.DescribeFormMetadataResponse{
		EventTypes:       eventTypeChoices(),
		CheckoutModes:    checkoutModeChoices(),
		NodeConditions:   nodeConditionChoices(),
		JobStatuses:      jobStatusChoices(),
		WorkflowStatuses: workflowStatusChoices(),
		CiSourceTypes:    ciSourceTypeChoices(),
	}, nil
}

// eventTypeChoices is derived from internal/vcs/event_types.go. EventPing and
// EventUnknown are deliberately absent: a ping is a webhook liveness check
// that never starts CI, and the empty "unknown" value is a parse fallback, so
// neither is a valid entry in a project's allowed_event_types.
func eventTypeChoices() []csilapi.EnumChoice {
	return []csilapi.EnumChoice{
		{Value: string(vcs.EventPush), Label: "Push",
			Description: "A commit is pushed to a branch."},
		{Value: string(vcs.EventPullRequestOpened), Label: "Pull request opened",
			Description: "A pull request is opened against a target branch."},
		{Value: string(vcs.EventPullRequestUpdated), Label: "Pull request updated",
			Description: "New commits are pushed to an open pull request."},
		{Value: string(vcs.EventPullRequestMerged), Label: "Pull request merged",
			Description: "A pull request is merged into its target branch."},
		{Value: string(vcs.EventPullRequestClosed), Label: "Pull request closed",
			Description: "A pull request is closed without a merge."},
		{Value: string(vcs.EventTagCreated), Label: "Tag created",
			Description: "A tag is created in the repository."},
		{Value: string(vcs.EventDirectlySubmitted), Label: "Directly submitted",
			Description: "A job is submitted through the API or CLI, not by a webhook."},
	}
}

func checkoutModeChoices() []csilapi.EnumChoice {
	return []csilapi.EnumChoice{
		{Value: "isolated", Label: "Isolated clones",
			Description: "Each pull-request job clones the repository on its own. Slower, and every job is fully independent."},
		{Value: "shared", Label: "Shared object store",
			Description: "Pull-request jobs share a git object store. Faster checkouts for a large repository."},
	}
}

// nodeConditionChoices matches the conditions internal/workflowengine/rules.go
// evaluates. An empty condition means all_success, so that is the documented
// default rather than a separate choice.
func nodeConditionChoices() []csilapi.EnumChoice {
	return []csilapi.EnumChoice{
		{Value: "all_success", Label: "All dependencies succeeded",
			Description: "The default. The node runs only when every node it depends on succeeded."},
		{Value: "any_failed", Label: "Any dependency failed",
			Description: "The node runs when at least one node it depends on failed. Use this for notification and cleanup nodes."},
		{Value: "always", Label: "Always",
			Description: "The node runs once its dependencies finish, whatever their result."},
	}
}

// jobStatusChoices matches the CHECK constraint on jobs.status
// (models/job.go). "cancelling" is transient but is a real filterable state.
func jobStatusChoices() []csilapi.EnumChoice {
	return []csilapi.EnumChoice{
		{Value: "submitted", Label: "Submitted", Description: "Accepted, not yet queued."},
		{Value: "queued", Label: "Queued", Description: "Waiting for a worker."},
		{Value: "running", Label: "Running", Description: "Executing on a worker."},
		{Value: "cancelling", Label: "Cancelling", Description: "A cancel was requested; the worker has not confirmed the container stopped."},
		{Value: "completed", Label: "Completed", Description: "Finished with exit code 0."},
		{Value: "failed", Label: "Failed", Description: "Finished with a non-zero exit code."},
		{Value: "cancelled", Label: "Cancelled", Description: "Stopped on request."},
		{Value: "timeout", Label: "Timed out", Description: "Exceeded its time limit."},
	}
}

func workflowStatusChoices() []csilapi.EnumChoice {
	return []csilapi.EnumChoice{
		{Value: "evaluating", Label: "Evaluating", Description: "Deciding which nodes to run next."},
		{Value: "running", Label: "Running", Description: "At least one node is executing."},
		{Value: "success", Label: "Success", Description: "Every node reached a successful or skipped state."},
		{Value: "failed", Label: "Failed", Description: "At least one node failed."},
		{Value: "cancelling", Label: "Cancelling", Description: "A cancel was requested and is being applied to the nodes."},
		{Value: "cancelled", Label: "Cancelled", Description: "Stopped on request."},
		{Value: "skipped", Label: "Skipped", Description: "No node qualified to run."},
	}
}

func ciSourceTypeChoices() []csilapi.EnumChoice {
	return []csilapi.EnumChoice{
		{Value: "git", Label: "Git repository",
			Description: "Trusted CI content is cloned from a git URL."},
		{Value: "inline", Label: "Inline",
			Description: "Trusted CI content travels with the job specification."},
	}
}
