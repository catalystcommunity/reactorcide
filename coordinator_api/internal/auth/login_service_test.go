package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// fakeBackend is a hand-rolled LoginBackend test double: no real LinkKeys
// crypto/network, just configurable return values and call counters.
type fakeBackend struct {
	mode Mode

	beginRedirect string
	beginPending  []byte
	beginErr      error
	beginCalls    int

	completeIdentity *VerifiedIdentity
	completeErr      error
	completeCalls    int
}

func (b *fakeBackend) Mode() Mode { return b.mode }

func (b *fakeBackend) BeginLogin(context.Context, string, string) (string, []byte, error) {
	b.beginCalls++
	if b.beginErr != nil {
		return "", nil, b.beginErr
	}
	return b.beginRedirect, b.beginPending, nil
}

func (b *fakeBackend) CompleteLogin(context.Context, []byte, string) (*VerifiedIdentity, error) {
	b.completeCalls++
	if b.completeErr != nil {
		return nil, b.completeErr
	}
	return b.completeIdentity, nil
}

func trustDomain(t *testing.T, fs *fakeStore, domain string) {
	t.Helper()
	must(t, fs.UpsertTrustedIdentity(context.Background(), &models.AuthTrustedIdentity{Domain: domain, Handle: "", Source: models.TrustedIdentitySourceAdmin}))
}

func TestLoginServiceModeNoneDisabled(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	backend := &fakeBackend{mode: ModeNone}
	ls := NewLoginService(fs, backend)

	if _, err := ls.StartLogin(ctx, "alice@example.com", "https://cb"); !errors.Is(err, ErrLoginDisabled) {
		t.Fatalf("StartLogin() error = %v, want ErrLoginDisabled", err)
	}
	if _, _, err := ls.FinishLogin(ctx, "whatever", "https://cb"); !errors.Is(err, ErrLoginDisabled) {
		t.Fatalf("FinishLogin() error = %v, want ErrLoginDisabled", err)
	}
	if backend.beginCalls != 0 || backend.completeCalls != 0 {
		t.Fatal("backend must not be called when mode is none")
	}
}

func TestLoginServiceStartLoginNotAdmitted(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore() // nothing trusted
	backend := &fakeBackend{mode: ModeLocalRP}
	ls := NewLoginService(fs, backend)

	_, err := ls.StartLogin(ctx, "alice@untrusted.example.com", "https://cb")
	if !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("StartLogin() error = %v, want ErrNotAdmitted", err)
	}
	if backend.beginCalls != 0 {
		t.Fatal("backend.BeginLogin must not be called for a non-admitted selector")
	}
}

func TestLoginServiceFinishLoginNotAdmittedOnVerifiedIdentity(t *testing.T) {
	// The requested selector is admitted, but the identity that actually
	// completes the login is a different (non-admitted) one — FinishLogin
	// must re-check admission on the VERIFIED identity, not just trust the
	// pre-check.
	ctx := context.Background()
	fs := newFakeStore()
	trustDomain(t, fs, "trusted.example.com")

	backend := &fakeBackend{
		mode:          ModeLocalRP,
		beginRedirect: "https://trusted.example.com/auth",
		beginPending:  []byte("pending"),
		completeIdentity: &VerifiedIdentity{
			Subject: "eve",
			Domain:  "untrusted.example.com",
			Handle:  "eve",
		},
	}
	ls := NewLoginService(fs, backend)

	started, err := ls.StartLogin(ctx, "alice@trusted.example.com", "https://cb")
	if err != nil {
		t.Fatalf("StartLogin() error = %v", err)
	}

	_, _, err = ls.FinishLogin(ctx, started.AttemptToken, "https://cb?encrypted_token=abc")
	if !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("FinishLogin() error = %v, want ErrNotAdmitted", err)
	}
}

func TestLoginServiceFinishLoginSingleUse(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	trustDomain(t, fs, "trusted.example.com")

	backend := &fakeBackend{
		mode:          ModeLocalRP,
		beginRedirect: "https://trusted.example.com/auth",
		beginPending:  []byte("pending"),
		completeIdentity: &VerifiedIdentity{
			Subject: "alice",
			Domain:  "trusted.example.com",
			Handle:  "alice",
		},
	}
	ls := NewLoginService(fs, backend)

	started, err := ls.StartLogin(ctx, "alice@trusted.example.com", "https://cb")
	if err != nil {
		t.Fatalf("StartLogin() error = %v", err)
	}

	token, user, err := ls.FinishLogin(ctx, started.AttemptToken, "https://cb?encrypted_token=abc")
	if err != nil {
		t.Fatalf("FinishLogin() error = %v", err)
	}
	if token == "" || user == nil {
		t.Fatal("FinishLogin() returned an empty token or nil user")
	}

	// Replay: the same attempt token must now be rejected.
	if _, _, err := ls.FinishLogin(ctx, started.AttemptToken, "https://cb?encrypted_token=abc"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replayed FinishLogin() error = %v, want store.ErrNotFound", err)
	}
}

