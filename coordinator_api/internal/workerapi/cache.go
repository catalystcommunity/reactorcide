package workerapi

import "sync"

// leaseSecretCache holds the resolved secret VALUES for a lease's lifetime,
// in coordinator process memory only -- never persisted, never returned by
// any op. It exists purely as a masking backstop for AppendLogs (WORKERS_
// PLAN.md's "Mask secrets coordinator-side as a backstop"): the worker is
// responsible for masking its own job output before shipping a log chunk
// (the same way runnerlib masks locally today), but if a chunk still
// contains a secret value verbatim, the coordinator can catch it here
// because it already resolved that exact value for this lease at
// RequestJob time. Entries are removed on ReportResult (finalize) and by
// the lease reaper, so nothing outlives the lease it was resolved for.
type leaseSecretCache struct {
	mu     sync.Mutex
	values map[string][]string // lease_id -> resolved secret values
}

func newLeaseSecretCache() *leaseSecretCache {
	return &leaseSecretCache{values: make(map[string][]string)}
}

func (c *leaseSecretCache) put(leaseID string, values []string) {
	if leaseID == "" || len(values) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[leaseID] = values
}

func (c *leaseSecretCache) get(leaseID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.values[leaseID]
}

func (c *leaseSecretCache) delete(leaseID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, leaseID)
}
