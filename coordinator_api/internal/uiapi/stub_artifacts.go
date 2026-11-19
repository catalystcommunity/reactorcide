package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// WP15 stubs (ui_artifacts.go has the real operations).

func (StubUi) ListJobArtifacts(ctx context.Context, req csilapi.ListJobArtifactsRequest) (csilapi.ListJobArtifactsResponse, error) {
	return csilapi.ListJobArtifactsResponse{}, ErrUnimplemented("ReactorcideUi/list-job-artifacts")
}

func (StubUi) ReadJobArtifact(ctx context.Context, req csilapi.ReadJobArtifactRequest) (csilapi.ReadJobArtifactResponse, error) {
	return csilapi.ReadJobArtifactResponse{}, ErrUnimplemented("ReactorcideUi/read-job-artifact")
}

func (StubUi) GetJobTestResults(ctx context.Context, req csilapi.GetJobTestResultsRequest) (csilapi.GetJobTestResultsResponse, error) {
	return csilapi.GetJobTestResultsResponse{}, ErrUnimplemented("ReactorcideUi/get-job-test-results")
}

func (StubUi) ListFlakyTests(ctx context.Context, req csilapi.ListFlakyTestsRequest) (csilapi.ListFlakyTestsResponse, error) {
	return csilapi.ListFlakyTestsResponse{}, ErrUnimplemented("ReactorcideUi/list-flaky-tests")
}

func (StubUi) ListTestQuarantines(ctx context.Context, req csilapi.ListTestQuarantinesRequest) (csilapi.ListTestQuarantinesResponse, error) {
	return csilapi.ListTestQuarantinesResponse{}, ErrUnimplemented("ReactorcideUi/list-test-quarantines")
}

func (StubUi) QuarantineTest(ctx context.Context, req csilapi.QuarantineTestRequest) (csilapi.QuarantineTestResponse, error) {
	return csilapi.QuarantineTestResponse{}, ErrUnimplemented("ReactorcideUi/quarantine-test")
}

func (StubUi) UnquarantineTest(ctx context.Context, req csilapi.UnquarantineTestRequest) (csilapi.UnquarantineTestResponse, error) {
	return csilapi.UnquarantineTestResponse{}, ErrUnimplemented("ReactorcideUi/unquarantine-test")
}

func (StubUi) GetJobProvenance(ctx context.Context, req csilapi.GetJobProvenanceRequest) (csilapi.GetJobProvenanceResponse, error) {
	return csilapi.GetJobProvenanceResponse{}, ErrUnimplemented("ReactorcideUi/get-job-provenance")
}
