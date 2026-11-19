package imageset

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// LeaseName is the coordinator lease of the promotion loop. Only the
// replica that holds it promotes pending pins.
const LeaseName = "image-alias-rollout"

// DefaultPromoteInterval is the time between two promotion passes.
const DefaultPromoteInterval = 15 * time.Second

// Fleet is the workers, pools, and class grants that a rollout uses.
type Fleet struct {
	Workers []models.Worker
	Pools   map[string]*models.WorkerPool
	Grants  []models.PoolClassGrant
}

// FleetStore reads a Fleet.
type FleetStore interface {
	ListWorkers(ctx context.Context, poolID *string) ([]models.Worker, error)
	ListWorkerPools(ctx context.Context, orgID *string) ([]models.WorkerPool, error)
	ListPoolClassGrants(ctx context.Context, poolIDs []string) ([]models.PoolClassGrant, error)
}

// LoadFleet reads all workers (with their session state), all pools, and all
// class grants.
func LoadFleet(ctx context.Context, st FleetStore) (Fleet, error) {
	workers, err := st.ListWorkers(ctx, nil)
	if err != nil {
		return Fleet{}, err
	}
	pools, err := st.ListWorkerPools(ctx, nil)
	if err != nil {
		return Fleet{}, err
	}
	grants, err := st.ListPoolClassGrants(ctx, nil)
	if err != nil {
		return Fleet{}, err
	}
	fleet := Fleet{Workers: workers, Pools: make(map[string]*models.WorkerPool, len(pools)), Grants: grants}
	for i := range pools {
		fleet.Pools[pools[i].PoolID] = &pools[i]
	}
	return fleet, nil
}

// RolloutWorker is one image-aware worker that serves an alias.
type RolloutWorker struct {
	Worker     *models.Worker
	Online     bool
	HasActive  bool
	HasPending bool
	// Ready is true when the worker reported the pending digest in its
	// ready digests. The state of the other images of the worker does not
	// count: a failed image of another alias (on a hosted pool, maybe of
	// another organization) must not stop this rollout.
	Ready bool
	// ActiveFailure and PendingFailure are the newest reported failures of
	// the active and the pending digest on this worker, or nil.
	ActiveFailure  *models.ImageFailure
	PendingFailure *models.ImageFailure
}

// Rollout is the rollout state of one alias.
type Rollout struct {
	Alias   *models.ImageAlias
	Workers []RolloutWorker
	// Online and Ready count the online serving workers, and the online
	// serving workers that are ready.
	Online int
	Ready  int
	// ActiveFailedPermanent counts the serving workers that report a
	// permanent failure of the active digest. The alias owner must fix the
	// image (or the access to it): no retry can help.
	ActiveFailedPermanent int
	// PendingFailedPermanent counts the online serving workers that report
	// a permanent failure of the pending digest.
	PendingFailedPermanent int
	// Promotable is true when the alias has a pending pin, at least one
	// serving worker is online, and every online serving worker is ready.
	Promotable bool
	// Blocked tells why a pending pin is not promotable, or "".
	Blocked string
}

// Rollout reasons.
const (
	BlockedNoWorkers        = "no serving worker is online"
	BlockedNotReady         = "an online serving worker does not have the pending image"
	BlockedPendingPermanent = "an online serving worker cannot pull the pending image (permanent failure): fix the image or the access to it, or set the alias to another reference"
)

// Problems returns the failures that the alias owner must fix.
func (r Rollout) Problems() []string {
	var out []string
	if r.ActiveFailedPermanent > 0 {
		out = append(out, fmt.Sprintf("%d serving worker(s) cannot pull the active image %s (permanent failure); jobs of this alias cannot run on them: fix the image or the access to it, or set the alias to another reference",
			r.ActiveFailedPermanent, r.Alias.ActiveRef))
	}
	if r.PendingFailedPermanent > 0 && r.Alias.PendingRef != nil {
		out = append(out, fmt.Sprintf("%d online serving worker(s) cannot pull the pending image %s (permanent failure); the rollout cannot complete",
			r.PendingFailedPermanent, *r.Alias.PendingRef))
	}
	return out
}

