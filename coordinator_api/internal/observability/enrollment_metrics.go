package observability

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// workersOnDeactivatedTokenDesc is the count of workers that have an active
// session from an enrollment token that is now deactivated. Such a worker
// runs until it stops, and then it cannot register again. Every coordinator
// replica reports the same value: aggregate with max, not sum.
var workersOnDeactivatedTokenDesc = prometheus.NewDesc(
	"reactorcide_workers_on_deactivated_enrollment_token",
	"Workers with an active session from a deactivated enrollment token (they cannot register again after they stop)",
	[]string{"pool_id", "enrollment_token_id"}, nil,
)

// EnrollmentTokenCollector reads the workers at each scrape.
type EnrollmentTokenCollector struct {
	Lister  WorkerLister
	Timeout time.Duration
}

// Describe implements prometheus.Collector.
func (c *EnrollmentTokenCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- workersOnDeactivatedTokenDesc
}

// Collect implements prometheus.Collector. A store error gives no series.
func (c *EnrollmentTokenCollector) Collect(ch chan<- prometheus.Metric) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	workers, err := c.Lister.ListWorkers(ctx, nil)
	if err != nil {
		return
	}
	for _, row := range CountOnDeactivatedTokens(workers) {
		ch <- prometheus.MustNewConstMetric(workersOnDeactivatedTokenDesc, prometheus.GaugeValue, float64(row.Count), row.PoolID, row.TokenID)
	}
}

// DeactivatedTokenCount is the count of workers of one pool on one
// deactivated enrollment token.
type DeactivatedTokenCount struct {
	PoolID  string
	TokenID string
	Count   int
}

// CountOnDeactivatedTokens groups the workers on deactivated enrollment
// tokens by pool and token, sorted.
func CountOnDeactivatedTokens(workers []models.Worker) []DeactivatedTokenCount {
	counts := map[[2]string]int{}
	for i := range workers {
		w := &workers[i]
		if !w.OnDeactivatedEnrollmentToken() || w.EnrollmentTokenID == nil {
			continue
		}
		counts[[2]string{w.PoolID, *w.EnrollmentTokenID}]++
	}
	out := make([]DeactivatedTokenCount, 0, len(counts))
	for k, n := range counts {
		out = append(out, DeactivatedTokenCount{PoolID: k[0], TokenID: k[1], Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PoolID != out[j].PoolID {
			return out[i].PoolID < out[j].PoolID
		}
		return out[i].TokenID < out[j].TokenID
	})
	return out
}

var enrollmentCollectorOnce sync.Once

// RegisterEnrollmentTokenCollector registers the collector with the default
// registry one time.
func RegisterEnrollmentTokenCollector(lister WorkerLister) {
	enrollmentCollectorOnce.Do(func() {
		_ = prometheus.Register(&EnrollmentTokenCollector{Lister: lister})
	})
}
