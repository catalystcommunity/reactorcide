package store

import "context"

// InfraRetryStore resolves the infra_retries setting (organization value,
// project override) in force for a job. Like the other optional stores it
// is separate from Store; a caller that finds no implementation uses
// models.DefaultInfraRetries.
type InfraRetryStore interface {
	EffectiveInfraRetries(ctx context.Context, orgID string, projectID *string) (int, error)
	// SetOrganizationInfraRetries stores the organization value.
	SetOrganizationInfraRetries(ctx context.Context, orgID string, value int) error
}
