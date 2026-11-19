package store

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// ImageAliasStore reads and writes image aliases (migration 000068,
// VM_IMAGES_PLAN.md "Contracts"). projectID nil is the organization scope.
// Callers find it with a type assertion on the store; a store without it
// (some in-memory test stores) has no aliases.
type ImageAliasStore interface {
	// CreateImageAlias inserts an alias. It returns ErrInvalidInput when
	// alias.Validate fails and ErrAlreadyExists when the scope already has
	// an alias with that name.
	CreateImageAlias(ctx context.Context, alias *models.ImageAlias) error
	// UpdateImageAlias writes every mutable value of the alias with the
	// AliasID of alias: kind, os, arch, characteristics, the active and
	// pending pins, and updated_by. The scope and the name do not change.
	UpdateImageAlias(ctx context.Context, alias *models.ImageAlias) error
	// GetImageAlias returns the alias with this name in exactly this scope.
	GetImageAlias(ctx context.Context, orgID string, projectID *string, name string) (*models.ImageAlias, error)
	// GetImageAliasByID returns the alias with this ID.
	GetImageAliasByID(ctx context.Context, aliasID string) (*models.ImageAlias, error)
	// ListImageAliases returns the aliases of exactly this scope, by name.
	ListImageAliases(ctx context.Context, orgID string, projectID *string) ([]models.ImageAlias, error)
	// ListOrgImageAliases returns the aliases of the organization and of
	// all its projects.
	ListOrgImageAliases(ctx context.Context, orgID string) ([]models.ImageAlias, error)
	// DeleteImageAlias deletes the alias with this name in exactly this
	// scope. It returns ErrNotFound when there is no such alias.
	DeleteImageAlias(ctx context.Context, orgID string, projectID *string, name string) error
	// ResolveImageAlias returns the alias that a job of projectID gets for
	// name: the project alias, else the organization alias. It returns
	// ErrNotFound when neither exists.
	ResolveImageAlias(ctx context.Context, orgID string, projectID *string, name string) (*models.ImageAlias, error)
	// ListImageAliasesWithDigest returns the aliases that a job of
	// projectID can see (its project and its organization) whose active or
	// pending digest is digest, and whose kind and platform are platform
	// (models.ImagePlatform: an empty Arch matches each arch). Project
	// aliases come first.
	ListImageAliasesWithDigest(ctx context.Context, orgID string, projectID *string, digest string, platform models.ImagePlatform) ([]models.ImageAlias, error)
	// SetImageAliasPending sets the pending pin of an alias to ref (a pinned
	// reference) and pending_since to now.
	SetImageAliasPending(ctx context.Context, aliasID, ref, updatedBy string) (*models.ImageAlias, error)
	// ClearImageAliasPending removes the pending pin of an alias.
	ClearImageAliasPending(ctx context.Context, aliasID, updatedBy string) (*models.ImageAlias, error)
	// PromoteImageAliasPending moves the pending pin to the active pin, but
	// only when the pending digest is still expectedPendingDigest. promoted
	// is false when the alias has another pending digest or none.
	PromoteImageAliasPending(ctx context.Context, aliasID, expectedPendingDigest string) (alias *models.ImageAlias, promoted bool, err error)
}

// WorkerImageStore reads and writes the image columns of workers
// (migration 000068).
type WorkerImageStore interface {
	// SetWorkerImageSet records the image set version and set hash that
	// the coordinator wants the worker to hold.
	SetWorkerImageSet(ctx context.Context, workerID string, version int64, hash string) error
	// UpdateWorkerImageState records the image state of an image set
	// report.
	UpdateWorkerImageState(ctx context.Context, workerID string, state models.WorkerImageState) error
	// GetWorkerImageState returns the newest recorded image state of the
	// worker, or nil when the worker sent no report.
	GetWorkerImageState(ctx context.Context, workerID string) (*models.WorkerImageState, error)
}

// ImageSetStore reads the inputs of worker image sets and of image alias
// rollouts (docs/image-aliases.md). PostgresDbStore implements it.
type ImageSetStore interface {
	// ListPoolClassGrants returns the worker class grants of the pools in
	// poolIDs, or of all pools when poolIDs is nil. Each grant gives the
	// organization of the class.
	ListPoolClassGrants(ctx context.Context, poolIDs []string) ([]models.PoolClassGrant, error)
	// ListImageAliasesForOrgs returns the aliases (organization and project
	// scopes) of the organizations in orgIDs.
	ListImageAliasesForOrgs(ctx context.Context, orgIDs []string) ([]models.ImageAlias, error)
	// ListPendingImageAliases returns every alias that has a pending pin.
	ListPendingImageAliases(ctx context.Context) ([]models.ImageAlias, error)
	// ListDrainingImages returns, for the organizations in orgIDs, each
	// distinct (project, alias name, characteristics, reference, digest) of
	// the jobs that named an alias and are not terminal (the
	// characteristics give the platform: a draining job maps to its alias
	// only when the kind and platform agree), and each distinct (worker
	// class, characteristics, reference, digest) of the VM jobs that named
	// a direct pinned reference and are not terminal (AliasName "").
	ListDrainingImages(ctx context.Context, orgIDs []string) ([]models.DrainingImage, error)
	// LockWorkerImageSet reads the worker row with a row lock (SELECT ...
	// FOR UPDATE). Call it in a transaction: the computation and the
	// version change of one worker then do not run at the same time on two
	// replicas.
	LockWorkerImageSet(ctx context.Context, workerID string) (*models.Worker, error)
}

// ImageAliasUsage counts the jobs that use an alias. PostgresDbStore
// implements it.
type ImageAliasUsage interface {
	// CountActiveImageAliasJobs returns the number of jobs that are not
	// terminal and that use alias: a job that named the alias (image_alias
	// is the alias name and the job resolved this alias: for an
	// organization alias, the job project has no project alias with that
	// name), or a job with a direct pinned reference (no alias) in the
	// scope of the alias whose image_digest is the active or the pending
	// digest of the alias.
	CountActiveImageAliasJobs(ctx context.Context, alias *models.ImageAlias) (int64, error)
}

// ImageAliasLocker locks one alias row. PostgresDbStore implements it.
type ImageAliasLocker interface {
	// LockImageAlias reads the alias with a row lock (SELECT ... FOR
	// UPDATE). Call it in a transaction: an alias change and a promotion
	// then do not undo each other.
	LockImageAlias(ctx context.Context, aliasID string) (*models.ImageAlias, error)
}
