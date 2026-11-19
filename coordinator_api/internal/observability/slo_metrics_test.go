package observability

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// sloFake serves the reads of SLOStatuses: heartbeats for availability, and
// nothing else (no rollups yet).
type sloFake struct {
	store.ObservabilityStore
	orgs    []models.Organization
	beats   []models.HeartbeatHour
	targets map[string][]models.SLOTarget
	fail    bool
	reads   int
}

func (f *sloFake) ListOrganizations(_ context.Context, limit, offset int) ([]models.Organization, error) {
	f.reads++
	if f.fail {
		return nil, errors.New("database down")
	}
	if offset >= len(f.orgs) {
		return nil, nil
	}
	return f.orgs[offset:min(len(f.orgs), offset+limit)], nil
}
func (f *sloFake) ListSLOTargets(_ context.Context, orgID string) ([]models.SLOTarget, error) {
	return f.targets[orgID], nil
}
func (f *sloFake) ListEvalRollups(context.Context, string, store.RollupFilter) ([]models.EvalRollup, error) {
	return nil, nil
}
func (f *sloFake) ListJobRollups(context.Context, string, store.RollupFilter) ([]models.JobRollup, error) {
	return nil, nil
}
func (f *sloFake) ListHeartbeats(context.Context, time.Time, time.Time) ([]models.HeartbeatHour, error) {
	return f.beats, nil
}

func collectLines(t *testing.T, c prometheus.Collector) []string {
	t.Helper()
	ch := make(chan prometheus.Metric, 200)
	c.Collect(ch)
	close(ch)
	var out []string
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatal(err)
		}
		var labels []string
		for _, l := range pb.GetLabel() {
			labels = append(labels, l.GetName()+"="+l.GetValue())
		}
		name := m.Desc().String()
		name = name[strings.Index(name, `fqName: "`)+9:]
		name = name[:strings.Index(name, `"`)]
		out = append(out, name+"{"+strings.Join(labels, ",")+"} "+strconv.FormatFloat(pb.GetGauge().GetValue(), 'f', -1, 64))
	}
	sort.Strings(out)
	return out
}

func TestSLOCollector(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 30, 0, 0, time.UTC)
	f := &sloFake{
		orgs: []models.Organization{{OrgID: "o1", Name: "acme", Status: "active"}, {OrgID: "o2", Name: "gone", Status: "deleted"}},
		// Every minute of the last 2 hours is up.
		beats:   []models.HeartbeatHour{{HourStart: now.Truncate(time.Hour).Add(-time.Hour), UpMask: -1}, {HourStart: now.Truncate(time.Hour), UpMask: -1}},
		targets: map[string][]models.SLOTarget{"o1": {{OrgID: "o1", SLI: models.SLIQueueWait, Objective: 0.9, ThresholdSeconds: 900, WindowDays: 7}}},
	}
	c := &SLOCollector{Source: f, Now: func() time.Time { return now }}
	lines := collectLines(t, c)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"reactorcide_slo_objective_ratio{lane=,org=acme,sli=availability} 0.995",
		"reactorcide_slo_compliance_ratio{lane=,org=acme,sli=availability} 1",
		"reactorcide_slo_objective_ratio{lane=,org=acme,sli=queue_wait} 0.9",
		"reactorcide_slo_threshold_seconds{lane=,org=acme,sli=queue_wait} 900",
		"reactorcide_slo_window_days{lane=,org=acme,sli=queue_wait} 7",
		"reactorcide_slo_error_budget_remaining_ratio{lane=,org=acme,sli=queue_wait} 1",
		"reactorcide_slo_last_computed_timestamp_seconds{} ",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("no %s in\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "org=gone") {
		t.Fatal("an organization that is not active has SLO series")
	}
	for _, absent := range []string{
		"reactorcide_slo_compliance_ratio{lane=,org=acme,sli=queue_wait}", // no data
		"reactorcide_slo_p95_seconds{lane=,org=acme,sli=queue_wait}",      // no data
		"reactorcide_slo_threshold_seconds{lane=,org=acme,sli=availability}",
	} {
		if strings.Contains(joined, absent) {
			t.Fatalf("unexpected %s in\n%s", absent, joined)
		}
	}

	// The cache: a second scrape in the same minute does not read again,
	// and a failure later keeps the old values.
	reads := f.reads
	collectLines(t, c)
	if f.reads != reads {
		t.Fatal("a scrape within CacheFor computed again")
	}
	now = now.Add(2 * time.Minute)
	f.fail = true
	if got := collectLines(t, c); len(got) != len(lines) {
		t.Fatalf("a failed computation changed the series: %d, want %d", len(got), len(lines))
	}
}

func TestSLOCollectorNoDataBeforeFirstComputation(t *testing.T) {
	c := &SLOCollector{Source: &sloFake{fail: true}}
	if got := collectLines(t, c); len(got) != 0 {
		t.Fatalf("got %v before any successful computation", got)
	}
}
