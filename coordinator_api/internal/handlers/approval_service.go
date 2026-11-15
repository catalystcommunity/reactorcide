package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobcontrol"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/sirupsen/logrus"
)

// The webhook handler is the one approval path. A PR comment, a PR review,
// the REST API, and the UI all call Execute, so a command has the same
// authorization and the same effects whatever channel it comes from.
var _ approvals.Executor = (*WebhookHandler)(nil)

type approvalIdentityStore interface {
	ResolveVCSIdentityLink(ctx context.Context, provider, subject string) (*models.VCSIdentityLink, error)
	ListVCSIdentityLinks(ctx context.Context) ([]models.VCSIdentityLink, error)
	ListGroupsForUser(ctx context.Context, userID string) ([]models.Group, error)
}

// approvalPerson is the person who gives a command and every approver
// subject they have now.
type approvalPerson struct {
	Person   string
	Provider string
	Login    string
	UserID   *string
	Subjects []string
}

func (p approvalPerson) has(subjects []string) bool {
	for _, wanted := range subjects {
		for _, have := range p.Subjects {
			if wanted == have {
				return true
			}
		}
	}
	return false
}

// prTarget is a pull request with the client that can read it.
type prTarget struct {
	Provider vcs.Provider
	Repo     string
	CloneURL string
	Client   vcs.Client
	PR       *vcs.PullRequestInfo
}

func (h *WebhookHandler) loadPRTarget(ctx context.Context, project *models.Project, prNumber int) (*prTarget, error) {
	provider, repo, cloneURL, err := h.projectRepository(project)
	if err != nil {
		return nil, err
	}
	client := h.getStatusClient(ctx, project, provider, h.vcsClients[provider])
	if client == nil {
		return nil, fmt.Errorf("no %s client is configured", provider)
	}
	pr, err := client.GetPRInfo(ctx, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("loading pull request: %w", err)
	}
	return &prTarget{Provider: provider, Repo: repo, CloneURL: cloneURL, Client: client, PR: pr}, nil
}

// resolvePerson finds the approver subjects of a VCS account or of a
// Reactorcide user. A user with a verified identity link for the provider is
// that VCS account, so a user has the same roles in the UI as in a comment.
func (h *WebhookHandler) resolvePerson(ctx context.Context, project *models.Project, target *prTarget, login, userID string) (approvalPerson, error) {
	identities, _ := h.store.(approvalIdentityStore)
	person := approvalPerson{Provider: string(target.Provider), Login: strings.TrimSpace(login)}
	if userID != "" {
		id := userID
		person.UserID = &id
		if person.Login == "" && identities != nil {
			links, err := identities.ListVCSIdentityLinks(ctx)
			if err != nil {
				return approvalPerson{}, fmt.Errorf("resolving identity links: %w", err)
			}
			for _, link := range links {
				if link.UserID == userID && strings.EqualFold(link.Provider, string(target.Provider)) && link.VerifiedBy != "" {
					person.Login = link.ExternalSubject
					break
				}
			}
		}
	}
	subjects := []string{}
	if person.Login != "" {
		subjects = append(subjects, vcsUserSubject(target.Provider, person.Login))
		if resolver, ok := target.Client.(vcs.ActorSubjectResolver); ok {
			resolved, err := resolver.ResolveActorSubjects(ctx, target.Repo, person.Login)
			if err != nil {
				return approvalPerson{}, fmt.Errorf("resolving approver facts: %w", err)
			}
			subjects = append(subjects, resolved...)
		}
		if person.UserID == nil && identities != nil {
			if link, err := identities.ResolveVCSIdentityLink(ctx, string(target.Provider), person.Login); err == nil && link != nil && link.VerifiedBy != "" {
				id := link.UserID
				person.UserID = &id
			}
		}
	}
	if person.UserID != nil && identities != nil {
		groups, err := identities.ListGroupsForUser(ctx, *person.UserID)
		if err != nil {
			return approvalPerson{}, fmt.Errorf("resolving approver groups: %w", err)
		}
		for _, group := range groups {
			if group.OrgID == project.OwnershipOrgID() {
				subjects = append(subjects, "reactorcide_group:"+group.Name)
			}
		}
		if roleStore, ok := h.store.(authz.RoleStore); ok {
			owner, err := authz.NewResolver(roleStore).IsProjectOwner(ctx, authz.UserIdentity(*person.UserID), project.ProjectID)
			if err != nil {
				return approvalPerson{}, fmt.Errorf("resolving project owner: %w", err)
			}
			if owner {
				subjects = append(subjects, "project_owner")
			}
		}
	}
	valid := []string{}
	for _, subject := range uniqueStrings(subjects) {
		// A resolver fact that an approval cannot record is not a subject.
		if models.ValidApproverSubject(subject) {
			valid = append(valid, subject)
		}
	}
	person.Subjects = valid
	switch {
	case person.Login != "":
		person.Person = models.VCSPerson(string(target.Provider), person.Login)
	case person.UserID != nil:
		person.Person = models.UserPerson(*person.UserID)
	default:
		return approvalPerson{}, fmt.Errorf("the command has no person")
	}
	return person, nil
}