func TestLoginServiceFinishLoginExpired(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	trustDomain(t, fs, "trusted.example.com")

	backend := &fakeBackend{
		mode:          ModeLocalRP,
		beginRedirect: "https://trusted.example.com/auth",
		beginPending:  []byte("pending"),
		completeIdentity: &VerifiedIdentity{
			Subject: "alice",
			Domain:  "trusted.example.com",
			Handle:  "alice",
		},
	}
	ls := NewLoginService(fs, backend)
	// Backdate StartLogin's clock so the persisted attempt's expires_at is
	// already in the past relative to the real wall clock
	// (models.AuthLoginAttempt.IsExpired() uses time.Now(), not an
	// injectable clock).
	ls.now = func() time.Time { return time.Now().Add(-1 * time.Hour) }

	started, err := ls.StartLogin(ctx, "alice@trusted.example.com", "https://cb")
	if err != nil {
		t.Fatalf("StartLogin() error = %v", err)
	}

	_, _, err = ls.FinishLogin(ctx, started.AttemptToken, "https://cb?encrypted_token=abc")
	if !errors.Is(err, ErrAttemptExpired) {
		t.Fatalf("FinishLogin() error = %v, want ErrAttemptExpired", err)
	}
}

func TestLoginServiceProvisionUserCreatesThenReuses(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	trustDomain(t, fs, "trusted.example.com")

	identity := &VerifiedIdentity{Subject: "alice-subj", Domain: "trusted.example.com", Handle: "alice", DisplayName: "Alice"}
	backend := &fakeBackend{mode: ModeLocalRP, beginRedirect: "https://x", beginPending: []byte("p"), completeIdentity: identity}
	ls := NewLoginService(fs, backend)

	login := func() *models.User {
		t.Helper()
		started, err := ls.StartLogin(ctx, "alice@trusted.example.com", "https://cb")
		if err != nil {
			t.Fatalf("StartLogin() error = %v", err)
		}
		_, user, err := ls.FinishLogin(ctx, started.AttemptToken, "https://cb?encrypted_token=abc")
		if err != nil {
			t.Fatalf("FinishLogin() error = %v", err)
		}
		return user
	}

	first := login()
	second := login()

	if first.UserID != second.UserID {
		t.Fatalf("expected the same provisioned user across logins, got %q then %q", first.UserID, second.UserID)
	}
	if len(fs.users) != 1 {
		t.Fatalf("expected exactly one user row, got %d", len(fs.users))
	}
}

func TestLoginServiceFirstAdminGrantedExactlyOnce(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	trustDomain(t, fs, "trusted.example.com")

	origFirstAdmin := config.FirstAdmin
	config.FirstAdmin = "trusted.example.com" // bare-domain selector: any handle at that domain
	defer func() { config.FirstAdmin = origFirstAdmin }()

	backend := &fakeBackend{mode: ModeLocalRP, beginRedirect: "https://x", beginPending: []byte("p")}
	ls := NewLoginService(fs, backend)

	loginAs := func(subject, handle string) *models.User {
		t.Helper()
		backend.completeIdentity = &VerifiedIdentity{Subject: subject, Domain: "trusted.example.com", Handle: handle}
		started, err := ls.StartLogin(ctx, handle+"@trusted.example.com", "https://cb")
		if err != nil {
			t.Fatalf("StartLogin() error = %v", err)
		}
		_, user, err := ls.FinishLogin(ctx, started.AttemptToken, "https://cb?encrypted_token=abc")
		if err != nil {
			t.Fatalf("FinishLogin() error = %v", err)
		}
		return user
	}

	firstUser := loginAs("first-subj", "first")
	secondUser := loginAs("second-subj", "second")

	globalAssignments, err := fs.ListRoleAssignmentsByScope(ctx, models.ScopeTypeGlobal, nil)
	if err != nil {
		t.Fatalf("ListRoleAssignmentsByScope() error = %v", err)
	}
	adminCount := 0
	var adminPrincipal string
	for _, a := range globalAssignments {
		if a.Role == models.RoleAdmin {
			adminCount++
			adminPrincipal = a.PrincipalID
		}
	}
	if adminCount != 1 {
		t.Fatalf("expected exactly one global admin grant, got %d", adminCount)
	}
	if adminPrincipal != firstUser.UserID {
		t.Fatalf("expected the FIRST login (%s) to receive the admin grant, got %s (second user %s)", firstUser.UserID, adminPrincipal, secondUser.UserID)
	}
}

