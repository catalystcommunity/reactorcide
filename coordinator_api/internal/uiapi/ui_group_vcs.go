package uiapi

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcsteams"
)

// Groups linked to VCS teams (plan item B3, docs/principals.md). An org
// admin of the group's organization (session, user token, service principal
// token, or the admin token) manages the links (owner decision D24).

// GroupVCSSyncer syncs one link now. *vcsteams.Syncer implements it.
type GroupVCSSyncer interface {
	SyncLink(ctx context.Context, link *models.GroupVCSLink) *vcsteams.Outcome
}

// groupVCSAdmin authorizes an org admin of the group and returns the store.
// A token needs organizations:manage for the org of the group, as in the
// REST group routes.
func (s *UiService) groupVCSAdmin(ctx context.Context, groupID string, read ...bool) (context.Context, store.GroupVCSStore, *models.Group, error) {
	ctx, id, _, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return ctx, nil, nil, authErr
	}
	if err := requireNonEmpty("group_id", groupID, 64); err != nil {
		return ctx, nil, nil, err
	}
	group, err := s.deps.Store.GetGroupByID(ctx, groupID)
	if err != nil {
		return ctx, nil, nil, mapStoreErr(err, "group not found")
	}
	capabilities := []string{tokencaps.OrganizationsManage}
	if len(read) > 0 && read[0] {
		capabilities = orgReadCapabilities
	}
	if err := s.requireOrgAdminGrant(ctx, id, group.OrgID, capabilities...); err != nil {
		return ctx, nil, nil, err
	}
	st, ok := s.deps.Store.(store.GroupVCSStore)
	if !ok {
		return ctx, nil, nil, NewServiceError("unavailable", "group VCS links are not available")
	}
	return principals.WithActor(ctx, principals.ActorOf(ctx)), st, group, nil
}

func groupVCSLinkToWire(link *models.GroupVCSLink) csilapi.GroupVcsLink {
	return csilapi.GroupVcsLink{LinkId: link.LinkID, GroupId: link.GroupID, Provider: link.Provider,
		Team: link.Owner + "/" + link.TeamSlug, Subject: link.Subject(),
		LastSyncedAt: formatTimePtr(link.LastSyncedAt), LastAttemptAt: formatTimePtr(link.LastAttemptAt),
		NextSyncAt: formatTimePtr(link.NextSyncAt), LastError: link.LastError,
		FailureCount: int64(link.FailureCount), MemberCount: int64(link.MemberCount), CreatedAt: formatTime(link.CreatedAt)}
}

func (s *UiService) ListGroupVcsLinks(ctx context.Context, req csilapi.ListGroupVcsLinksRequest) (csilapi.ListGroupVcsLinksResponse, error) {
	ctx, st, group, err := s.groupVCSAdmin(ctx, req.GroupId, true)
	if err != nil {
		return csilapi.ListGroupVcsLinksResponse{}, err
	}
	links, err := st.ListGroupVCSLinks(ctx, group.GroupID)
	if err != nil {
		return csilapi.ListGroupVcsLinksResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.GroupVcsLink, 0, len(links))
	for i := range links {
		out = append(out, groupVCSLinkToWire(&links[i]))
	}
	return csilapi.ListGroupVcsLinksResponse{Links: out}, nil
}

func (s *UiService) LinkGroupVcsTeam(ctx context.Context, req csilapi.LinkGroupVcsTeamRequest) (csilapi.LinkGroupVcsTeamResponse, error) {
	ctx, st, group, err := s.groupVCSAdmin(ctx, req.GroupId)
	if err != nil {
		return csilapi.LinkGroupVcsTeamResponse{}, err
	}
	if err := requireNonEmpty("team", req.Team, 210); err != nil {
		return csilapi.LinkGroupVcsTeamResponse{}, err
	}
	link, err := vcsteams.CreateLink(ctx, st, group, derefOr(req.Provider, ""), req.Team, principals.ActorOf(ctx).PrincipalID)
	switch {
	case vcsteams.IsValidationError(err):
		return csilapi.LinkGroupVcsTeamResponse{}, NewServiceError("invalid_argument", err.Error())
	case errors.Is(err, store.ErrAlreadyExists):
		return csilapi.LinkGroupVcsTeamResponse{}, NewServiceError("conflict", "the group already links this team")
	case err != nil:
		return csilapi.LinkGroupVcsTeamResponse{}, NewServiceError("internal", "failed to link the team")
	}
	s.recordAudit(ctx, group.OrgID, "group.team_link", "group", group.GroupID,
		models.JSONB{"link_id": link.LinkID, "provider": link.Provider, "team": link.Owner + "/" + link.TeamSlug})
	return csilapi.LinkGroupVcsTeamResponse{Link: groupVCSLinkToWire(link)}, nil
}