func (h *WebhookHandler) effectiveTrust(ctx context.Context, project *models.Project) (models.EffectiveTrustSettings, error) {
	settings, ok := h.store.(store.CITrustSettingsStore)
	if !ok {
		return models.EffectiveTrust(nil, nil), nil
	}
	return settings.EffectiveCITrust(ctx, project.OwnershipOrgID(), project.ProjectID)
}

// overrideSubjects are the subjects that can give override-ci: the setting,
// and the approval subjects of every head_ci policy rule. A rule's own
// approval list makes that rule's narrower grant possible.
func (h *WebhookHandler) overrideSubjects(ctx context.Context, project *models.Project, trust models.EffectiveTrustSettings) []string {
	subjects := append([]string{}, trust.OverrideApprovers...)
	if policies, ok := h.store.(ciPolicyStore); ok {
		if stored, err := policies.GetCIPolicyByProject(ctx, project.ProjectID); err == nil {
			if parsed, parseErr := parseStoredPolicy(stored); parseErr == nil {
				for _, rule := range parsed.HeadCI {
					subjects = append(subjects, rule.Approval.Any...)
				}
			}
		}
	}
	return uniqueStrings(subjects)
}

// Execute runs one command for one pull request.
func (h *WebhookHandler) Execute(ctx context.Context, request approvals.Request) (approvals.Result, error) {
	project := request.Project
	if project == nil || request.PRNumber <= 0 {
		return approvals.Result{}, fmt.Errorf("a project and a pull request are required")
	}
	actions, ok := h.store.(store.CIApprovalStore)
	if !ok {
		return approvals.Result{}, fmt.Errorf("CI approvals are not available")
	}
	target, err := h.loadPRTarget(ctx, project, request.PRNumber)
	if err != nil {
		return approvals.Result{}, err
	}
	pr := target.PR
	if request.HeadSHA != "" && request.HeadSHA != pr.HeadSHA {
		return approvals.Result{}, approvals.ErrHeadChanged
	}
	if !strings.EqualFold(pr.State, "open") && request.Command.Name != approvals.CommandRevoke {
		return approvals.Result{}, approvals.ErrPullRequestClosed
	}
	person, err := h.resolvePerson(ctx, project, target, request.Login, request.UserID)
	if err != nil {
		return approvals.Result{}, err
	}
	trust, err := h.effectiveTrust(ctx, project)
	if err != nil {
		return approvals.Result{}, fmt.Errorf("loading CI trust settings: %w", err)
	}
	fields := logrus.Fields{"project": project.Name, "pr_number": pr.Number, "head_sha": pr.HeadSHA,
		"command": request.Command.Name, "person": person.Person, "channel": request.Channel}

	kinds := []string{}
	switch request.Command.Name {
	case approvals.CommandApproveCI, approvals.CommandRetry, approvals.CommandRerun:
		if !person.has(trust.RunApprovers) {
			h.recordRefusedCommand(ctx, project, pr, person, request)
			return approvals.Result{}, approvals.ErrNotAllowed
		}
		if request.Command.Name == approvals.CommandApproveCI {
			kinds = []string{models.CIApprovalKindRun}
		}
	case approvals.CommandOverrideCI:
		if !person.has(h.overrideSubjects(ctx, project, trust)) {
			h.recordRefusedCommand(ctx, project, pr, person, request)
			return approvals.Result{}, approvals.ErrNotAllowed
		}
		kinds = []string{models.CIApprovalKindHeadCI}
	case approvals.CommandAccept:
		if request.Command.Label != "" && !models.ValidApprovalLabel(request.Command.Label) {
			return approvals.Result{}, fmt.Errorf("label %q is not valid", request.Command.Label)
		}
		kinds = []string{models.CIApprovalKindAccept}
	case approvals.CommandRevoke:
		kinds = []string{models.CIApprovalKindRun, models.CIApprovalKindHeadCI, models.CIApprovalKindAccept}
		if request.RevokeOnly != "" {
			kinds = []string{request.RevokeOnly}
		}
	default:
		return approvals.Result{}, fmt.Errorf("command %q is unknown", request.Command.Name)
	}

	result := approvals.Result{Recorded: []models.CIApproval{}}
	headRepository := target.Repo
	if pr.HeadRepository != nil && pr.HeadRepository.FullName != "" {
		headRepository = pr.HeadRepository.FullName
	}
	subjects := person.Subjects
	if request.PersonSubjectsOnly && person.Login != "" {
		subjects = []string{vcsUserSubject(target.Provider, person.Login)}
	}
	duplicate := false
	for _, kind := range kinds {
		action := &models.CIApproval{Kind: kind, Action: models.CIApprovalActionApproved,
			OrgID: project.OwnershipOrgID(), ProjectID: project.ProjectID, PRNumber: pr.Number,
			HeadRepository: headRepository, HeadSHA: pr.HeadSHA, Person: person.Person, Subjects: subjects,
			Channel: request.Channel, ApproverUserID: person.UserID, ApproverProvider: person.Provider}
		if request.Command.Name == approvals.CommandRevoke {
			action.Action = models.CIApprovalActionRevoked
		}
		if kind == models.CIApprovalKindAccept && action.Action == models.CIApprovalActionApproved {
			action.Label = request.Command.Label
		}
		if request.SourceObject != "" {
			object := request.SourceObject
			if len(kinds) > 1 {
				object += ":" + kind
			}
			action.SourceObject = &object
		}
		if err := actions.CreateCIApproval(ctx, action); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				duplicate = true
				continue
			}
			return approvals.Result{}, fmt.Errorf("recording approval: %w", err)
		}
		result.Recorded = append(result.Recorded, *action)
		audit.Record(ctx, h.store, action.OrgID, "ci_approval."+action.Action, "ci_approval", action.ApprovalID,
			models.JSONB{"project_id": action.ProjectID, "pr_number": action.PRNumber, "head_sha": action.HeadSHA,
				"kind": action.Kind, "label": action.Label, "person": action.Person, "subjects": []string(action.Subjects),
				"channel": action.Channel})
	}
	if duplicate && len(result.Recorded) == 0 {
		// A redelivery of a provider object: the first delivery already had
		// its effect.
		result.Message = "already recorded"
		return result, nil
	}

	switch request.Command.Name {
	case approvals.CommandApproveCI:
		result.Effect, result.Message = h.reevaluateIfGated(ctx, project, target, result.Recorded[0])
	case approvals.CommandOverrideCI:
		result.Effect = "re-evaluate"
		result.Message = "override-ci recorded; evaluating the head again"
		h.startCoordinatorEvent(ctx, project, target, models.CIEventSourceApproval, result.Recorded[0].ApprovalID)
	case approvals.CommandAccept, approvals.CommandRevoke:
		result.Effect = "reviews"
		result.Message = request.Command.Name + " recorded"
		h.publishReviewsStatus(ctx, project, target)
	case approvals.CommandRetry:
		result.Effect = "retry"
		result.Message = h.retryHead(ctx, project, pr)
	case approvals.CommandRerun:
		result.Effect = "rerun"
		id := request.SourceObject
		if id == "" {
			id = fmt.Sprintf("%s:%d", person.Person, time.Now().UnixNano())
		}
		h.startCoordinatorEvent(ctx, project, target, models.CIEventSourceRerun, id)
		result.Message = "running every workflow of the head again"
	}
	h.logger.WithFields(fields).WithField("effect", result.Effect).Info(result.Message)
	return result, nil
}

