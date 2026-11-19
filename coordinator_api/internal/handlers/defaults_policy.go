package handlers

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// errOrganizationUnavailable: the store could not read the organization of
// the project, so the coordinator cannot know its defaults profile.
var errOrganizationUnavailable = errors.New("the organization of the project could not be read")

// projectOrganization returns the organization of a project. It returns nil
// and no error when the store cannot read organizations at all (a store
// with no organizations). A read error, or an organization that is not
// there, returns errOrganizationUnavailable: the caller must fail closed.
func projectOrganization(ctx context.Context, value any, project *models.Project) (*models.Organization, error) {
	orgs, ok := value.(organizationByIDStore)
	if !ok || project == nil {
		return nil, nil
	}
	org, err := orgs.GetOrganizationByID(ctx, project.OwnershipOrgID())
	if err != nil || org == nil {
		return nil, errOrganizationUnavailable
	}
	return org, nil
}

// attachBuiltInCIPolicy puts the built-in CI policy on the eval job of a pull
// request when the project stores no policy and its organization has the
// secure defaults profile (plan item A5). The eval then runs the base CI and
// reports head candidates, as with a stored policy. The head's CI changes
// run only through override-ci. A legacy organization keeps the behavior
// from before: the eval runs the CI of a same-repository head.
func (h *WebhookHandler) attachBuiltInCIPolicy(ctx context.Context, job *models.Job, project *models.Project) ciPolicyState {
	org, err := projectOrganization(ctx, h.store, project)
	if err != nil {
		// Fail closed: the eval keeps the trusted base CI, as for a stored
		// policy that could not be read. Only a known legacy organization
		// runs the head CI of a same-repository pull request.
		h.logger.WithError(err).WithField("project_id", project.ProjectID).Error("Could not read the defaults profile; head CI will fail closed")
		return ciPolicyUnavailable
	}
	if org == nil || !models.UsesBuiltInPolicy(org.DefaultsProfile) {
		return ciPolicyNone
	}
	policy, document, err := cipolicy.BuiltIn()
	if err != nil {
		h.logger.WithError(err).Error("Built-in CI policy failed validation; head CI will fail closed")
		return ciPolicyUnavailable
	}
	job.JobEnvVars["REACTORCIDE_CI_POLICY"] = string(document)
	job.JobEnvVars["REACTORCIDE_CI_POLICY_REVISION"] = policy.Revision
	return ciPolicyAttached
}
