package authz

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// principalFakeStore adds service principals to fakeStore.
type principalFakeStore struct {
	*fakeStore
	principals        map[string]*models.Principal
	groupsByPrincipal map[string][]models.Group
}

func (f *principalFakeStore) GetPrincipal(ctx context.Context, id string) (*models.Principal, error) {
	p, ok := f.principals[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return p, nil
}

func (f *principalFakeStore) ListGroupsForPrincipal(ctx context.Context, id string) ([]models.Group, error) {
	return f.groupsByPrincipal[id], nil
}

func (f *principalFakeStore) ListRoleAssignmentsForPrincipalID(ctx context.Context, id string, groupIDs []string) ([]models.RoleAssignment, error) {
	groups := map[string]bool{}
	for _, g := range groupIDs {
		groups[g] = true
	}
	var out []models.RoleAssignment
	for _, a := range f.assignments {
		if (a.PrincipalType == models.PrincipalTypePrincipal && a.PrincipalID == id) ||
			(a.PrincipalType == models.PrincipalTypeGroup && groups[a.PrincipalID]) {
			out = append(out, a)
		}
	}
	return out, nil
}

func newPrincipalFake() *principalFakeStore {
	org := "org-a"
	return &principalFakeStore{
		fakeStore: newFakeStore(),
		principals: map[string]*models.Principal{
			"svc": {PrincipalID: "svc", Kind: models.PrincipalKindService, Name: "deployer", OrgID: &org, Status: "active"},
		},
		groupsByPrincipal: map[string][]models.Group{},
	}
}

func serviceToken(capabilities ...string) *checkauth.Principal {
	set, _ := tokencaps.New(capabilities...)
	return &checkauth.Principal{CredentialID: "tok", CredentialType: "service_token", OwnerOrgID: "org-a",
		OrganizationIDs: []string{"org-a"}, Capabilities: set, PrincipalID: "svc", IntersectRoles: true}
}

func TestServicePrincipalTokenWithoutRolesHasNothing(t *testing.T) {
	fake := newPrincipalFake()
	resolver := NewResolver(fake)
	token := serviceToken(tokencaps.JobsSubmit, tokencaps.OrganizationsManage)
	if err := resolver.NarrowServicePrincipalToken(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if token.HasOrganization("org-a") || token.HasCapability(tokencaps.JobsSubmit) {
		t.Fatalf("a principal with no roles keeps no scope: %+v", token)
	}
	id := IdentityFromPrincipal(serviceToken(tokencaps.OrganizationsManage), nil)
	if id.PrincipalID != "svc" {
		t.Fatal("a service principal token identity carries the principal")
	}
	admin, err := resolver.IsOrgAdmin(context.Background(), id, "org-a")
	if err != nil || admin {
		t.Fatalf("no role means no org admin (admin=%v err=%v)", admin, err)
	}
}

func TestServicePrincipalTokenIntersectsRoles(t *testing.T) {
	fake := newPrincipalFake()
	org := "org-a"
	fake.assignments = append(fake.assignments, models.RoleAssignment{PrincipalType: models.PrincipalTypePrincipal,
		PrincipalID: "svc", ScopeType: models.ScopeTypeOrg, ScopeID: &org, Role: models.RoleMember})
	resolver := NewResolver(fake)

	member := serviceToken(tokencaps.JobsSubmit, tokencaps.JobsRead)
	if err := resolver.NarrowServicePrincipalToken(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	if !member.HasOrganization("org-a") || !member.HasCapability(tokencaps.JobsRead) || member.HasCapability(tokencaps.JobsSubmit) {
		t.Fatalf("a member keeps read capabilities only: %+v", member)
	}

	// An admin role through a group keeps the issued capabilities, never more.
	fake.groupsByPrincipal["svc"] = []models.Group{{GroupID: "g1", OrgID: "org-a", Name: "admins"}}
	fake.assignments = append(fake.assignments, models.RoleAssignment{PrincipalType: models.PrincipalTypeGroup,
		PrincipalID: "g1", ScopeType: models.ScopeTypeOrg, ScopeID: &org, Role: models.RoleAdmin})
	admin := serviceToken(tokencaps.JobsSubmit)
	if err := resolver.NarrowServicePrincipalToken(context.Background(), admin); err != nil {
		t.Fatal(err)
	}
	if !admin.HasCapability(tokencaps.JobsSubmit) || admin.HasCapability(tokencaps.SecretsManage) {
		t.Fatalf("an admin keeps exactly the issued capabilities: %+v", admin)
	}
	id := IdentityFromPrincipal(serviceToken(tokencaps.OrganizationsManage), nil)
	ok, err := resolver.IsOrgAdmin(context.Background(), id, "org-a")
	if err != nil || !ok {
		t.Fatalf("token capability and admin role together make an org admin (ok=%v err=%v)", ok, err)
	}
	noCapability := IdentityFromPrincipal(serviceToken(tokencaps.JobsRead), nil)
	if ok, _ := resolver.IsOrgAdmin(context.Background(), noCapability, "org-a"); ok {
		t.Fatal("the role alone is not enough without the token capability")
	}
	if ok, _ := resolver.IsOrgAdmin(context.Background(), id, "org-b"); ok {
		t.Fatal("a role in one org gives nothing in another")
	}
}

func TestDisabledServicePrincipalTokenIsRejected(t *testing.T) {
	fake := newPrincipalFake()
	fake.principals["svc"].Status = models.PrincipalStatusDisabled
	if err := NewResolver(fake).NarrowServicePrincipalToken(context.Background(), serviceToken(tokencaps.JobsRead)); err == nil {
		t.Fatal("a disabled principal must invalidate its token")
	}
}

func TestLegacyServiceTokenIsNotNarrowed(t *testing.T) {
	fake := newPrincipalFake()
	token := serviceToken(tokencaps.JobsSubmit)
	token.IntersectRoles = false
	if err := NewResolver(fake).NarrowServicePrincipalToken(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if !token.HasCapability(tokencaps.JobsSubmit) || !token.HasOrganization("org-a") {
		t.Fatal("a token made before principals keeps its authority")
	}
	if id := IdentityFromPrincipal(token, nil); id.PrincipalID != "" {
		t.Fatal("a legacy token uses the token-only path")
	}
}
