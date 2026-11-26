package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	subjectgrammar "github.com/catalystcommunity/reactorcide/coordinator_api/internal/subjects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// principalResolverFor returns a resolver over the store, or nil when the
// store has no principals (test fakes). The resolver has no cache here: an
// approval or an admission decision reads the current links.
func principalResolverFor(value any) *principals.Resolver {
	principalStore, ok := value.(principals.Store)
	if !ok {
		return nil
	}
	return principals.NewResolver(principalStore, 0)
}

// resolveVCSPrincipal returns the principal of a VCS login from a signed
// webhook, and creates it on first sight. It returns nil when principals are
// not available or the login is empty.
func (h *WebhookHandler) resolveVCSPrincipal(ctx context.Context, provider vcs.Provider, login string) *principals.Resolution {
	resolver := principalResolverFor(h.store)
	if resolver == nil || models.VCSIdentityValue(string(provider), login) == "" {
		return nil
	}
	resolution, err := resolver.ResolveOrCreateVCS(ctx, string(provider), login)
	if err != nil {
		h.logger.WithError(err).WithField("login", login).Warn("Could not resolve the principal of a VCS login")
		return nil
	}
	return resolution
}

// lookupPrincipal resolves an identity without creating a principal.
func (h *WebhookHandler) lookupPrincipal(ctx context.Context, identityType, value string) *principals.Resolution {
	resolver := principalResolverFor(h.store)
	if resolver == nil || value == "" {
		return nil
	}
	resolution, err := resolver.Resolve(ctx, identityType, value)
	if err != nil {
		return nil
	}
	return resolution
}

type principalGroupStore interface {
	ListGroupsForPrincipal(ctx context.Context, principalID string) ([]models.Group, error)
}

type organizationByIDStore interface {
	GetOrganizationByID(ctx context.Context, orgID string) (*models.Organization, error)
}

// principalSubjects returns the subjects a principal has in the project's
// organization: its own identity subjects (principals.Subjects), the groups
// of the principal, and project_owner for a principal that is not a user and
// owns the project. The groups and the ownership of a user come from the
// user paths of the callers.
func (h *WebhookHandler) principalSubjects(ctx context.Context, project *models.Project, resolution *principals.Resolution) ([]string, error) {
	return principalSubjectsFor(ctx, h.store, project, resolution)
}

func principalSubjectsFor(ctx context.Context, value any, project *models.Project, resolution *principals.Resolution) ([]string, error) {
	if resolution == nil || project == nil || resolution.Inactive {
		// A disabled principal grants nothing: no principal, group, or
		// ownership subject.
		return nil, nil
	}
	orgID := project.OwnershipOrgID()
	scope := principals.SubjectContext{OrgID: orgID}
	if orgs, ok := value.(organizationByIDStore); ok && orgID != "" {
		if org, err := orgs.GetOrganizationByID(ctx, orgID); err == nil && org != nil {
			scope.OrgName = org.Name
		}
	}
	result := principals.Subjects(resolution, scope)
	if groups, ok := value.(principalGroupStore); ok {
		list, err := groups.ListGroupsForPrincipal(ctx, resolution.Principal.PrincipalID)
		if err != nil {
			return nil, err
		}
		for _, group := range list {
			if group.OrgID == orgID {
				result = append(result, subjectgrammar.PrefixGroup+group.Name)
			}
		}
	}
	if resolution.UserID() == "" && resolution.Principal.Kind == models.PrincipalKindService {
		if roleStore, ok := value.(authz.RoleStore); ok {
			owner, err := authz.NewResolver(roleStore).IsProjectOwner(ctx,
				authz.Identity{PrincipalID: resolution.Principal.PrincipalID}, project.ProjectID)
			if err != nil {
				return nil, err
			}
			if owner {
				result = append(result, subjectgrammar.ProjectOwner)
			}
		}
	}
	return result, nil
}

// webhookActor is the actor of a signed webhook delivery: the principal of
// the sender. A coordinator event (a re-evaluation or a rerun) carries the
// principal that started it.
func (h *WebhookHandler) webhookActor(ctx context.Context, event *vcs.WebhookEvent) principals.Actor {
	if event == nil {
		return principals.Actor{}
	}
	if event.Coordinator != nil && event.Coordinator.ActorPrincipalID != "" {
		return principals.Actor{PrincipalID: event.Coordinator.ActorPrincipalID, Identity: event.Coordinator.ActorIdentity}
	}
	if event.Coordinator != nil {
		// The sender of a coordinator event is the earlier actor of the
		// head, for admission facts. It did not start this run.
		return principals.Actor{}
	}
	return principals.FromResolution(h.resolveVCSPrincipal(ctx, event.Provider, event.SenderLogin))
}

type jobByIDStore interface {
	GetJobByID(ctx context.Context, jobID string) (*models.Job, error)
}

