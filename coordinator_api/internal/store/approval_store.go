package store

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// CIApprovalStore stores approval actions. The rows are append-only; the
// approvals package calculates the state from them.
type CIApprovalStore interface {
	// CreateCIApproval returns ErrAlreadyExists when an action from the same
	// provider object (SourceObject) is already stored.
	CreateCIApproval(ctx context.Context, approval *models.CIApproval) error
	// ListCIApprovalsForHead returns the actions for one head that are not
	// invalidated, oldest first.
	ListCIApprovalsForHead(ctx context.Context, projectID string, prNumber int, headSHA string) ([]models.CIApproval, error)
	// InvalidateCIApprovalsForNewHead invalidates the actions of every other
	// head of the pull request.
	InvalidateCIApprovalsForNewHead(ctx context.Context, projectID string, prNumber int, headSHA string) (int64, error)
}

// CITrustSettingsStore stores run gate, override, and required-review
// settings.
type CITrustSettingsStore interface {
	// GetCITrustSettings returns the row of the organization (projectID nil)
	// or of the project, or ErrNotFound.
	GetCITrustSettings(ctx context.Context, orgID string, projectID *string) (*models.CITrustSettings, error)
	// PutCITrustSettings inserts or replaces the row of its scope.
	PutCITrustSettings(ctx context.Context, settings *models.CITrustSettings) error
	// EffectiveCITrust returns the settings in force for a project.
	EffectiveCITrust(ctx context.Context, orgID, projectID string) (models.EffectiveTrustSettings, error)
}

// PRHeadStore finds the recorded events of a pull request head and the people
// who changed the pull request.
type PRHeadStore interface {
	// ListPRHeadUpdateActors returns the actors of the recorded events that
	// opened the pull request or pushed to its head, newest first.
	ListPRHeadUpdateActors(ctx context.Context, projectID string, prNumber int) ([]PRHeadUpdate, error)
	// ListCIEventsForPRHead returns the events of one pull request head,
	// newest first.
	ListCIEventsForPRHead(ctx context.Context, projectID string, prNumber int, headSHA string) ([]models.CIEvent, error)
}

// PRHeadUpdate is one recorded head update of a pull request.
type PRHeadUpdate struct {
	Actor   string
	HeadSHA string
}
