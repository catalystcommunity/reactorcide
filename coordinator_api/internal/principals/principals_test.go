package principals

import (
	"context"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type fakeStore struct {
	principals map[string]models.Principal
	identities []models.PrincipalIdentity
	reads      int
	created    int
}

func (f *fakeStore) GetPrincipal(_ context.Context, id string) (*models.Principal, error) {
	f.reads++
	p, ok := f.principals[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &p, nil
}

func (f *fakeStore) ResolvePrincipalIdentity(ctx context.Context, identityType, value string) (*models.Principal, error) {
	f.reads++
	for _, identity := range f.identities {
		if identity.IdentityType == identityType && identity.IdentityValue == value {
			return f.GetPrincipal(ctx, identity.PrincipalID)
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) ListPrincipalIdentities(_ context.Context, id string) ([]models.PrincipalIdentity, error) {
	result := []models.PrincipalIdentity{}
	for _, identity := range f.identities {
		if identity.PrincipalID == id {
			result = append(result, identity)
		}
	}
	return result, nil
}

func (f *fakeStore) EnsureVCSPrincipal(_ context.Context, provider, login string) (*models.Principal, bool, error) {
	f.created++
	value := models.VCSIdentityValue(provider, login)
	p := models.Principal{PrincipalID: "new-" + value, Kind: models.PrincipalKindHuman, Name: value}
	f.principals[p.PrincipalID] = p
	f.identities = append(f.identities, models.PrincipalIdentity{IdentityType: "vcs", IdentityValue: value, PrincipalID: p.PrincipalID})
	return &p, true, nil
}

func newFake() *fakeStore {
	org := "org-1"
	return &fakeStore{
		principals: map[string]models.Principal{
			"alice": {PrincipalID: "alice", Kind: models.PrincipalKindHuman, Name: "alice"},
			"bot":   {PrincipalID: "bot", Kind: models.PrincipalKindService, Name: "deployer", OrgID: &org},
		},
		identities: []models.PrincipalIdentity{
			{IdentityType: "user", IdentityValue: "alice", PrincipalID: "alice"},
			{IdentityType: "vcs", IdentityValue: "github/alice", PrincipalID: "alice"},
			{IdentityType: "linkkeys", IdentityValue: "alice@example.com", PrincipalID: "alice"},
			{IdentityType: "token", IdentityValue: "tok-1", PrincipalID: "bot"},
		},
	}
}

func TestResolveReturnsLinkedIdentities(t *testing.T) {
	resolver := NewResolver(newFake(), 0)
	resolution, err := resolver.Resolve(context.Background(), "linkkeys", "Alice@Example.com")
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Principal.PrincipalID != "alice" || !resolution.HasIdentity("vcs", "github/alice") {
		t.Fatalf("unexpected resolution %+v", resolution)
	}
	if resolution.Identity != "linkkeys:alice@example.com" || resolution.UserID() != "alice" {
		t.Fatalf("identity %q user %q", resolution.Identity, resolution.UserID())
	}
}

func TestResolveOrCreateVCS(t *testing.T) {
	fake := newFake()
	resolver := NewResolver(fake, 0)
	resolution, err := resolver.ResolveOrCreateVCS(context.Background(), "github", "Newcomer")
	if err != nil || resolution.Principal.Kind != models.PrincipalKindHuman || fake.created != 1 {
		t.Fatalf("resolution %+v err %v created %d", resolution, err, fake.created)
	}
	if _, err := resolver.ResolveOrCreateVCS(context.Background(), "github", "newcomer"); err != nil || fake.created != 1 {
		t.Fatalf("a second sighting must not create: err %v created %d", err, fake.created)
	}
	existing, _ := resolver.ResolveOrCreateVCS(context.Background(), "github", "alice")
	if existing.Principal.PrincipalID != "alice" {
		t.Fatal("a known login resolves to its principal")
	}
}

func TestResolverCache(t *testing.T) {
	fake := newFake()
	resolver := NewResolver(fake, time.Minute)
	now := time.Now()
	resolver.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if _, err := resolver.Resolve(context.Background(), "vcs", "github/alice"); err != nil {
			t.Fatal(err)
		}
	}
	if fake.reads != 2 {
		t.Fatalf("cached lookups read the store %d times", fake.reads)
	}
	now = now.Add(2 * time.Minute)
	_, _ = resolver.Resolve(context.Background(), "vcs", "github/alice")
	if fake.reads != 4 {
		t.Fatalf("an expired entry must read again, reads %d", fake.reads)
	}
	resolver.Invalidate()
	_, _ = resolver.Resolve(context.Background(), "vcs", "github/alice")
	if fake.reads != 6 {
		t.Fatalf("Invalidate must drop the cache, reads %d", fake.reads)
	}
}

func TestSubjects(t *testing.T) {
	resolver := NewResolver(newFake(), 0)
	alice, _ := resolver.ResolveID(context.Background(), "alice")
	got := Subjects(alice, SubjectContext{OrgID: "org-1", OrgName: "acme"})
	want := map[string]bool{"principal:alice": true, "principal_kind:human": true, "vcs_user:github/alice": true,
		"identity:linkkeys:alice@example.com": true}
	if len(got) != len(want) {
		t.Fatalf("subjects %v", got)
	}
	for _, subject := range got {
		if !want[subject] {
			t.Fatalf("unexpected subject %q in %v", subject, got)
		}
	}
	bot, _ := resolver.ResolveID(context.Background(), "bot")
	inOrg := Subjects(bot, SubjectContext{OrgID: "org-1", OrgName: "acme"})
	if !contains(inOrg, "service:deployer") || !contains(inOrg, "service:acme/deployer") {
		t.Fatalf("service subjects in its org: %v", inOrg)
	}
	elsewhere := Subjects(bot, SubjectContext{OrgID: "org-2", OrgName: "other"})
	if contains(elsewhere, "service:deployer") {
		t.Fatal("a service principal has no service subject in another organization")
	}
}

func TestActors(t *testing.T) {
	user := &models.User{UserID: "alice"}
	if actor := FromCredential(nil, user); actor.PrincipalID != "alice" || actor.Identity != "user:alice" {
		t.Fatalf("session actor %+v", actor)
	}
	token := &checkauth.Principal{CredentialID: "tok-1", CredentialType: "service_token", PrincipalID: "bot"}
	if actor := FromCredential(token, nil); actor.PrincipalID != "bot" || actor.Identity != "token:tok-1" {
		t.Fatalf("token actor %+v", actor)
	}
	jobToken := &checkauth.Principal{CredentialID: "tok-2", CredentialType: "job_token"}
	if actor := FromCredential(jobToken, nil); !actor.IsZero() {
		t.Fatalf("a job token acts for its job, got %+v", actor)
	}
	ctx := WithActor(checkauth.SetPrincipalContext(context.Background(), token), System())
	if ActorOf(ctx).PrincipalID != SystemPrincipalID {
		t.Fatal("an explicit actor wins over the credential")
	}
	job := &models.Job{}
	ApplyToJob(job, System())
	ApplyToJob(job, FromCredential(token, nil))
	if *job.ActorPrincipalID != SystemPrincipalID {
		t.Fatal("ApplyToJob must not replace an actor")
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// A disabled principal (a disabled user, a disabled service principal, or
// the end of a merge chain that is disabled) grants nothing. Its
// identities stay for display.
func TestDisabledPrincipalGrantsNothing(t *testing.T) {
	fake := newFake()
	alice := fake.principals["alice"]
	alice.Status = models.PrincipalStatusDisabled
	fake.principals["alice"] = alice
	bot := fake.principals["bot"]
	bot.Status = models.PrincipalStatusDisabled
	fake.principals["bot"] = bot
	resolver := NewResolver(fake, time.Minute)
	scope := SubjectContext{OrgID: "org-1", OrgName: "acme"}

	for _, lookup := range []func() (*Resolution, error){
		func() (*Resolution, error) { return resolver.Resolve(context.Background(), "vcs", "github/alice") },
		func() (*Resolution, error) { return resolver.ResolveID(context.Background(), "alice") },
		func() (*Resolution, error) {
			return resolver.ResolveOrCreateVCS(context.Background(), "github", "alice")
		},
		func() (*Resolution, error) { return resolver.ResolveID(context.Background(), "bot") },
		// A second read comes from the cache.
		func() (*Resolution, error) { return resolver.ResolveID(context.Background(), "bot") },
	} {
		resolution, err := lookup()
		if err != nil {
			t.Fatal(err)
		}
		if !resolution.Inactive || resolution.UserID() != "" || len(Subjects(resolution, scope)) != 0 {
			t.Fatalf("disabled principal %s: inactive=%v user=%q subjects=%v", resolution.Principal.PrincipalID,
				resolution.Inactive, resolution.UserID(), Subjects(resolution, scope))
		}
		if len(resolution.Identities) == 0 {
			t.Fatal("the identities stay for display")
		}
	}
	active, _ := NewResolver(newFake(), 0).ResolveID(context.Background(), "alice")
	if active.Inactive || active.UserID() != "alice" || len(Subjects(active, scope)) == 0 {
		t.Fatal("an active principal keeps its subjects")
	}
}
