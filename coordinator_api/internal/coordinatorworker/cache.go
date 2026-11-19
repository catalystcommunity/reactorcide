package coordinatorworker

import (
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// cacheConfigFromLease turns Lease.cache into the runner cache decision
// (docs/builds-and-caches.md). nil (a lease from an older coordinator)
// keeps the shared builder cache volume with writes.
func cacheConfigFromLease(cache *csilapi.CacheLease) *worker.CacheConfig {
	if cache == nil {
		return nil
	}
	return &worker.CacheConfig{
		Namespace:         cache.Namespace,
		VolumeKey:         cache.VolumeKey,
		Writable:          cache.Writable,
		ArtifactNamespace: cache.ArtifactNamespace,
	}
}
