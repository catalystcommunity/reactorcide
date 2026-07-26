package workerapi

import (
	"errors"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
)

// assertServiceErrorCode fails t unless err is a *uiapi.ServiceErr with the
// given code -- the same recognition path uiapi's CSIL-RPC dispatcher uses
// (wrapOp's errors.As(err, &svcErr)) to turn an implementation's error into
// the wire "ServiceError" response variant.
func assertServiceErrorCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a ServiceError %q, got nil error", wantCode)
	}
	var svcErr *uiapi.ServiceErr
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected a *uiapi.ServiceErr, got %T: %v", err, err)
	}
	if svcErr.Code != wantCode {
		t.Fatalf("expected ServiceError code %q, got %q (%s)", wantCode, svcErr.Code, svcErr.Message)
	}
}
