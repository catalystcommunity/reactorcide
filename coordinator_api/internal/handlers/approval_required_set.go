package handlers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/ratelimit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/sirupsen/logrus"
)

// executeRequiredSetCommand runs "/reactorcide promote <workflow-id>" and
// "/reactorcide demote <workflow-id>" from a pull request through the
// required set service (docs/required-set.md). The rules are the rules of
// REST and the UI. The live VCS subjects of the person (vcs_team:,
// repository_write) also count toward the approver rules, because the
// command comes in a signed webhook. An approver changes the set at once;
// another person with a role in the project makes a pending proposal.
//
// Separation of duties: an author of the pull request (the pull request
// author, a person who pushed a head, or the account of a commit author or
// committer) cannot demote a workflow from a comment on that pull request.
// Otherwise an author could remove a failing required check from the change
// that it gates. When the authors cannot be listed, the demote is refused
// (fail closed). REST, the CLI, and the UI do not have this rule.
func (h *WebhookHandler) executeRequiredSetCommand(ctx context.Context, project *models.Project, target *prTarget, person approvalPerson, request approvals.Request) (approvals.Result, error) {
	pr := target.PR
	if request.Command.Name == approvals.CommandDemote {
		if refused := h.refuseAuthorDemote(ctx, project, target, person); refused != nil {
			h.recordRefusedCommand(ctx, project, pr, person, request)
			return approvals.Result{}, refused
		}
	}
	service := &requiredset.Service{Store: h.store, Record: func(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB) {
		audit.Record(ctx, h.store, orgID, action, subjectType, subjectID, details)
	}}
	caller := requiredset.Caller{Actor: person.actor(), ExternalSubjects: person.Subjects}
	switch {
	case person.UserID != nil && *person.UserID != "":
		caller.Identity = authz.UserIdentity(*person.UserID)
	case person.Principal != nil && !person.Principal.Inactive:
		caller.Identity = authz.Identity{PrincipalID: person.Principal.Principal.PrincipalID}
	default:
		caller.Identity = authz.AnonymousIdentity()
	}
	if err := requiredset.ValidateSecurityID(request.Command.Workflow); err != nil {
		return approvals.Result{}, &approvals.RefusedError{Reason: "the workflow ID is not valid"}
	}
	action := models.RequiredSetActionPromote
	if request.Command.Name == approvals.CommandDemote {
		action = models.RequiredSetActionDemote
	}
	approver, err := service.IsApprover(ctx, caller, project, "")
	if requiredset.ErrorCode(err) == "unavailable" {
		return approvals.Result{}, &approvals.RefusedError{Reason: "the required set is not available"}
	} else if err != nil {
		return approvals.Result{}, fmt.Errorf("resolving required set approvers: %w", err)
	}
	proposal, err := service.Propose(ctx, caller, project, requiredset.ProposeRequest{Action: action,
		WorkflowSecurityID: request.Command.Workflow, Approve: approver,
		Reason: fmt.Sprintf("%s from a comment on pull request #%d", request.Command.Name, pr.Number)})
	if err != nil {
		switch requiredset.ErrorCode(err) {
		case "permission_denied":
			h.recordRefusedCommand(ctx, project, pr, person, request)
			return approvals.Result{}, approvals.ErrNotAllowed
		case "unavailable":
			return approvals.Result{}, &approvals.RefusedError{Reason: "the required set is not available"}
		case "conflict":
			return approvals.Result{}, &approvals.RefusedError{Reason: "the same change is already in the required set or already pending"}
		case "not_found":
			return approvals.Result{}, &approvals.RefusedError{Reason: "the workflow is not in the required set"}
		case "invalid_argument", "failed_precondition":
			// The service text can contain the workflow ID; a fixed text
			// does not repeat it.
			return approvals.Result{}, &approvals.RefusedError{Reason: "the required set refused the change (look at the required set page of the project)"}
		}
		return approvals.Result{}, err
	}
	result := approvals.Result{Recorded: []models.CIApproval{}, Effect: "required_set"}
	if proposal.State == models.RequiredSetProposalApproved {
		result.Message = fmt.Sprintf("the %s is done; the required set changed", request.Command.Name)
	} else {
		result.Message = fmt.Sprintf("the %s is proposed; an approver of the required set must approve it", request.Command.Name)
	}
	// The workflow ID is not in the message: the reply on the pull request
	// never repeats text of the comment.
	return result, nil
}

