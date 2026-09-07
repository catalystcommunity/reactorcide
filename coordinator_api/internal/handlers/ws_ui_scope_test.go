package handlers

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// scopeFakeStore is the smallest store that satisfies scopeStore,
// authz.RoleStore and the optional scopeOrganizationLookup, so the
// cache-population path orgAllows can be driven against organizations rows.
type scopeFakeStore struct {
	users       map[string]*models.User
	orgs        map[string]*models.Organization
	assignments []models.RoleAssignment
}

func (f *scopeFakeStore) GetProjectByID(context.Context, string) (*models.Project, error) {
	return nil, store.ErrNotFound
}
func (f *scopeFakeStore) GetJobByID(context.Context, string) (*models.Job, error) {
	return nil, store.ErrNotFound
}
func (f *scopeFakeStore) GetUserByID(_ context.Context, userID string) (*models.User, error) {
	if u, ok := f.users[userID]; ok {
		return u, nil
	}
	return nil, store.ErrNotFound
}
func (f *scopeFakeStore) GetOrganizationByID(_ context.Context, orgID string) (*models.Organization, error) {
	if o, ok := f.orgs[orgID]; ok {
		return o, nil
	}
	return nil, store.ErrNotFound
}
func (f *scopeFakeStore) ListGroupsForUser(context.Context, string) ([]models.Group, error) {
	return nil, nil
}
func (f *scopeFakeStore) ListRoleAssignmentsForPrincipal(_ context.Context, userID string, _ []string) ([]models.RoleAssignment, error) {
	var out []models.RoleAssignment
	for _, a := range f.assignments {
		if a.PrincipalType == models.PrincipalTypeUser && a.PrincipalID == userID {
			out = append(out, a)
		}
	}
	return out, nil
}

func scopeWithStore(identity authz.Identity, fs *scopeFakeStore) *uiStreamScope {
	scope := scopeFor(identity, false)
	scope.store = fs
	scope.resolver = authz.NewResolver(fs)
	return scope
}

// scopeFor builds a scope with pre-seeded caches, so these tests exercise the
// decision logic in canSee without needing a store. The cache-population paths
// (projectAllows/orgAllows/jobAllows) delegate to authz.Resolver, which has its
// own tests in internal/authz.
func scopeFor(identity authz.Identity, globalAdmin bool) *uiStreamScope {
	return &uiStreamScope{
		identity:       identity,
		globalAdmin:    globalAdmin,
		projectVisible: map[string]bool{},
		orgVisible:     map[string]bool{},
		jobVisible:     map[string]bool{},
	}
}

// TestScopeDropsUnauthorizableEvent is the fail-closed property. An event with
// no ownership and no job to resolve it from must not be broadcast. Getting
// this backwards would send every such frame to every connected browser.
func TestScopeDropsUnauthorizableEvent(t *testing.T) {
	scope := scopeFor(authz.AnonymousIdentity(), false)

	if scope.canSee(pubsub.Event{Type: pubsub.EventJobUpdate}) {
		t.Error("an event carrying no ownership and no job id must be dropped, not broadcast")
	}
}

func TestScopeHidesPrivateProjectEventsFromAnonymous(t *testing.T) {
	scope := scopeFor(authz.AnonymousIdentity(), false)
	scope.projectVisible["proj-public"] = true
	scope.projectVisible["proj-private"] = false

	public := pubsub.Event{
		Type: pubsub.EventJobUpdate, JobID: "j1",
		ProjectID: "proj-public", OwnerUserID: "org-1",
	}
	private := pubsub.Event{
		Type: pubsub.EventJobUpdate, JobID: "j2",
		ProjectID: "proj-private", OwnerUserID: "org-1",
	}

	if !scope.canSee(public) {
		t.Error("a public project's event should reach an anonymous caller")
	}
	if scope.canSee(private) {
		t.Error("a private project's event must NOT reach an anonymous caller")
	}
}

// TestScopeAnonymousIsNotTreatedAsTheEmptyOwner guards a specific mistake: an
// anonymous identity has UserID "", and an event whose OwnerUserID is somehow
// also "" must not match it as "the owner".
func TestScopeAnonymousIsNotTreatedAsTheEmptyOwner(t *testing.T) {
	scope := scopeFor(authz.AnonymousIdentity(), false)

	evt := pubsub.Event{Type: pubsub.EventJobUpdate, JobID: "j1", ProjectID: "proj-private"}
	scope.projectVisible["proj-private"] = false

	if scope.canSee(evt) {
		t.Error("an anonymous caller must not match an empty owner id as ownership")
	}
}

