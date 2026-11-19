package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// principalWebhookStore adds an in-memory principal store to the event
// webhook store, so the webhook path resolves the sender to a principal.
type principalWebhookStore struct {
	*eventWebhookStore
	principals map[string]models.Principal
	identities map[string]models.PrincipalIdentity
	groups     map[string][]models.Group
}

func newPrincipalWebhookStore() *principalWebhookStore {
	return &principalWebhookStore{eventWebhookStore: &eventWebhookStore{WebhookMockStore: &WebhookMockStore{}},
		principals: map[string]models.Principal{}, identities: map[string]models.PrincipalIdentity{}, groups: map[string][]models.Group{}}
}

func (s *principalWebhookStore) GetPrincipal(_ context.Context, id string) (*models.Principal, error) {
	p, ok := s.principals[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &p, nil
}

func (s *principalWebhookStore) ResolvePrincipalIdentity(ctx context.Context, identityType, value string) (*models.Principal, error) {
	identity, ok := s.identities[identityType+":"+value]
	if !ok {
		return nil, store.ErrNotFound
	}
	return s.GetPrincipal(ctx, identity.PrincipalID)
}

func (s *principalWebhookStore) ListPrincipalIdentities(_ context.Context, id string) ([]models.PrincipalIdentity, error) {
	out := []models.PrincipalIdentity{}
	for _, identity := range s.identities {
		if identity.PrincipalID == id {
			out = append(out, identity)
		}
	}
	return out, nil
}

func (s *principalWebhookStore) EnsureVCSPrincipal(_ context.Context, provider, login string) (*models.Principal, bool, error) {
	value := models.VCSIdentityValue(provider, login)
	id := "11111111-1111-1111-1111-" + "000000000001"
	p := models.Principal{PrincipalID: id, Kind: models.PrincipalKindHuman, Name: value, DisplayName: login}
	s.principals[id] = p
	s.identities["vcs:"+value] = models.PrincipalIdentity{IdentityType: "vcs", IdentityValue: value, PrincipalID: id}
	return &p, true, nil
}

func (s *principalWebhookStore) ListGroupsForPrincipal(_ context.Context, id string) ([]models.Group, error) {
	return s.groups[id], nil
}

func TestWebhookRecordsSenderPrincipal(t *testing.T) {
	st := newPrincipalWebhookStore()
	project := webhookTestProject()
	st.GetProjectByRepoURLFunc = func(context.Context, string) (*models.Project, error) { return project, nil }
	handler := newEventWebhookHandler(t, st.eventWebhookStore, prWebhookEvent())
	handler.store = st
	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "abc123", "feature", "main", 42)

	deliverWebhook(t, handler, "pull_request", "delivery-actor", body)

	resolved, err := st.ResolvePrincipalIdentity(context.Background(), "vcs", "github/dev")
	require.NoError(t, err, "a signed webhook makes the sender a principal")
	require.Len(t, st.events, 1)
	event := st.events[0]
	require.NotNil(t, event.ActorPrincipalID)
	assert.Equal(t, resolved.PrincipalID, *event.ActorPrincipalID)
	assert.Equal(t, "vcs:github/dev", event.ActorIdentity)
	require.Len(t, st.CreateJobCalls, 1)
	job := st.CreateJobCalls[0]
	require.NotNil(t, job.ActorPrincipalID)
	assert.Equal(t, resolved.PrincipalID, *job.ActorPrincipalID)
	var subjects []string
	require.NoError(t, json.Unmarshal([]byte(job.JobEnvVars["REACTORCIDE_ACTOR_SUBJECTS"].(string)), &subjects))
	assert.Contains(t, subjects, "vcs_user:github/dev")
	assert.Contains(t, subjects, "principal:"+resolved.PrincipalID)
	assert.Contains(t, subjects, "principal_kind:human")
}

func TestCoordinatorEventActorIsTheStarter(t *testing.T) {
	st := newPrincipalWebhookStore()
	handler := NewWebhookHandler(st, nil)
	starter := principals.Actor{PrincipalID: "22222222-2222-2222-2222-222222222222", Identity: "vcs:github/approver"}
	event := prWebhookEvent()
	event.Coordinator = &vcs.CoordinatorEvent{Source: models.CIEventSourceRerun, ID: "x",
		ActorPrincipalID: starter.PrincipalID, ActorIdentity: starter.Identity}
	assert.Equal(t, starter, handler.webhookActor(context.Background(), event),
		"a rerun records the re-runner, not the earlier sender")
	event.Coordinator.ActorPrincipalID = ""
	assert.True(t, handler.webhookActor(context.Background(), event).IsZero(),
		"with no starter the database gives the system principal")
}

func TestApprovalPersonGetsPrincipalSubjects(t *testing.T) {
	st := newPrincipalWebhookStore()
	userID := "33333333-3333-3333-3333-333333333333"
	st.principals[userID] = models.Principal{PrincipalID: userID, Kind: models.PrincipalKindHuman, Name: "alice"}
	st.identities["user:"+userID] = models.PrincipalIdentity{IdentityType: "user", IdentityValue: userID, PrincipalID: userID}
	st.identities["vcs:github/alice"] = models.PrincipalIdentity{IdentityType: "vcs", IdentityValue: "github/alice", PrincipalID: userID}
	st.identities["linkkeys:alice@example.com"] = models.PrincipalIdentity{IdentityType: "linkkeys", IdentityValue: "alice@example.com", PrincipalID: userID}
	project := webhookTestProject()
	st.groups[userID] = []models.Group{{GroupID: "g", OrgID: project.OwnershipOrgID(), Name: "release"}}
	handler := NewWebhookHandler(st, nil)
	target := &prTarget{Provider: vcs.GitHub, Repo: "test-org/test-repo", Client: &MockVCSClient{}}

	// A UI session of the user: the login comes from the principal's linked
	// VCS identity, and the LinkKeys identity is a subject too.
	person, err := handler.resolvePerson(context.Background(), project, target, "", userID, false)
	require.NoError(t, err)
	assert.Equal(t, "github/alice", person.Person)
	assert.Contains(t, person.Subjects, "identity:linkkeys:alice@example.com")
	assert.Contains(t, person.Subjects, "principal:"+userID)
	assert.Contains(t, person.Subjects, "reactorcide_group:release")
	assert.True(t, person.has([]string{"*"}))
	assert.False(t, person.has(nil), "an omitted subject list never means anyone")
	assert.Equal(t, userID, person.actor().PrincipalID)
}

func TestScheduleFireActorIsTheSystemPrincipal(t *testing.T) {
	st, firer := newFirer(t, &branchHeadClient{MockVCSClient: &MockVCSClient{}, sha: "feedface1234567"})
	_, err := firer.Fire(context.Background(), testSchedule(st.project), time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, st.events, 1)
	require.NotNil(t, st.events[0].ActorPrincipalID)
	assert.Equal(t, models.SystemPrincipalID, *st.events[0].ActorPrincipalID)
	assert.Equal(t, models.SystemActorIdentity, st.events[0].ActorIdentity)
	require.Len(t, st.CreateJobCalls, 1)
	require.NotNil(t, st.CreateJobCalls[0].ActorPrincipalID)
	assert.Equal(t, models.SystemPrincipalID, *st.CreateJobCalls[0].ActorPrincipalID)
}
