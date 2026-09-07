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

// TestLoginServiceFirstAdminGrantedExactlyOnce pins the one-shot rule: the
// configured identity is granted global admin only while NO global admin
// exists. Once any admin exists, even one created some other way, the
// variable is inert. A standing grant would let whoever edits the deployment
// configuration take over an instance that already has administrators.
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
	globalAdmins := func() []string {
		t.Helper()
		assignments, err := fs.ListRoleAssignmentsByScope(ctx, models.ScopeTypeGlobal, nil)
		if err != nil {
			t.Fatalf("ListRoleAssignmentsByScope() error = %v", err)
		}
		var out []string
		for _, a := range assignments {
			if a.Role == models.RoleAdmin {
				out = append(out, a.PrincipalID)
			}
		}
		return out
	}

	firstUser := loginAs("first-subj", "first")
	secondUser := loginAs("second-subj", "second")
	loginAs("first-subj", "first") // a repeat login must not add a row either

	admins := globalAdmins()
	if len(admins) != 1 || admins[0] != firstUser.UserID {
		t.Fatalf("expected exactly one global admin grant, to the FIRST login %s, got %v (second user %s)", firstUser.UserID, admins, secondUser.UserID)
	}

	// An admin that exists before the configured identity ever logs in makes
	// the variable inert: nothing is granted.
	fs2 := newFakeStore()
	trustDomain(t, fs2, "trusted.example.com")
	if err := fs2.CreateRoleAssignment(ctx, &models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser, PrincipalID: "pre-existing-admin",
		ScopeType: models.ScopeTypeGlobal, Role: models.RoleAdmin,
	}); err != nil {
		t.Fatalf("seeding admin: %v", err)
	}
	ls2 := NewLoginService(fs2, backend)
	backend.completeIdentity = &VerifiedIdentity{Subject: "late-subj", Domain: "trusted.example.com", Handle: "late"}
	started, err := ls2.StartLogin(ctx, "late@trusted.example.com", "https://cb")
	if err != nil {
		t.Fatalf("StartLogin() error = %v", err)
	}
	if _, _, err := ls2.FinishLogin(ctx, started.AttemptToken, "https://cb?encrypted_token=abc"); err != nil {
		t.Fatalf("FinishLogin() error = %v", err)
	}
	assignments, err := fs2.ListRoleAssignmentsByScope(ctx, models.ScopeTypeGlobal, nil)
	if err != nil {
		t.Fatalf("ListRoleAssignmentsByScope() error = %v", err)
	}
	if len(assignments) != 1 || assignments[0].PrincipalID != "pre-existing-admin" {
		t.Fatalf("expected the configured identity to receive nothing once an admin exists, got %+v", assignments)
	}
}

