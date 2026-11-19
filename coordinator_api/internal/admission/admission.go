// Package admission holds the resource-aware admission rules that a worker
// and the coordinator share.
//
// A worker in "resources" mode reports its capacity and its current headroom
// on each RequestJob call. The coordinator then offers only work whose
// resource requests fit the headroom. Work selection stays in Corndogs, which
// claims the best task across a set of queues. The coordinator therefore
// chooses the set of queues: it includes a queue only when the queue's head
// job fits. See SelectQueues for the anti-starvation rule.
//
// CPU is compared by request (millicores). Memory is compared by limit
// (bytes), because a Reactorcide job has no memory request: the Kubernetes
// runner sets only a memory limit, and Kubernetes then uses the limit as the
// request.
package admission

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
)

// Admission modes. ModeConcurrency is the default and keeps the fixed lease
// count of a worker. ModeResources admits work by resource headroom.
const (
	ModeConcurrency = "concurrency"
	ModeResources   = "resources"
)

// ParseMode normalizes an admission mode. An empty value is ModeConcurrency.
func ParseMode(raw string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(raw)); mode {
	case "", ModeConcurrency:
		return ModeConcurrency, nil
	case ModeResources:
		return ModeResources, nil
	default:
		return "", fmt.Errorf("admission mode must be %q or %q, not %q", ModeConcurrency, ModeResources, raw)
	}
}

// Resources is an amount of CPU (millicores) and memory (bytes).
type Resources struct {
	CPUMillicores int64 `json:"cpu_millicores"`
	MemoryBytes   int64 `json:"memory_bytes"`
}

// Add returns r + o.
func (r Resources) Add(o Resources) Resources {
	return Resources{CPUMillicores: r.CPUMillicores + o.CPUMillicores, MemoryBytes: r.MemoryBytes + o.MemoryBytes}
}

// Sub returns r - o. A result can be negative: a worker can be over its
// budget when other work started outside its control.
func (r Resources) Sub(o Resources) Resources {
	return Resources{CPUMillicores: r.CPUMillicores - o.CPUMillicores, MemoryBytes: r.MemoryBytes - o.MemoryBytes}
}

// Fits reports whether need fits in r for both CPU and memory.
func (r Resources) Fits(need Resources) bool {
	return need.CPUMillicores <= r.CPUMillicores && need.MemoryBytes <= r.MemoryBytes
}

// IsPositive reports whether r has room for any work at all.
func (r Resources) IsPositive() bool {
	return r.CPUMillicores > 0 && r.MemoryBytes > 0
}

// JobNeed returns the admission need of a job from its stored resource
// strings: the CPU request and the memory limit. An empty value takes the
// Reactorcide default, the same value the submit path stores. An invalid
// value is an error; the caller decides how to treat that job.
func JobNeed(cpuRequest, memoryLimit string) (Resources, error) {
	cpuRequest, _, memoryLimit = resources.ApplyDefaults(cpuRequest, "", memoryLimit)
	cpu, err := resources.CPUMillicores(cpuRequest)
	if err != nil {
		return Resources{}, fmt.Errorf("cpu request: %w", err)
	}
	memory, err := resources.MemoryBytes(memoryLimit)
	if err != nil {
		return Resources{}, fmt.Errorf("memory limit: %w", err)
	}
	return Resources{CPUMillicores: cpu, MemoryBytes: memory}, nil
}

// Report is what a resources-mode worker sends on RequestJob.
type Report struct {
	Capacity Resources
	Headroom Resources
}

// QueueHead is the first waiting job of one queue, in the Corndogs order:
// highest priority first, then oldest.
type QueueHead struct {
	QueueUUID string
	JobID     string
	Priority  int
	CreatedAt time.Time
	Need      Resources
}

// ahead reports whether a runs before b in the Corndogs order.
func ahead(a, b QueueHead) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	return a.CreatedAt.Before(b.CreatedAt)
}

// Selection is the result of SelectQueues.
type Selection struct {
	// Queues are the queue UUIDs to claim from, in input order.
	Queues []string
	// Reserved is the starving head that this worker holds room for, or nil.
	// While Reserved is set, the worker gets only work that runs before it,
	// so its running jobs finish and its headroom grows until the head fits.
	Reserved *QueueHead
}

// SelectQueues returns the queues that a resources-mode worker can claim
// from.
//
// queues are the queue UUIDs that the worker's characteristics satisfy.
// heads holds the head job of each of those queues that has waiting work.
//
// Rules:
//
//  1. A queue with no known head is included. The coordinator does not know
//     what is in it, so the claim-time fit check decides.
//  2. A head that is larger than the worker's capacity never blocks this
//     worker. That job can never run here, and other workers can take it.
//  3. A head that fits the headroom is included.
//  4. Anti-starvation: a head that fits the capacity but not the headroom,
//     and has waited at least reserveAfter, is "starving". The worker then
//     reserves room for the first starving head in the Corndogs order: it
//     claims only from fitting queues whose head runs before that head. In
//     most cases no such queue exists, so the worker takes no new work until
//     enough of its jobs finish. Without this rule, a stream of small jobs
//     can keep the headroom of every worker below a large job forever.
//     reserveAfter <= 0 turns the rule off.
//
// A large head blocks only its own queue (first in, first out stays true in
// a queue). Smaller work in other queues continues until rule 4 applies.
func SelectQueues(queues []string, heads []QueueHead, report Report, now time.Time, reserveAfter time.Duration) Selection {
	byQueue := make(map[string]QueueHead, len(heads))
	for _, head := range heads {
		byQueue[head.QueueUUID] = head
	}

	var reserved *QueueHead
	if reserveAfter > 0 {
		starving := make([]QueueHead, 0)
		for _, queue := range queues {
			head, ok := byQueue[queue]
			if !ok || !report.Capacity.Fits(head.Need) || report.Headroom.Fits(head.Need) {
				continue
			}
			if now.Sub(head.CreatedAt) >= reserveAfter {
				starving = append(starving, head)
			}
		}
		if len(starving) > 0 {
			sort.SliceStable(starving, func(i, j int) bool { return ahead(starving[i], starving[j]) })
			first := starving[0]
			reserved = &first
		}
	}

	out := make([]string, 0, len(queues))
	for _, queue := range queues {
		head, ok := byQueue[queue]
		if !ok {
			if reserved == nil {
				out = append(out, queue)
			}
			continue
		}
		if !report.Headroom.Fits(head.Need) {
			continue
		}
		if reserved != nil && !ahead(head, *reserved) {
			continue
		}
		out = append(out, queue)
	}
	return Selection{Queues: out, Reserved: reserved}
}