func (h *WebhookHandler) recordRefusedCommand(ctx context.Context, project *models.Project, pr *vcs.PullRequestInfo, person approvalPerson, request approvals.Request) {
	audit.Record(ctx, h.store, project.OwnershipOrgID(), "ci_approval.refused", "project", project.ProjectID,
		models.JSONB{"pr_number": pr.Number, "head_sha": pr.HeadSHA, "command": request.Command.Name,
			"person": person.Person, "subjects": person.Subjects, "channel": request.Channel})
}

// reevaluateIfGated evaluates the head again when the run gate held its last
// event. A head that already ran has nothing to release.
func (h *WebhookHandler) reevaluateIfGated(ctx context.Context, project *models.Project, target *prTarget, approval models.CIApproval) (string, string) {
	heads, ok := h.store.(store.PRHeadStore)
	if !ok {
		h.startCoordinatorEvent(ctx, project, target, models.CIEventSourceApproval, approval.ApprovalID)
		return "re-evaluate", "approve-ci recorded; evaluating the head"
	}
	events, err := heads.ListCIEventsForPRHead(ctx, project.ProjectID, target.PR.Number, target.PR.HeadSHA)
	if err != nil {
		h.logger.WithError(err).Warn("Could not list the events of the head; evaluating it again")
	}
	if err == nil && len(events) > 0 && events[0].Status != models.CIEventStatusAwaitingApproval {
		return "", "approve-ci recorded; the head is not held by the run gate"
	}
	h.startCoordinatorEvent(ctx, project, target, models.CIEventSourceApproval, approval.ApprovalID)
	return "re-evaluate", "approve-ci recorded; evaluating the head"
}

