package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// WP8 stubs (ui_group_vcs.go has the real operations).

func (StubUi) ListGroupMemberDetails(ctx context.Context, req csilapi.ListGroupMemberDetailsRequest) (csilapi.ListGroupMemberDetailsResponse, error) {
	return csilapi.ListGroupMemberDetailsResponse{}, ErrUnimplemented("ReactorcideUi/list-group-member-details")
}

func (StubUi) ListGroupVcsLinks(ctx context.Context, req csilapi.ListGroupVcsLinksRequest) (csilapi.ListGroupVcsLinksResponse, error) {
	return csilapi.ListGroupVcsLinksResponse{}, ErrUnimplemented("ReactorcideUi/list-group-vcs-links")
}

func (StubUi) LinkGroupVcsTeam(ctx context.Context, req csilapi.LinkGroupVcsTeamRequest) (csilapi.LinkGroupVcsTeamResponse, error) {
	return csilapi.LinkGroupVcsTeamResponse{}, ErrUnimplemented("ReactorcideUi/link-group-vcs-team")
}

func (StubUi) UnlinkGroupVcsTeam(ctx context.Context, req csilapi.UnlinkGroupVcsTeamRequest) (csilapi.UnlinkGroupVcsTeamResponse, error) {
	return csilapi.UnlinkGroupVcsTeamResponse{}, ErrUnimplemented("ReactorcideUi/unlink-group-vcs-team")
}

func (StubUi) SyncGroupVcsTeams(ctx context.Context, req csilapi.SyncGroupVcsTeamsRequest) (csilapi.SyncGroupVcsTeamsResponse, error) {
	return csilapi.SyncGroupVcsTeamsResponse{}, ErrUnimplemented("ReactorcideUi/sync-group-vcs-teams")
}