// TestLoginServiceFirstAdminMatchesEmailClaim covers rp mode, where the
// subject is a uuid and the handle claim is best effort: the operator writes
// the login name they know, and that is the email claim. Still one-shot.
func TestLoginServiceFirstAdminMatchesEmailClaim(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	trustDomain(t, fs, "trusted.example.com")

	origFirstAdmin := config.FirstAdmin
	config.FirstAdmin = "Tod@Trusted.Example.com"
	defer func() { config.FirstAdmin = origFirstAdmin }()

	backend := &fakeBackend{mode: ModeLocalRP, beginRedirect: "https://x", beginPending: []byte("p")}
	ls := NewLoginService(fs, backend)
	backend.completeIdentity = &VerifiedIdentity{
		Subject: "0191e6c4-0000-7000-8000-000000000001",
		Domain:  "trusted.example.com",
		// No handle claim came back.
		Claims: map[string]string{"email": "tod@trusted.example.com"},
	}
	started, err := ls.StartLogin(ctx, "tod@trusted.example.com", "https://cb")
	if err != nil {
		t.Fatalf("StartLogin() error = %v", err)
	}
	_, user, err := ls.FinishLogin(ctx, started.AttemptToken, "https://cb?encrypted_token=abc")
	if err != nil {
		t.Fatalf("FinishLogin() error = %v", err)
	}
	assignments, err := fs.ListRoleAssignmentsByScope(ctx, models.ScopeTypeGlobal, nil)
	if err != nil {
		t.Fatalf("ListRoleAssignmentsByScope() error = %v", err)
	}
	if len(assignments) != 1 || assignments[0].PrincipalID != user.UserID || assignments[0].Role != models.RoleAdmin {
		t.Fatalf("expected a global admin grant for %s via the email claim, got %+v", user.UserID, assignments)
	}

	// The email match is exact on the whole selector: a different local part
	// at the same domain must not match through the email path.
	if matchesFirstAdminSelector(&VerifiedIdentity{Domain: "trusted.example.com", Claims: map[string]string{"email": "lorna@trusted.example.com"}}, "tod", "trusted.example.com") {
		t.Fatal("a different email at the same domain must not match a handle selector")
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

// TestMatchesFirstAdminSelectorCaseInsensitive pins the matching rule for
// REACTORCIDE_FIRST_ADMIN: domains are DNS names and handles are not
// case-distinct, so a selector an operator typed with different casing (or
// stray whitespace) than the identity LinkKeys verifies must still match.
// Production ran with no global admin because "tod@todandlorna.com" was
// compared byte-for-byte against the verified identity.
func TestMatchesFirstAdminSelectorCaseInsensitive(t *testing.T) {
	verified := &VerifiedIdentity{Handle: "tod", Subject: "0f4c1c5e-uuid", Domain: "todandlorna.com"}

	cases := []struct {
		name     string
		selector string
		want     bool
	}{
		{"exact", "tod@todandlorna.com", true},
		{"upper-case handle", "Tod@todandlorna.com", true},
		{"upper-case domain", "tod@TodAndLorna.COM", true},
		{"both upper-case", "TOD@TODANDLORNA.COM", true},
		{"surrounding whitespace", "  tod@todandlorna.com  ", true},
		{"subject instead of handle, mixed case", "0F4C1C5E-UUID@todandlorna.com", true},
		{"bare domain, mixed case", "TodAndLorna.com", true},
		{"different handle", "lorna@todandlorna.com", false},
		{"different domain", "tod@example.com", false},
		{"handle as a prefix only", "to@todandlorna.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handle, domain, err := ParseSelector(tc.selector)
			if err != nil {
				t.Fatalf("ParseSelector(%q) error = %v", tc.selector, err)
			}
			if got := matchesFirstAdminSelector(verified, handle, domain); got != tc.want {
				t.Errorf("matchesFirstAdminSelector(%q) = %v, want %v", tc.selector, got, tc.want)
			}
		})
	}

	// The verified side is normalised too: an identity that arrives with
	// upper-case parts still matches a lower-case selector.
	upper := &VerifiedIdentity{Handle: "TOD", Subject: "S", Domain: "TODANDLORNA.COM"}
	if !matchesFirstAdminSelector(upper, "tod", "todandlorna.com") {
		t.Error("an upper-case verified identity must match a lower-case selector")
	}

	// matchesFirstAdmin (the config-reading wrapper) applies the same rule.
	origFirstAdmin := config.FirstAdmin
	config.FirstAdmin = "TOD@TodAndLorna.com"
	defer func() { config.FirstAdmin = origFirstAdmin }()
	if !matchesFirstAdmin(verified) {
		t.Error("matchesFirstAdmin must be case-insensitive")
	}
	config.FirstAdmin = "not a selector with @@ empty domain@"
	if matchesFirstAdmin(verified) {
		t.Error("an unparseable selector must never match")
	}
}

// TestLoginServiceFirstAdminGrantIsCaseInsensitive runs the full login flow
// with a mixed-case REACTORCIDE_FIRST_ADMIN and asserts the grant lands.
func TestLoginServiceFirstAdminGrantIsCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	trustDomain(t, fs, "trusted.example.com")

	origFirstAdmin := config.FirstAdmin
	config.FirstAdmin = "Alice@Trusted.Example.COM"
	defer func() { config.FirstAdmin = origFirstAdmin }()

	backend := &fakeBackend{mode: ModeLocalRP, beginRedirect: "https://x", beginPending: []byte("p")}
	ls := NewLoginService(fs, backend)

	backend.completeIdentity = &VerifiedIdentity{Subject: "alice-subj", Domain: "trusted.example.com", Handle: "alice"}
	started, err := ls.StartLogin(ctx, "alice@trusted.example.com", "https://cb")
	if err != nil {
		t.Fatalf("StartLogin() error = %v", err)
	}
	_, user, err := ls.FinishLogin(ctx, started.AttemptToken, "https://cb?encrypted_token=abc")
	if err != nil {
		t.Fatalf("FinishLogin() error = %v", err)
	}

	globalAssignments, err := fs.ListRoleAssignmentsByScope(ctx, models.ScopeTypeGlobal, nil)
	if err != nil {
		t.Fatalf("ListRoleAssignmentsByScope() error = %v", err)
	}
	granted := false
	for _, a := range globalAssignments {
		if a.Role == models.RoleAdmin && a.PrincipalID == user.UserID {
			granted = true
		}
	}
	if !granted {
		t.Fatalf("expected %s to receive the global admin grant for a mixed-case FIRST_ADMIN selector", user.UserID)
	}
}
