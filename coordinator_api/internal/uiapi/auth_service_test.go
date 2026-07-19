package uiapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// fakeLoginBackend is a minimal auth.LoginBackend for exercising
// AuthService's BeginLogin/CompleteLogin without any real LinkKeys
// SDK/network involvement — it always verifies to the same fixed identity,
// regardless of what CompleteLogin is asked to verify. Mirrors this
// package's fakeStore/fakeSecretsProvider "no real network/DB" testing
// convention.
type fakeLoginBackend struct {
	mode     auth.Mode
	identity auth.VerifiedIdentity
}

func (b fakeLoginBackend) Mode() auth.Mode { return b.mode }

func (b fakeLoginBackend) BeginLogin(_ context.Context, identitySelector, callbackURL string) (string, []byte, error) {
	return "https://" + b.identity.Domain + "/authorize?cb=" + callbackURL, []byte("pending"), nil
}

func (b fakeLoginBackend) CompleteLogin(_ context.Context, pendingBlob []byte, arrivedURL string) (*auth.VerifiedIdentity, error) {
	v := b.identity
	return &v, nil
}

var _ auth.LoginBackend = fakeLoginBackend{}

func withUICallbackURL(t *testing.T) {
	t.Helper()
	prev := config.UICallbackURL
	config.UICallbackURL = "https://coordinator.example.test"
	t.Cleanup(func() { config.UICallbackURL = prev })
}

func TestGetAuthConfig_SafeForAnonymous(t *testing.T) {
	withAuthMode(t, config.UIAuthModeLocalRP)
	deps, _ := newTestDeps(t)
	as := NewAuthService(deps)

	resp, err := as.GetAuthConfig(anonCtx(), csilapi.GetAuthConfigRequest{})
	requireOK(t, err)
	if resp.AuthMode != "local-rp" {
		t.Errorf("AuthMode = %q, want local-rp", resp.AuthMode)
	}
	if resp.HasGlobalAdmin {
		t.Errorf("HasGlobalAdmin = true, want false (no admin seeded)")
	}
}

func TestBeginLogin_ModeNoneDisabled(t *testing.T) {
	withAuthMode(t, config.UIAuthModeNone)
	deps, _ := newTestDeps(t)
	as := NewAuthService(deps)

	_, err := as.BeginLogin(anonCtx(), csilapi.BeginLoginRequest{IdentityHint: strPtr("alice@example.com")})
	requireCode(t, err, "login_disabled")
}

func TestBeginLogin_RequiresIdentityHint(t *testing.T) {
	withAuthMode(t, config.UIAuthModeLocalRP)
	withUICallbackURL(t)
	st := newFakeStore()
	deps := NewDeps(st, fakeLoginBackend{mode: auth.ModeLocalRP, identity: auth.VerifiedIdentity{Subject: "u1", Domain: "example.com", Handle: "alice"}}, nil, nil)
	as := NewAuthService(deps)

	_, err := as.BeginLogin(anonCtx(), csilapi.BeginLoginRequest{})
	requireCode(t, err, "invalid_argument")
}

func TestBeginLogin_NotAdmitted(t *testing.T) {
	withAuthMode(t, config.UIAuthModeLocalRP)
	withUICallbackURL(t)
	st := newFakeStore()
	deps := NewDeps(st, fakeLoginBackend{mode: auth.ModeLocalRP, identity: auth.VerifiedIdentity{Subject: "u1", Domain: "example.com", Handle: "alice"}}, nil, nil)
	as := NewAuthService(deps)

	// No trusted identity/domain pattern seeded — admission must reject.
	_, err := as.BeginLogin(anonCtx(), csilapi.BeginLoginRequest{IdentityHint: strPtr("alice@example.com")})
	requireCode(t, err, "forbidden")
}

