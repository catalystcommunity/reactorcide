// Package workerapi implements the coordinator side of the ReactorcideWorker
// CSIL-RPC protocol: Register, RequestJob, Heartbeat, AppendLogs, and
// ReportResult. All state (corndogs, Postgres, object storage) lives behind
// this package; a worker's only capability is an authenticated CSIL-RPC
// connection built from internal/workerapi/csilapi (generated from
// csil/reactorcide-worker.csil).
//
// SECURITY: resolved secret values (see RequestJob in service.go) are placed
// ONLY in the lease response's dedicated `secrets` field, kept separate from
// `env`, and are never written to the corndogs TaskPayload, never logged,
// and never returned by any other op. See secrets.go for the coordinator-side
// resolution + grant-authorization path, which reuses
// internal/worker.AuthorizeSecretAccess/ResolveSecretsInEnvFull/BuildJobEnv
// rather than reimplementing them.
package workerapi

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/pubsub"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerauth"
)

// JobStatusReporter posts a job's own VCS commit-status check (e.g. the
// "reactorcide/eval" check on a PR). Satisfied by *internal/vcs.JobStatusUpdater
// and is a no-op for jobs without VCS metadata. ReportResult calls it on
// completion so a job's individual check resolves (pending -> success/failure)
// instead of hanging forever -- the coordinator-mediated path previously only
// posted the pending check at creation and never the terminal one. Workflow
// node jobs are deliberately NOT reported this way (the aggregate workflow
// check covers them); only non-node jobs (the eval parent, standalone jobs)
// get an individual terminal check.
type JobStatusReporter interface {
	UpdateJobStatus(ctx context.Context, job *models.Job) error
}

// WorkflowFinalizer advances a job's workflow instance when the job starts and
// finishes. It is satisfied by *internal/worker.TriggerProcessor, whose
// ProcessWorkflowJobStarted/ProcessWorkflowCompletion roll the node's status
// and re-evaluate the workflow. Declared here as a narrow interface so the
// worker service depends on the behavior, not the concrete TriggerProcessor
// (and tests can substitute a fake). workspaceDir is passed empty on the
// coordinator-mediated path: the coordinator has no access to the job's
// (possibly remote/ephemeral) workspace, so workflow-output.json vars are not
// merged here -- only status/DAG progression, which needs no workspace.
type WorkflowFinalizer interface {
	ProcessWorkflowJobStarted(ctx context.Context, job *models.Job) error
	ProcessWorkflowCompletion(ctx context.Context, workspaceDir string, job *models.Job) error
}

// DataStore is everything the ReactorcideWorker service implementations need
// from the store: the enrollment/session primitives workerauth.Enrollment/
// WorkerSessions consume, the worker/lease operations
// postgres_store/worker_operations.go added (P2-A1), queue listing (for
// characteristic matching), job read/guarded-transition, and secret-grant
// lookup (reused, unchanged, from internal/worker's own authorization path).
// This repo's consumer-defined-narrow-interface convention (see
// internal/uiapi/store.go's DataStore for the sibling CSIL service):
// production wiring (router.go) type-asserts store.AppStore onto this once
// at startup; tests build a hand-rolled fake satisfying it directly.
type DataStore interface {
	workerauth.EnrollmentTokenStore
	workerauth.WorkerSessionStore

	// --- workers ---
	UpsertWorkerByKey(ctx context.Context, worker *models.Worker) (*models.Worker, error)
	GetWorkerByID(ctx context.Context, workerID string) (*models.Worker, error)
	UpdateWorkerStatus(ctx context.Context, workerID, status string) error
	TouchWorkerLastSeen(ctx context.Context, workerID string) error

	// --- worker_leases ---
	CreateWorkerLease(ctx context.Context, workerID, jobID string, queueUUID *string) (*models.WorkerLease, error)
	GetWorkerLeaseByID(ctx context.Context, leaseID string) (*models.WorkerLease, error)
	TouchWorkerLeaseHeartbeat(ctx context.Context, leaseID string) error
	ReleaseWorkerLease(ctx context.Context, leaseID, outcome string) error
	ListActiveLeasesForWorker(ctx context.Context, workerID string) ([]models.WorkerLease, error)
	ListStaleActiveLeases(ctx context.Context, olderThan time.Time) ([]models.WorkerLease, error)

	// --- queues ---
	ListQueues(ctx context.Context, limit, offset int) ([]models.Queue, error)

	// --- jobs ---
	GetJobByID(ctx context.Context, jobID string) (*models.Job, error)
	UpdateJob(ctx context.Context, job *models.Job) error

	// --- projects/users ---
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
	GetUserByID(ctx context.Context, userID string) (*models.User, error)

	// --- project_vcs_credentials rotation (reused unchanged from the
	// deleted internal/worker/vcs_checkout_auth.go's own narrow
	// vcsCredentialRotationStore interface -- see vcs_auth.go) ---
	ListActiveProjectVCSCredentials(ctx context.Context, projectID, provider string) ([]models.ProjectVCSCredential, error)
	TouchProjectVCSCredentialLastUsed(ctx context.Context, id string) error

	// UpdateJobStatusGuarded is the same race-safe transition primitive
	// internal/jobcontrol and internal/worker/corndogs_worker.go's own
	// (unexported, identically-shaped) guardedJobStore interfaces reach via
	// type assertion; declared directly here (rather than type-asserted)
	// since every production DataStore (*postgres_store.PostgresDbStore)
	// implements it and every test fake needs it anyway to exercise
	// RequestJob/ReportResult's claim/finalize paths.
	UpdateJobStatusGuarded(ctx context.Context, jobID string, fromStatuses []string, apply func(*models.Job)) (*models.Job, bool, error)

	// --- secret_grants (reused unchanged from internal/worker's own
	// authorization path -- see internal/worker.SecretGrantStore, which this
	// interface is structurally identical to) ---
	ListSecretGrantsForJob(ctx context.Context, userID string, projectID *string, jobName string) ([]models.SecretGrant, error)
}