// EvaluateRollout returns the rollout state of alias in fleet at now.
// Workers that are not image aware are not serving workers: they cannot
// get the image, and they cannot claim a job that names an alias.
func EvaluateRollout(alias *models.ImageAlias, fleet Fleet, now time.Time) Rollout {
	rollout := Rollout{Alias: alias}
	pending := ""
	if alias.PendingDigest != nil {
		pending = *alias.PendingDigest
	}
	for i := range fleet.Workers {
		w := &fleet.Workers[i]
		if !ImageAware(w) {
			continue
		}
		pool := fleet.Pools[w.PoolID]
		if pool == nil {
			continue
		}
		if !TargetFor(w, pool, fleet.Grants).Serves(alias) {
			continue
		}
		entry := RolloutWorker{Worker: w, Online: Online(w, now), HasActive: HasDigest(w, alias.ActiveDigest)}
		if !entry.HasActive {
			entry.ActiveFailure = models.FailureFor(w.ImageFailed, alias.ActiveDigest)
		}
		if entry.ActiveFailure != nil && entry.ActiveFailure.Permanent {
			rollout.ActiveFailedPermanent++
		}
		if pending != "" {
			entry.HasPending = HasDigest(w, pending)
			entry.Ready = entry.HasPending
			if !entry.HasPending {
				entry.PendingFailure = models.FailureFor(w.ImageFailed, pending)
			}
		}
		if entry.Online {
			rollout.Online++
			if entry.Ready {
				rollout.Ready++
			}
			if entry.PendingFailure != nil && entry.PendingFailure.Permanent {
				rollout.PendingFailedPermanent++
			}
		}
		rollout.Workers = append(rollout.Workers, entry)
	}
	if pending == "" {
		return rollout
	}
	switch {
	case rollout.Online == 0:
		rollout.Blocked = BlockedNoWorkers
	case rollout.PendingFailedPermanent > 0:
		rollout.Blocked = BlockedPendingPermanent
	case rollout.Ready < rollout.Online:
		rollout.Blocked = BlockedNotReady
	default:
		rollout.Promotable = true
	}
	return rollout
}

// PromoterStore is what the promotion loop needs from the store.
type PromoterStore interface {
	FleetStore
	ListPendingImageAliases(ctx context.Context) ([]models.ImageAlias, error)
	PromoteImageAliasPending(ctx context.Context, aliasID, expectedPendingDigest string) (*models.ImageAlias, bool, error)
}

// Promoter moves pending pins to active pins (owner decision I10). Run it
// under the lease LeaseName.
type Promoter struct {
	Store    PromoterStore
	Interval time.Duration
	// Now is the clock (tests).
	Now func() time.Time
}

func (p *Promoter) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

// Run runs passes until ctx ends.
func (p *Promoter) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultPromoteInterval
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if _, err := p.Tick(ctx); err != nil && ctx.Err() == nil {
				logging.Log.WithError(err).Warn("image alias promotion pass failed")
			}
			timer.Reset(interval)
		}
	}
}

// Tick runs one pass. It returns the aliases that it promoted.
func (p *Promoter) Tick(ctx context.Context) ([]models.ImageAlias, error) {
	pending, err := p.Store.ListPendingImageAliases(ctx)
	if err != nil || len(pending) == 0 {
		return nil, err
	}
	fleet, err := LoadFleet(ctx, p.Store)
	if err != nil {
		return nil, err
	}
	now := p.now()
	var promoted []models.ImageAlias
	for i := range pending {
		alias := &pending[i]
		rollout := EvaluateRollout(alias, fleet, now)
		if !rollout.Promotable {
			continue
		}
		previousRef, previousDigest := alias.ActiveRef, alias.ActiveDigest
		updated, ok, err := p.Store.PromoteImageAliasPending(ctx, alias.AliasID, *alias.PendingDigest)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return promoted, err
		}
		if !ok {
			// The pending pin changed or was promoted at the same time (for
			// example by alias set --force). The next pass sees the new
			// state.
			continue
		}
		promoted = append(promoted, *updated)
		Invalidate()
		logging.Log.WithFields(map[string]any{
			"alias": updated.Name, "org_id": updated.OrgID, "ref": updated.ActiveRef, "previous_ref": previousRef,
			"workers": rollout.Online,
		}).Info("Image alias promoted: every online serving worker has the new image")
		audit.Record(ctx, p.Store, updated.OrgID, "image_alias.promote", "image_alias", updated.AliasID, PromotionDetails(updated, previousRef, previousDigest, false, rollout.Online))
	}
	return promoted, nil
}

// PromotionDetails are the audit details of a promotion.
func PromotionDetails(alias *models.ImageAlias, previousRef, previousDigest string, forced bool, workers int) models.JSONB {
	details := models.JSONB{
		"name":           alias.Name,
		"kind":           alias.Kind,
		"old_ref":        previousRef,
		"old_digest":     previousDigest,
		"new_ref":        alias.ActiveRef,
		"new_digest":     alias.ActiveDigest,
		"forced":         forced,
		"online_workers": workers,
		"display":        alias.Name + " (" + alias.ActiveRef + ")",
		"project_id":     "",
		"scope":          "organization",
	}
	if alias.ProjectID != nil {
		details["project_id"] = *alias.ProjectID
		details["scope"] = "project"
	}
	return details
}
