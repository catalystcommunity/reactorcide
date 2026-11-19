package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// WP14 stubs (ui_build_settings.go has the real operations).

func (StubUi) ListEnvDefaults(ctx context.Context, req csilapi.ListEnvDefaultsRequest) (csilapi.ListEnvDefaultsResponse, error) {
	return csilapi.ListEnvDefaultsResponse{}, ErrUnimplemented("ReactorcideUi/list-env-defaults")
}

func (StubUi) PutEnvDefault(ctx context.Context, req csilapi.PutEnvDefaultRequest) (csilapi.PutEnvDefaultResponse, error) {
	return csilapi.PutEnvDefaultResponse{}, ErrUnimplemented("ReactorcideUi/put-env-default")
}

func (StubUi) DeleteEnvDefault(ctx context.Context, req csilapi.DeleteEnvDefaultRequest) (csilapi.DeleteEnvDefaultResponse, error) {
	return csilapi.DeleteEnvDefaultResponse{}, ErrUnimplemented("ReactorcideUi/delete-env-default")
}

func (StubUi) GetImagePolicy(ctx context.Context, req csilapi.GetImagePolicyRequest) (csilapi.GetImagePolicyResponse, error) {
	return csilapi.GetImagePolicyResponse{}, ErrUnimplemented("ReactorcideUi/get-image-policy")
}

func (StubUi) PutImagePolicy(ctx context.Context, req csilapi.PutImagePolicyRequest) (csilapi.PutImagePolicyResponse, error) {
	return csilapi.PutImagePolicyResponse{}, ErrUnimplemented("ReactorcideUi/put-image-policy")
}

func (StubUi) ListJobLinks(ctx context.Context, req csilapi.ListJobLinksRequest) (csilapi.ListJobLinksResponse, error) {
	return csilapi.ListJobLinksResponse{}, ErrUnimplemented("ReactorcideUi/list-job-links")
}
