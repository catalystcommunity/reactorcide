package requiredset

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/owners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// WP7: an owner of a workflow (a workflow role holder, or a member of the
// owner group of the latest trusted definition) can quarantine it.
func init() {
	OwnerApprover = workflowOwnerApprover
}

func workflowOwnerApprover(ctx context.Context, value any, project *models.Project, securityID string, caller Caller) bool {
	roles, ok := value.(authz.RoleStore)
	if !ok || project == nil || securityID == "" {
		return false
	}
	snapshot, err := owners.ForWorkflow(ctx, value, project.ProjectID, securityID)
	if err != nil {
		return false
	}
	owner, err := authz.NewResolver(roles).IsWorkflowOwner(ctx, caller.Identity, project.ProjectID,
		authz.WorkflowScope{SecurityID: securityID, Owner: snapshot})
	return err == nil && owner
}