// TestLoginFlow_HappyPath drives BeginLogin -> CompleteLogin -> Authenticate
// -> Logout end to end against a fake LoginBackend, proving the full
// session lifecycle: a minted session resolves to the right identity, and a
// revoked session no longer authenticates.
func TestLoginFlow_HappyPath(t *testing.T) {
	withAuthMode(t, config.UIAuthModeLocalRP)
	withUICallbackURL(t)
	st := newFakeStore()
	if err := st.UpsertTrustedIdentity(context.Background(), &models.AuthTrustedIdentity{Domain: "example.com", Source: models.TrustedIdentitySourceAdmin}); err != nil {
		t.Fatalf("seed trusted identity: %v", err)
	}
	deps := NewDeps(st, fakeLoginBackend{mode: auth.ModeLocalRP, identity: auth.VerifiedIdentity{Subject: "u1", Domain: "example.com", Handle: "alice", DisplayName: "Alice"}}, nil, nil)
	as := NewAuthService(deps)

	begun, err := as.BeginLogin(anonCtx(), csilapi.BeginLoginRequest{IdentityHint: strPtr("alice@example.com")})
	requireOK(t, err)
	if begun.AttemptToken == "" {
		t.Fatalf("AttemptToken is empty")
	}

	completed, err := as.CompleteLogin(anonCtx(), csilapi.CompleteLoginRequest{
		AttemptToken:   begun.AttemptToken,
		EncryptedToken: "opaque-encrypted-token",
	})
	requireOK(t, err)
	if completed.SessionToken == "" {
		t.Fatalf("SessionToken is empty")
	}
	if completed.Identity.Handle != "alice" || completed.Identity.Domain != "example.com" {
		t.Fatalf("Identity = %+v, want handle=alice domain=example.com", completed.Identity)
	}

	// Replaying the same attempt token must fail (single-use).
	_, err = as.CompleteLogin(anonCtx(), csilapi.CompleteLoginRequest{
		AttemptToken:   begun.AttemptToken,
		EncryptedToken: "opaque-encrypted-token",
	})
	requireCode(t, err, "invalid_argument")

	authCtx := WithAuthToken(anonCtx(), completed.SessionToken)
	authResp, err := as.Authenticate(authCtx, csilapi.AuthenticateRequest{})
	requireOK(t, err)
	if !authResp.Authenticated || authResp.Identity == nil {
		t.Fatalf("Authenticated = %v, Identity = %v, want true/non-nil", authResp.Authenticated, authResp.Identity)
	}

	logoutResp, err := as.Logout(authCtx, csilapi.LogoutRequest{})
	requireOK(t, err)
	if !logoutResp.Ok {
		t.Fatalf("Logout Ok = false")
	}

	afterLogout, err := as.Authenticate(authCtx, csilapi.AuthenticateRequest{})
	requireOK(t, err)
	if afterLogout.Authenticated {
		t.Fatalf("Authenticated = true after logout, want false")
	}
}

func TestAuthenticate_NoTokenIsUnauthenticatedNotError(t *testing.T) {
	deps, _ := newTestDeps(t)
	as := NewAuthService(deps)

	resp, err := as.Authenticate(anonCtx(), csilapi.AuthenticateRequest{})
	requireOK(t, err)
	if resp.Authenticated {
		t.Fatalf("Authenticated = true for a request with no auth token")
	}
}

// TestBootstrapAdmin_InertFailure asserts every failure path (not
// configured, wrong token, admin already exists) uniformly reports
// "unauthorized" — never a distinguishing error — per
// LoginService.BootstrapAdminSession's no-oracle design.
func TestBootstrapAdmin_InertFailure(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		deps, _ := newTestDeps(t)
		as := NewAuthService(deps)
		_, err := as.BootstrapAdmin(anonCtx(), csilapi.BootstrapAdminRequest{BootstrapToken: "anything"})
		requireCode(t, err, "unauthorized")
	})

	t.Run("wrong token", func(t *testing.T) {
		prev := config.BootstrapAdminToken
		config.BootstrapAdminToken = "correct-token"
		t.Cleanup(func() { config.BootstrapAdminToken = prev })

		deps, _ := newTestDeps(t)
		as := NewAuthService(deps)
		_, err := as.BootstrapAdmin(anonCtx(), csilapi.BootstrapAdminRequest{BootstrapToken: "wrong-token"})
		requireCode(t, err, "unauthorized")
	})
}

func TestBootstrapAdmin_HappyPath(t *testing.T) {
	prev := config.BootstrapAdminToken
	config.BootstrapAdminToken = "correct-token"
	t.Cleanup(func() { config.BootstrapAdminToken = prev })

	deps, _ := newTestDeps(t)
	as := NewAuthService(deps)

	resp, err := as.BootstrapAdmin(anonCtx(), csilapi.BootstrapAdminRequest{BootstrapToken: "correct-token"})
	requireOK(t, err)
	if resp.SessionToken == "" {
		t.Fatalf("SessionToken is empty")
	}

	// The session must actually resolve to a global admin.
	authCtx := WithAuthToken(anonCtx(), resp.SessionToken)
	ui := NewUiService(deps)
	caps, err := ui.GetCapabilities(authCtx, csilapi.GetCapabilitiesRequest{})
	requireOK(t, err)
	if !caps.IsGlobalAdmin {
		t.Fatalf("IsGlobalAdmin = false after bootstrap-admin, want true")
	}

	// A second bootstrap attempt must now be inert (an admin already exists).
	_, err = as.BootstrapAdmin(anonCtx(), csilapi.BootstrapAdminRequest{BootstrapToken: "correct-token"})
	requireCode(t, err, "unauthorized")
}
