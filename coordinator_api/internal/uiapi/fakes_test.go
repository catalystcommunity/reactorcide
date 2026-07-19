package uiapi

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// fakeStore is an in-memory implementation of uiapi.DataStore, following
// this repo's convention of consumer-defined narrow interfaces backed by
// hand-rolled fakes in tests (see internal/auth/fakes_test.go, which this
// mirrors and extends to the much larger surface Task G's DataStore needs:
// projects, jobs, workflows, groups, role assignments, sessions, login
// attempts, auth identities, trusted identities/domain patterns, global
// settings, webhook-secret/vcs-credential rotation, and secret grants). No
// real network/DB — every op test in this package drives the real
// AuthService/UiService implementations against this fake rather than a
// live Postgres.
type fakeStore struct {
	mu     sync.Mutex
	nextID int

	users     map[string]models.User
	projects  map[string]models.Project
	jobs      map[string]models.Job
	workflows map[string]models.WorkflowInstance
	nodes     map[string][]models.WorkflowNode
	events    []models.WorkflowEvent

	groups       map[string]models.Group
	groupMembers map[string]map[string]bool // groupID -> set of userID
	assignments  []models.RoleAssignment

	sessions                map[string]models.UISession // key: string(tokenHash)
	loginAttempts           map[string]models.AuthLoginAttempt
	authIdentitiesBySubject map[string]models.AuthIdentity
	authIdentitiesByUser    map[string]models.AuthIdentity

	trustedIdentities map[string]models.AuthTrustedIdentity // key: domain+"\x00"+handle
	trustedPatterns   []models.AuthTrustedDomainPattern

	globalSettings map[string]models.GlobalSetting

	webhookSecrets map[string]models.ProjectWebhookSecret
	vcsCreds       map[string]models.ProjectVCSCredential

	secretGrants map[string]models.SecretGrant

	authCredentials map[string]models.AuthCredential
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:                   map[string]models.User{},
		projects:                map[string]models.Project{},
		jobs:                    map[string]models.Job{},
		workflows:               map[string]models.WorkflowInstance{},
		nodes:                   map[string][]models.WorkflowNode{},
		groups:                  map[string]models.Group{},
		groupMembers:            map[string]map[string]bool{},
		sessions:                map[string]models.UISession{},
		loginAttempts:           map[string]models.AuthLoginAttempt{},
		authIdentitiesBySubject: map[string]models.AuthIdentity{},
		authIdentitiesByUser:    map[string]models.AuthIdentity{},
		trustedIdentities:       map[string]models.AuthTrustedIdentity{},
		globalSettings:          map[string]models.GlobalSetting{},
		webhookSecrets:          map[string]models.ProjectWebhookSecret{},
		vcsCreds:                map[string]models.ProjectVCSCredential{},
		secretGrants:            map[string]models.SecretGrant{},
		authCredentials:         map[string]models.AuthCredential{},
	}
}

func (f *fakeStore) genID(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%d", prefix, f.nextID)
}

// --- test-only helpers (not part of DataStore) -----------------------------

// putUser inserts/replaces a user row directly, for test setup.
func (f *fakeStore) putUser(u models.User) models.User {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u.UserID == "" {
		u.UserID = f.genID("user")
	}
	f.users[u.UserID] = u
	return u
}

func (f *fakeStore) putProject(p models.Project) models.Project {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.ProjectID == "" {
		p.ProjectID = f.genID("project")
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = fakeNow()
	}
	p.UpdatedAt = fakeNow()
	f.projects[p.ProjectID] = p
	return p
}

func (f *fakeStore) putJob(j models.Job) models.Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j.JobID == "" {
		j.JobID = f.genID("job")
	}
	f.jobs[j.JobID] = j
	return j
}

func (f *fakeStore) putWorkflow(w models.WorkflowInstance) models.WorkflowInstance {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w.WorkflowID == "" {
		w.WorkflowID = f.genID("workflow")
	}
	f.workflows[w.WorkflowID] = w
	return w
}

