package store

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// RequiredSetStore stores the required set of each project (plan items A1
// and A4, docs/required-set.md).
//
// It is separate from Store, so the many full Store fakes do not each need
// these methods. A caller type-asserts. A store without it has no required
// set: every workflow is required, as before the feature.
type RequiredSetStore interface {
	// GetProjectRequiredSetSettings returns the settings of a project. A
	// project with no row gets a zero value (the set is off).
	GetProjectRequiredSetSettings(ctx context.Context, projectID string) (*models.ProjectRequiredSetSettings, error)
	// GetOrgRequiredSetSettings returns the organization defaults. An
	// organization with no row gets the built-in defaults.
	GetOrgRequiredSetSettings(ctx context.Context, orgID string) (*models.OrgRequiredSetSettings, error)
	// SetRequiredSetEnabled turns the set of a project on or off. When it
	// turns the set on, it adds every workflow security ID the project knows
	// to the set (source "seed") in the same transaction, and returns the
	// IDs that it added.
	SetRequiredSetEnabled(ctx context.Context, projectID, orgID string, enabled bool, by *string) ([]string, error)
	// SetProjectRequiredSetApprovers replaces the approver settings of a
	// project. nil values inherit the organization values.
	SetProjectRequiredSetApprovers(ctx context.Context, projectID, orgID string, approvers []string, rules *[]models.RequiredSetApproverRule) error
	// UpsertOrgRequiredSetSettings replaces the organization defaults.
	UpsertOrgRequiredSetSettings(ctx context.Context, settings *models.OrgRequiredSetSettings) error

	// ListRequiredWorkflows returns the entries of the set of a project.
	ListRequiredWorkflows(ctx context.Context, projectID string) ([]models.RequiredWorkflow, error)
	// ListKnownWorkflows returns every workflow security ID of the root
	// workflows and the eval decisions of the project.
	ListKnownWorkflows(ctx context.Context, projectID string) ([]models.KnownWorkflow, error)
	// SetRequiredWorkflowQuarantine sets (until not nil) or clears the
	// quarantine of one entry. It returns ErrNotFound when the entry does
	// not exist.
	SetRequiredWorkflowQuarantine(ctx context.Context, projectID, securityID, branchPattern string, until *time.Time, reason string, by *string) error

	// CreateRequiredSetProposal stores a pending proposal. It returns
	// ErrAlreadyExists when the same change is already pending.
	CreateRequiredSetProposal(ctx context.Context, proposal *models.RequiredSetProposal) error
	GetRequiredSetProposal(ctx context.Context, proposalID string) (*models.RequiredSetProposal, error)
	// ListRequiredSetProposals lists the proposals of a project, newest
	// first. An empty state lists all states.
	ListRequiredSetProposals(ctx context.Context, projectID, state string, limit int) ([]models.RequiredSetProposal, error)
	// DecideRequiredSetProposal moves a pending proposal to state. For an
	// approved proposal it also adds (promote) or removes (demote) the entry,
	// in the same transaction. It returns ErrConflict when the proposal is
	// no longer pending, and ErrNotFound when a demotion names no entry.
	DecideRequiredSetProposal(ctx context.Context, proposalID, state string, by *string, reason string) (*models.RequiredSetProposal, error)
}

// RequiredSetRepublishStore finds the pull request events whose "Reactorcide
// Checks" status can change because the required set of their project
// changed (docs/required-set.md#status-after-a-change). It is separate from
// RequiredSetStore so that fakes need not implement it.
type RequiredSetRepublishStore interface {
	// ListRequiredSetChangedProjects returns the projects that had a set
	// change (enable, disable, promotion, demotion, quarantine, end of a
	// quarantine) recorded after since, and the projects with a quarantine
	// that expired after since and at or before now.
	ListRequiredSetChangedProjects(ctx context.Context, since, now time.Time) ([]string, error)
	// ListOpenPullRequestEventIDs returns the newest event of each pull
	// request of the project received after since, when that event is not a
	// close or merge event and is not a duplicate. At most limit IDs.
	ListOpenPullRequestEventIDs(ctx context.Context, projectID string, since time.Time, limit int) ([]string, error)
}
