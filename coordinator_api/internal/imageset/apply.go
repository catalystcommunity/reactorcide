package imageset

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Store is what the image set computation needs from the store.
// PostgresDbStore implements it.
type Store interface {
	store.ImageSetStore
	store.WorkerImageStore
	GetWorkerPoolByID(ctx context.Context, poolID string) (*models.WorkerPool, error)
}

// Versioned is a computed set with the version that the coordinator wants
// the worker to hold.
type Versioned struct {
	Set     Set
	Version int64
	// Changed is true when this computation increased the version.
	Changed bool
}

// LoadTarget returns the target of a worker and the inputs of its set.
func LoadTarget(ctx context.Context, st Store, w *models.Worker) (Target, []models.ImageAlias, []models.DrainingImage, error) {
	pool, err := st.GetWorkerPoolByID(ctx, w.PoolID)
	if err != nil {
		return Target{}, nil, nil, fmt.Errorf("read worker pool: %w", err)
	}
	grants, err := st.ListPoolClassGrants(ctx, []string{w.PoolID})
	if err != nil {
		return Target{}, nil, nil, err
	}
	target := TargetFor(w, pool, grants)
	orgIDs := target.Grants.OrgIDs()
	if len(orgIDs) == 0 {
		return target, nil, nil, nil
	}
	aliases, err := st.ListImageAliasesForOrgs(ctx, orgIDs)
	if err != nil {
		return Target{}, nil, nil, err
	}
	drains, err := st.ListDrainingImages(ctx, orgIDs)
	if err != nil {
		return Target{}, nil, nil, err
	}
	return target, aliases, drains, nil
}

// ComputeFor computes the set of a worker. It does not change the version.
func ComputeFor(ctx context.Context, st Store, w *models.Worker) (Set, error) {
	target, aliases, drains, err := LoadTarget(ctx, st, w)
	if err != nil {
		return Set{}, err
	}
	return Compute(target, aliases, drains), nil
}

// Apply computes the set of the worker and, when its hash is not the hash
// that the worker row records, increases the version (SetWorkerImageSet).
// When st has InTransaction, the work runs in one transaction that first
// locks the worker row: two replicas then cannot compute and write the set
// of one worker at the same time, and the later write always uses inputs
// that are at least as new as the earlier write.
func Apply(ctx context.Context, st Store, workerID string) (Versioned, error) {
	var result Versioned
	run := func(txCtx context.Context) error {
		w, err := st.LockWorkerImageSet(txCtx, workerID)
		if err != nil {
			return err
		}
		set, err := ComputeFor(txCtx, st, w)
		if err != nil {
			return err
		}
		result = Versioned{Set: set, Version: w.ImageSetVersion}
		if w.ImageSetHash != nil && *w.ImageSetHash == set.Hash && w.ImageSetVersion > 0 {
			return nil
		}
		result.Version = w.ImageSetVersion + 1
		result.Changed = true
		return st.SetWorkerImageSet(txCtx, workerID, result.Version, set.Hash)
	}
	if tx, ok := st.(interface {
		InTransaction(context.Context, func(context.Context) error) error
	}); ok {
		if err := tx.InTransaction(ctx, run); err != nil {
			return Versioned{}, err
		}
		return result, nil
	}
	if err := run(ctx); err != nil {
		return Versioned{}, err
	}
	return result, nil
}

// Worker readiness values for the worker list (owner decision I8).
const (
	ReadinessHealthy  = "healthy"
	ReadinessUpdating = "updating images"
	ReadinessFailed   = "image update failed"
)

// Readiness returns the image readiness of a worker and the failure
// detail. ok is false for a worker that is not image aware.
//
//   - No report yet: updating images.
//   - The newest report has state failed: image update failed.
//   - The report version is older than the wanted version, or the state is
//     updating: updating images.
//   - Else: healthy.
func Readiness(w *models.Worker) (status, detail string, ok bool) {
	if !ImageAware(w) {
		return "", "", false
	}
	if w.ImageState == nil {
		return ReadinessUpdating, "", true
	}
	reported := int64(0)
	if w.ImageStateVersion != nil {
		reported = *w.ImageStateVersion
	}
	if w.ImageStateDetail != nil {
		detail = *w.ImageStateDetail
	}
	switch {
	case *w.ImageState == models.WorkerImageStateFailed:
		return ReadinessFailed, detail, true
	case reported < w.ImageSetVersion || *w.ImageState != models.WorkerImageStateUpdated:
		return ReadinessUpdating, detail, true
	}
	return ReadinessHealthy, "", true
}

// HasDigest reports whether the worker reported digest as ready.
func HasDigest(w *models.Worker, digest string) bool {
	for _, ready := range w.ImageReadyDigests {
		if ready == digest {
			return true
		}
	}
	return false
}
