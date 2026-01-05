package secrets

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// AuthorizationError represents an access denial.
type AuthorizationError struct {
	UserID string
	OrgID  string
	Reason string
}

func (e *AuthorizationError) Error() string {
	return fmt.Sprintf("authorization denied: user %s cannot access org %s: %s",
		e.UserID, e.OrgID, e.Reason)
}

// OrgAuthorizer verifies user access to organizations.
type OrgAuthorizer struct {
	db *gorm.DB
}

// NewOrgAuthorizer creates a new OrgAuthorizer.
func NewOrgAuthorizer(db *gorm.DB) *OrgAuthorizer {
	return &OrgAuthorizer{db: db}
}

// CanAccessOrg checks if a user can access the specified org's secrets.
// Currently: user can only access their own org (user_id == org_id).
// Future: Support org membership, delegation, etc.
func (a *OrgAuthorizer) CanAccessOrg(ctx context.Context, userID, orgID string) error {
	// For now, user IS the org - they can only access their own secrets
	if userID != orgID {
		return &AuthorizationError{
			UserID: userID,
			OrgID:  orgID,
			Reason: "user does not belong to organization",
		}
	}
	return nil
}

// IsAuthorizationError returns true if the error is an AuthorizationError.
func IsAuthorizationError(err error) bool {
	_, ok := err.(*AuthorizationError)
	return ok
}
