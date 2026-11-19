// Package backlog computes the demand signal of each queue: how many jobs
// wait for a worker, how long the oldest one waits, and the sum of their CPU
// requests and memory limits. The signal is available as Prometheus gauges
// (metrics.go), a REST endpoint (GET /api/v1/backlog), and the UI
// list-queues operation.
//
// Each cluster or pool scales itself from this signal with its own
// autoscaler. Many clusters can drain one queue, so the signal says how much
// work waits, not where it must run. See docs/workers.md "Backlog and
// autoscaling".
package backlog

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/admission"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// QueueBacklog is the waiting work of one queue.
type QueueBacklog struct {
	QueueID     string `json:"queue_id,omitempty"`
	QueueUUID   string `json:"queue_uuid"`
	DisplayName string `json:"display_name,omitempty"`
	OrgID       string `json:"org_id,omitempty"`
	WorkerClass string `json:"worker_class,omitempty"`
	IsDefault   bool   `json:"is_default"`
	// Characteristics are the worker characteristics the queue requires.
	Characteristics map[string]string `json:"characteristics"`
	// Known is false for waiting jobs whose queue has no queues row (a job
	// from before queue routing).
	Known bool `json:"known"`

	Count                int64      `json:"count"`
	OldestCreatedAt      *time.Time `json:"oldest_created_at,omitempty"`
	OldestAgeSeconds     int64      `json:"oldest_age_seconds"`
	CPURequestMillicores int64      `json:"cpu_request_millicores"`
	MemoryLimitBytes     int64      `json:"memory_limit_bytes"`
}

// CharacteristicBacklog is the waiting work of every queue that requires one
// characteristic value, for example region=us-east-1. A queue with many
// characteristics counts in each of them.
type CharacteristicBacklog struct {
	Key                  string `json:"key"`
	Value                string `json:"value"`
	Queues               int    `json:"queues"`
	Count                int64  `json:"count"`
	OldestAgeSeconds     int64  `json:"oldest_age_seconds"`
	CPURequestMillicores int64  `json:"cpu_request_millicores"`
	MemoryLimitBytes     int64  `json:"memory_limit_bytes"`
}

// Snapshot is the backlog at one time.
type Snapshot struct {
	GeneratedAt      time.Time               `json:"generated_at"`
	Queues           []QueueBacklog          `json:"queues"`
	ByCharacteristic []CharacteristicBacklog `json:"by_characteristic"`
	// Totals is the sum over the queues in this snapshot.
	Totals QueueTotals `json:"totals"`
}

// QueueTotals is the sum of the waiting work in a snapshot.
type QueueTotals struct {
	Count                int64 `json:"count"`
	OldestAgeSeconds     int64 `json:"oldest_age_seconds"`
	CPURequestMillicores int64 `json:"cpu_request_millicores"`
	MemoryLimitBytes     int64 `json:"memory_limit_bytes"`
}

// Source is the store data the backlog reads.
type Source interface {
	ListQueues(ctx context.Context, limit, offset int) ([]models.Queue, error)
	ListWaitingJobGroups(ctx context.Context) ([]models.WaitingJobGroup, error)
}

// SourceFrom returns appStore as a Source, or nil when it cannot list
// queues or waiting jobs (the Postgres store can).
func SourceFrom(appStore any) Source {
	source, ok := appStore.(Source)
	if !ok {
		return nil
	}
	return source
}

// Filter selects the queues of a snapshot. A nil Filter keeps every queue,
// including waiting jobs of unknown queues.
type Filter func(queue models.Queue) bool

// Load reads the queues and the waiting jobs and computes a snapshot.
func Load(ctx context.Context, source Source, filter Filter, now time.Time) (Snapshot, error) {
	queues, err := source.ListQueues(ctx, 0, 0)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list queues: %w", err)
	}
	groups, err := source.ListWaitingJobGroups(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list waiting jobs: %w", err)
	}
	return Compute(queues, groups, filter, now), nil
}