// startCoordinatorEvent starts a new evaluation of the current head. The event
// uses the pull request as the provider reports it now. Its actor is the actor
// of the last webhook event for that head, so admission sees the same actor
// facts as the event that ran CI first; an approver is only an approver. If no
// such event is recorded, the pull request author is the actor.
func (h *WebhookHandler) startCoordinatorEvent(ctx context.Context, project *models.Project, target *prTarget, source, id string) {
	pr := target.PR
	fields := logrus.Fields{"project": project.Name, "pr_number": pr.Number, "source": source, "id": id}
	actor := pr.AuthorLogin
	if finder, ok := h.store.(store.CIEventActorStore); ok {
		latest, err := finder.LatestCIEventActorForPR(ctx, project.ProjectID, pr.Number, pr.HeadSHA)
		if err != nil {
			h.logger.WithError(err).WithFields(fields).Warn("Could not find the actor of the last event; using the pull request author")
		} else if latest != "" {
			actor = latest
		}
	}
	current := *pr
	current.Action = source
	event := &vcs.WebhookEvent{
		Provider:     target.Provider,
		EventType:    "ci_" + source,
		GenericEvent: vcs.EventPullRequestUpdated,
		Repository:   vcs.RepositoryInfo{FullName: target.Repo, CloneURL: target.CloneURL},
		PullRequest:  &current,
		SenderLogin:  actor,
		DeliveryID:   source + ":" + id,
		Coordinator:  &vcs.CoordinatorEvent{Source: source, ID: id},
	}
	if err := h.processPullRequestEvent(event, target.Client, project); err != nil {
		// The approval is stored. A push or a reopen still evaluates the head.
		h.logger.WithError(err).WithFields(fields).Warn("Could not start a new evaluation of the pull request")
	}
}

// retryHead retries the failed and cancelled jobs of every root workflow of
// the newest evaluated event of the head.
func (h *WebhookHandler) retryHead(ctx context.Context, project *models.Project, pr *vcs.PullRequestInfo) string {
	heads, okHeads := h.store.(store.PRHeadStore)
	checks, okChecks := h.store.(store.CIEventChecksStore)
	if !okHeads || !okChecks {
		return "retry is not available"
	}
	events, err := heads.ListCIEventsForPRHead(ctx, project.ProjectID, pr.Number, pr.HeadSHA)
	if err != nil {
		return "could not list the events of the head"
	}
	for _, event := range events {
		if event.EvalJobID == nil {
			continue
		}
		workflows, err := checks.ListRootWorkflowInstancesForEvent(ctx, event.EventID)
		if err != nil {
			return "could not list the workflows of the head"
		}
		retried := 0
		for _, workflow := range workflows {
			jobs, retryErr := jobcontrol.RetryUnsuccessfulJobs(ctx, h.store, h.corndogsClient, workflow.WorkflowID)
			if retryErr != nil {
				h.logger.WithError(retryErr).WithField("workflow_id", workflow.WorkflowID).Warn("Could not retry workflow jobs")
				continue
			}
			retried += len(jobs)
		}
		return fmt.Sprintf("retried %d jobs", retried)
	}
	return "the head has no workflows to retry"
}

