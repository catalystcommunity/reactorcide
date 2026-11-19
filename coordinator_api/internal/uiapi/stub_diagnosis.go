package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// WP17 stubs (ui_diagnosis.go has the real operations).

func (StubUi) ListDiagnoses(ctx context.Context, req csilapi.ListDiagnosesRequest) (csilapi.ListDiagnosesResponse, error) {
	return csilapi.ListDiagnosesResponse{}, ErrUnimplemented("ReactorcideUi/list-diagnoses")
}

func (StubUi) PutDiagnosis(ctx context.Context, req csilapi.PutDiagnosisRequest) (csilapi.PutDiagnosisResponse, error) {
	return csilapi.PutDiagnosisResponse{}, ErrUnimplemented("ReactorcideUi/put-diagnosis")
}