func (s *UiService) UnlinkGroupVcsTeam(ctx context.Context, req csilapi.UnlinkGroupVcsTeamRequest) (csilapi.UnlinkGroupVcsTeamResponse, error) {
	ctx, st, group, err := s.groupVCSAdmin(ctx, req.GroupId)
	if err != nil {
		return csilapi.UnlinkGroupVcsTeamResponse{}, err
	}
	link, err := vcsteams.FindLink(ctx, st, group.GroupID, req.LinkId)
	if err != nil {
		return csilapi.UnlinkGroupVcsTeamResponse{}, mapStoreErr(err, "team link not found")
	}
	if err := st.DeleteGroupVCSLink(ctx, link.LinkID); err != nil {
		return csilapi.UnlinkGroupVcsTeamResponse{}, mapStoreErr(err, "team link not found")
	}
	s.recordAudit(ctx, group.OrgID, "group.team_unlink", "group", group.GroupID,
		models.JSONB{"link_id": link.LinkID, "provider": link.Provider, "team": link.Owner + "/" + link.TeamSlug})
	return csilapi.UnlinkGroupVcsTeamResponse{Removed: true}, nil
}

func (s *UiService) SyncGroupVcsTeams(ctx context.Context, req csilapi.SyncGroupVcsTeamsRequest) (csilapi.SyncGroupVcsTeamsResponse, error) {
	ctx, st, group, err := s.groupVCSAdmin(ctx, req.GroupId)
	if err != nil {
		return csilapi.SyncGroupVcsTeamsResponse{}, err
	}
	links, err := st.ListGroupVCSLinks(ctx, group.GroupID)
	if err != nil {
		return csilapi.SyncGroupVcsTeamsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	results := make([]csilapi.GroupVcsSyncResult, 0, len(links))
	if s.deps.GroupVCSSync == nil {
		ids := make([]string, 0, len(links))
		for _, link := range links {
			ids = append(ids, link.LinkID)
		}
		if err := st.RequestGroupVCSSync(ctx, ids); err != nil {
			return csilapi.SyncGroupVcsTeamsResponse{}, NewServiceError("internal", "an internal error occurred")
		}
		return csilapi.SyncGroupVcsTeamsResponse{Results: results}, nil
	}
	for i := range links {
		outcome := s.deps.GroupVCSSync.SyncLink(ctx, &links[i])
		result := csilapi.GroupVcsSyncResult{LinkId: outcome.LinkID, Team: links[i].Owner + "/" + links[i].TeamSlug,
			Members: int64(outcome.Members), Added: int64(len(outcome.Added)), Removed: int64(len(outcome.Removed))}
		if outcome.Error != "" {
			message := outcome.Error
			result.Error = &message
		}
		results = append(results, result)
	}
	s.recordAudit(ctx, group.OrgID, "group.team_sync", "group", group.GroupID, models.JSONB{"links": len(links)})
	return csilapi.SyncGroupVcsTeamsResponse{Results: results}, nil
}

func (s *UiService) ListGroupMemberDetails(ctx context.Context, req csilapi.ListGroupMemberDetailsRequest) (csilapi.ListGroupMemberDetailsResponse, error) {
	ctx, st, group, err := s.groupVCSAdmin(ctx, req.GroupId, true)
	if err != nil {
		return csilapi.ListGroupMemberDetailsResponse{}, err
	}
	details, err := st.ListGroupMemberDetails(ctx, group.GroupID)
	if err != nil {
		return csilapi.ListGroupMemberDetailsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	out := make([]csilapi.GroupMemberDetail, 0, len(details))
	for _, d := range details {
		entry := csilapi.GroupMemberDetail{PrincipalId: d.PrincipalID, Kind: d.Kind, Name: d.Name, DisplayName: d.DisplayName,
			Source: d.Source, Manual: d.Manual, Teams: append([]string{}, d.Teams...),
			VcsLogins: append([]string{}, d.VCSLogins...), AddedAt: formatTime(d.AddedAt)}
		if d.UserID != "" {
			userID := d.UserID
			entry.UserId = &userID
		}
		if d.Username != "" {
			username := d.Username
			entry.Username = &username
		}
		out = append(out, entry)
	}
	return csilapi.ListGroupMemberDetailsResponse{Members: out}, nil
}