func TestLoginServiceBootstrapAdminSession(t *testing.T) {
	ctx := context.Background()

	origToken := config.BootstrapAdminToken
	defer func() { config.BootstrapAdminToken = origToken }()

	t.Run("not configured is inert", func(t *testing.T) {
		config.BootstrapAdminToken = ""
		fs := newFakeStore()
		ls := NewLoginService(fs, &fakeBackend{mode: ModeNone})
		token, err := ls.BootstrapAdminSession(ctx, "anything")
		if err != nil {
			t.Fatalf("BootstrapAdminSession() error = %v", err)
		}
		if token != "" {
			t.Fatal("expected an empty session token when the bootstrap token isn't configured")
		}
	})

	t.Run("wrong token is inert", func(t *testing.T) {
		config.BootstrapAdminToken = "correct-token"
		fs := newFakeStore()
		ls := NewLoginService(fs, &fakeBackend{mode: ModeNone})
		token, err := ls.BootstrapAdminSession(ctx, "wrong-token")
		if err != nil {
			t.Fatalf("BootstrapAdminSession() error = %v", err)
		}
		if token != "" {
			t.Fatal("expected an empty session token for a wrong bootstrap token")
		}
		if len(fs.users) != 0 {
			t.Fatal("expected no user to be provisioned for a wrong bootstrap token")
		}
	})

	t.Run("admins already exist is inert", func(t *testing.T) {
		config.BootstrapAdminToken = "correct-token"
		fs := newFakeStore()
		must(t, fs.CreateUser(ctx, &models.User{UserID: "existing-admin", Username: "existing-admin"}))
		must(t, fs.CreateRoleAssignment(ctx, &models.RoleAssignment{
			PrincipalType: models.PrincipalTypeUser,
			PrincipalID:   "existing-admin",
			ScopeType:     models.ScopeTypeGlobal,
			Role:          models.RoleAdmin,
		}))
		ls := NewLoginService(fs, &fakeBackend{mode: ModeNone})

		token, err := ls.BootstrapAdminSession(ctx, "correct-token")
		if err != nil {
			t.Fatalf("BootstrapAdminSession() error = %v", err)
		}
		if token != "" {
			t.Fatal("expected an empty session token once a global admin already exists")
		}
		if len(fs.users) != 1 {
			t.Fatalf("expected no additional user to be provisioned, got %d users", len(fs.users))
		}
	})

	t.Run("happy path grants admin and mints a session", func(t *testing.T) {
		config.BootstrapAdminToken = "correct-token"
		fs := newFakeStore()
		ls := NewLoginService(fs, &fakeBackend{mode: ModeNone})

		token, err := ls.BootstrapAdminSession(ctx, "correct-token")
		if err != nil {
			t.Fatalf("BootstrapAdminSession() error = %v", err)
		}
		if token == "" {
			t.Fatal("expected a non-empty session token")
		}

		user, session, err := ls.sessions.ResolveSession(ctx, token)
		if err != nil {
			t.Fatalf("ResolveSession() error = %v", err)
		}
		if user.Username != BootstrapAdminUsername {
			t.Fatalf("user.Username = %q, want %q", user.Username, BootstrapAdminUsername)
		}
		if session.UserID != user.UserID {
			t.Fatal("session.UserID does not match resolved user")
		}

		globalAssignments, err := fs.ListRoleAssignmentsByScope(ctx, models.ScopeTypeGlobal, nil)
		if err != nil {
			t.Fatalf("ListRoleAssignmentsByScope() error = %v", err)
		}
		found := false
		for _, a := range globalAssignments {
			if a.Role == models.RoleAdmin && a.PrincipalID == user.UserID {
				found = true
			}
		}
		if !found {
			t.Fatal("expected a global admin role assignment for the bootstrap admin user")
		}

		// Calling again while the admin now exists must be inert (idempotent).
		token2, err := ls.BootstrapAdminSession(ctx, "correct-token")
		if err != nil {
			t.Fatalf("second BootstrapAdminSession() error = %v", err)
		}
		if token2 != "" {
			t.Fatal("expected the second bootstrap call to be inert now that an admin exists")
		}
	})
}
