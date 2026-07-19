package uiapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// newTestDeps builds a *Deps wired to a fresh fakeStore and a fake, in-memory
// secrets.Provider (deps.SecretsProvider), with the none-mode login backend
// by default. Tests that exercise a real login flow build their own Deps
// with NewDeps + a fake auth.LoginBackend instead (see auth_service_test.go).
func newTestDeps(t *testing.T) (*Deps, *fakeStore) {
	t.Helper()
	st := newFakeStore()
	deps := NewDeps(st, auth.NewNoneBackend(), nil, nil)
	provider := newFakeSecretsProvider()
	deps.SecretsProvider = func(ctx context.Context, orgID string) (secrets.Provider, error) {
		return provider, nil
	}
	return deps, st
}

// withAuthMode scopes a change to config.UIAuthMode (the package var
// auth.CurrentMode() reads) to the running test, restoring the previous
// value on cleanup. Needed to exercise the anonymous-may-cancel-in-mode-none
// vs anonymous-may-not-cancel-in-local-rp/rp-mode permission matrix rows.
func withAuthMode(t *testing.T, mode string) {
	t.Helper()
	prev := config.UIAuthMode
	config.UIAuthMode = mode
	t.Cleanup(func() { config.UIAuthMode = prev })
}

// mintSessionCtx mints a session for userID and returns a context carrying
// it as the CSIL-RPC envelope auth token, exactly as the dispatcher does for
// a real request (see WithAuthToken/AuthTokenFromContext).
func mintSessionCtx(t *testing.T, deps *Deps, userID string) context.Context {
	t.Helper()
	token, err := deps.Sessions.MintSession(context.Background(), userID)
	if err != nil {
		t.Fatalf("MintSession: %v", err)
	}
	return WithAuthToken(context.Background(), token)
}

// anonCtx is an anonymous caller: no auth field on the envelope at all.
func anonCtx() context.Context {
	return context.Background()
}

// serviceErrCode extracts the ServiceErr code from err, failing the test if
// err isn't a *ServiceErr at all.
func serviceErrCode(t *testing.T, err error) string {
	t.Helper()
	se, ok := err.(*ServiceErr)
	if !ok {
		t.Fatalf("err = %v (%T), want *ServiceErr", err, err)
	}
	return se.Code
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

// requireCode asserts err is a *ServiceErr with the given code.
func requireCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want ServiceErr code %q", wantCode)
	}
	if got := serviceErrCode(t, err); got != wantCode {
		t.Fatalf("err code = %q, want %q (err: %v)", got, wantCode, err)
	}
}

// requireOK asserts err is nil.
func requireOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// seedOrgAdmin grants userID an org/admin role assignment at orgID, the
// setup every "org admin" row of the permission matrix needs.
func seedOrgAdmin(st *fakeStore, userID, orgID string) {
	st.grantRole(models.PrincipalTypeUser, userID, models.ScopeTypeOrg, &orgID, models.RoleAdmin)
}

// seedGlobalAdmin grants userID the global/admin role assignment.
func seedGlobalAdmin(st *fakeStore, userID string) {
	st.grantRole(models.PrincipalTypeUser, userID, models.ScopeTypeGlobal, nil, models.RoleAdmin)
}

// seedProjectOwner grants userID a project/owner role assignment at projectID.
func seedProjectOwner(st *fakeStore, userID, projectID string) {
	st.grantRole(models.PrincipalTypeUser, userID, models.ScopeTypeProject, &projectID, models.RoleOwner)
}

// seedProjectMember grants userID a project/member role assignment at projectID.
func seedProjectMember(st *fakeStore, userID, projectID string) {
	st.grantRole(models.PrincipalTypeUser, userID, models.ScopeTypeProject, &projectID, models.RoleMember)
}
