package uiapi

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func TestGroups_HappyPath(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	admin := st.putUser(models.User{UserID: "admin-1"})
	member := st.putUser(models.User{UserID: "member-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	created, err := ui.CreateGroup(ctx, csilapi.CreateGroupRequest{OrgId: "org-1", Name: "team-a"})
	requireOK(t, err)
	if created.Group.Name != "team-a" {
		t.Fatalf("Name = %q, want team-a", created.Group.Name)
	}

	listed, err := ui.ListGroups(ctx, csilapi.ListGroupsRequest{OrgId: "org-1"})
	requireOK(t, err)
	if len(listed.Groups) != 1 {
		t.Fatalf("len(Groups) = %d, want 1", len(listed.Groups))
	}

	updated, err := ui.UpdateGroup(ctx, csilapi.UpdateGroupRequest{GroupId: created.Group.GroupId, Name: strPtr("team-b")})
	requireOK(t, err)
	if updated.Group.Name != "team-b" {
		t.Fatalf("Name = %q, want team-b", updated.Group.Name)
	}

	addResp, err := ui.AddGroupMember(ctx, csilapi.AddGroupMemberRequest{GroupId: created.Group.GroupId, UserId: member.UserID})
	requireOK(t, err)
	if !addResp.Added {
		t.Fatalf("Added = false")
	}

	removeResp, err := ui.RemoveGroupMember(ctx, csilapi.RemoveGroupMemberRequest{GroupId: created.Group.GroupId, UserId: member.UserID})
	requireOK(t, err)
	if !removeResp.Removed {
		t.Fatalf("Removed = false")
	}

	deleteResp, err := ui.DeleteGroup(ctx, csilapi.DeleteGroupRequest{GroupId: created.Group.GroupId})
	requireOK(t, err)
	if !deleteResp.Deleted {
		t.Fatalf("Deleted = false")
	}
}

func TestListGroupMembers_HappyPath(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	admin := st.putUser(models.User{UserID: "admin-1"})
	member := st.putUser(models.User{UserID: "member-1", Username: "member-one"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	created, err := ui.CreateGroup(ctx, csilapi.CreateGroupRequest{OrgId: "org-1", Name: "team-a"})
	requireOK(t, err)

	addResp, err := ui.AddGroupMember(ctx, csilapi.AddGroupMemberRequest{GroupId: created.Group.GroupId, UserId: member.UserID})
	requireOK(t, err)
	if !addResp.Added {
		t.Fatalf("Added = false")
	}

	listed, err := ui.ListGroupMembers(ctx, csilapi.ListGroupMembersRequest{GroupId: created.Group.GroupId})
	requireOK(t, err)
	if len(listed.Members) != 1 {
		t.Fatalf("len(Members) = %d, want 1", len(listed.Members))
	}
	if listed.Members[0].UserId != member.UserID || listed.Members[0].Username != "member-one" {
		t.Fatalf("Members[0] = %+v, want {user_id: %q, username: member-one}", listed.Members[0], member.UserID)
	}
}

func TestListGroupMembers_Forbidden(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "org-1"})
	outsider := st.putUser(models.User{UserID: "outsider-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	ui := NewUiService(deps)
	adminCtx := mintSessionCtx(t, deps, admin.UserID)

	created, err := ui.CreateGroup(adminCtx, csilapi.CreateGroupRequest{OrgId: "org-1", Name: "team-a"})
	requireOK(t, err)

	outsiderCtx := mintSessionCtx(t, deps, outsider.UserID)
	_, err = ui.ListGroupMembers(outsiderCtx, csilapi.ListGroupMembersRequest{GroupId: created.Group.GroupId})
	requireCode(t, err, "forbidden")
}

func TestGroups_InvalidArgument(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "org-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	_, err := ui.CreateGroup(ctx, csilapi.CreateGroupRequest{OrgId: "org-1", Name: "   "})
	requireCode(t, err, "invalid_argument")

	_, err = ui.AddGroupMember(ctx, csilapi.AddGroupMemberRequest{GroupId: "does-not-exist", UserId: "org-1"})
	requireCode(t, err, "not_found")
}

func TestRoles_HappyPath(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "org-1"})
	target := st.putUser(models.User{UserID: "target-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	assigned, err := ui.AssignRole(ctx, csilapi.AssignRoleRequest{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalId:   target.UserID,
		ScopeType:     models.ScopeTypeOrg,
		ScopeId:       strPtr("org-1"),
		Role:          models.RoleMember,
	})
	requireOK(t, err)
	if assigned.Assignment.Role != models.RoleMember {
		t.Fatalf("Role = %q, want member", assigned.Assignment.Role)
	}

	listed, err := ui.ListRoleAssignments(ctx, csilapi.ListRoleAssignmentsRequest{
		ScopeType: strPtr(models.ScopeTypeOrg),
		ScopeId:   strPtr("org-1"),
	})
	requireOK(t, err)
	if len(listed.Assignments) != 2 { // the admin's own seeded org-admin row + the new one
		t.Fatalf("len(Assignments) = %d, want 2", len(listed.Assignments))
	}

	revoked, err := ui.RevokeRole(ctx, csilapi.RevokeRoleRequest{AssignmentId: assigned.Assignment.AssignmentId})
	requireOK(t, err)
	if !revoked.Revoked {
		t.Fatalf("Revoked = false")
	}
}

func TestRoles_InvalidArgument(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "org-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	_, err := ui.AssignRole(ctx, csilapi.AssignRoleRequest{
		PrincipalType: "not-a-type",
		PrincipalId:   "x",
		ScopeType:     models.ScopeTypeOrg,
		ScopeId:       strPtr("org-1"),
		Role:          models.RoleMember,
	})
	requireCode(t, err, "invalid_argument")

	_, err = ui.AssignRole(ctx, csilapi.AssignRoleRequest{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalId:   "does-not-exist",
		ScopeType:     models.ScopeTypeOrg,
		ScopeId:       strPtr("org-1"),
		Role:          models.RoleMember,
	})
	requireCode(t, err, "invalid_argument")
}
