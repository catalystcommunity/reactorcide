package store

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// ReportingStore stores report modes, the agreement report, the code owners
// import setting, and path acceptances (plan items A3, A2, A7; migration
// 000056; docs/reporting-and-ownership.md).
//
// It is separate from Store, so the many full Store fakes do not each need
// these methods. A caller type-asserts. A store without it has the built-in
// defaults: full reports, no agreement report, no code owners import, and no
// path acceptance.
type ReportingStore interface {
	// GetOrgReportingSettings returns nil and no error when the organization
	// has no row (the built-in defaults apply).
	GetOrgReportingSettings(ctx context.Context, orgID string) (*models.OrgReportingSettings, error)
	// GetProjectReportingSettings returns nil and no error when the project
	// has no row (every value inherits).
	GetProjectReportingSettings(ctx context.Context, projectID string) (*models.ProjectReportingSettings, error)
	UpsertOrgReportingSettings(ctx context.Context, settings *models.OrgReportingSettings) error
	UpsertProjectReportingSettings(ctx context.Context, settings *models.ProjectReportingSettings) error

	// RecordCIAgreementResult stores the Reactorcide result of a commit. A
	// new result (or a changed one) starts the comparison again: status
	// pending, attempts 0, next check now.
	RecordCIAgreementResult(ctx context.Context, row *models.CIAgreement) error
	// ListDueCIAgreements returns pending rows with next_check_at at or before
	// now, oldest first.
	ListDueCIAgreements(ctx context.Context, now time.Time, limit int) ([]models.CIAgreement, error)
	// UpdateCIAgreementObservation stores one read of the other CI. It
	// changes the row only when its Reactorcide result is still the one that
	// was read (the reactorcide_completed_at value matches).
	UpdateCIAgreementObservation(ctx context.Context, row *models.CIAgreement) error
	// ListCIAgreements lists the rows of a project, newest first. since nil
	// lists every row.
	ListCIAgreements(ctx context.Context, projectID string, since *time.Time, limit int) ([]models.CIAgreement, error)

	// CreatePathAcceptance stores a new row. It returns ErrAlreadyExists when
	// an open row (requested or accepted) exists for the same workflow,
	// pattern, and owner.
	CreatePathAcceptance(ctx context.Context, row *models.PathAcceptance) error
	GetPathAcceptance(ctx context.Context, acceptanceID string) (*models.PathAcceptance, error)
	// ListPathAcceptances lists the rows of a project, newest first. An empty
	// state lists every state.
	ListPathAcceptances(ctx context.Context, projectID, state string) ([]models.PathAcceptance, error)
	// TransitionPathAcceptance moves a row from one of the from states to
	// row.State and stores the decision columns. It returns ErrConflict when
	// the row is not in a from state.
	TransitionPathAcceptance(ctx context.Context, row *models.PathAcceptance, from ...string) error
}
