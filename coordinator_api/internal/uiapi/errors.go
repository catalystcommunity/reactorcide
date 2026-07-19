package uiapi

import (
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// ServiceErr is the Go error type an implementation returns for an
// application-level failure. The dispatcher recognizes it (via errors.As)
// and encodes it as the CSIL-RPC "ServiceError" response variant with
// transport status 0 — a typed reply the generated client surfaces as a
// *ClientError with Code/Message set. Any other error an implementation
// returns is treated as a transport-level failure instead (non-zero status,
// no typed payload).
type ServiceErr struct {
	csilapi.ServiceError
}

func (e *ServiceErr) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NewServiceError builds a ServiceErr for the given code/message. Codes are
// short machine-readable strings (e.g. "unimplemented", "not_found",
// "forbidden", "invalid_argument"); message is a human-readable detail.
func NewServiceError(code, message string) error {
	return &ServiceErr{ServiceError: csilapi.ServiceError{Code: code, Message: message}}
}

// ErrUnimplemented builds the ServiceErr stub implementations return for
// every op, identifying which (service, op) was called.
func ErrUnimplemented(op string) error {
	return NewServiceError("unimplemented", fmt.Sprintf("%s is not implemented yet", op))
}
