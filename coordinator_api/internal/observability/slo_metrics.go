package observability

import (
	"context"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// SLO analysis gauges. Reactorcide does not alert on SLOs: an observability
// tool alerts on these gauges. Each coordinator replica computes them from
// the rollups (at most once per CacheFor), so all replicas give the same
// value: aggregate with max, not sum.
var (
	sloLabels         = []string{"org", "sli", "lane"}
	sloObjectiveDesc  = prometheus.NewDesc("reactorcide_slo_objective_ratio", "SLO objective: the good fraction that the target asks for", sloLabels, nil)
	sloComplianceDesc = prometheus.NewDesc("reactorcide_slo_compliance_ratio", "SLO compliance over the window: good / total (no series when there is no data)", sloLabels, nil)
	sloBudgetDesc     = prometheus.NewDesc("reactorcide_slo_error_budget_remaining_ratio", "Error budget left over the window: 1 - bad / ((1 - objective) x total); 0 or less is exhausted", sloLabels, nil)
	sloGoodDesc       = prometheus.NewDesc("reactorcide_slo_window_good", "Good events (or up minutes for availability) in the SLO window", sloLabels, nil)
	sloTotalDesc      = prometheus.NewDesc("reactorcide_slo_window_total", "All events (or minutes for availability) in the SLO window", sloLabels, nil)
	sloThresholdDesc  = prometheus.NewDesc("reactorcide_slo_threshold_seconds", "Latency threshold of a good event (eval_latency and queue_wait only)", sloLabels, nil)
	sloP95Desc        = prometheus.NewDesc("reactorcide_slo_p95_seconds", "Estimated 95th percentile over the window (eval_latency and queue_wait only)", sloLabels, nil)
	sloWindowDesc     = prometheus.NewDesc("reactorcide_slo_window_days", "Length of the SLO window", sloLabels, nil)
	sloLastDesc       = prometheus.NewDesc("reactorcide_slo_last_computed_timestamp_seconds", "Time of the last successful SLO computation of this replica", nil, nil)
)

// SLOSource is what the SLO collector reads: the rollups and the
// organizations.
type SLOSource interface {
	store.ObservabilityStore
	ListOrganizations(ctx context.Context, limit, offset int) ([]models.Organization, error)
}

// SLOCollector exports the SLO statuses of each active organization.
type SLOCollector struct {
	Source   SLOSource
	CacheFor time.Duration
	Timeout  time.Duration
	Now      func() time.Time

	mu       sync.Mutex
	computed time.Time
	rows     []sloRow
}

type sloRow struct {
	org    string
	status SLOStatus
}

// Describe implements prometheus.Collector.
func (c *SLOCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{sloObjectiveDesc, sloComplianceDesc, sloBudgetDesc, sloGoodDesc, sloTotalDesc, sloThresholdDesc, sloP95Desc, sloWindowDesc, sloLastDesc} {
		ch <- d
	}
}

func (c *SLOCollector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}

// rowsFor returns the cached rows, and computes them again when they are
// older than CacheFor. A failed computation keeps the old rows, so a short
// database error does not remove the series.
func (c *SLOCollector) rowsFor() ([]sloRow, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cacheFor := c.CacheFor
	if cacheFor <= 0 {
		cacheFor = time.Minute
	}
	now := c.now()
	if !c.computed.IsZero() && now.Sub(c.computed) < cacheFor {
		return c.rows, c.computed
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	rows, err := ComputeSLORows(ctx, c.Source, now)
	if err != nil {
		logging.Log.WithError(err).Warn("observability: SLO metrics computation failed; keeping the previous values")
		return c.rows, c.computed
	}
	c.rows, c.computed = rows, now
	return rows, now
}

// ComputeSLORows computes the SLO statuses of each active organization.
func ComputeSLORows(ctx context.Context, src SLOSource, now time.Time) ([]sloRow, error) {
	var rows []sloRow
	const page = 200
	for offset := 0; ; offset += page {
		orgs, err := src.ListOrganizations(ctx, page, offset)
		if err != nil {
			return nil, err
		}
		for _, org := range orgs {
			if org.Status != "" && org.Status != "active" {
				continue
			}
			statuses, err := SLOStatuses(ctx, src, store.RollupFilter{OrgID: org.OrgID, AllProjects: true}, now)
			if err != nil {
				return nil, err
			}
			for _, s := range statuses {
				rows = append(rows, sloRow{org: org.Name, status: s})
			}
		}
		if len(orgs) < page {
			return rows, nil
		}
	}
}

// Collect implements prometheus.Collector.
func (c *SLOCollector) Collect(ch chan<- prometheus.Metric) {
	rows, computed := c.rowsFor()
	if computed.IsZero() {
		return
	}
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}
	for _, r := range rows {
		s := r.status
		l := []string{r.org, s.SLI, s.Lane}
		gauge(sloObjectiveDesc, s.Objective, l...)
		gauge(sloBudgetDesc, s.ErrorBudgetRemaining, l...)
		gauge(sloGoodDesc, s.Good, l...)
		gauge(sloTotalDesc, s.Total, l...)
		gauge(sloWindowDesc, float64(s.WindowDays), l...)
		if s.Compliance != nil {
			gauge(sloComplianceDesc, *s.Compliance, l...)
		}
		if s.SLI == models.SLIEvalLatency || s.SLI == models.SLIQueueWait {
			gauge(sloThresholdDesc, s.ThresholdSeconds, l...)
			if s.P95Seconds != nil && s.Total > 0 {
				gauge(sloP95Desc, *s.P95Seconds, l...)
			}
		}
	}
	gauge(sloLastDesc, float64(computed.Unix()))
}

var sloCollectorOnce sync.Once

// RegisterSLOCollector registers the SLO collector with the default registry
// one time.
func RegisterSLOCollector(src SLOSource) {
	sloCollectorOnce.Do(func() {
		_ = prometheus.Register(&SLOCollector{Source: src})
	})
}