// Deps is the dependency bag every ReactorcideWorker op implementation is
// built against (mirrors internal/uiapi.Deps' shape for the sibling CSIL
// service). One Deps is constructed once at startup (see
// handlers/router.go's buildWorkerAPIDeps) and shared by the single
// *WorkerService instance mounted on the dispatcher.
type Deps struct {
	Store          DataStore
	CorndogsClient corndogs.ClientInterface
	Enrollment     *workerauth.Enrollment
	Sessions       *workerauth.WorkerSessions
	KeyManager     *secrets.MasterKeyManager
	ObjectStore    objects.ObjectStore
	Publisher      *pubsub.Publisher

	// WorkflowFinalizer advances a job's workflow instance across the job's
	// lifecycle: mark the node running when the worker starts it, and mark the
	// node terminal + re-evaluate the workflow (submit ready downstream nodes,
	// recompute the instance status, update the VCS check) when it finishes.
	// Without this the coordinator-mediated ReportResult path finalizes the job
	// but never rolls the workflow forward, leaving workflow_instances stuck in
	// "running" even after every job completes. Satisfied by
	// *internal/worker.TriggerProcessor; wired in router.go from the concrete
	// store + the configured VCS status updater. nil in tests / stores that
	// don't exercise workflows (calls are then skipped).
	WorkflowFinalizer WorkflowFinalizer

	// JobStatusReporter posts a completing job's own VCS check (see the
	// JobStatusReporter interface doc). nil in tests / when VCS is unconfigured.
	JobStatusReporter JobStatusReporter

	// SecretsProvider resolves a secrets.Provider scoped to a job's owning
	// organization (job.OrgID, with job.UserID as legacy attribution), matching
	// internal/worker's own getSecretsProvider/GetOrgEncryptionKey(...,
	// job.UserID) call). NewDeps wires this to the real DB-backed default
	// (defaultSecretsProviderForOrg, identical in shape to
	// uiapi.Deps.SecretsProvider); tests substitute an in-memory
	// secrets.Provider fake here directly.
	SecretsProvider func(ctx context.Context, orgID string) (secrets.Provider, error)
}

// NewDeps constructs a Deps. keyManager/objectStore/publisher may be nil:
// secret resolution fails cleanly (ServiceError "internal") without a key
// manager, AppendLogs fails cleanly without an object store, and
// PublishJobUpdate/PublishLogAvailable calls are simply skipped without a
// publisher -- mirroring uiapi.Deps' same nil-tolerant construction.
func NewDeps(store DataStore, corndogsClient corndogs.ClientInterface, enrollment *workerauth.Enrollment, sessions *workerauth.WorkerSessions, keyManager *secrets.MasterKeyManager, objectStore objects.ObjectStore, publisher *pubsub.Publisher) *Deps {
	d := &Deps{
		Store:          store,
		CorndogsClient: corndogsClient,
		Enrollment:     enrollment,
		Sessions:       sessions,
		KeyManager:     keyManager,
		ObjectStore:    objectStore,
		Publisher:      publisher,
	}
	d.SecretsProvider = d.defaultSecretsProviderForOrg
	return d
}
