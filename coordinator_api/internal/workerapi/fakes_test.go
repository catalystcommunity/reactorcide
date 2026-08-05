package workerapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	pb "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/v1alpha1"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/google/uuid"
)

// --- fakeStore: an in-memory workerapi.DataStore --------------------------

type fakeStore struct {
	mu sync.Mutex

	pools            map[string]*models.WorkerPool
	enrollmentTokens map[string]*models.PoolEnrollmentToken // hex(hash) -> token
	workers          map[string]*models.Worker              // worker_id -> worker
	workersByKey     map[string]string                      // worker_key -> worker_id
	sessions         map[string]*models.WorkerSession       // hex(hash) -> session
	leases           map[string]*models.WorkerLease
	queues           []models.Queue
	jobs             map[string]*models.Job
	secretGrants     []models.SecretGrant
	projects         map[string]*models.Project
	users            map[string]*models.User
	vcsCredentials   []models.ProjectVCSCredential
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		pools:            map[string]*models.WorkerPool{},
		enrollmentTokens: map[string]*models.PoolEnrollmentToken{},
		workers:          map[string]*models.Worker{},
		workersByKey:     map[string]string{},
		sessions:         map[string]*models.WorkerSession{},
		leases:           map[string]*models.WorkerLease{},
		jobs:             map[string]*models.Job{},
		projects:         map[string]*models.Project{},
		users:            map[string]*models.User{},
	}
}

func hashHex(b []byte) string {
	return hex.EncodeToString(b)
}

// --- enrollment tokens ---

func (f *fakeStore) seedPool(pool *models.WorkerPool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pool.PoolID == "" {
		pool.PoolID = uuid.NewString()
	}
	f.pools[pool.PoolID] = pool
}

func (f *fakeStore) seedEnrollmentToken(tok *models.PoolEnrollmentToken) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if tok.TokenID == "" {
		tok.TokenID = uuid.NewString()
	}
	f.enrollmentTokens[hashHex(tok.TokenHash)] = tok
}

