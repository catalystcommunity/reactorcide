package store

import "context"

// EgressControlStore reads and writes the egress control settings (WP18,
// docs/egress-proxy.md).
type EgressControlStore interface {
	// EffectiveEgressControl reports whether egress control is on for a
	// job: on when the organization, the project, or the named execution
	// profile of the organization has it on.
	EffectiveEgressControl(ctx context.Context, orgID string, projectID *string, profile string) (bool, error)
	// SetOrganizationEgressControl sets the organization setting.
	SetOrganizationEgressControl(ctx context.Context, orgID string, on bool) error
}
