package workerapi

import (
	"context"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imageset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// Image set delivery (docs/image-aliases.md, owner decision I8).
//
// The coordinator computes the image set of an image-aware worker at
// Register and at heartbeats (imageset.Apply). A heartbeat uses the set
// that this replica computed for the worker in the last
// imageSetRecomputeEvery, unless an alias changed on this replica since
// then (imageset.Generation). So a worker heartbeat reads the alias tables
// at most one time each minute per replica, and an alias change on another
// replica reaches the worker in at most one minute. imageset.Apply locks
// the worker row, so two replicas never write an older set over a newer
// one.

// imageSetRecomputeEvery is the longest time that a replica uses a cached
// set of a worker.
const imageSetRecomputeEvery = time.Minute

type imageSetCacheEntry struct {
	at         time.Time
	generation uint64
	versioned  imageset.Versioned
}

// imageSetCache holds the newest set that this replica computed for each
// worker.
type imageSetCache struct {
	mu      sync.Mutex
	entries map[string]imageSetCacheEntry
	now     func() time.Time
}

func newImageSetCache() *imageSetCache {
	return &imageSetCache{entries: map[string]imageSetCacheEntry{}, now: time.Now}
}

func (c *imageSetCache) get(workerID string) (imageset.Versioned, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[workerID]
	if !ok || entry.generation != imageset.Generation() || c.now().Sub(entry.at) >= imageSetRecomputeEvery {
		return imageset.Versioned{}, false
	}
	return entry.versioned, true
}

func (c *imageSetCache) put(workerID string, versioned imageset.Versioned, generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.entries[workerID] = imageSetCacheEntry{at: now, generation: generation, versioned: versioned}
	// Remove old entries, so that workers that stopped do not stay in
	// memory.
	if len(c.entries) > 64 {
		for id, entry := range c.entries {
			if now.Sub(entry.at) > 10*imageSetRecomputeEvery {
				delete(c.entries, id)
			}
		}
	}
}

// imageSetStore returns the store as an imageset.Store, or nil when the
// store has no image tables (some in-memory test stores).
func (s *WorkerService) imageSetStore() imageset.Store {
	st, _ := s.deps.Store.(imageset.Store)
	return st
}

// computeImageSet returns the image set of a worker. With force false, a
// recent set from the cache is used.
func (s *WorkerService) computeImageSet(ctx context.Context, workerID string, force bool) (imageset.Versioned, bool) {
	st := s.imageSetStore()
	if st == nil {
		return imageset.Versioned{}, false
	}
	if !force {
		if cached, ok := s.imageSets.get(workerID); ok {
			return cached, true
		}
	}
	generation := imageset.Generation()
	versioned, err := imageset.Apply(ctx, st, workerID)
	if err != nil {
		logging.Log.WithError(err).WithField("worker_id", workerID).Warn("Failed to compute worker image set")
		return imageset.Versioned{}, false
	}
	if versioned.Changed {
		logging.Log.WithFields(map[string]any{
			"worker_id": workerID, "image_set_version": versioned.Version, "images": len(versioned.Set.Entries),
		}).Info("Worker image set changed")
	}
	s.imageSets.put(workerID, versioned, generation)
	return versioned, true
}

// registerImageSet returns the image set for the Register response of an
// image-aware worker, or nil.
func (s *WorkerService) registerImageSet(ctx context.Context, wkr *models.Worker) *csilapi.ImageSet {
	if wkr.ImageRuntime == nil {
		return nil
	}
	versioned, ok := s.computeImageSet(ctx, wkr.WorkerID, true)
	if !ok {
		return nil
	}
	return wireImageSet(versioned)
}

// heartbeatImageSet returns the image set for a heartbeat response, or nil.
// An image-aware worker (it sent a runtime at Register or a report now)
// gets the set when it sent no report, or when its report version is not
// the wanted version: older, or newer (the worker row of a new enrollment
// starts again at version 1, so the version of the worker can be higher).
// An older worker gets nothing.
func (s *WorkerService) heartbeatImageSet(ctx context.Context, wkr *models.Worker, report *csilapi.ImageSetReport) *csilapi.ImageSet {
	if !imageset.ImageAware(wkr) && report == nil {
		return nil
	}
	versioned, ok := s.computeImageSet(ctx, wkr.WorkerID, false)
	if !ok {
		return nil
	}
	if report != nil && report.Version == versioned.Version {
		return nil
	}
	return wireImageSet(versioned)
}

func wireImageSet(versioned imageset.Versioned) *csilapi.ImageSet {
	out := &csilapi.ImageSet{Version: versioned.Version, Images: make([]csilapi.ImageSetEntry, 0, len(versioned.Set.Entries))}
	for _, entry := range versioned.Set.Entries {
		wire := csilapi.ImageSetEntry{
			Ref: entry.Ref, Digest: entry.Digest, Kind: entry.Kind, Aliases: append([]string{}, entry.Aliases...),
		}
		// Only a pending entry carries the field. A missing value is not
		// pending, so the startup gate of the worker waits for the entry.
		if entry.Pending {
			pending := true
			wire.Pending = &pending
		}
		out.Images = append(out.Images, wire)
	}
	return out
}

// registerRuntime returns the worker runtime of a Register request, or nil
// for an older worker or an unknown value. An unknown value does not refuse
// the Register: the worker works as an older worker.
func registerRuntime(info csilapi.WorkerInfo) *string {
	if info.Runtime == nil || *info.Runtime == "" {
		return nil
	}
	runtime := *info.Runtime
	if !models.ValidWorkerRuntime(runtime) {
		logging.Log.WithFields(map[string]any{"worker_key": info.WorkerKey, "runtime": runtime}).
			Warn("Worker sent an unknown runtime; it gets no image set")
		return nil
	}
	return &runtime
}
