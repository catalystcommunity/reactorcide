package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func (StubUi) ListImageAliases(ctx context.Context, req csilapi.ListImageAliasesRequest) (csilapi.ListImageAliasesResponse, error) {
	return csilapi.ListImageAliasesResponse{}, ErrUnimplemented("ReactorcideUi/list-image-aliases")
}

func (StubUi) GetImageAlias(ctx context.Context, req csilapi.GetImageAliasRequest) (csilapi.GetImageAliasResponse, error) {
	return csilapi.GetImageAliasResponse{}, ErrUnimplemented("ReactorcideUi/get-image-alias")
}

func (StubUi) SetImageAlias(ctx context.Context, req csilapi.SetImageAliasRequest) (csilapi.SetImageAliasResponse, error) {
	return csilapi.SetImageAliasResponse{}, ErrUnimplemented("ReactorcideUi/set-image-alias")
}

func (StubUi) DeleteImageAlias(ctx context.Context, req csilapi.DeleteImageAliasRequest) (csilapi.DeleteImageAliasResponse, error) {
	return csilapi.DeleteImageAliasResponse{}, ErrUnimplemented("ReactorcideUi/delete-image-alias")
}

func (StubUi) GetImageRolloutStatus(ctx context.Context, req csilapi.GetImageRolloutStatusRequest) (csilapi.GetImageRolloutStatusResponse, error) {
	return csilapi.GetImageRolloutStatusResponse{}, ErrUnimplemented("ReactorcideUi/get-image-rollout-status")
}

func (StubUi) GetWorkerImageState(ctx context.Context, req csilapi.GetWorkerImageStateRequest) (csilapi.GetWorkerImageStateResponse, error) {
	return csilapi.GetWorkerImageStateResponse{}, ErrUnimplemented("ReactorcideUi/get-worker-image-state")
}
