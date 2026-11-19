package store

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// PriorityStore stores priority lanes and group priority shifts
// (docs/priority.md).
//
// It is separate from Store, so the many full Store fakes do not each need
// these methods. A caller type-asserts; a store without it has only the
// built-in lanes and no group shifts.
type PriorityStore interface {
	// ListPriorityLanes lists the lanes of one scope: the organization lanes
	// when projectID is nil, else the lanes of that project only.
	ListPriorityLanes(ctx context.Context, orgID string, projectID *string) ([]models.PriorityLane, error)
	GetPriorityLane(ctx context.Context, orgID string, projectID *string, name string) (*models.PriorityLane, error)
	// UpsertPriorityLane creates the lane, or replaces the lane with the
	// same scope and name.
	UpsertPriorityLane(ctx context.Context, lane *models.PriorityLane) error
	DeletePriorityLane(ctx context.Context, orgID string, projectID *string, name string) error

	// ListPrincipalGroupShifts lists the groups of the organization that the
	// principal is a member of and that have a shift other than 0.
	ListPrincipalGroupShifts(ctx context.Context, orgID, principalID string) ([]models.GroupPriorityShift, error)
	// ListGroupShifts lists the shifts of the groups with these IDs in the
	// organization (the owner groups of a workflow), with shift other than 0.
	ListGroupShifts(ctx context.Context, orgID string, groupIDs []string) ([]models.GroupPriorityShift, error)
	// SetGroupPriorityShift sets the shift of one group.
	SetGroupPriorityShift(ctx context.Context, groupID string, shift int) error
}
