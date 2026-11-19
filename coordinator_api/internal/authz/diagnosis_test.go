package authz

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

func canPost(t *testing.T, resolver *Resolver, id Identity, projectID *string) bool {
	t.Helper()
	ok, err := resolver.CanPostDiagnosis(context.Background(), id, "org-a", projectID)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// narrowed returns the identity of a service principal token after the
// auth-time intersection with its roles, as the middleware does.
func narrowed(t *testing.T, resolver *Resolver, token *checkauth.Principal) Identity {
	t.Helper()
	if err := resolver.NarrowServicePrincipalToken(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	return IdentityFromPrincipal(token, nil)
}

func TestCanPostDiagnosisServicePrincipal(t *testing.T) {
	org := "org-a"
	project := "proj-a"
	fake := newPrincipalFake()
	resolver := NewResolver(fake)

	// No role: the token has no scope after narrowing, also with the
	// capability.
	if canPost(t, resolver, narrowed(t, resolver, serviceToken(tokencaps.DiagnosisWrite, tokencaps.LogsRead)), &project) {
		t.Fatal("a service principal with no role cannot post")
	}

	fake.assignments = append(fake.assignments, models.RoleAssignment{PrincipalType: models.PrincipalTypePrincipal,
		PrincipalID: "svc", ScopeType: models.ScopeTypeOrg, ScopeID: &org, Role: models.RoleMember})

	member := serviceToken(tokencaps.DiagnosisWrite, tokencaps.LogsRead, tokencaps.JobsRead, tokencaps.JobsSubmit)
	id := narrowed(t, resolver, member)
	if !member.HasCapability(tokencaps.DiagnosisWrite) || member.HasCapability(tokencaps.JobsSubmit) {
		t.Fatalf("a member keeps diagnosis:write and the read capabilities only: %+v", member.Capabilities)
	}
	if !member.HasCapability(tokencaps.LogsRead) {
		t.Fatal("a member keeps logs:read, so the tool can read the logs of the jobs in its org")
	}
	if !canPost(t, resolver, id, &project) {
		t.Fatal("a member role with diagnosis:write can post")
	}
	if ok, _ := resolver.CanPostDiagnosis(context.Background(), id, "org-b", nil); ok {
		t.Fatal("a role in org-a gives nothing in org-b")
	}

	without := narrowed(t, resolver, serviceToken(tokencaps.LogsRead, tokencaps.JobsRead))
	if canPost(t, resolver, without, &project) {
		t.Fatal("a member role without diagnosis:write cannot post")
	}
}

func TestCanPostDiagnosisOtherCallers(t *testing.T) {
	project := "proj-a"
	fake := newPrincipalFake()
	resolver := NewResolver(fake)

	if canPost(t, resolver, AnonymousIdentity(), &project) {
		t.Fatal("an anonymous caller never posts, also on a public project")
	}

	set, _ := tokencaps.New(tokencaps.DiagnosisWrite)
	job := &checkauth.Principal{CredentialType: "job_token", OrganizationIDs: []string{"org-a"}, Capabilities: set, BoundJobID: "job-1"}
	if canPost(t, resolver, IdentityFromPrincipal(job, nil), &project) {
		t.Fatal("a job token never posts")
	}

	legacy := &checkauth.Principal{CredentialType: "service_token", OrganizationIDs: []string{"org-a"}, Capabilities: set, PrincipalID: "svc"}
	if !canPost(t, resolver, IdentityFromPrincipal(legacy, nil), &project) {
		t.Fatal("a token with no live roles has its capabilities")
	}
	noCap, _ := tokencaps.New(tokencaps.JobsRead)
	legacyRead := &checkauth.Principal{CredentialType: "service_token", OrganizationIDs: []string{"org-a"}, Capabilities: noCap}
	if canPost(t, resolver, IdentityFromPrincipal(legacyRead, nil), &project) {
		t.Fatal("a token without diagnosis:write cannot post")
	}
	admin := &checkauth.Principal{CredentialType: "instance_token", AllOrganizations: true, AllCapabilities: true}
	if !canPost(t, resolver, IdentityFromPrincipal(admin, nil), nil) {
		t.Fatal("the admin token can post, so the feature works with no logins")
	}

	// A user session: a project role in the project is enough; no role is
	// not.
	user := &models.User{UserID: "user-1", Status: "active"}
	fake.users[user.UserID] = user
	if canPost(t, resolver, IdentityFromUser(user), &project) {
		t.Fatal("a user with no role cannot post")
	}
	fake.assignments = append(fake.assignments, models.RoleAssignment{PrincipalType: models.PrincipalTypeUser,
		PrincipalID: "user-1", ScopeType: models.ScopeTypeProject, ScopeID: &project, Role: models.RoleMember})
	if !canPost(t, resolver, IdentityFromUser(user), &project) {
		t.Fatal("a project member can post on the project")
	}
	other := "proj-b"
	if canPost(t, resolver, IdentityFromUser(user), &other) {
		t.Fatal("a project role gives nothing in another project")
	}

	// A user token narrows the role: without diagnosis:write, no post.
	userToken := &checkauth.Principal{CredentialType: "user_token", UserID: "user-1", OrganizationIDs: []string{"org-a"}, Capabilities: noCap}
	if canPost(t, resolver, IdentityFromPrincipal(userToken, user), &project) {
		t.Fatal("a user token without diagnosis:write cannot post")
	}
	userToken.Capabilities = set
	if !canPost(t, resolver, IdentityFromPrincipal(userToken, user), &project) {
		t.Fatal("a user token with diagnosis:write and a project role can post")
	}
}