// Compute builds a snapshot from queue rows and waiting job groups. Every
// selected queue is in the result, also with no waiting work, so a consumer
// sees zero instead of a missing value.
func Compute(queues []models.Queue, groups []models.WaitingJobGroup, filter Filter, now time.Time) Snapshot {
	now = now.UTC()
	byUUID := make(map[string]*QueueBacklog, len(queues))
	order := make([]string, 0, len(queues))
	for _, queue := range queues {
		if filter != nil && !filter(queue) {
			continue
		}
		entry := &QueueBacklog{
			QueueID: queue.QueueID, QueueUUID: queue.QueueUUID, DisplayName: queue.DisplayName,
			OrgID: queue.OrgID, WorkerClass: queue.WorkerClass, IsDefault: queue.IsDefault,
			Characteristics: characteristicStrings(queue.Characteristics), Known: true,
		}
		byUUID[queue.QueueUUID] = entry
		order = append(order, queue.QueueUUID)
	}
	for _, group := range groups {
		entry, ok := byUUID[group.QueueName]
		if !ok {
			if filter != nil {
				// An unknown queue has no owner, so only the unfiltered
				// (global) view shows it.
				continue
			}
			entry = &QueueBacklog{QueueUUID: group.QueueName, Characteristics: map[string]string{}}
			byUUID[group.QueueName] = entry
			order = append(order, group.QueueName)
		}
		need, err := admission.JobNeed(group.ResourceCPURequest, group.ResourceMemoryLimit)
		if err != nil {
			need, _ = admission.JobNeed("", "")
		}
		entry.Count += group.Count
		entry.CPURequestMillicores += need.CPUMillicores * group.Count
		entry.MemoryLimitBytes += need.MemoryBytes * group.Count
		if oldest := group.OldestCreatedAt.UTC(); entry.OldestCreatedAt == nil || oldest.Before(*entry.OldestCreatedAt) {
			entry.OldestCreatedAt = &oldest
		}
	}

	snapshot := Snapshot{GeneratedAt: now, Queues: make([]QueueBacklog, 0, len(order))}
	byChar := map[[2]string]*CharacteristicBacklog{}
	for _, queueUUID := range order {
		entry := byUUID[queueUUID]
		if entry.OldestCreatedAt != nil {
			entry.OldestAgeSeconds = int64(now.Sub(*entry.OldestCreatedAt) / time.Second)
			if entry.OldestAgeSeconds < 0 {
				entry.OldestAgeSeconds = 0
			}
		}
		snapshot.Queues = append(snapshot.Queues, *entry)
		snapshot.Totals.Count += entry.Count
		snapshot.Totals.CPURequestMillicores += entry.CPURequestMillicores
		snapshot.Totals.MemoryLimitBytes += entry.MemoryLimitBytes
		snapshot.Totals.OldestAgeSeconds = max(snapshot.Totals.OldestAgeSeconds, entry.OldestAgeSeconds)
		for key, value := range entry.Characteristics {
			id := [2]string{key, value}
			agg := byChar[id]
			if agg == nil {
				agg = &CharacteristicBacklog{Key: key, Value: value}
				byChar[id] = agg
			}
			agg.Queues++
			agg.Count += entry.Count
			agg.CPURequestMillicores += entry.CPURequestMillicores
			agg.MemoryLimitBytes += entry.MemoryLimitBytes
			agg.OldestAgeSeconds = max(agg.OldestAgeSeconds, entry.OldestAgeSeconds)
		}
	}
	sort.SliceStable(snapshot.Queues, func(i, j int) bool {
		a, b := snapshot.Queues[i], snapshot.Queues[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.QueueUUID < b.QueueUUID
	})
	snapshot.ByCharacteristic = make([]CharacteristicBacklog, 0, len(byChar))
	for _, agg := range byChar {
		snapshot.ByCharacteristic = append(snapshot.ByCharacteristic, *agg)
	}
	sort.Slice(snapshot.ByCharacteristic, func(i, j int) bool {
		a, b := snapshot.ByCharacteristic[i], snapshot.ByCharacteristic[j]
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Value < b.Value
	})
	return snapshot
}

// characteristicStrings renders queue characteristics as strings. Queue
// values are always scalar.
func characteristicStrings(c characteristics.Characteristics) map[string]string {
	out := make(map[string]string, len(c))
	for key, value := range c {
		switch v := value.(type) {
		case characteristics.StringValue:
			out[key] = string(v)
		case characteristics.IntValue:
			out[key] = strconv.FormatInt(int64(v), 10)
		case characteristics.BoolValue:
			out[key] = strconv.FormatBool(bool(v))
		case nil:
		default:
			out[key] = v.String()
		}
	}
	return out
}
