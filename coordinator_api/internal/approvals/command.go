package approvals

import (
	"context"
	"errors"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Commands a person can give for a pull request head.
const (
	// CommandApproveCI satisfies the run gate. The head runs on trusted base CI.
	CommandApproveCI = "approve-ci"
	// CommandOverrideCI lets the head's own CI run.
	CommandOverrideCI = "override-ci"
	// CommandAccept counts toward required reviews. It has an optional label.
	CommandAccept = "accept"
	// CommandRevoke withdraws the person's approvals for the head.
	CommandRevoke = "revoke"
	// CommandRetry retries the failed and cancelled jobs of the head's
	// workflows.
	CommandRetry = "retry"
	// CommandRerun runs every workflow of the head again.
	CommandRerun = "rerun"
	// CommandPromote proposes to add a workflow to the required set of the
	// project (docs/required-set.md). An approver of the set adds it at once.
	CommandPromote = "promote"
	// CommandDemote proposes to remove a workflow from the required set. An
	// approver of the set removes it at once.
	CommandDemote = "demote"
)

// Command is one parsed command.
type Command struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	// Shifts are the optional resource and timeout changes of a retry or a
	// rerun, for example "/reactorcide retry memory=8Gi timeout=2h". They
	// are the only changes a command can make (D18, D25).
	Shifts models.RunShifts `json:"shifts,omitempty"`
	// Workflow is the workflow security ID of a promote or demote command.
	Workflow string `json:"workflow,omitempty"`
}

// ParseComment reads a command from a whole comment body. The body must be
// only the command, for example "/reactorcide accept trust-anchors".
func ParseComment(body string) (Command, bool) {
	fields := strings.Fields(strings.TrimSpace(body))
	if len(fields) < 2 || fields[0] != "/reactorcide" {
		return Command{}, false
	}
	command := Command{Name: fields[1]}
	switch command.Name {
	case CommandApproveCI, CommandOverrideCI, CommandRevoke:
		return command, len(fields) == 2
	case CommandRetry, CommandRerun:
		// Each further word must be one key=value shift. Anything else makes
		// the comment not a command.
		shifts, err := runshifts.ParseArgs(fields[2:])
		if err != nil {
			return Command{}, false
		}
		command.Shifts = shifts
		return command, true
	case CommandPromote, CommandDemote:
		// Exactly one workflow security ID. The required set validates it.
		if len(fields) != 3 {
			return Command{}, false
		}
		command.Workflow = fields[2]
		return command, true
	case CommandAccept:
		if len(fields) == 3 {
			command.Label = fields[2]
			return command, models.ValidApprovalLabel(command.Label)
		}
		return command, len(fields) == 2
	}
	return Command{}, false
}

// ParseReviewLabel returns the label of an accept command on its own line in a
// review body, or "" when there is none.
func ParseReviewLabel(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if command, ok := ParseComment(line); ok && command.Name == CommandAccept {
			return command.Label
		}
	}
	return ""
}

// Request is one command from one person for one pull request.
type Request struct {
	Project  *models.Project
	PRNumber int
	// HeadSHA, when set, must be the current head of the pull request. The
	// command is refused when the head changed.
	HeadSHA string
	Command Command
	// Channel is a models.CIApprovalChannel* value.
	Channel string
	// SourceObject is the provider object that made the command, for
	// example "github:comment:123". A redelivery of it does nothing.
	SourceObject string
	// The person is a VCS account (Provider and Login) or a Reactorcide user
	// (UserID). A user with a verified VCS identity link acts as that account.
	Provider string
	Login    string
	UserID   string
	// PrincipalID is a service principal that gives the command through its
	// token. Its person key is "principal:UUID".
	PrincipalID string
	// InstanceAdmin is true for the instance administrator, in every auth
	// mode: the admin token, or a wide service principal token issued on a
	// no-login instance (checkauth.Principal.IsWideInstanceToken).
	// The caller sets it only after that check. The person is the principal
	// of the token (PrincipalID), or the token itself (TokenID) when it has
	// none. It passes every approver check of approve-ci, override-ci,
	// retry, and rerun, and its approvals record the subject project_owner
	// (a global admin is the owner of every project).
	InstanceAdmin bool
	// TokenID is the API token of an InstanceAdmin request without a
	// principal. Its person key is "token:UUID".
	TokenID string
	// RevokeOnly limits a revoke to one kind (a review revokes only accept).
	RevokeOnly string
	// PersonSubjectsOnly records the revoke with only the person's own
	// subject. A dismissed review cancels only that reviewer's approval.
	PersonSubjectsOnly bool
}

// Result says what a command did.
type Result struct {
	Recorded []models.CIApproval `json:"recorded"`
	// Effect is what the command started: "re-evaluate", "rerun", "retry",
	// "reviews", or "" for nothing.
	Effect  string `json:"effect,omitempty"`
	Message string `json:"message"`
}

// HeadReviews is the state of the required reviews of one pull request head.
type HeadReviews struct {
	HeadSHA string         `json:"head_sha"`
	Reviews []ReviewResult `json:"reviews"`
	// State is "success" or "pending".
	State   string `json:"state"`
	Summary string `json:"summary"`
}

// Executor runs commands. The webhook handler implements it, so every
// channel (comment, review, REST, UI) has the same effects.
type Executor interface {
	Execute(ctx context.Context, request Request) (Result, error)
	// HeadReviews returns the required-review state of the current head and
	// the head SHA. Reviews is nil when the project needs no review.
	HeadReviews(ctx context.Context, project *models.Project, prNumber int) (*HeadReviews, error)
}

// ErrNotAllowed is returned when the person has no subject that the command
// needs.
var ErrNotAllowed = errors.New("the person has no subject that this command needs")

// ErrHeadChanged is returned when the request names a head that is no longer
// the head of the pull request.
var ErrHeadChanged = errors.New("the pull request head changed")

// ErrPullRequestClosed is returned for a command on a pull request that is not
// open.
var ErrPullRequestClosed = errors.New("the pull request is not open")

// IsCommandAttempt reports whether a comment body starts with the command
// prefix. Such a comment that ParseComment does not accept gets a short
// reply (it is an invalid command, not a normal comment).
func IsCommandAttempt(body string) bool {
	fields := strings.Fields(strings.TrimSpace(body))
	return len(fields) > 0 && fields[0] == "/reactorcide"
}

// RefusedError is a command that was refused for a reason that the person
// can read on the pull request. Reason is a fixed text of Reactorcide; it
// never contains text of the comment.
type RefusedError struct {
	Reason string
}

func (e *RefusedError) Error() string { return e.Reason }

// CommentError returns why a comment that starts a retry or a rerun command
// is not a command: its shift arguments are not valid, or it asks for a
// change that only a new run can make (D25). It returns nil for every other
// comment.
func CommentError(body string) error {
	fields := strings.Fields(strings.TrimSpace(body))
	if len(fields) < 3 || fields[0] != "/reactorcide" || (fields[1] != CommandRetry && fields[1] != CommandRerun) {
		return nil
	}
	_, err := runshifts.ParseArgs(fields[2:])
	return err
}
