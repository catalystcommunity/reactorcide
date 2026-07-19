package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func groupToSummary(g *models.Group) csilapi.GroupSummary {
	out := csilapi.GroupSummary{
		GroupId:   g.GroupID,
		OrgId:     g.OrgID,
		Name:      g.Name,
		CreatedAt: formatTime(g.CreatedAt),
		UpdatedAt: formatTime(g.UpdatedAt),
	}
	if g.Description != "" {
		desc := g.Description
		out.Description = &desc
	}
	return out
}

// ListGroups requires org admin (of org_id) or global admin — group
// membership is management surface, per UI_AUTH_PLAN.md's "manage groups /
// assign roles" matrix row; there is no separate "view groups" capability.
func (s *UiService) ListGroups(ctx context.Context, req csilapi.ListGroupsRequest) (csilapi.ListGroupsResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.ListGroupsResponse{}, authErr
	}
	if err := requireNonEmpty("org_id", req.OrgId, 64); err != nil {
		return csilapi.ListGroupsResponse{}, err
	}
	if err := s.deps.Resolver.RequireOrgAdmin(ctx, id, req.OrgId); err != nil {
		return csilapi.ListGroupsResponse{}, mapPermissionErr(err)
	}

	groups, err := s.deps.Store.ListGroupsByOrg(ctx, req.OrgId)
	if err != nil {
		return csilapi.ListGroupsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.GroupSummary, len(groups))
	for i := range groups {
		out[i] = groupToSummary(&groups[i])
	}
	return csilapi.ListGroupsResponse{Groups: out}, nil
}

// CreateGroup requires org admin (of org_id) or global admin.
func (s *UiService) CreateGroup(ctx context.Context, req csilapi.CreateGroupRequest) (csilapi.CreateGroupResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.CreateGroupResponse{}, authErr
	}
	if err := requireNonEmpty("org_id", req.OrgId, 64); err != nil {
		return csilapi.CreateGroupResponse{}, err
	}
	if err := requireNonEmpty("name", req.Name, maxNameLength); err != nil {
		return csilapi.CreateGroupResponse{}, err
	}
	if err := s.deps.Resolver.RequireOrgAdmin(ctx, id, req.OrgId); err != nil {
		return csilapi.CreateGroupResponse{}, mapPermissionErr(err)
	}

	group := &models.Group{OrgID: req.OrgId, Name: req.Name, Description: derefOr(req.Description, "")}
	if err := s.deps.Store.CreateGroup(ctx, group); err != nil {
		return csilapi.CreateGroupResponse{}, NewServiceError("internal", "failed to create group")
	}
	return csilapi.CreateGroupResponse{Group: groupToSummary(group)}, nil
}

// UpdateGroup requires org admin (of the group's org) or global admin.
func (s *UiService) UpdateGroup(ctx context.Context, req csilapi.UpdateGroupRequest) (csilapi.UpdateGroupResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.UpdateGroupResponse{}, authErr
	}
	if err := requireNonEmpty("group_id", req.GroupId, 64); err != nil {
		return csilapi.UpdateGroupResponse{}, err
	}

	group, err := s.deps.Store.GetGroupByID(ctx, req.GroupId)
	if err != nil {
		return csilapi.UpdateGroupResponse{}, mapStoreErr(err, "group not found")
	}
	if err := s.deps.Resolver.RequireOrgAdmin(ctx, id, group.OrgID); err != nil {
		return csilapi.UpdateGroupResponse{}, mapPermissionErr(err)
	}

	if req.Name != nil {
		if err := requireNonEmpty("name", *req.Name, maxNameLength); err != nil {
			return csilapi.UpdateGroupResponse{}, err
		}
		group.Name = *req.Name
	}
	if req.Description != nil {
		group.Description = *req.Description
	}
	if err := s.deps.Store.UpdateGroup(ctx, group); err != nil {
		return csilapi.UpdateGroupResponse{}, NewServiceError("internal", "failed to update group")
	}
	return csilapi.UpdateGroupResponse{Group: groupToSummary(group)}, nil
}

// DeleteGroup requires org admin (of the group's org) or global admin.
func (s *UiService) DeleteGroup(ctx context.Context, req csilapi.DeleteGroupRequest) (csilapi.DeleteGroupResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.DeleteGroupResponse{}, authErr
	}
	if err := requireNonEmpty("group_id", req.GroupId, 64); err != nil {
		return csilapi.DeleteGroupResponse{}, err
	}

	group, err := s.deps.Store.GetGroupByID(ctx, req.GroupId)
	if err != nil {
		return csilapi.DeleteGroupResponse{}, mapStoreErr(err, "group not found")
	}
	if err := s.deps.Resolver.RequireOrgAdmin(ctx, id, group.OrgID); err != nil {
		return csilapi.DeleteGroupResponse{}, mapPermissionErr(err)
	}

	if err := s.deps.Store.DeleteGroup(ctx, req.GroupId); err != nil {
		return csilapi.DeleteGroupResponse{}, mapStoreErr(err, "group not found")
	}
	return csilapi.DeleteGroupResponse{Deleted: true}, nil
}

