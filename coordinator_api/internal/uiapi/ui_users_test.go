package uiapi

import (
	"context"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// seedListUsersFixture builds the org, an org admin, a plain member, and two
// grant targets: one login-provisioned (with an auth identity) and one
// bootstrap-style (no identity).
func seedListUsersFixture(t *testing.T) (*Deps, *fakeStore, models.User, models.User) {
	t.Helper()
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1", Username: "org-one"})
	admin := st.putUser(models.User{UserID: "admin-1", Username: "admin"})
	member := st.putUser(models.User{UserID: "member-1", Username: "plain-member"})
	seedOrgAdmin(st, admin.UserID, "org-1")

	lastLogin := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	st.putUser(models.User{UserID: "user-alice", Username: "alice", Email: "alice@example.test"})
	if err := st.CreateAuthIdentity(context.Background(), &models.AuthIdentity{
		UserID:      "user-alice",
		Subject:     "alice@linkkeys.test",
		Handle:      "alice",
		Domain:      "linkkeys.test",
		DisplayName: "Alice Liddell",
		LastLoginAt: &lastLogin,
	}); err != nil {
		t.Fatalf("CreateAuthIdentity: %v", err)
	}
	st.putUser(models.User{UserID: "user-bob", Username: "bob", Email: "bob@example.test"})
	st.putUser(models.User{UserID: "user-gone", Username: "carol", Status: "disabled"})
	return deps, st, admin, member
}

func usersByID(resp csilapi.ListUsersResponse) map[string]csilapi.UserSummary {
	out := map[string]csilapi.UserSummary{}
	for _, u := range resp.Users {
		out[u.UserId] = u
	}
	return out
}

func TestListUsers_AnonymousIsUnauthorized(t *testing.T) {
	deps, _, _, _ := seedListUsersFixture(t)
	ui := NewUiService(deps)

	_, err := ui.ListUsers(anonCtx(), csilapi.ListUsersRequest{OrgId: "org-1"})
	requireCode(t, err, "unauthorized")
}

func TestListUsers_NonAdminIsForbidden(t *testing.T) {
	deps, _, _, member := seedListUsersFixture(t)
	ui := NewUiService(deps)

	_, err := ui.ListUsers(mintSessionCtx(t, deps, member.UserID), csilapi.ListUsersRequest{OrgId: "org-1"})
	requireCode(t, err, "forbidden")
}

func TestListUsers_RequiresOrgID(t *testing.T) {
	deps, _, admin, _ := seedListUsersFixture(t)
	ui := NewUiService(deps)

	_, err := ui.ListUsers(mintSessionCtx(t, deps, admin.UserID), csilapi.ListUsersRequest{})
	requireCode(t, err, "invalid_argument")
}

func TestListUsers_OrgAdminSeesActiveUsersWithIdentity(t *testing.T) {
	deps, _, admin, _ := seedListUsersFixture(t)
	ui := NewUiService(deps)

	resp, err := ui.ListUsers(mintSessionCtx(t, deps, admin.UserID), csilapi.ListUsersRequest{OrgId: "org-1"})
	requireOK(t, err)
	got := usersByID(resp)

	if _, ok := got["user-gone"]; ok {
		t.Error("disabled user must not be offered as a grant target")
	}
	alice, ok := got["user-alice"]
	if !ok {
		t.Fatalf("alice missing from %+v", resp.Users)
	}
	if alice.Username != "alice" {
		t.Errorf("alice.Username = %q", alice.Username)
	}
	if alice.Subject == nil || *alice.Subject != "alice@linkkeys.test" {
		t.Errorf("alice.Subject = %v, want alice@linkkeys.test", alice.Subject)
	}
	if alice.DisplayName == nil || *alice.DisplayName != "Alice Liddell" {
		t.Errorf("alice.DisplayName = %v, want Alice Liddell", alice.DisplayName)
	}
	if alice.LastLoginAt == nil || *alice.LastLoginAt != "2026-09-01T12:00:00Z" {
		t.Errorf("alice.LastLoginAt = %v, want RFC3339 2026-09-01T12:00:00Z", alice.LastLoginAt)
	}

	bob, ok := got["user-bob"]
	if !ok {
		t.Fatalf("bob (no auth identity) missing from %+v", resp.Users)
	}
	if bob.Subject != nil || bob.DisplayName != nil || bob.LastLoginAt != nil {
		t.Errorf("bob has no identity; optional fields must be absent, got %+v", bob)
	}

	// Ordered by username.
	for i := 1; i < len(resp.Users); i++ {
		if resp.Users[i-1].Username > resp.Users[i].Username {
			t.Fatalf("users not ordered by username: %+v", resp.Users)
		}
	}
}

func TestListUsers_GlobalAdminPasses(t *testing.T) {
	deps, st, _, _ := seedListUsersFixture(t)
	global := st.putUser(models.User{UserID: "global-1", Username: "root"})
	seedGlobalAdmin(st, global.UserID)
	ui := NewUiService(deps)

	resp, err := ui.ListUsers(mintSessionCtx(t, deps, global.UserID), csilapi.ListUsersRequest{OrgId: "org-1"})
	requireOK(t, err)
	if _, ok := usersByID(resp)["user-alice"]; !ok {
		t.Fatalf("global admin should list users, got %+v", resp.Users)
	}
}

func TestListUsers_QueryFiltersAcrossUsernameAndIdentity(t *testing.T) {
	deps, _, admin, _ := seedListUsersFixture(t)
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	// Display name match, case-insensitive.
	resp, err := ui.ListUsers(ctx, csilapi.ListUsersRequest{OrgId: "org-1", Query: strPtr("LIDDELL")})
	requireOK(t, err)
	if len(resp.Users) != 1 || resp.Users[0].UserId != "user-alice" {
		t.Fatalf("query LIDDELL: got %+v, want only alice", resp.Users)
	}

	// Username match.
	resp, err = ui.ListUsers(ctx, csilapi.ListUsersRequest{OrgId: "org-1", Query: strPtr("bob")})
	requireOK(t, err)
	if len(resp.Users) != 1 || resp.Users[0].UserId != "user-bob" {
		t.Fatalf("query bob: got %+v, want only bob", resp.Users)
	}

	// No match.
	resp, err = ui.ListUsers(ctx, csilapi.ListUsersRequest{OrgId: "org-1", Query: strPtr("nobody-here")})
	requireOK(t, err)
	if len(resp.Users) != 0 {
		t.Fatalf("query nobody-here: got %+v, want none", resp.Users)
	}
}

func TestListUsers_DispatcherRoutesOp(t *testing.T) {
	// The op table is hand-maintained; a missing row is a silent 'unknown
	// operation' at runtime rather than a compile error.
	deps, _, _, _ := seedListUsersFixture(t)
	h := NewHandler(NewAuthService(deps), NewUiService(deps))
	if _, ok := h.ops["ReactorcideUi"]["list-users"]; !ok {
		t.Fatal("dispatcher has no ReactorcideUi/list-users op")
	}
}
