package uiapi

import (
	"context"
	"errors"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// listUsersLimit bounds one list-users response. The op feeds a picker, not
// a directory; a caller narrows with query rather than paging.
const listUsersLimit = 100

// maxUserQueryLength bounds the free-text filter so a pasted blob cannot
// become an unbounded ILIKE pattern.
const maxUserQueryLength = 128

// ListUsers serves the user picker behind role grants and group membership.
//
// assign-role and add-group-member take a principal user_id, but until this
// op nothing exposed one: list-group-members shows ids only for users who are
// already members, and login-provisioned users have no other surface at all.
// An org admin therefore could not grant a role without asking the target to
// read a UUID out of a cookie. This op closes that gap.
//
// Authorization is org admin (of org_id) or global admin, the same gate as
// list-groups: the user directory is management surface, and org_id is
// required so a plain member cannot enumerate accounts by omitting a scope.
// The list itself is not org-filtered -- users are global rows and a grant
// may target any account -- so the org only authorizes the call.
func (s *UiService) ListUsers(ctx context.Context, req csilapi.ListUsersRequest) (csilapi.ListUsersResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.ListUsersResponse{}, authErr
	}
	if err := requireNonEmpty("org_id", req.OrgId, 64); err != nil {
		return csilapi.ListUsersResponse{}, err
	}
	query := strings.TrimSpace(derefOr(req.Query, ""))
	if len(query) > maxUserQueryLength {
		return csilapi.ListUsersResponse{}, NewServiceError("invalid_argument", "query is too long")
	}
	if err := s.deps.Resolver.RequireOrgAdmin(ctx, id, req.OrgId); err != nil {
		return csilapi.ListUsersResponse{}, mapPermissionErr(err)
	}

	users, err := s.deps.Store.ListUsers(ctx, query, listUsersLimit)
	if err != nil {
		return csilapi.ListUsersResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.UserSummary, 0, len(users))
	for i := range users {
		summary := csilapi.UserSummary{UserId: users[i].UserID, Username: users[i].Username}
		identity, err := s.deps.Store.GetAuthIdentityByUserID(ctx, users[i].UserID)
		switch {
		case err == nil && identity != nil:
			fillUserSummaryFromIdentity(&summary, identity)
		case errors.Is(err, store.ErrNotFound):
			// Bootstrap-created and REST-created users have no login
			// identity; they are still valid grant targets.
		case err != nil:
			return csilapi.ListUsersResponse{}, NewServiceError("internal", "an internal error occurred")
		}
		out = append(out, summary)
	}
	return csilapi.ListUsersResponse{Users: out}, nil
}

// fillUserSummaryFromIdentity copies the picker-relevant identity fields.
// auth_identities.subject is stored as "subject@domain" by
// auth.LoginService (see subjectFor); an identity written without the
// domain suffix is completed from handle/domain so the picker always shows
// the same shape.
func fillUserSummaryFromIdentity(summary *csilapi.UserSummary, identity *models.AuthIdentity) {
	if identity.DisplayName != "" {
		name := identity.DisplayName
		summary.DisplayName = &name
	}
	if subject := identitySubject(identity); subject != "" {
		summary.Subject = &subject
	}
	summary.LastLoginAt = formatTimePtr(identity.LastLoginAt)
}

func identitySubject(identity *models.AuthIdentity) string {
	if strings.Contains(identity.Subject, "@") || identity.Domain == "" {
		return identity.Subject
	}
	local := identity.Handle
	if local == "" {
		local = identity.Subject
	}
	if local == "" {
		return ""
	}
	return local + "@" + identity.Domain
}