// runAuthorityFor decides the run gate and override for a pull request event.
// held is true when the gate holds the event: it must not be evaluated.
func (h *WebhookHandler) runAuthorityFor(ctx context.Context, project *models.Project, event *vcs.WebhookEvent, client vcs.Client) (authority approvals.RunAuthority, held bool, err error) {
	pr := event.PullRequest
	if pr == nil || event.GenericEvent == vcs.EventPullRequestMerged || event.GenericEvent == vcs.EventPullRequestClosed {
		return authority, false, nil
	}
	trust, err := h.effectiveTrust(ctx, project)
	if err != nil {
		return authority, true, fmt.Errorf("loading CI trust settings: %w", err)
	}
	rows := []models.CIApproval{}
	if actions, ok := h.store.(store.CIApprovalStore); ok {
		rows, err = actions.ListCIApprovalsForHead(ctx, project.ProjectID, pr.Number, pr.HeadSHA)
		if err != nil {
			return authority, true, fmt.Errorf("loading approvals: %w", err)
		}
	}
	now := time.Now().UTC()
	if granted := approvals.Granted(rows, models.CIApprovalKindHeadCI, pr.HeadSHA, trust.OverrideApprovers, now); granted != nil {
		authority.Override = &approvals.RunOverride{ApprovalID: granted.ApprovalID, Person: granted.Person, Profile: trust.OverrideProfile}
	}
	gated, reasons := h.runGateApplies(ctx, trust, event, client)
	if !gated {
		return authority, false, nil
	}
	release := approvals.Granted(rows, models.CIApprovalKindRun, pr.HeadSHA, trust.RunApprovers, now)
	if release == nil && authority.Override != nil {
		// override-ci is a stronger approval than approve-ci.
		release = &models.CIApproval{ApprovalID: authority.Override.ApprovalID}
	}
	if release == nil {
		h.logger.WithFields(logrus.Fields{"project": project.Name, "pr_number": pr.Number, "reasons": reasons}).
			Info("The run gate holds the pull request until approve-ci")
		return authority, true, nil
	}
	authority.Gated = true
	authority.Profile = trust.GatedRunProfile()
	authority.RunApprovalID = release.ApprovalID
	return authority, false, nil
}

// runGateApplies reports whether one gate condition matches the event. A fact
// that cannot be verified holds the pull request.
func (h *WebhookHandler) runGateApplies(ctx context.Context, trust models.EffectiveTrustSettings, event *vcs.WebhookEvent, client vcs.Client) (bool, []string) {
	pr := event.PullRequest
	var reasons []string
	if trust.HasRunGate(models.RunGateForks) && pr.HeadRepository != nil {
		reasons = append(reasons, "fork")
	}
	if trust.HasRunGate(models.RunGateFirstTimeContributors) {
		switch strings.ToUpper(pr.AuthorAssociation) {
		case "OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR":
		default:
			reasons = append(reasons, "first-time contributor")
		}
	}
	if trust.HasRunGate(models.RunGateOutsideContributors) {
		resolver, ok := client.(vcs.ActorSubjectResolver)
		for _, login := range uniqueStrings([]string{pr.AuthorLogin, event.SenderLogin}) {
			if login == "" {
				continue
			}
			if !ok {
				reasons = append(reasons, "repository write access cannot be verified")
				break
			}
			subjects, err := resolver.ResolveActorSubjects(ctx, event.Repository.FullName, login)
			if err != nil || !containsSubject(subjects, "repository_write") {
				reasons = append(reasons, "outside contributor "+login)
			}
		}
	}
	return len(reasons) > 0, reasons
}

func containsSubject(subjects []string, wanted string) bool {
	for _, subject := range subjects {
		if subject == wanted {
			return true
		}
	}
	return false
}

// holdForRunGate records that the run gate holds an event and posts the
// pending "Reactorcide Checks" status.
func (h *WebhookHandler) holdForRunGate(ctx context.Context, ciEvent *models.CIEvent) {
	if ciEvent == nil {
		return
	}
	events, ok := h.store.(store.CIEventStore)
	if !ok {
		return
	}
	if err := events.SetCIEventStatus(ctx, ciEvent.EventID, models.CIEventStatusAwaitingApproval); err != nil {
		h.logger.WithError(err).WithField("event_id", ciEvent.EventID).Warn("Could not mark the event as held by the run gate")
		return
	}
	if err := jobcontrol.PublishEventChecks(ctx, ciEvent.EventID); err != nil {
		h.logger.WithError(err).WithField("event_id", ciEvent.EventID).Warn("Could not post the checks status of a held event")
	}
}

func parseStoredPolicy(stored *models.CIPolicy) (*cipolicy.Policy, error) {
	document, err := json.Marshal(stored.Document)
	if err != nil {
		return nil, err
	}
	return cipolicy.ParseDocument(document)
}
