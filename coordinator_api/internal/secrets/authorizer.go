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

// CanAccessOrg checks direct and group RBAC membership for the organization.
func (a *OrgAuthorizer) CanAccessOrg(ctx context.Context, userID, orgID string) error {
	// Same-UUID backfilled organizations preserve the former single-user case.
	if userID == orgID {
		return nil
	}
	if a.db == nil {
		return &AuthorizationError{UserID: userID, OrgID: orgID, Reason: "user does not belong to organization"}
	}
	var allowed bool
	err := a.db.WithContext(ctx).Raw(`SELECT EXISTS (
		SELECT 1 FROM role_assignments ra
		WHERE ra.role IN ('admin', 'owner', 'member')
		  AND (ra.scope_type = 'global' OR (ra.scope_type = 'org' AND ra.scope_id = ?))
		  AND (
			ra.principal_type = 'user' AND ra.principal_id = ?
			OR ra.principal_type = 'group' AND EXISTS (
				SELECT 1 FROM group_members gm
				JOIN groups g ON g.group_id = gm.group_id
				WHERE gm.group_id = ra.principal_id AND gm.user_id = ? AND g.org_id = ?
			)
		  )
	)`, orgID, userID, userID, orgID).Scan(&allowed).Error
	if err != nil {
		return err
	}
	if !allowed {
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