func TestScopeOwnerSeesOwnEvents(t *testing.T) {
	scope := scopeFor(authz.UserIdentity("org-1"), false)
	// Deliberately cached as NOT visible: ownership must win on its own,
	// without consulting the project at all.
	scope.projectVisible["proj-private"] = false

	evt := pubsub.Event{
		Type: pubsub.EventJobUpdate, JobID: "j1",
		ProjectID: "proj-private", OwnerUserID: "org-1",
	}
	if !scope.canSee(evt) {
		t.Error("the owning org should see its own private project's events")
	}
}

func TestScopeGlobalAdminSeesEverything(t *testing.T) {
	scope := scopeFor(authz.UserIdentity("admin-1"), true)
	scope.projectVisible["proj-private"] = false

	evt := pubsub.Event{
		Type: pubsub.EventJobUpdate, JobID: "j1",
		ProjectID: "proj-private", OwnerUserID: "org-1",
	}
	if !scope.canSee(evt) {
		t.Error("a global admin should see every event")
	}
}

// TestScopeProjectUpdateInvalidatesCachedVisibility is the reason
// EventProjectUpdate exists. A project flipped to private must stop feeding an
// ALREADY OPEN socket, not merely the next one to connect.
func TestScopeProjectUpdateInvalidatesCachedVisibility(t *testing.T) {
	scope := scopeFor(authz.AnonymousIdentity(), false)
	scope.projectVisible["proj-1"] = true

	update := pubsub.Event{Type: pubsub.EventProjectUpdate, ProjectID: "proj-1", OwnerUserID: "org-1"}
	scope.canSee(update)

	if _, cached := scope.projectVisible["proj-1"]; cached {
		t.Error("a project_update must drop the cached visibility answer for that project")
	}
}

// TestScopePrivateOrgEventFollowsOrganizationsRow drives orgAllows for a
// project-less event whose OwnerUserID is a first-class org id: the org's
// privacy comes from organizations.is_private (there is no users row for it),
// and its admin — resolved by an org-scoped role assignment on that id — sees
// the event while a stranger and an anonymous caller do not.
func TestScopePrivateOrgEventFollowsOrganizationsRow(t *testing.T) {
	orgID := "org-1"
	fs := &scopeFakeStore{
		users: map[string]*models.User{},
		orgs:  map[string]*models.Organization{orgID: {OrgID: orgID, Name: "org-one", IsPrivate: true}},
		assignments: []models.RoleAssignment{
			{PrincipalType: models.PrincipalTypeUser, PrincipalID: "admin-1", ScopeType: models.ScopeTypeOrg, ScopeID: &orgID, Role: models.RoleAdmin},
		},
	}
	evt := pubsub.Event{Type: pubsub.EventJobUpdate, JobID: "j1", OwnerUserID: orgID}

	if scopeWithStore(authz.UserIdentity("stranger-1"), fs).canSee(evt) {
		t.Error("a private org's event must NOT reach a stranger")
	}
	if scopeWithStore(authz.AnonymousIdentity(), fs).canSee(evt) {
		t.Error("a private org's event must NOT reach an anonymous caller")
	}
	if !scopeWithStore(authz.UserIdentity("admin-1"), fs).canSee(evt) {
		t.Error("a private org's event should reach that org's admin")
	}

	// Flip the organizations row public: everyone sees it, with no users row
	// consulted at all.
	fs.orgs[orgID].IsPrivate = false
	if !scopeWithStore(authz.UserIdentity("stranger-1"), fs).canSee(evt) {
		t.Error("a public org's event should reach a stranger")
	}
}

// TestScopeOrgPrivacyFallsBackToLegacyUserRow keeps the pre-organizations
// shape working: an owner id with no organizations row but a users row
// (a login-provisioned user acting as its own org) still reads users.is_private.
func TestScopeOrgPrivacyFallsBackToLegacyUserRow(t *testing.T) {
	fs := &scopeFakeStore{
		users: map[string]*models.User{"user-org": {UserID: "user-org", IsPrivate: true}},
		orgs:  map[string]*models.Organization{},
	}
	evt := pubsub.Event{Type: pubsub.EventJobUpdate, JobID: "j1", OwnerUserID: "user-org"}

	if scopeWithStore(authz.UserIdentity("stranger-1"), fs).canSee(evt) {
		t.Error("a private legacy user-org's event must NOT reach a stranger")
	}
	if !scopeWithStore(authz.UserIdentity("user-org"), fs).canSee(evt) {
		t.Error("the legacy user-org must see its own event")
	}
	// Unknown owner everywhere: fail closed.
	unknown := pubsub.Event{Type: pubsub.EventJobUpdate, JobID: "j2", OwnerUserID: "nobody"}
	if scopeWithStore(authz.UserIdentity("stranger-1"), fs).canSee(unknown) {
		t.Error("an owner with neither an organizations nor a users row must not be visible")
	}
}