// grantRole directly inserts a role_assignments row, for test setup (bypasses
// authorization — tests use this to establish the roles they're then testing
// authorization *against*).
func (f *fakeStore) grantRole(principalType, principalID, scopeType string, scopeID *string, role string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assignments = append(f.assignments, models.RoleAssignment{
		AssignmentID:  f.genID("assignment"),
		PrincipalType: principalType,
		PrincipalID:   principalID,
		ScopeType:     scopeType,
		ScopeID:       scopeID,
		Role:          role,
		CreatedAt:     fakeNow(),
	})
}

// --- store.Store -------------------------------------------------------

func (f *fakeStore) Initialize() (func(), error) { return func() {}, nil }

func (f *fakeStore) CreateProject(_ context.Context, project *models.Project) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if project.ProjectID == "" {
		project.ProjectID = f.genID("project")
	}
	project.CreatedAt = fakeNow()
	project.UpdatedAt = fakeNow()
	f.projects[project.ProjectID] = *project
	return nil
}

func (f *fakeStore) GetProjectByID(_ context.Context, projectID string) (*models.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[projectID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &p, nil
}

func (f *fakeStore) GetProjectByRepoURL(_ context.Context, repoURL string) (*models.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.projects {
		if p.RepoURL == repoURL {
			return &p, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) UpdateProject(_ context.Context, project *models.Project) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[project.ProjectID]; !ok {
		return store.ErrNotFound
	}
	project.UpdatedAt = fakeNow()
	f.projects[project.ProjectID] = *project
	return nil
}

func (f *fakeStore) DeleteProject(_ context.Context, projectID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[projectID]; !ok {
		return store.ErrNotFound
	}
	delete(f.projects, projectID)
	return nil
}

func (f *fakeStore) ListProjects(_ context.Context, limit, offset int) ([]models.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.Project, 0, len(f.projects))
	for _, p := range f.projects {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeStore) ListProjectsByOrg(_ context.Context, orgID string, limit, offset int) ([]models.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.Project, 0)
	for _, p := range f.projects {
		if p.UserID != nil && *p.UserID == orgID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) GetJobsByUser(_ context.Context, userID string, limit, offset int) ([]models.Job, error) {
	return nil, nil
}

func (f *fakeStore) GetJobByID(_ context.Context, jobID string) (*models.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[jobID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &j, nil
}

func (f *fakeStore) CreateJob(_ context.Context, job *models.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if job.JobID == "" {
		job.JobID = f.genID("job")
	}
	f.jobs[job.JobID] = *job
	return nil
}

func (f *fakeStore) UpdateJob(_ context.Context, job *models.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.jobs[job.JobID]; !ok {
		return store.ErrNotFound
	}
	f.jobs[job.JobID] = *job
	return nil
}

func (f *fakeStore) DeleteJob(_ context.Context, jobID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.jobs, jobID)
	return nil
}

func (f *fakeStore) ListJobs(_ context.Context, filters map[string]interface{}, limit, offset int) ([]models.Job, error) {
	return nil, nil
}

func (f *fakeStore) ListJobsForPRCommit(_ context.Context, repo string, prNumber int, commitSHA string) ([]models.Job, error) {
	return nil, nil
}

func (f *fakeStore) ListJobsForPR(_ context.Context, repo string, prNumber int) ([]models.Job, error) {
	return nil, nil
}

func (f *fakeStore) ForPRCommit(ctx context.Context, repo string, prNumber int, commitSHA string, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

func (f *fakeStore) IsPRMerged(_ context.Context, repo string, prNumber int) (bool, error) {
	return false, nil
}

func (f *fakeStore) MarkPRMerged(_ context.Context, repo string, prNumber int) error { return nil }

func (f *fakeStore) ValidateAPIToken(_ context.Context, token string) (*models.APIToken, *models.User, error) {
	return nil, nil, store.ErrNotFound
}

func (f *fakeStore) CreateAPIToken(_ context.Context, apiToken *models.APIToken) error { return nil }

func (f *fakeStore) UpdateTokenLastUsed(_ context.Context, tokenID string, lastUsed time.Time) error {
	return nil
}

func (f *fakeStore) GetAPITokensByUser(_ context.Context, userID string) ([]models.APIToken, error) {
	return nil, nil
}

func (f *fakeStore) DeleteAPIToken(_ context.Context, tokenID string) error { return nil }

func (f *fakeStore) GetUserByID(_ context.Context, userID string) (*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[userID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &u, nil
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

func (f *fakeStore) EnsureDefaultUser() error { return nil }

// --- authz.RoleStore (ListGroupsForUser/ListRoleAssignmentsForPrincipal) ---

func (f *fakeStore) ListGroupsForUser(_ context.Context, userID string) ([]models.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.Group
	for groupID, members := range f.groupMembers {
		if members[userID] {
			if g, ok := f.groups[groupID]; ok {
				out = append(out, g)
			}
		}
	}
	return out, nil
}

func (f *fakeStore) ListRoleAssignmentsForPrincipal(_ context.Context, userID string, groupIDs []string) ([]models.RoleAssignment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	groupSet := map[string]bool{}
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

// --- auth.AdmissionStore -------------------------------------------------

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
	if identity.CreatedAt.IsZero() {
		identity.CreatedAt = fakeNow()
	}
	f.trustedIdentities[identity.Domain+"\x00"+identity.Handle] = *identity
	return nil
}

func (f *fakeStore) ListTrustedIdentities(_ context.Context) ([]models.AuthTrustedIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.AuthTrustedIdentity, 0, len(f.trustedIdentities))
	for _, t := range f.trustedIdentities {
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeStore) DeleteTrustedIdentity(_ context.Context, domain, handle string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := domain + "\x00" + handle
	if _, ok := f.trustedIdentities[key]; !ok {
		return store.ErrNotFound
	}
	delete(f.trustedIdentities, key)
	return nil
}

func (f *fakeStore) CreateTrustedDomainPattern(_ context.Context, pattern *models.AuthTrustedDomainPattern) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pattern.PatternID == "" {
		pattern.PatternID = f.genID("pattern")
	}
	pattern.CreatedAt = fakeNow()
	f.trustedPatterns = append(f.trustedPatterns, *pattern)
	return nil
}

func (f *fakeStore) DeleteTrustedDomainPattern(_ context.Context, patternID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.trustedPatterns {
		if p.PatternID == patternID {
			f.trustedPatterns = append(f.trustedPatterns[:i], f.trustedPatterns[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

// --- auth.LoginAttemptStore -----------------------------------------------

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

// --- auth.UserProvisionStore -----------------------------------------------

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
	f.authIdentitiesByUser[identity.UserID] = *identity
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
			f.authIdentitiesByUser[identity.UserID] = identity
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) GetAuthIdentityByUserID(_ context.Context, userID string) (*models.AuthIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	identity, ok := f.authIdentitiesByUser[userID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &identity, nil
}

func (f *fakeStore) ListRoleAssignmentsByScope(_ context.Context, scopeType string, scopeID *string) ([]models.RoleAssignment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.RoleAssignment
	for _, a := range f.assignments {
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
	assignment.CreatedAt = fakeNow()
	f.assignments = append(f.assignments, *assignment)
	return nil
}

func (f *fakeStore) DeleteRoleAssignment(_ context.Context, assignmentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.assignments {
		if a.AssignmentID == assignmentID {
			f.assignments = append(f.assignments[:i], f.assignments[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) GetRoleAssignmentByID(_ context.Context, assignmentID string) (*models.RoleAssignment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.assignments {
		if a.AssignmentID == assignmentID {
			cp := a
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}

// --- auth.SessionStore ------------------------------------------------------

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

// --- auth.CredentialStore ---------------------------------------------------

func (f *fakeStore) UpsertAuthCredential(_ context.Context, credential *models.AuthCredential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authCredentials[credential.Name] = *credential
	return nil
}

func (f *fakeStore) GetAuthCredential(_ context.Context, name string) (*models.AuthCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.authCredentials[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &c, nil
}

func (f *fakeStore) GetDB() *gorm.DB { return nil }

// --- groups / group_members -------------------------------------------------

func (f *fakeStore) CreateGroup(_ context.Context, group *models.Group) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if group.GroupID == "" {
		group.GroupID = f.genID("group")
	}
	group.CreatedAt = fakeNow()
	group.UpdatedAt = fakeNow()
	f.groups[group.GroupID] = *group
	return nil
}

func (f *fakeStore) GetGroupByID(_ context.Context, groupID string) (*models.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.groups[groupID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &g, nil
}

func (f *fakeStore) ListGroupsByOrg(_ context.Context, orgID string) ([]models.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.Group
	for _, g := range f.groups {
		if g.OrgID == orgID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateGroup(_ context.Context, group *models.Group) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.groups[group.GroupID]; !ok {
		return store.ErrNotFound
	}
	group.UpdatedAt = fakeNow()
	f.groups[group.GroupID] = *group
	return nil
}

func (f *fakeStore) DeleteGroup(_ context.Context, groupID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.groups[groupID]; !ok {
		return store.ErrNotFound
	}
	delete(f.groups, groupID)
	delete(f.groupMembers, groupID)
	return nil
}

func (f *fakeStore) AddGroupMember(_ context.Context, groupID, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.groupMembers[groupID] == nil {
		f.groupMembers[groupID] = map[string]bool{}
	}
	f.groupMembers[groupID][userID] = true
	return nil
}

func (f *fakeStore) RemoveGroupMember(_ context.Context, groupID, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.groupMembers[groupID] == nil || !f.groupMembers[groupID][userID] {
		return store.ErrNotFound
	}
	delete(f.groupMembers[groupID], userID)
	return nil
}

func (f *fakeStore) ListGroupMembers(_ context.Context, groupID string) ([]models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.User
	for userID := range f.groupMembers[groupID] {
		if u, ok := f.users[userID]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

// --- project_webhook_secrets / project_vcs_credentials ----------------------

func (f *fakeStore) CreateProjectWebhookSecret(_ context.Context, secret *models.ProjectWebhookSecret) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.webhookSecrets {
		if existing.ProjectID == secret.ProjectID && existing.Provider == secret.Provider && existing.Name == secret.Name {
			return fmt.Errorf("duplicate webhook secret")
		}
	}
	if secret.ID == "" {
		secret.ID = f.genID("whsecret")
	}
	secret.CreatedAt = fakeNow()
	f.webhookSecrets[secret.ID] = *secret
	return nil
}

func (f *fakeStore) ListProjectWebhookSecrets(_ context.Context, projectID string, provider *string) ([]models.ProjectWebhookSecret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.ProjectWebhookSecret
	for _, r := range f.webhookSecrets {
		if r.ProjectID != projectID {
			continue
		}
		if provider != nil && *provider != "" && r.Provider != *provider {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// DeactivateProjectWebhookSecret mirrors the real store's idempotent
// semantics (internal/store/postgres_store/rotation_operations.go): a row
// that's already inactive is a no-op that preserves its original
// DeactivatedAt, not a fresh re-stamp. Only a genuinely missing row is
// store.ErrNotFound.
func (f *fakeStore) DeactivateProjectWebhookSecret(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.webhookSecrets[id]
	if !ok {
		return store.ErrNotFound
	}
	if !r.IsActive {
		return nil
	}
	now := fakeNow()
	r.IsActive = false
	r.DeactivatedAt = &now
	f.webhookSecrets[id] = r
	return nil
}

func (f *fakeStore) DeleteProjectWebhookSecret(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.webhookSecrets[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.webhookSecrets, id)
	return nil
}

func (f *fakeStore) GetProjectWebhookSecretByID(_ context.Context, id string) (*models.ProjectWebhookSecret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.webhookSecrets[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &r, nil
}

func (f *fakeStore) CreateProjectVCSCredential(_ context.Context, cred *models.ProjectVCSCredential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.vcsCreds {
		if existing.ProjectID == cred.ProjectID && existing.Provider == cred.Provider && existing.Name == cred.Name {
			return fmt.Errorf("duplicate vcs credential")
		}
	}
	if cred.ID == "" {
		cred.ID = f.genID("vcscred")
	}
	cred.CreatedAt = fakeNow()
	f.vcsCreds[cred.ID] = *cred
	return nil
}

func (f *fakeStore) ListProjectVCSCredentials(_ context.Context, projectID string, provider *string) ([]models.ProjectVCSCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.ProjectVCSCredential
	for _, r := range f.vcsCreds {
		if r.ProjectID != projectID {
			continue
		}
		if provider != nil && *provider != "" && r.Provider != *provider {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// DeactivateProjectVCSCredential mirrors DeactivateProjectWebhookSecret's
// idempotent-preserving-original-timestamp semantics; see that method's doc
// comment.
func (f *fakeStore) DeactivateProjectVCSCredential(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.vcsCreds[id]
	if !ok {
		return store.ErrNotFound
	}
	if !r.IsActive {
		return nil
	}
	now := fakeNow()
	r.IsActive = false
	r.DeactivatedAt = &now
	f.vcsCreds[id] = r
	return nil
}

func (f *fakeStore) DeleteProjectVCSCredential(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.vcsCreds[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.vcsCreds, id)
	return nil
}

func (f *fakeStore) GetProjectVCSCredentialByID(_ context.Context, id string) (*models.ProjectVCSCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.vcsCreds[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &r, nil
}

// --- global_settings ---------------------------------------------------

func (f *fakeStore) GetGlobalSetting(_ context.Context, key string) (*models.GlobalSetting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.globalSettings[key]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &s, nil
}

func (f *fakeStore) SetGlobalSetting(_ context.Context, key string, value models.JSONValue) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.globalSettings[key] = models.GlobalSetting{Key: key, Value: value, UpdatedAt: fakeNow()}
	return nil
}

func (f *fakeStore) ListGlobalSettings(_ context.Context) ([]models.GlobalSetting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.GlobalSetting, 0, len(f.globalSettings))
	for _, s := range f.globalSettings {
		out = append(out, s)
	}
	return out, nil
}

// --- secret_grants -----------------------------------------------------

func (f *fakeStore) CreateSecretGrant(_ context.Context, grant *models.SecretGrant) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if grant.GrantID == "" {
		grant.GrantID = f.genID("grant")
	}
	grant.CreatedAt = fakeNow()
	grant.UpdatedAt = fakeNow()
	f.secretGrants[grant.GrantID] = *grant
	return nil
}

func (f *fakeStore) ListSecretGrantsByOrg(_ context.Context, orgID string) ([]models.SecretGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.SecretGrant
	for _, g := range f.secretGrants {
		if g.UserID == orgID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateSecretGrant(_ context.Context, grant *models.SecretGrant) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.secretGrants[grant.GrantID]; !ok {
		return store.ErrNotFound
	}
	grant.UpdatedAt = fakeNow()
	f.secretGrants[grant.GrantID] = *grant
	return nil
}

func (f *fakeStore) DeleteSecretGrant(_ context.Context, userID string, projectID *string, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, g := range f.secretGrants {
		if g.UserID != userID {
			continue
		}
		if id == ref || g.Name == ref {
			delete(f.secretGrants, id)
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) GetSecretGrantByID(_ context.Context, grantID string) (*models.SecretGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.secretGrants[grantID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &g, nil
}

// --- workflows (workflowInstanceGetter / jobcontrol's workflowControlStore,
// workflowRetryStore, and worker.TriggerProcessor's own unexported
// workflowStore interface — RetryWorkflow drives EvaluateWorkflow, so this
// fake must satisfy that full structural interface too, same as
// jobcontrol's retryMockStore in internal/jobcontrol/retry_test.go) --------

func (f *fakeStore) CreateWorkflowInstance(_ context.Context, wf *models.WorkflowInstance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if wf.WorkflowID == "" {
		wf.WorkflowID = f.genID("workflow")
	}
	f.workflows[wf.WorkflowID] = *wf
	return nil
}

func (f *fakeStore) GetWorkflowInstance(_ context.Context, workflowID string) (*models.WorkflowInstance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workflows[workflowID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &w, nil
}

func (f *fakeStore) UpdateWorkflowInstance(_ context.Context, wf *models.WorkflowInstance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workflows[wf.WorkflowID] = *wf
	return nil
}

func (f *fakeStore) CreateWorkflowNode(_ context.Context, node *models.WorkflowNode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if node.NodeID == "" {
		node.NodeID = f.genID("node")
	}
	f.nodes[node.WorkflowID] = append(f.nodes[node.WorkflowID], *node)
	return nil
}

// GetWorkflowVars/UpsertWorkflowVar/ListWorkflowEvents are unused by any
// retry-op test today (every fixture retries a workflow with either zero
// nodes or nodes whose JobSpec needs no var substitution), but
// worker.TriggerProcessor.EvaluateWorkflow type-asserts its store onto the
// full workflowStore interface structurally, so fakeStore must implement
// them for that assertion to succeed at all.
func (f *fakeStore) GetWorkflowVars(_ context.Context, workflowID string) (map[string]models.JSONB, error) {
	return map[string]models.JSONB{}, nil
}

func (f *fakeStore) UpsertWorkflowVar(_ context.Context, v *models.WorkflowVar) error {
	return nil
}

func (f *fakeStore) ListWorkflowEvents(_ context.Context, workflowID string, limit, offset int) ([]models.WorkflowEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.WorkflowEvent
	for _, e := range f.events {
		if e.WorkflowID == workflowID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) ListWorkflowNodes(_ context.Context, workflowID string) ([]models.WorkflowNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.WorkflowNode, len(f.nodes[workflowID]))
	copy(out, f.nodes[workflowID])
	return out, nil
}

func (f *fakeStore) UpdateWorkflowNode(_ context.Context, node *models.WorkflowNode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	nodes := f.nodes[node.WorkflowID]
	for i := range nodes {
		if nodes[i].NodeID == node.NodeID {
			nodes[i] = *node
			f.nodes[node.WorkflowID] = nodes
			return nil
		}
	}
	return store.ErrNotFound
}

// GetWorkflowNodeByJobID satisfies jobcontrol's workflowControlStore, which
// added this method for the retry feature's node-rebind lookup (see
// internal/jobcontrol/retry.go's rebindWorkflowNodeForRetry) — every store
// jobcontrol.CancelWorkflow also runs against must implement it now, even
// though CancelWorkflow itself never calls it.
func (f *fakeStore) GetWorkflowNodeByJobID(_ context.Context, jobID string) (*models.WorkflowNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, nodes := range f.nodes {
		for i := range nodes {
			if nodes[i].JobID != nil && *nodes[i].JobID == jobID {
				n := nodes[i]
				return &n, nil
			}
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) CreateWorkflowEvent(_ context.Context, event *models.WorkflowEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if event.EventID == "" {
		event.EventID = f.genID("event")
	}
	f.events = append(f.events, *event)
	return nil
}

var _ DataStore = (*fakeStore)(nil)

// fakeNow is a var (rather than a direct time.Now() call at each site) so a
// future test could override it; today it's just real wall-clock time.
var fakeNow = time.Now

// --- fake secrets.Provider ---------------------------------------------

// fakeSecretsProvider is an in-memory secrets.Provider (see
// internal/secrets/provider.go's interface): no filesystem, no DB. Tests
// wire it into a *Deps via deps.SecretsProvider so rotation/secret-value ops
// are exercisable without a live Postgres — see Deps.SecretsProvider's doc
// comment for why that field exists.
type fakeSecretsProvider struct {
	mu   sync.Mutex
	data map[string]map[string]string // path -> key -> value
}

func newFakeSecretsProvider() *fakeSecretsProvider {
	return &fakeSecretsProvider{data: map[string]map[string]string{}}
}

func (p *fakeSecretsProvider) Get(_ context.Context, path, key string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.data[path][key], nil
}

func (p *fakeSecretsProvider) Set(_ context.Context, path, key, value string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.data[path] == nil {
		p.data[path] = map[string]string{}
	}
	p.data[path][key] = value
	return nil
}

func (p *fakeSecretsProvider) Delete(_ context.Context, path, key string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.data[path] == nil {
		return false, nil
	}
	_, ok := p.data[path][key]
	delete(p.data[path], key)
	return ok, nil
}

func (p *fakeSecretsProvider) ListKeys(_ context.Context, path string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for k := range p.data[path] {
		out = append(out, k)
	}
	return out, nil
}

func (p *fakeSecretsProvider) ListPaths(_ context.Context) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for path := range p.data {
		out = append(out, path)
	}
	return out, nil
}

func (p *fakeSecretsProvider) GetMulti(ctx context.Context, refs []secrets.SecretRef) (map[string]string, error) {
	return nil, nil
}

var _ secrets.Provider = (*fakeSecretsProvider)(nil)
