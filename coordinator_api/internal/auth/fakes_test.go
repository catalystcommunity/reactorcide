package auth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// fakeStore is an in-memory implementation of LoginServiceStore (and, by
// extension, AdmissionStore/LoginAttemptStore/UserProvisionStore/
// SessionStore individually), following this repo's convention of
// consumer-defined narrow interfaces backed by hand-rolled fakes in tests
// (no real network/DB — see AGENTS.md, "For Go code, inspect nearby package
// tests").
type fakeStore struct {
	mu sync.Mutex

	trustedIdentities map[string]models.AuthTrustedIdentity // key: domain+"\x00"+handle
	trustedPatterns   []models.AuthTrustedDomainPattern

	loginAttempts map[string]models.AuthLoginAttempt // key: string(attemptHash)

	authIdentitiesBySubject map[string]models.AuthIdentity
	users                   map[string]models.User
	roleAssignments         []models.RoleAssignment

	sessions map[string]models.UISession // key: string(tokenHash)

	nextID int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		trustedIdentities:       map[string]models.AuthTrustedIdentity{},
		authIdentitiesBySubject: map[string]models.AuthIdentity{},
		users:                   map[string]models.User{},
		loginAttempts:           map[string]models.AuthLoginAttempt{},
		sessions:                map[string]models.UISession{},
	}
}

func (f *fakeStore) genID(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%d", prefix, f.nextID)
}

// --- AdmissionStore ---------------------------------------------------

func (f *fakeStore) TrustedIdentityExists(_ context.Context, domain, handle string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.trustedIdentities[domain+"\x00"+handle]; ok {
		return true, nil
	}
	if _, ok := f.trustedIdentities[domain+"\x00"]; ok {
		return true, nil
	}
	return false, nil
}

func (f *fakeStore) ListTrustedDomainPatterns(_ context.Context) ([]models.AuthTrustedDomainPattern, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.AuthTrustedDomainPattern, len(f.trustedPatterns))
	copy(out, f.trustedPatterns)
	return out, nil
}

func (f *fakeStore) UpsertTrustedIdentity(_ context.Context, identity *models.AuthTrustedIdentity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trustedIdentities[identity.Domain+"\x00"+identity.Handle] = *identity
	return nil
}

// --- LoginAttemptStore --------------------------------------------------

func (f *fakeStore) CreateLoginAttempt(_ context.Context, attempt *models.AuthLoginAttempt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginAttempts[string(attempt.AttemptHash)] = *attempt
	return nil
}

func (f *fakeStore) ConsumeLoginAttempt(_ context.Context, attemptHash []byte) (*models.AuthLoginAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	attempt, ok := f.loginAttempts[string(attemptHash)]
	if !ok {
		return nil, store.ErrNotFound
	}
	delete(f.loginAttempts, string(attemptHash))
	return &attempt, nil
}

// --- UserProvisionStore ---------------------------------------------------

func (f *fakeStore) GetAuthIdentityBySubject(_ context.Context, subject string) (*models.AuthIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	identity, ok := f.authIdentitiesBySubject[subject]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &identity, nil
}

func (f *fakeStore) CreateAuthIdentity(_ context.Context, identity *models.AuthIdentity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if identity.IdentityID == "" {
		identity.IdentityID = f.genID("identity")
	}
	f.authIdentitiesBySubject[identity.Subject] = *identity
	return nil
}

func (f *fakeStore) UpdateAuthIdentityLogin(_ context.Context, identityID string, displayName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for subject, identity := range f.authIdentitiesBySubject {
		if identity.IdentityID == identityID {
			identity.DisplayName = displayName
			now := fakeNow()
			identity.LastLoginAt = &now
			f.authIdentitiesBySubject[subject] = identity
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) CreateUser(_ context.Context, user *models.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if user.UserID == "" {
		user.UserID = f.genID("user")
	}
	f.users[user.UserID] = *user
	return nil
}

func (f *fakeStore) GetUserByID(_ context.Context, userID string) (*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	user, ok := f.users[userID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &user, nil
}

func (f *fakeStore) ListRoleAssignmentsByScope(_ context.Context, scopeType string, scopeID *string) ([]models.RoleAssignment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.RoleAssignment
	for _, a := range f.roleAssignments {
		if a.ScopeType != scopeType {
			continue
		}
		if scopeID == nil || *scopeID == "" {
			if a.ScopeID == nil {
				out = append(out, a)
			}
			continue
		}
		if a.ScopeID != nil && *a.ScopeID == *scopeID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateRoleAssignment(_ context.Context, assignment *models.RoleAssignment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if assignment.AssignmentID == "" {
		assignment.AssignmentID = f.genID("assignment")
	}
	f.roleAssignments = append(f.roleAssignments, *assignment)
	return nil
}

// --- SessionStore ---------------------------------------------------------

func (f *fakeStore) CreateUISession(_ context.Context, session *models.UISession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if session.SessionID == "" {
		session.SessionID = f.genID("session")
	}
	f.sessions[string(session.TokenHash)] = *session
	return nil
}

func (f *fakeStore) GetActiveUISessionByTokenHash(_ context.Context, tokenHash []byte) (*models.UISession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[string(tokenHash)]
	if !ok {
		return nil, store.ErrNotFound
	}
	if session.IsRevoked() || session.IsExpired() {
		return nil, store.ErrNotFound
	}
	return &session, nil
}

func (f *fakeStore) TouchUISessionLastSeen(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for hash, session := range f.sessions {
		if session.SessionID == sessionID {
			session.LastSeenAt = fakeNow()
			f.sessions[hash] = session
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) RevokeUISession(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for hash, session := range f.sessions {
		if session.SessionID == sessionID {
			now := fakeNow()
			session.RevokedAt = &now
			f.sessions[hash] = session
			return nil
		}
	}
	return store.ErrNotFound
}

// fakeNow is a var (rather than a direct time.Now() call at each site) so a
// future test could override it; today it's just real wall-clock time.
var fakeNow = time.Now