func TestTopicMatching(t *testing.T) {
	jobEvent := pubsub.Event{Type: pubsub.EventJobUpdate, JobID: "j1", WorkflowID: "wf1", ProjectID: "p1"}
	workflowEvent := pubsub.Event{Type: pubsub.EventWorkflowUpdate, WorkflowID: "wf1", ProjectID: "p1"}
	nodeEvent := pubsub.Event{Type: pubsub.EventWorkflowNodeUpdate, WorkflowID: "wf1", NodeName: "build"}

	cases := []struct {
		topic uiTopic
		evt   pubsub.Event
		want  bool
		why   string
	}{
		{"jobs", jobEvent, true, "the jobs topic takes job events"},
		{"jobs", workflowEvent, false, "the jobs topic must not take workflow events"},
		{"workflows", workflowEvent, true, "the workflows topic takes workflow events"},
		{"workflow:wf1", jobEvent, true, "a workflow topic covers its jobs, so a detail page needs one subscription"},
		{"workflow:wf1", nodeEvent, true, "a workflow topic covers its nodes, which is what animates the DAG"},
		{"workflow:wf2", jobEvent, false, "a different workflow's topic must not match"},
		{"job:j1", jobEvent, true, "a job topic matches its own job"},
		{"job:j2", jobEvent, false, "a different job's topic must not match"},
		{"project:p1", jobEvent, true, "a project topic matches events in that project"},
		{"nonsense", jobEvent, false, "an unrecognized topic matches nothing"},
	}
	for _, tc := range cases {
		if got := topicMatches(tc.topic, tc.evt); got != tc.want {
			t.Errorf("topicMatches(%q, %s) = %v, want %v: %s", tc.topic, tc.evt.Type, got, tc.want, tc.why)
		}
	}
}

// TestEmptyTopicSetReceivesNothing pins that a connection which has not said
// what it is showing does not get a firehose.
func TestEmptyTopicSetReceivesNothing(t *testing.T) {
	topics := newTopicSet(nil)
	if topics.matches(pubsub.Event{Type: pubsub.EventJobUpdate, JobID: "j1"}) {
		t.Error("a connection with no subscriptions must receive nothing")
	}
}

func TestTopicSetApplyAndUnsubscribe(t *testing.T) {
	topics := newTopicSet(nil)
	topics.apply(uiClientMessage{Subscribe: []string{"jobs", "workflow:wf1"}})

	evt := pubsub.Event{Type: pubsub.EventJobUpdate, JobID: "j1"}
	if !topics.matches(evt) {
		t.Fatal("subscribed topic should match")
	}

	topics.apply(uiClientMessage{Unsubscribe: []string{"jobs"}})
	if topics.matches(evt) {
		t.Error("an unsubscribed topic must stop matching; leaking this is the memory/bandwidth leak the SPA store guards against")
	}
}

func TestTopicSetIsBounded(t *testing.T) {
	topics := newTopicSet(nil)
	many := make([]string, maxTopicsPerConnection+50)
	for i := range many {
		many[i] = "job:" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	topics.apply(uiClientMessage{Subscribe: many})

	topics.mu.RLock()
	count := len(topics.topics)
	topics.mu.RUnlock()
	if count > maxTopicsPerConnection {
		t.Errorf("topic count = %d, must be capped at %d", count, maxTopicsPerConnection)
	}
}

// TestJobTopicsAreRetainedAndReleased pins the cross-replica subscription that
// per-job events depend on. log_available and metrics_available are published
// on per-job NOTIFY channels, so a "job:<id>" subscription must retain that
// job's topic — otherwise the listener never LISTENs for it and a live log tail
// receives nothing — and must hand it back on unsubscribe or close.
func TestJobTopicsAreRetainedAndReleased(t *testing.T) {
	retained := map[string]int{}
	retain := func(jobID string) func() {
		retained[jobID]++
		return func() { retained[jobID]-- }
	}

	topics := newTopicSet(retain)
	topics.apply(uiClientMessage{Subscribe: []string{"jobs", "job:j1", "workflow:wf1"}})
	if retained["j1"] != 1 {
		t.Fatalf("retained[j1] = %d, want 1: a job topic must be activated on subscribe", retained["j1"])
	}

	// A reconnect re-declares the whole set; that must not double-retain.
	topics.apply(uiClientMessage{Subscribe: []string{"jobs", "job:j1"}})
	if retained["j1"] != 1 {
		t.Errorf("retained[j1] = %d after a re-declare, want 1", retained["j1"])
	}

	topics.apply(uiClientMessage{Unsubscribe: []string{"job:j1"}})
	if retained["j1"] != 0 {
		t.Errorf("retained[j1] = %d after unsubscribe, want 0", retained["j1"])
	}

	topics.apply(uiClientMessage{Subscribe: []string{"job:j2"}})
	topics.releaseAll()
	if retained["j2"] != 0 {
		t.Errorf("retained[j2] = %d after releaseAll, want 0: a closed socket must not pin a topic", retained["j2"])
	}
}
