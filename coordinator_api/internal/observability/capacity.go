package observability

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/backlog"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// WorkerCounts are the counts of registered workers.
type WorkerCounts struct {
	Total       int `json:"total"`
	Active      int `json:"active"`
	Quarantined int `json:"quarantined"`
	Disabled    int `json:"disabled"`
	// Seen is the count of active workers seen in the last 5 minutes.
	Seen int `json:"seen"`
}

// CountWorkers counts workers by status.
func CountWorkers(workers []models.Worker, now time.Time) WorkerCounts {
	var c WorkerCounts
	for _, w := range workers {
		c.Total++
		switch w.Status {
		case "active":
			c.Active++
			if w.LastSeenAt != nil && now.Sub(*w.LastSeenAt) <= 5*time.Minute {
				c.Seen++
			}
		case "quarantined":
			c.Quarantined++
		case "disabled":
			c.Disabled++
		}
	}
	return c
}

// QueueBacklog is the waiting work of one queue (WP13a backlog).
type QueueBacklog struct {
	QueueUUID        string `json:"queue_uuid"`
	DisplayName      string `json:"display_name"`
	Count            int64  `json:"count"`
	OldestAgeSeconds int64  `json:"oldest_age_seconds"`
}

// Capacity is the live capacity part of the CI owner dashboard.
type Capacity struct {
	Workers              WorkerCounts   `json:"workers"`
	BacklogJobs          int64          `json:"backlog_jobs"`
	BacklogOldestSeconds int64          `json:"backlog_oldest_age_seconds"`
	Queues               []QueueBacklog `json:"queues"`
}

// WorkerLister lists workers (the Postgres store).
type WorkerLister interface {
	ListWorkers(ctx context.Context, poolID *string) ([]models.Worker, error)
}

// LoadCapacity reads the backlog of the queues of an organization and the
// worker counts. A store without them gives zero values.
func LoadCapacity(ctx context.Context, appStore any, orgID string, now time.Time) Capacity {
	c := Capacity{Queues: []QueueBacklog{}}
	if lister, ok := appStore.(WorkerLister); ok {
		if workers, err := lister.ListWorkers(ctx, nil); err == nil {
			c.Workers = CountWorkers(workers, now)
		}
	}
	source := backlog.SourceFrom(appStore)
	if source == nil {
		return c
	}
	snapshot, err := backlog.Load(ctx, source, func(q models.Queue) bool { return q.OrgID == orgID }, now)
	if err != nil {
		return c
	}
	c.BacklogJobs, c.BacklogOldestSeconds = snapshot.Totals.Count, snapshot.Totals.OldestAgeSeconds
	for _, q := range snapshot.Queues {
		c.Queues = append(c.Queues, QueueBacklog{QueueUUID: q.QueueUUID, DisplayName: q.DisplayName, Count: q.Count, OldestAgeSeconds: q.OldestAgeSeconds})
	}
	return c
}