// AddGroupMember requires org admin (of the group's org) or global admin.
func (s *UiService) AddGroupMember(ctx context.Context, req csilapi.AddGroupMemberRequest) (csilapi.AddGroupMemberResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.AddGroupMemberResponse{}, authErr
	}
	if err := requireNonEmpty("group_id", req.GroupId, 64); err != nil {
		return csilapi.AddGroupMemberResponse{}, err
	}
	if err := requireNonEmpty("user_id", req.UserId, 64); err != nil {
		return csilapi.AddGroupMemberResponse{}, err
	}

	group, err := s.deps.Store.GetGroupByID(ctx, req.GroupId)
	if err != nil {
		return csilapi.AddGroupMemberResponse{}, mapStoreErr(err, "group not found")
	}
	if err := s.deps.Resolver.RequireOrgAdmin(ctx, id, group.OrgID); err != nil {
		return csilapi.AddGroupMemberResponse{}, mapPermissionErr(err)
	}
	if _, err := s.deps.Store.GetUserByID(ctx, req.UserId); err != nil {
		return csilapi.AddGroupMemberResponse{}, NewServiceError("invalid_argument", "user_id does not refer to a known user")
	}

	if err := s.deps.Store.AddGroupMember(ctx, req.GroupId, req.UserId); err != nil {
		return csilapi.AddGroupMemberResponse{}, NewServiceError("internal", "failed to add group member")
	}
	return csilapi.AddGroupMemberResponse{Added: true}, nil
}

// RemoveGroupMember requires org admin (of the group's org) or global admin.
func (s *UiService) RemoveGroupMember(ctx context.Context, req csilapi.RemoveGroupMemberRequest) (csilapi.RemoveGroupMemberResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.RemoveGroupMemberResponse{}, authErr
	}
	if err := requireNonEmpty("group_id", req.GroupId, 64); err != nil {
		return csilapi.RemoveGroupMemberResponse{}, err
	}
	if err := requireNonEmpty("user_id", req.UserId, 64); err != nil {
		return csilapi.RemoveGroupMemberResponse{}, err
	}

	group, err := s.deps.Store.GetGroupByID(ctx, req.GroupId)
	if err != nil {
		return csilapi.RemoveGroupMemberResponse{}, mapStoreErr(err, "group not found")
	}
	if err := s.deps.Resolver.RequireOrgAdmin(ctx, id, group.OrgID); err != nil {
		return csilapi.RemoveGroupMemberResponse{}, mapPermissionErr(err)
	}

	if err := s.deps.Store.RemoveGroupMember(ctx, req.GroupId, req.UserId); err != nil {
		return csilapi.RemoveGroupMemberResponse{}, mapStoreErr(err, "group member not found")
	}
	return csilapi.RemoveGroupMemberResponse{Removed: true}, nil
}

// ListGroupMembers requires org admin (of the group's org) or global admin,
// same as the other group ops — there is no separate "view group members"
// capability.
func (s *UiService) ListGroupMembers(ctx context.Context, req csilapi.ListGroupMembersRequest) (csilapi.ListGroupMembersResponse, error) {
	id, _, authErr := s.deps.requireUser(ctx)
	if authErr != nil {
		return csilapi.ListGroupMembersResponse{}, authErr
	}
	if err := requireNonEmpty("group_id", req.GroupId, 64); err != nil {
		return csilapi.ListGroupMembersResponse{}, err
	}

	group, err := s.deps.Store.GetGroupByID(ctx, req.GroupId)
	if err != nil {
		return csilapi.ListGroupMembersResponse{}, mapStoreErr(err, "group not found")
	}
	if err := s.deps.Resolver.RequireOrgAdmin(ctx, id, group.OrgID); err != nil {
		return csilapi.ListGroupMembersResponse{}, mapPermissionErr(err)
	}

	members, err := s.deps.Store.ListGroupMembers(ctx, req.GroupId)
	if err != nil {
		return csilapi.ListGroupMembersResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.GroupMemberEntry, len(members))
	for i := range members {
		out[i] = csilapi.GroupMemberEntry{UserId: members[i].UserID, Username: members[i].Username}
	}
	return csilapi.ListGroupMembersResponse{Members: out}, nil
}
