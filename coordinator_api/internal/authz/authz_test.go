package authz

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// fakeStore is a hand-rolled in-memory RoleStore + SettingsStore for authz
// unit tests — this package's convention (see internal/auth's fakes_test.go)
// rather than a generated mock, since the surface is small and the tests
// want full control over role-assignment/group shape.
type fakeStore struct {
	users        map[string]*models.User
	projects     map[string]*models.Project
	groupsByUser map[string][]models.Group
	assignments  []models.RoleAssignment
	settings     map[string]*models.GlobalSetting

	getUserCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:        map[string]*models.User{},
		projects:     map[string]*models.Project{},
		groupsByUser: map[string][]models.Group{},
		settings:     map[string]*models.GlobalSetting{},
	}
}

func (f *fakeStore) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	f.getUserCalls++
	u, ok := f.users[userID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return u, nil
}

func (f *fakeStore) GetProjectByID(ctx context.Context, projectID string) (*models.Project, error) {
	p, ok := f.projects[projectID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) ListGroupsForUser(ctx context.Context, userID string) ([]models.Group, error) {
	return f.groupsByUser[userID], nil
}

func (f *fakeStore) ListRoleAssignmentsForPrincipal(ctx context.Context, userID string, groupIDs []string) ([]models.RoleAssignment, error) {
	groupSet := make(map[string]bool, len(groupIDs))
	for _, g := range groupIDs {
		groupSet[g] = true
	}
	var out []models.RoleAssignment
	for _, a := range f.assignments {
		if a.PrincipalType == models.PrincipalTypeUser && a.PrincipalID == userID {
			out = append(out, a)
			continue
		}
		if a.PrincipalType == models.PrincipalTypeGroup && groupSet[a.PrincipalID] {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeStore) GetGlobalSetting(ctx context.Context, key string) (*models.GlobalSetting, error) {
	s, ok := f.settings[key]
	if !ok {
		return nil, store.ErrNotFound
	}
	return s, nil
}

func strPtr(s string) *string { return &s }

func mustJSON(t *testing.T, v interface{}) models.JSONValue {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return models.JSONValue(b)
}

// --- Capability matrix -----------------------------------------------------

func TestCapabilities_Matrix(t *testing.T) {
	origMode := config.UIAuthMode
	defer func() { config.UIAuthMode = origMode }()

	const (
		orgID     = "org-1"
		projectID = "proj-1"

		memberID    = "user-member"
		ownerID     = "user-owner"
		selfOrgID   = "user-self-org" // owns projectID's org directly (org_id == user_id)
		orgAdminID  = "user-org-admin"
		globalAdmID = "user-global-admin"
		strangerID  = "user-stranger"
	)

	fs := newFakeStore()
	fs.projects[projectID] = &models.Project{ProjectID: projectID, UserID: strPtr(orgID)}
	fs.assignments = []models.RoleAssignment{
		{PrincipalType: models.PrincipalTypeUser, PrincipalID: memberID, ScopeType: models.ScopeTypeProject, ScopeID: strPtr(projectID), Role: models.RoleMember},
		{PrincipalType: models.PrincipalTypeUser, PrincipalID: ownerID, ScopeType: models.ScopeTypeProject, ScopeID: strPtr(projectID), Role: models.RoleOwner},
		{PrincipalType: models.PrincipalTypeUser, PrincipalID: orgAdminID, ScopeType: models.ScopeTypeOrg, ScopeID: strPtr(orgID), Role: models.RoleAdmin},
		{PrincipalType: models.PrincipalTypeUser, PrincipalID: globalAdmID, ScopeType: models.ScopeTypeGlobal, Role: models.RoleAdmin},
	}

	r := NewResolver(fs)
	scope := Scope{ProjectID: strPtr(projectID)}

	fullOrgAdmin := func() Caps { return orgAdminCaps() }
	fullGlobalAdmin := func() Caps { c := orgAdminCaps(); c.GlobalAdmin = true; return c }

	cases := []struct {
		name string
		mode string
		id   Identity
		want Caps
	}{
		{"anon none-mode may cancel and retry only", config.UIAuthModeNone, AnonymousIdentity(), Caps{Cancel: true, Retry: true}},
		{"anon auth-mode gets nothing", config.UIAuthModeLocalRP, AnonymousIdentity(), Caps{}},
		{"anon rp-mode gets nothing", config.UIAuthModeRP, AnonymousIdentity(), Caps{}},
		{"stranger (no assignment) gets nothing", config.UIAuthModeLocalRP, UserIdentity(strangerID), Caps{}},
		{"member: view private only", config.UIAuthModeLocalRP, UserIdentity(memberID), Caps{ViewPrivate: true}},
		{"project owner", config.UIAuthModeLocalRP, UserIdentity(ownerID), Caps{ViewPrivate: true, Cancel: true, Retry: true, ProjectSettings: true}},
		{"self-org (owns the project's org directly)", config.UIAuthModeLocalRP, UserIdentity(orgID), fullOrgAdmin()},
		{"org admin", config.UIAuthModeLocalRP, UserIdentity(orgAdminID), fullOrgAdmin()},
		{"global admin", config.UIAuthModeLocalRP, UserIdentity(globalAdmID), fullGlobalAdmin()},
		// none-mode doesn't gate a resolved, logged-in identity's tier —
		// only anonymous callers are special-cased to Cancel-only.
		{"org admin still full caps in none-mode", config.UIAuthModeNone, UserIdentity(orgAdminID), fullOrgAdmin()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config.UIAuthMode = tc.mode
			got, err := r.Capabilities(context.Background(), tc.id, scope)
			if err != nil {
				t.Fatalf("Capabilities: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Capabilities() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCapabilities_GlobalScopeOnlyGlobalAdminTrue(t *testing.T) {
	origMode := config.UIAuthMode
	config.UIAuthMode = config.UIAuthModeLocalRP
	defer func() { config.UIAuthMode = origMode }()

	fs := newFakeStore()
	fs.assignments = []models.RoleAssignment{
		{PrincipalType: models.PrincipalTypeUser, PrincipalID: "u-global", ScopeType: models.ScopeTypeGlobal, Role: models.RoleAdmin},
		{PrincipalType: models.PrincipalTypeUser, PrincipalID: "u-plain", ScopeType: models.ScopeTypeOrg, ScopeID: strPtr("some-org"), Role: models.RoleMember},
	}
	r := NewResolver(fs)

	got, err := r.Capabilities(context.Background(), UserIdentity("u-plain"), Scope{})
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if got != (Caps{}) {
		t.Fatalf("non-admin at global scope should get zero caps, got %+v", got)
	}

	got, err = r.Capabilities(context.Background(), UserIdentity("u-global"), Scope{})
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	want := orgAdminCaps()
	want.GlobalAdmin = true
	if got != want {
		t.Fatalf("global admin at global scope = %+v, want %+v", got, want)
	}
}

// --- Role resolution via groups --------------------------------------------

func TestResolver_RoleResolutionViaGroups(t *testing.T) {
	const (
		orgID     = "org-1"
		projectID = "proj-1"
		groupID   = "group-1"
		userID    = "user-1"
	)

	fs := newFakeStore()
	fs.projects[projectID] = &models.Project{ProjectID: projectID, UserID: strPtr(orgID)}
	fs.groupsByUser[userID] = []models.Group{{GroupID: groupID, OrgID: orgID, Name: "admins"}}
	fs.assignments = []models.RoleAssignment{
		{PrincipalType: models.PrincipalTypeGroup, PrincipalID: groupID, ScopeType: models.ScopeTypeOrg, ScopeID: strPtr(orgID), Role: models.RoleAdmin},
	}

	r := NewResolver(fs)
	id := UserIdentity(userID)
	ctx := context.Background()

	if ok, err := r.IsOrgAdmin(ctx, id, orgID); err != nil || !ok {
		t.Fatalf("IsOrgAdmin via group = %v, %v; want true, nil", ok, err)
	}
	if ok, err := r.IsOrgAdmin(ctx, id, "other-org"); err != nil || ok {
		t.Fatalf("IsOrgAdmin for unrelated org = %v, %v; want false, nil", ok, err)
	}
	// Org admin (even via group) implies project ownership of the org's projects.
	if ok, err := r.IsProjectOwner(ctx, id, projectID); err != nil || !ok {
		t.Fatalf("IsProjectOwner via group-derived org admin = %v, %v; want true, nil", ok, err)
	}
	// A user with no assignment at all, direct or via group, is not an org admin.
	if ok, err := r.IsOrgAdmin(ctx, UserIdentity("nobody"), orgID); err != nil || ok {
		t.Fatalf("IsOrgAdmin for user with no assignments = %v, %v; want false, nil", ok, err)
	}
}

func TestResolver_IsProjectOwnerDirectGroupGrant(t *testing.T) {
	const (
		projectID = "proj-1"
		groupID   = "group-owners"
		userID    = "user-1"
	)
	fs := newFakeStore()
	fs.projects[projectID] = &models.Project{ProjectID: projectID, UserID: strPtr("some-other-org")}
	fs.groupsByUser[userID] = []models.Group{{GroupID: groupID}}
	fs.assignments = []models.RoleAssignment{
		{PrincipalType: models.PrincipalTypeGroup, PrincipalID: groupID, ScopeType: models.ScopeTypeProject, ScopeID: strPtr(projectID), Role: models.RoleOwner},
	}
	r := NewResolver(fs)
	ok, err := r.IsProjectOwner(context.Background(), UserIdentity(userID), projectID)
	if err != nil || !ok {
		t.Fatalf("IsProjectOwner via direct group grant = %v, %v; want true, nil", ok, err)
	}
}

func TestResolver_EffectiveRoleForProject(t *testing.T) {
	const (
		orgID     = "org-1"
		projectID = "proj-1"
		memberID  = "user-member"
	)
	fs := newFakeStore()
	fs.projects[projectID] = &models.Project{ProjectID: projectID, UserID: strPtr(orgID)}
	fs.assignments = []models.RoleAssignment{
		{PrincipalType: models.PrincipalTypeUser, PrincipalID: memberID, ScopeType: models.ScopeTypeProject, ScopeID: strPtr(projectID), Role: models.RoleMember},
	}
	r := NewResolver(fs)
	ctx := context.Background()

	role, err := r.EffectiveRoleForProject(ctx, UserIdentity(memberID), projectID)
	if err != nil || role != models.RoleMember {
		t.Fatalf("EffectiveRoleForProject(member) = %q, %v; want %q, nil", role, err, models.RoleMember)
	}
	role, err = r.EffectiveRoleForProject(ctx, UserIdentity(orgID), projectID)
	if err != nil || role != models.RoleAdmin {
		t.Fatalf("EffectiveRoleForProject(self-org) = %q, %v; want %q, nil", role, err, models.RoleAdmin)
	}
	role, err = r.EffectiveRoleForProject(ctx, UserIdentity("stranger"), projectID)
	if err != nil || role != "" {
		t.Fatalf("EffectiveRoleForProject(stranger) = %q, %v; want \"\", nil", role, err)
	}
	role, err = r.EffectiveRoleForProject(ctx, AnonymousIdentity(), projectID)
	if err != nil || role != "" {
		t.Fatalf("EffectiveRoleForProject(anon) = %q, %v; want \"\", nil", role, err)
	}
}

// --- Guards ------------------------------------------------------------

func TestGuards_ReturnPermissionError(t *testing.T) {
	fs := newFakeStore()
	fs.projects["proj-1"] = &models.Project{ProjectID: "proj-1", UserID: strPtr("org-1")}
	r := NewResolver(fs)
	ctx := context.Background()
	stranger := UserIdentity("stranger")

	if err := r.RequireGlobalAdmin(ctx, stranger); !IsPermissionError(err) {
		t.Fatalf("RequireGlobalAdmin: want PermissionError, got %v", err)
	}
	if err := r.RequireOrgAdmin(ctx, stranger, "org-1"); !IsPermissionError(err) {
		t.Fatalf("RequireOrgAdmin: want PermissionError, got %v", err)
	}
	if err := r.RequireProjectOwner(ctx, stranger, "proj-1"); !IsPermissionError(err) {
		t.Fatalf("RequireProjectOwner: want PermissionError, got %v", err)
	}
	// The org's own user always passes every guard scoped to their own org.
	if err := r.RequireOrgAdmin(ctx, UserIdentity("org-1"), "org-1"); err != nil {
		t.Fatalf("RequireOrgAdmin(self-org): unexpected error %v", err)
	}
	if err := r.RequireProjectOwner(ctx, UserIdentity("org-1"), "proj-1"); err != nil {
		t.Fatalf("RequireProjectOwner(self-org owner): unexpected error %v", err)
	}
}

// --- Visibility --------------------------------------------------------

func TestCanViewProject_Public(t *testing.T) {
	fs := newFakeStore()
	fs.users["org-1"] = &models.User{UserID: "org-1", IsPrivate: false}
	project := &models.Project{ProjectID: "proj-1", UserID: strPtr("org-1"), IsPrivate: false}
	r := NewResolver(fs)

	ok, err := r.CanViewProject(context.Background(), AnonymousIdentity(), project)
	if err != nil || !ok {
		t.Fatalf("public project should be visible to anonymous: %v, %v", ok, err)
	}
	ok, err = r.CanViewProject(context.Background(), UserIdentity("stranger"), project)
	if err != nil || !ok {
		t.Fatalf("public project should be visible to any logged-in stranger: %v, %v", ok, err)
	}
}

func TestCanViewProject_PrivateAssignedVsStranger(t *testing.T) {
	fs := newFakeStore()
	fs.users["org-1"] = &models.User{UserID: "org-1", IsPrivate: false}
	project := &models.Project{ProjectID: "proj-1", UserID: strPtr("org-1"), IsPrivate: true}
	fs.projects["proj-1"] = project
	fs.assignments = []models.RoleAssignment{
		{PrincipalType: models.PrincipalTypeUser, PrincipalID: "assigned-user", ScopeType: models.ScopeTypeProject, ScopeID: strPtr("proj-1"), Role: models.RoleMember},
	}
	r := NewResolver(fs)
	ctx := context.Background()

	if ok, err := r.CanViewProject(ctx, AnonymousIdentity(), project); err != nil || ok {
		t.Fatalf("private project should be hidden from anonymous: %v, %v", ok, err)
	}
	if ok, err := r.CanViewProject(ctx, UserIdentity("stranger"), project); err != nil || ok {
		t.Fatalf("private project should be hidden from an unrelated logged-in user: %v, %v", ok, err)
	}
	if ok, err := r.CanViewProject(ctx, UserIdentity("assigned-user"), project); err != nil || !ok {
		t.Fatalf("private project should be visible to an assigned member: %v, %v", ok, err)
	}
	if ok, err := r.CanViewProject(ctx, UserIdentity("org-1"), project); err != nil || !ok {
		t.Fatalf("private project should be visible to its own org: %v, %v", ok, err)
	}
}

func TestCanViewProject_OrgPrivateCascade(t *testing.T) {
	fs := newFakeStore()
	// Project itself is not marked private, but its owning org (user) is —
	// IsEffectivelyPrivate should still treat it as private.
	fs.users["org-1"] = &models.User{UserID: "org-1", IsPrivate: true}
	project := &models.Project{ProjectID: "proj-1", UserID: strPtr("org-1"), IsPrivate: false}
	r := NewResolver(fs)
	ctx := context.Background()

	if ok, err := r.CanViewProject(ctx, AnonymousIdentity(), project); err != nil || ok {
		t.Fatalf("org-private cascade should hide the project from anonymous: %v, %v", ok, err)
	}
	if ok, err := r.CanViewProject(ctx, UserIdentity("stranger"), project); err != nil || ok {
		t.Fatalf("org-private cascade should hide the project from a stranger: %v, %v", ok, err)
	}
	if ok, err := r.CanViewProject(ctx, UserIdentity("org-1"), project); err != nil || !ok {
		t.Fatalf("org-private cascade should not hide the project from its own org: %v, %v", ok, err)
	}
}

func TestCanViewJob_NoProjectFallsBackToOwningOrg(t *testing.T) {
	fs := newFakeStore()
	fs.users["org-1"] = &models.User{UserID: "org-1", IsPrivate: false}
	publicJob := &models.Job{JobID: "job-public", UserID: "org-1"}
	r := NewResolver(fs)
	ctx := context.Background()

	if ok, err := r.CanViewJob(ctx, AnonymousIdentity(), publicJob); err != nil || !ok {
		t.Fatalf("loose job under a public org should be visible to anonymous: %v, %v", ok, err)
	}

	fs2 := newFakeStore()
	fs2.users["org-2"] = &models.User{UserID: "org-2", IsPrivate: true}
	privateJob := &models.Job{JobID: "job-private", UserID: "org-2"}
	r2 := NewResolver(fs2)

	if ok, err := r2.CanViewJob(ctx, AnonymousIdentity(), privateJob); err != nil || ok {
		t.Fatalf("loose job under a private org should be hidden from anonymous: %v, %v", ok, err)
	}
	if ok, err := r2.CanViewJob(ctx, UserIdentity("stranger"), privateJob); err != nil || ok {
		t.Fatalf("loose job under a private org should be hidden from a stranger: %v, %v", ok, err)
	}
	if ok, err := r2.CanViewJob(ctx, UserIdentity("org-2"), privateJob); err != nil || !ok {
		t.Fatalf("loose job under a private org should be visible to its own org: %v, %v", ok, err)
	}
	fs2.assignments = []models.RoleAssignment{
		{PrincipalType: models.PrincipalTypeUser, PrincipalID: "org-admin", ScopeType: models.ScopeTypeOrg, ScopeID: strPtr("org-2"), Role: models.RoleAdmin},
	}
	if ok, err := r2.CanViewJob(ctx, UserIdentity("org-admin"), privateJob); err != nil || !ok {
		t.Fatalf("loose job under a private org should be visible to an org admin: %v, %v", ok, err)
	}
}

func TestCanViewJob_WithProjectUsesProjectVisibility(t *testing.T) {
	fs := newFakeStore()
	fs.users["org-1"] = &models.User{UserID: "org-1", IsPrivate: false}
	fs.projects["proj-1"] = &models.Project{ProjectID: "proj-1", UserID: strPtr("org-1"), IsPrivate: true}
	fs.assignments = []models.RoleAssignment{
		{PrincipalType: models.PrincipalTypeUser, PrincipalID: "assigned-user", ScopeType: models.ScopeTypeProject, ScopeID: strPtr("proj-1"), Role: models.RoleMember},
	}
	job := &models.Job{JobID: "job-1", UserID: "job-submitter", ProjectID: strPtr("proj-1")}
	r := NewResolver(fs)
	ctx := context.Background()

	// Submitter isn't the project's org and has no assignment: denied even
	// though they submitted the job — visibility here follows the project,
	// not job.UserID, once a project is attached.
	if ok, err := r.CanViewJob(ctx, UserIdentity("job-submitter"), job); err != nil || ok {
		t.Fatalf("job in a private project should not be visible just because the caller submitted it: %v, %v", ok, err)
	}
	if ok, err := r.CanViewJob(ctx, UserIdentity("assigned-user"), job); err != nil || !ok {
		t.Fatalf("job in a private project should be visible to an assigned project member: %v, %v", ok, err)
	}
	if ok, err := r.CanViewJob(ctx, UserIdentity("org-1"), job); err != nil || !ok {
		t.Fatalf("job in a private project should be visible to the project's own org: %v, %v", ok, err)
	}
}

func TestFilterVisibleProjects_BatchesOwnerLookups(t *testing.T) {
	fs := newFakeStore()
	fs.users["org-1"] = &models.User{UserID: "org-1", IsPrivate: false}
	fs.users["org-2"] = &models.User{UserID: "org-2", IsPrivate: true}
	projects := []models.Project{
		{ProjectID: "p1", UserID: strPtr("org-1"), IsPrivate: false},
		{ProjectID: "p2", UserID: strPtr("org-1"), IsPrivate: false},
		{ProjectID: "p3", UserID: strPtr("org-1"), IsPrivate: false},
		{ProjectID: "p4", UserID: strPtr("org-2"), IsPrivate: false}, // hidden: org-2 is private
	}
	r := NewResolver(fs)

	visible, err := r.FilterVisibleProjects(context.Background(), AnonymousIdentity(), projects)
	if err != nil {
		t.Fatalf("FilterVisibleProjects: %v", err)
	}
	if len(visible) != 3 {
		t.Fatalf("expected 3 visible projects, got %d: %+v", len(visible), visible)
	}
	// Two distinct owners across 4 projects: GetUserByID should be called
	// at most twice, not once per project.
	if fs.getUserCalls > 2 {
		t.Fatalf("expected owner lookups to be batched (<=2 calls for 2 distinct owners), got %d", fs.getUserCalls)
	}
}

// --- NewProjectIsPrivate -------------------------------------------------

func TestNewProjectIsPrivate(t *testing.T) {
	fs := newFakeStore()
	if got := NewProjectIsPrivate(context.Background(), fs); got != false {
		t.Fatalf("absent setting should default to false, got %v", got)
	}

	fs.settings[models.GlobalSettingNewProjectsPrivate] = &models.GlobalSetting{
		Key:   models.GlobalSettingNewProjectsPrivate,
		Value: mustJSON(t, true),
	}
	if got := NewProjectIsPrivate(context.Background(), fs); got != true {
		t.Fatalf("setting=true should return true, got %v", got)
	}

	fs.settings[models.GlobalSettingNewProjectsPrivate] = &models.GlobalSetting{
		Key:   models.GlobalSettingNewProjectsPrivate,
		Value: mustJSON(t, false),
	}
	if got := NewProjectIsPrivate(context.Background(), fs); got != false {
		t.Fatalf("setting=false should return false, got %v", got)
	}
}

// --- Identity helpers ------------------------------------------------------

func TestIdentityFromUser(t *testing.T) {
	if id := IdentityFromUser(nil); !id.Anonymous {
		t.Fatalf("IdentityFromUser(nil) should be anonymous, got %+v", id)
	}
	if id := IdentityFromUser(&models.User{}); !id.Anonymous {
		t.Fatalf("IdentityFromUser(user with empty UserID) should be anonymous, got %+v", id)
	}
	id := IdentityFromUser(&models.User{UserID: "u1"})
	if id.Anonymous || id.UserID != "u1" {
		t.Fatalf("IdentityFromUser(u1) = %+v, want {Anonymous:false UserID:u1}", id)
	}
}
