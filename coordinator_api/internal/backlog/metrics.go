package backlog

import (
	"context"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/prometheus/client_golang/prometheus"
)

// RefreshInterval is how often each coordinator replica reads the backlog
// for its Prometheus gauges. Every replica exports the same values; use max()
// or one replica in a query, never sum().
const RefreshInterval = 15 * time.Second

var (
	queueLabels = []string{"queue", "queue_name"}

	backlogJobsDesc = prometheus.NewDesc("reactorcide_queue_backlog_jobs",
		"Jobs that wait for a worker, by queue (queue is the queue UUID, queue_name the display name)", queueLabels, nil)
	backlogOldestDesc = prometheus.NewDesc("reactorcide_queue_backlog_oldest_age_seconds",
		"Wait time of the oldest job that waits for a worker, by queue", queueLabels, nil)
	backlogCPUDesc = prometheus.NewDesc("reactorcide_queue_backlog_cpu_request_cores",
		"Sum of the CPU requests of the jobs that wait for a worker, by queue", queueLabels, nil)
	backlogMemoryDesc = prometheus.NewDesc("reactorcide_queue_backlog_memory_limit_bytes",
		"Sum of the memory limits of the jobs that wait for a worker, by queue", queueLabels, nil)
	backlogUpdatedDesc = prometheus.NewDesc("reactorcide_queue_backlog_updated_timestamp_seconds",
		"Time of the last successful backlog read of this coordinator replica", nil, nil)
)

// Collector exports the newest backlog snapshot as Prometheus gauges. The
// labels are the queue UUID and its display name only, so the series count
// is the queue count.
type Collector struct {
	mu       sync.RWMutex
	snapshot *Snapshot
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- backlogJobsDesc
	ch <- backlogOldestDesc
	ch <- backlogCPUDesc
	ch <- backlogMemoryDesc
	ch <- backlogUpdatedDesc
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	snapshot := c.snapshot
	c.mu.RUnlock()
	if snapshot == nil {
		return
	}
	for _, queue := range snapshot.Queues {
		labels := []string{queue.QueueUUID, queue.DisplayName}
		ch <- prometheus.MustNewConstMetric(backlogJobsDesc, prometheus.GaugeValue, float64(queue.Count), labels...)
		ch <- prometheus.MustNewConstMetric(backlogOldestDesc, prometheus.GaugeValue, float64(queue.OldestAgeSeconds), labels...)
		ch <- prometheus.MustNewConstMetric(backlogCPUDesc, prometheus.GaugeValue, float64(queue.CPURequestMillicores)/1000, labels...)
		ch <- prometheus.MustNewConstMetric(backlogMemoryDesc, prometheus.GaugeValue, float64(queue.MemoryLimitBytes), labels...)
	}
	ch <- prometheus.MustNewConstMetric(backlogUpdatedDesc, prometheus.GaugeValue, float64(snapshot.GeneratedAt.Unix()))
}

// Set replaces the exported snapshot.
func (c *Collector) Set(snapshot Snapshot) {
	c.mu.Lock()
	c.snapshot = &snapshot
	c.mu.Unlock()
}

// Refresh reads the backlog once and exports it. A failed read keeps the
// previous snapshot; the updated timestamp shows its age.
func (c *Collector) Refresh(ctx context.Context, source Source) error {
	readCtx, cancel := context.WithTimeout(ctx, RefreshInterval)
	defer cancel()
	snapshot, err := Load(readCtx, source, nil, time.Now())
	if err != nil {
		return err
	}
	c.Set(snapshot)
	return nil
}

// Run refreshes the collector until ctx ends.
func (c *Collector) Run(ctx context.Context, source Source) {
	ticker := time.NewTicker(RefreshInterval)
	defer ticker.Stop()
	for {
		if err := c.Refresh(ctx, source); err != nil && ctx.Err() == nil {
			logging.Log.WithError(err).Warn("Failed to refresh queue backlog metrics")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

var startOnce sync.Once

// StartMetrics registers a Collector with the default Prometheus registry
// and refreshes it in the background. It runs at most once per process. A
// nil source does nothing.
func StartMetrics(source Source) {
	if source == nil {
		return
	}
	startOnce.Do(func() {
		collector := &Collector{}
		if err := prometheus.DefaultRegisterer.Register(collector); err != nil {
			logging.Log.WithError(err).Warn("Failed to register queue backlog metrics")
			return
		}
		go collector.Run(context.Background(), source)
	})
}
