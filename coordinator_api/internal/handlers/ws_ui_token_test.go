package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// streamTokenStore gives identityFor API tokens and UI sessions.
type streamTokenStore struct {
	*MockStore
	tokens   map[string]models.APIToken
	users    map[string]*models.User
	sessions map[string]*models.UISession
}

func (s *streamTokenStore) ValidateAPIToken(_ context.Context, token string) (*models.APIToken, *models.User, error) {
	apiToken, ok := s.tokens[token]
	if !ok {
		return nil, nil, store.ErrNotFound
	}
	return &apiToken, s.users[apiToken.UserID], nil
}

func (s *streamTokenStore) GetUserByID(_ context.Context, userID string) (*models.User, error) {
	if user, ok := s.users[userID]; ok {
		return user, nil
	}
	return nil, store.ErrNotFound
}

func (s *streamTokenStore) CreateUISession(_ context.Context, session *models.UISession) error {
	session.SessionID = "session-1"
	s.sessions[string(session.TokenHash)] = session
	return nil
}

func (s *streamTokenStore) GetActiveUISessionByTokenHash(_ context.Context, tokenHash []byte) (*models.UISession, error) {
	for key, session := range s.sessions {
		if bytes.Equal([]byte(key), tokenHash) {
			return session, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *streamTokenStore) TouchUISessionLastSeen(context.Context, string) error { return nil }
func (s *streamTokenStore) RevokeUISession(context.Context, string) error        { return nil }

func newStreamTokenStore() *streamTokenStore {
	return &streamTokenStore{MockStore: &MockStore{}, tokens: map[string]models.APIToken{},
		users:    map[string]*models.User{"user-1": {UserID: "user-1", Username: "u"}},
		sessions: map[string]*models.UISession{}}
}

func streamRequest(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, UIStreamPath, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// TestUIStreamIdentity: the stream accepts a session, an API token, or no
// credential (anonymous). It refuses a token that is neither.
func TestUIStreamIdentity(t *testing.T) {
	st := newStreamTokenStore()
	st.tokens["api-token"] = models.APIToken{TokenID: "t-1", SubjectType: "user_token", UserID: "user-1",
		OrganizationIDs: []string{"org-1"}, Capabilities: []string{tokencaps.JobsRead}}
	h := &UIStreamHandler{store: st, sessions: auth.NewSessions(st)}
	sessionToken, err := h.sessions.MintSession(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("MintSession: %v", err)
	}

	id, ok := h.identityFor(streamRequest(sessionToken))
	if !ok || id.UserID != "user-1" || id.Token != nil {
		t.Fatalf("session: identity %+v ok %v, want the user with no token", id, ok)
	}
	id, ok = h.identityFor(streamRequest("api-token"))
	if !ok || id.UserID != "user-1" || id.Token == nil || !id.Token.HasCapability(tokencaps.JobsRead) {
		t.Fatalf("api token: identity %+v ok %v, want the user with the token", id, ok)
	}
	id, ok = h.identityFor(streamRequest(""))
	if !ok || !id.Anonymous {
		t.Fatalf("no credential: identity %+v ok %v, want anonymous", id, ok)
	}
	if _, ok := h.identityFor(streamRequest("not-a-token")); ok {
		t.Fatal("an invalid token must be refused")
	}
}

// TestUIStreamRefusesInvalidToken: the handshake answers 401.
func TestUIStreamRefusesInvalidToken(t *testing.T) {
	st := newStreamTokenStore()
	h := &UIStreamHandler{store: st, sessions: auth.NewSessions(st), resolver: authz.NewResolver(&scopeFakeStore{})}
	rr := httptest.NewRecorder()
	h.Stream(rr, streamRequest("not-a-token"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rr.Code)
	}
}

// TestScopeTokenGetsOnlyEventsItCanRead: a token gets an event only with the
// read capability of the event type for the organization of the event.
func TestScopeTokenGetsOnlyEventsItCanRead(t *testing.T) {
	tokenFor := func(orgs []string, capabilities ...string) authz.Identity {
		set, _ := tokencaps.New(capabilities...)
		return authz.Identity{UserID: "user-1", Token: &checkauth.Principal{CredentialType: "user_token", UserID: "user-1",
			OrganizationIDs: orgs, Capabilities: set}}
	}
	job := pubsub.Event{Type: pubsub.EventJobUpdate, JobID: "j1", ProjectID: "proj-1", OwnerUserID: "org-1"}
	log := pubsub.Event{Type: pubsub.EventLogAvailable, JobID: "j1", ProjectID: "proj-1", OwnerUserID: "org-1"}
	workflow := pubsub.Event{Type: pubsub.EventWorkflowUpdate, ProjectID: "proj-1", OwnerUserID: "org-1"}

	scope := scopeFor(tokenFor([]string{"org-1"}, tokencaps.JobsRead), false)
	scope.projectVisible["proj-1"] = true
	if !scope.canSee(job) {
		t.Error("a jobs:read token for the org must get the job event")
	}
	if scope.canSee(log) {
		t.Error("a token without logs:read must not get the log event")
	}
	if scope.canSee(workflow) {
		t.Error("a token without workflows:read must not get the workflow event")
	}

	none := scopeFor(tokenFor([]string{"org-1"}, tokencaps.WorkersRead), false)
	none.projectVisible["proj-1"] = true
	if none.canSee(job) || none.canSee(log) || none.canSee(workflow) {
		t.Error("a token without a read capability must get no event")
	}

	other := scopeFor(tokenFor([]string{"org-2"}, tokencaps.JobsRead, tokencaps.LogsRead, tokencaps.WorkflowsRead), true)
	other.projectVisible["proj-1"] = true
	if other.canSee(job) || other.canSee(log) || other.canSee(workflow) {
		t.Error("a token for another org must get no event, even for a global admin")
	}

	// The token is intersected with the role: a private project that the
	// owner cannot see stays hidden.
	private := scopeFor(tokenFor([]string{"org-1"}, tokencaps.JobsRead), false)
	private.projectVisible["proj-1"] = false
	if private.canSee(job) {
		t.Error("a token must not get an event its owner cannot see")
	}

	// The session of the same user is not limited by any token.
	session := scopeFor(authz.UserIdentity("user-1"), false)
	session.projectVisible["proj-1"] = true
	if !session.canSee(job) || !session.canSee(log) || !session.canSee(workflow) {
		t.Error("the session must get the events of a visible project")
	}
}
