package store

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// RefClassStore stores operator ref class rules (docs/ref-classes.md).
//
// It is separate from Store, so the many full Store fakes do not each need
// these methods. A caller type-asserts; a store without it has no operator
// rules, and only the built-in rules apply.
type RefClassStore interface {
	// ListRefClassRules lists the rules of one scope: the organization rules
	// when projectID is nil, else the rules of that project only.
	ListRefClassRules(ctx context.Context, orgID string, projectID *string) ([]models.RefClassRule, error)
	// ListEffectiveRefClassRules lists the rules that apply to an event of
	// the project: the organization rules, then the project rules, in
	// evaluation order. projectID nil lists only the organization rules.
	ListEffectiveRefClassRules(ctx context.Context, orgID string, projectID *string) ([]models.RefClassRule, error)
	GetRefClassRule(ctx context.Context, orgID string, projectID *string, name string) (*models.RefClassRule, error)
	// UpsertRefClassRule creates the rule, or replaces the rule with the same
	// scope and name.
	UpsertRefClassRule(ctx context.Context, rule *models.RefClassRule) error
	DeleteRefClassRule(ctx context.Context, orgID string, projectID *string, name string) error
}