func rawTokenHash(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

func (f *fakeStore) GetActiveEnrollmentTokenByHash(ctx context.Context, tokenHash []byte) (*models.PoolEnrollmentToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok, ok := f.enrollmentTokens[hashHex(tokenHash)]
	if !ok || !tok.IsActive {
		return nil, store.ErrNotFound
	}
	return tok, nil
}

func (f *fakeStore) TouchEnrollmentTokenLastUsed(ctx context.Context, tokenID string) error {
	return nil
}

func (f *fakeStore) GetWorkerPoolByID(ctx context.Context, poolID string) (*models.WorkerPool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pools[poolID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return p, nil
}

// --- worker sessions ---

func (f *fakeStore) CreateWorkerSession(ctx context.Context, session *models.WorkerSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if session.SessionID == "" {
		session.SessionID = uuid.NewString()
	}
	f.sessions[hashHex(session.TokenHash)] = session
	return nil
}

func (f *fakeStore) GetActiveWorkerSessionByTokenHash(ctx context.Context, tokenHash []byte) (*models.WorkerSession, *models.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, ok := f.sessions[hashHex(tokenHash)]
	if !ok || sess.RevokedAt != nil || time.Now().After(sess.ExpiresAt) {
		return nil, nil, store.ErrNotFound
	}
	w, ok := f.workers[sess.WorkerID]
	if !ok {
		return nil, nil, store.ErrNotFound
	}
	return sess, w, nil
}

func (f *fakeStore) TouchWorkerSessionLastSeen(ctx context.Context, sessionID string) error {
	return nil
}

func (f *fakeStore) RevokeWorkerSession(ctx context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.SessionID == sessionID {
			now := time.Now().UTC()
			s.RevokedAt = &now
			return nil
		}
	}
	return store.ErrNotFound
}

// --- workers ---

func (f *fakeStore) UpsertWorkerByKey(ctx context.Context, w *models.Worker) (*models.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.workersByKey[w.WorkerKey]; ok {
		existing := f.workers[id]
		existing.PoolID = w.PoolID
		existing.Hostname = w.Hostname
		existing.OS = w.OS
		existing.Arch = w.Arch
		existing.Characteristics = w.Characteristics
		existing.WorkerVersion = w.WorkerVersion
		if w.Status != "" {
			existing.Status = w.Status
		}
		return existing, nil
	}
	toCreate := *w
	toCreate.WorkerID = uuid.NewString()
	if toCreate.Status == "" {
		toCreate.Status = models.WorkerStatusActive
	}
	f.workers[toCreate.WorkerID] = &toCreate
	f.workersByKey[w.WorkerKey] = toCreate.WorkerID
	return &toCreate, nil
}

func (f *fakeStore) GetWorkerByID(ctx context.Context, workerID string) (*models.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workers[workerID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return w, nil
}

func (f *fakeStore) UpdateWorkerStatus(ctx context.Context, workerID, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workers[workerID]
	if !ok {
		return store.ErrNotFound
	}
	w.Status = status
	return nil
}

func (f *fakeStore) TouchWorkerLastSeen(ctx context.Context, workerID string) error {
	return nil
}

// --- worker leases ---

func (f *fakeStore) CreateWorkerLease(ctx context.Context, workerID, jobID string, queueUUID *string) (*models.WorkerLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	lease := &models.WorkerLease{
		LeaseID:         uuid.NewString(),
		WorkerID:        workerID,
		JobID:           jobID,
		QueueUUID:       queueUUID,
		AcquiredAt:      time.Now().UTC(),
		LastHeartbeatAt: time.Now().UTC(),
	}
	f.leases[lease.LeaseID] = lease
	return lease, nil
}

func (f *fakeStore) GetWorkerLeaseByID(ctx context.Context, leaseID string) (*models.WorkerLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.leases[leaseID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return l, nil
}

func (f *fakeStore) TouchWorkerLeaseHeartbeat(ctx context.Context, leaseID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	lease, ok := f.leases[leaseID]
	if !ok {
		return store.ErrNotFound
	}
	lease.LastHeartbeatAt = time.Now().UTC()
	return nil
}

func (f *fakeStore) ReleaseWorkerLease(ctx context.Context, leaseID, outcome string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.leases[leaseID]
	if !ok {
		return store.ErrNotFound
	}
	if l.ReleasedAt == nil {
		now := time.Now().UTC()
		l.ReleasedAt = &now
		l.Outcome = outcome
	}
	return nil
}

func (f *fakeStore) ListActiveLeasesForWorker(ctx context.Context, workerID string) ([]models.WorkerLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.WorkerLease
	for _, l := range f.leases {
		if l.WorkerID == workerID && l.ReleasedAt == nil {
			out = append(out, *l)
		}
	}
	return out, nil
}

func (f *fakeStore) ListStaleActiveLeases(ctx context.Context, olderThan time.Time) ([]models.WorkerLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.WorkerLease
	for _, l := range f.leases {
		if l.ReleasedAt == nil && l.LastHeartbeatAt.Before(olderThan) {
			out = append(out, *l)
		}
	}
	return out, nil
}

// --- queues ---

func (f *fakeStore) seedQueue(q models.Queue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if q.QueueUUID == "" {
		q.QueueUUID = uuid.NewString()
	}
	f.queues = append(f.queues, q)
}

func (f *fakeStore) ListQueues(ctx context.Context, limit, offset int) ([]models.Queue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.Queue, len(f.queues))
	copy(out, f.queues)
	return out, nil
}

// --- jobs ---

func (f *fakeStore) seedJob(job *models.Job) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if job.JobID == "" {
		job.JobID = uuid.NewString()
	}
	if job.Status == "" {
		job.Status = "submitted"
	}
	f.jobs[job.JobID] = job
}

func (f *fakeStore) GetJobByID(ctx context.Context, jobID string) (*models.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[jobID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *j
	return &cp, nil
}

func (f *fakeStore) UpdateJob(ctx context.Context, job *models.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.jobs[job.JobID]; !ok {
		return store.ErrNotFound
	}
	cp := *job
	f.jobs[job.JobID] = &cp
	return nil
}

func (f *fakeStore) UpdateJobStatusGuarded(ctx context.Context, jobID string, fromStatuses []string, apply func(*models.Job)) (*models.Job, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[jobID]
	if !ok {
		return nil, false, store.ErrNotFound
	}
	matched := false
	for _, s := range fromStatuses {
		if j.Status == s {
			matched = true
			break
		}
	}
	if !matched {
		cp := *j
		return &cp, false, nil
	}
	cp := *j
	apply(&cp)
	f.jobs[jobID] = &cp
	out := cp
	return &out, true, nil
}

// --- secret grants ---

func (f *fakeStore) seedSecretGrant(g models.SecretGrant) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g.GrantID == "" {
		g.GrantID = uuid.NewString()
	}
	f.secretGrants = append(f.secretGrants, g)
}

func (f *fakeStore) ListSecretGrantsForJob(ctx context.Context, userID string, projectID *string, jobName string) ([]models.SecretGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.SecretGrant
	for _, g := range f.secretGrants {
		if g.UserID == userID {
			out = append(out, g)
		}
	}
	return out, nil
}

// --- projects/users/vcs credentials ---

func (f *fakeStore) seedProject(p *models.Project) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.ProjectID == "" {
		p.ProjectID = uuid.NewString()
	}
	f.projects[p.ProjectID] = p
}

func (f *fakeStore) seedVCSCredential(c models.ProjectVCSCredential) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	if !c.IsActive {
		c.IsActive = true
	}
	f.vcsCredentials = append(f.vcsCredentials, c)
}

