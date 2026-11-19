package store

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// DefaultsProfileStore reads and applies the defaults profile of an
// organization (plan item A5).
type DefaultsProfileStore interface {
	// ApplyOrganizationDefaultsProfile records profile on the organization
	// and writes the settings of the profile: the organization run_gate and
	// dynamic_jobs. updatedBy names the caller for the trust settings row.
	ApplyOrganizationDefaultsProfile(ctx context.Context, orgID, profile, updatedBy string) error
}

// DraftShedStore reads and writes the draft pull request and shed level
// settings (plan item D4).
type DraftShedStore interface {
	// EffectiveDraftPRs returns run, skip or skip_when_shedding for a project.
	EffectiveDraftPRs(ctx context.Context, projectID string) (string, error)
	// SetOrganizationDraftPRs sets the organization draft_prs value.
	SetOrganizationDraftPRs(ctx context.Context, orgID, value string) error
	// GetShedState returns the shed level in force for an organization. An
	// empty orgID returns the global level only.
	GetShedState(ctx context.Context, orgID string) (models.ShedState, error)
	// SetGlobalShedLevel sets the global level.
	SetGlobalShedLevel(ctx context.Context, level string) error
	// SetOrganizationShed sets the organization level and, when actions is
	// not nil, the actions of each level. clearActions returns the
	// organization to the built-in actions.
	SetOrganizationShed(ctx context.Context, orgID, level string, actions *models.ShedActionsConfig, clearActions bool) error
}