// requestActor is the actor of an API request: the principal of the token
// or the session user. A job token acts for its job, so its actor is the
// actor of that job.
func requestActor(ctx context.Context, value any) principals.Actor {
	actor := principals.ActorOf(ctx)
	if !actor.IsZero() {
		return actor
	}
	credential := checkauth.GetPrincipalFromContext(ctx)
	if credential == nil || credential.CredentialType != "job_token" || credential.BoundJobID == "" {
		return actor
	}
	jobs, ok := value.(jobByIDStore)
	if !ok {
		return actor
	}
	job, err := jobs.GetJobByID(ctx, credential.BoundJobID)
	if err != nil || job == nil || job.ActorPrincipalID == nil {
		return actor
	}
	return principals.Actor{PrincipalID: *job.ActorPrincipalID, Identity: job.ActorIdentity}
}

// describeJobActors fills the kind and display name of each response actor
// with one store read. The actor pointers are shared, so the copies in the
// slice see the change.
func describeJobActors(ctx context.Context, value any, responses []JobResponse) {
	ids := make([]string, 0, len(responses))
	for _, response := range responses {
		if response.Actor != nil {
			ids = append(ids, response.Actor.PrincipalID)
		}
	}
	described := principals.Describe(ctx, value, ids)
	for i := range responses {
		fillActor(responses[i].Actor, described)
	}
}

// describeWorkflowActors sets the actor of each workflow summary (and its
// children) with one store read.
func describeWorkflowActors(ctx context.Context, value any, summaries []models.WorkflowSummary) {
	ids := []string{}
	var collect func(list []models.WorkflowSummary)
	collect = func(list []models.WorkflowSummary) {
		for _, summary := range list {
			if summary.ActorPrincipalID != nil {
				ids = append(ids, *summary.ActorPrincipalID)
			}
			collect(summary.Children)
		}
	}
	collect(summaries)
	described := principals.Describe(ctx, value, ids)
	var apply func(list []models.WorkflowSummary)
	apply = func(list []models.WorkflowSummary) {
		for i := range list {
			list[i].Actor = principals.SummaryFor(described, list[i].ActorPrincipalID, list[i].ActorIdentity)
			apply(list[i].Children)
		}
	}
	apply(summaries)
}

func fillActor(actor *principals.Summary, described map[string]principals.Summary) {
	if actor == nil {
		return
	}
	if found, ok := described[actor.PrincipalID]; ok {
		actor.Kind = found.Kind
		actor.DisplayName = found.DisplayName
	}
}

// servicePrincipalOf returns the service principal of a token, or "".
func servicePrincipalOf(credential *checkauth.Principal) string {
	if credential.IsServicePrincipalToken() {
		return credential.PrincipalID
	}
	return ""
}

// resolveServicePerson is the approval person of a service principal that
// acts through its token. Its subjects are its own principal subjects, its
// groups, and project_owner when it has that role.
func (h *WebhookHandler) resolveServicePerson(ctx context.Context, project *models.Project, target *prTarget, principalID string) (approvalPerson, error) {
	resolver := principalResolverFor(h.store)
	if resolver == nil {
		return approvalPerson{}, fmt.Errorf("principals are not available")
	}
	resolution, err := resolver.ResolveID(ctx, principalID)
	if err != nil || !resolution.Principal.IsActive() {
		return approvalPerson{}, fmt.Errorf("the principal is not active")
	}
	values, err := h.principalSubjects(ctx, project, resolution)
	if err != nil {
		return approvalPerson{}, fmt.Errorf("resolving approver principal: %w", err)
	}
	valid := []string{}
	for _, subject := range uniqueStrings(values) {
		if models.ValidApproverSubject(subject) {
			valid = append(valid, subject)
		}
	}
	return approvalPerson{Person: "principal:" + resolution.Principal.PrincipalID, Provider: string(target.Provider),
		Subjects: valid, Principal: resolution}, nil
}

// resolveInstanceAdmin is the person of the instance administrator
// (approvals.Request.InstanceAdmin). A wide service
// principal token is its principal; the admin token, which has no
// principal, is the token. Both have the subject project_owner: the
// instance administrator is a global admin, and a global admin owns every
// project.
func (h *WebhookHandler) resolveInstanceAdmin(ctx context.Context, project *models.Project, target *prTarget, request approvals.Request) (approvalPerson, error) {
	if request.PrincipalID != "" {
		person, err := h.resolveServicePerson(ctx, project, target, request.PrincipalID)
		if err != nil {
			return approvalPerson{}, err
		}
		person.Subjects = uniqueStrings(append(person.Subjects, subjectgrammar.ProjectOwner))
		return person, nil
	}
	if strings.TrimSpace(request.TokenID) == "" {
		return approvalPerson{}, fmt.Errorf("the command has no person")
	}
	return approvalPerson{Person: "token:" + strings.TrimSpace(request.TokenID), Provider: string(target.Provider),
		Subjects: []string{subjectgrammar.ProjectOwner}}, nil
}
