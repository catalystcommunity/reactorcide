package uiapi

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
)

// maxNameLength bounds every free-text "name"-shaped field this service
// accepts (group/webhook-secret/vcs-credential/secret-grant names, etc): a
// generous sanity ceiling, not a product-driven limit.
const maxNameLength = 255

// mapStoreErr maps a store-layer error to the caller-facing ServiceErr the
// generated ServiceError arm should carry. notFoundMsg is used verbatim for
// store.ErrNotFound; any other error is treated as an internal failure and
// never has its (potentially detail-leaking) message forwarded to the
// caller.
func mapStoreErr(err error, notFoundMsg string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return NewServiceError("not_found", notFoundMsg)
	}
	return NewServiceError("internal", "an internal error occurred")
}

// mapPermissionErr maps an authz guard's error (RequireGlobalAdmin/
// RequireOrgAdmin/RequireProjectOwner) to a ServiceErr: a *authz.PermissionError
// becomes "forbidden"; anything else (a store failure while checking) is
// "internal".
func mapPermissionErr(err error) error {
	if err == nil {
		return nil
	}
	if authz.IsPermissionError(err) {
		return NewServiceError("forbidden", "you do not have permission to perform this operation")
	}
	return NewServiceError("internal", "an internal error occurred")
}

// requireNonEmpty validates that s (trimmed) is non-empty and no longer than
// maxLen, returning an "invalid_argument" ServiceErr naming field on
// failure.
func requireNonEmpty(field, s string, maxLen int) error {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return NewServiceError("invalid_argument", fmt.Sprintf("%s must not be empty", field))
	}
	if len(trimmed) > maxLen {
		return NewServiceError("invalid_argument", fmt.Sprintf("%s must be at most %d characters", field, maxLen))
	}
	return nil
}

// optionalMaxLen validates an optional string field's length only (empty is
// fine).
func optionalMaxLen(field, s string, maxLen int) error {
	if len(s) > maxLen {
		return NewServiceError("invalid_argument", fmt.Sprintf("%s must be at most %d characters", field, maxLen))
	}
	return nil
}

// mapSecretsErr maps a secrets.Provider error to a ServiceErr.
func mapSecretsErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, secrets.ErrInvalidPath) || errors.Is(err, secrets.ErrInvalidKey) {
		return NewServiceError("invalid_argument", err.Error())
	}
	if errors.Is(err, secrets.ErrNotInitialized) {
		return NewServiceError("internal", "secrets storage is not initialized for this org")
	}
	return NewServiceError("internal", "an internal error occurred")
}

// formatTime renders t as the RFC3339 timestamp every generated response
// type uses for its string-typed time fields.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// formatTimePtr renders an optional time.
func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := formatTime(*t)
	return &s
}

// derefOr returns *s, or def if s is nil.
func derefOr(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}