// refuseAuthorDemote returns a refusal when the person is an author of the
// pull request, or when the authors cannot be listed.
func (h *WebhookHandler) refuseAuthorDemote(ctx context.Context, project *models.Project, target *prTarget, person approvalPerson) error {
	authors := h.prAuthors(ctx, project, target)
	if authors.Unverified {
		return &approvals.RefusedError{Reason: "the authors of the pull request could not be verified, so a demote from a comment is not possible (use the required set page)"}
	}
	keys := []string{person.Person}
	if person.Login != "" {
		keys = append(keys, models.VCSPerson(string(target.Provider), person.Login))
	}
	for _, key := range keys {
		if key != "" && (authors.Authors[key] || key == authors.LastPusher) {
			return &approvals.RefusedError{Reason: "an author of the pull request cannot demote a workflow from it"}
		}
	}
	return nil
}

// Replies to pull request commands. A refused or invalid command gets one
// short reply, so the person knows that it did nothing. The reply is a fixed
// text: it never repeats text of the comment, and it never mentions a
// person. Replies are rate limited per pull request and per project, so a
// person cannot make Reactorcide post comments without limit.
const (
	commandReplyMarker = "<!-- reactorcide:command-reply -->"
	commandHelp        = "Commands: approve-ci, override-ci, accept [label], revoke, retry [shifts], rerun [shifts], promote <workflow-id>, demote <workflow-id>."
)

var (
	commandReplyLimiterOnce sync.Once
	commandReplyPRLimiter   *ratelimit.Limiter
	commandReplyProjLimiter *ratelimit.Limiter
)

func commandReplyLimiters() (*ratelimit.Limiter, *ratelimit.Limiter) {
	commandReplyLimiterOnce.Do(func() {
		// Per pull request: 3 replies at once, then one a minute.
		// Per project: 20 at once, then 10 a minute.
		if commandReplyPRLimiter == nil {
			commandReplyPRLimiter = ratelimit.New(1, 3)
		}
		if commandReplyProjLimiter == nil {
			commandReplyProjLimiter = ratelimit.New(10, 20)
		}
	})
	return commandReplyPRLimiter, commandReplyProjLimiter
}

// commandReplyText returns the reply for a command result, or "" for none.
func commandReplyText(err error, result approvals.Result, commandName string) string {
	var refused *approvals.RefusedError
	switch {
	case errors.Is(err, approvals.ErrNotAllowed):
		return fmt.Sprintf("Reactorcide did not run the %s command: you are not allowed to give it for this project.", commandName)
	case errors.Is(err, approvals.ErrHeadChanged):
		return fmt.Sprintf("Reactorcide did not run the %s command: the pull request head changed.", commandName)
	case errors.Is(err, approvals.ErrPullRequestClosed):
		return fmt.Sprintf("Reactorcide did not run the %s command: the pull request is not open.", commandName)
	case errors.As(err, &refused):
		return fmt.Sprintf("Reactorcide did not run the %s command: %s.", commandName, refused.Reason)
	case err == nil && result.Effect == "required_set":
		return "Reactorcide: " + result.Message + "."
	}
	return ""
}

// replyToCommand posts a reply on the pull request when the limits allow it.
// A failure is logged; the command result does not change.
func (h *WebhookHandler) replyToCommand(ctx context.Context, project *models.Project, prNumber int, text string) {
	if project == nil || prNumber <= 0 || text == "" {
		return
	}
	prLimiter, projectLimiter := commandReplyLimiters()
	if prLimiter != nil {
		if ok, _ := prLimiter.Allow(fmt.Sprintf("%s#%d", project.ProjectID, prNumber)); !ok {
			return
		}
	}
	if projectLimiter != nil {
		if ok, _ := projectLimiter.Allow(project.ProjectID); !ok {
			return
		}
	}
	provider, repo, _, err := h.projectRepository(project)
	if err != nil {
		return
	}
	client := h.getStatusClient(ctx, project, provider, h.vcsClients[provider])
	if client == nil {
		return
	}
	replyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := client.UpdatePRComment(replyCtx, repo, prNumber, text+"\n\n"+commandReplyMarker); err != nil {
		h.logger.WithError(err).WithFields(logrus.Fields{"project": project.Name, "pr_number": prNumber}).
			Warn("Could not reply to a pull request command")
	}
}

// invalidCommandReply is the reply for a comment that starts with the
// command prefix but is not a valid command.
func invalidCommandReply(reason string) string {
	if reason == "" {
		return "Reactorcide did not run the command: it is not a valid command. " + commandHelp
	}
	return "Reactorcide did not run the command: " + reason + ". " + commandHelp
}