func (f *fakeStore) GetProjectByID(ctx context.Context, projectID string) (*models.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[projectID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (f *fakeStore) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[userID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (f *fakeStore) ListActiveProjectVCSCredentials(ctx context.Context, projectID, provider string) ([]models.ProjectVCSCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.ProjectVCSCredential
	for _, c := range f.vcsCredentials {
		if c.ProjectID == projectID && c.Provider == provider && c.IsActive {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) TouchProjectVCSCredentialLastUsed(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	for i := range f.vcsCredentials {
		if f.vcsCredentials[i].ID == id {
			f.vcsCredentials[i].LastUsedAt = &now
			return nil
		}
	}
	return store.ErrNotFound
}

var _ DataStore = (*fakeStore)(nil)

// --- fakeSecretsProvider: an in-memory secrets.Provider -------------------

type fakeSecretsProvider struct {
	values map[string]string // "path:key" -> value
}

func newFakeSecretsProvider() *fakeSecretsProvider {
	return &fakeSecretsProvider{values: map[string]string{}}
}

func (p *fakeSecretsProvider) set(path, key, value string) {
	p.values[path+":"+key] = value
}

func (p *fakeSecretsProvider) Get(ctx context.Context, path, key string) (string, error) {
	v, ok := p.values[path+":"+key]
	if !ok {
		return "", fmt.Errorf("secret not found: %s:%s", path, key)
	}
	return v, nil
}

func (p *fakeSecretsProvider) Set(ctx context.Context, path, key, value string) error {
	p.set(path, key, value)
	return nil
}

func (p *fakeSecretsProvider) Delete(ctx context.Context, path, key string) (bool, error) {
	delete(p.values, path+":"+key)
	return true, nil
}

func (p *fakeSecretsProvider) ListKeys(ctx context.Context, path string) ([]string, error) {
	return nil, nil
}

func (p *fakeSecretsProvider) ListPaths(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (p *fakeSecretsProvider) GetMulti(ctx context.Context, refs []secrets.SecretRef) (map[string]string, error) {
	out := map[string]string{}
	for _, r := range refs {
		if v, ok := p.values[r.Path+":"+r.Key]; ok {
			out[r.Path+":"+r.Key] = v
		}
	}
	return out, nil
}

var _ secrets.Provider = (*fakeSecretsProvider)(nil)

// --- in-memory corndogs.ClientInterface with real GetNextTaskGroup
// matching semantics (priority + queue-group filtering + optimistic
// current-state transitions), built on top of corndogs.MockClient's
// Func-hook plumbing so RequestJob exercises the real
// corndogs.Client.GetNextTaskGroup wrapper's request/response shape against
// a controlled, in-process backend rather than reimplementing task claim
// logic in the tests themselves. ---

type fakeCorndogsBackend struct {
	mu    sync.Mutex
	tasks map[string]*pb.Task
}

func newFakeCorndogsClient() *corndogs.MockClient {
	backend := &fakeCorndogsBackend{tasks: map[string]*pb.Task{}}
	mc := corndogs.NewMockClient()

	mc.SubmitTaskToQueueFunc = func(ctx context.Context, queue string, payload *corndogs.TaskPayload, priority int64) (*pb.Task, error) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		payloadBytes, _ := json.Marshal(payload)
		t := &pb.Task{
			Uuid:            uuid.NewString(),
			Queue:           queue,
			CurrentState:    "submitted",
			AutoTargetState: "submitted-working",
			Payload:         payloadBytes,
			Priority:        priority,
		}
		backend.tasks[t.Uuid] = t
		return t, nil
	}

	mc.GetNextTaskGroupFunc = func(ctx context.Context, queues []string, currentState string, timeout int64) (*pb.Task, error) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		inGroup := make(map[string]bool, len(queues))
		for _, q := range queues {
			inGroup[q] = true
		}
		var best *pb.Task
		for _, t := range backend.tasks {
			if t.CurrentState != currentState || !inGroup[t.Queue] {
				continue
			}
			if best == nil || t.Priority > best.Priority {
				best = t
			}
		}
		if best == nil {
			return nil, nil
		}
		best.CurrentState = "claimed"
		return best, nil
	}

	mc.UpdateTaskFunc = func(ctx context.Context, taskID, currentState, newState string, payload []byte) (*pb.Task, error) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		t, ok := backend.tasks[taskID]
		if !ok {
			return nil, fmt.Errorf("task not found")
		}
		t.CurrentState = newState
		return t, nil
	}

	mc.CompleteTaskFunc = func(ctx context.Context, taskID, currentState string) (*pb.Task, error) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		t, ok := backend.tasks[taskID]
		if !ok {
			return nil, fmt.Errorf("task not found")
		}
		t.CurrentState = "completed"
		return t, nil
	}

	mc.CancelTaskFunc = func(ctx context.Context, taskID, currentState string) (*pb.Task, error) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		t, ok := backend.tasks[taskID]
		if !ok {
			return nil, fmt.Errorf("task not found")
		}
		t.CurrentState = "cancelled"
		return t, nil
	}

	mc.SendHeartbeatFunc = func(ctx context.Context, taskID, currentState string, timeoutExtensionSeconds int64) (*pb.Task, error) {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		t, ok := backend.tasks[taskID]
		if !ok {
			return nil, fmt.Errorf("task not found")
		}
		return t, nil
	}

	return mc
}
