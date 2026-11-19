package observability

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func onToken(pool, token string, active, session bool) models.Worker {
	return models.Worker{PoolID: pool, EnrollmentTokenID: &token, EnrollmentTokenActive: &active, HasActiveSession: session}
}

func TestCountOnDeactivatedTokens(t *testing.T) {
	workers := []models.Worker{
		onToken("p1", "old", false, true),
		onToken("p1", "old", false, true),
		onToken("p1", "old", false, false), // stopped: no warning
		onToken("p1", "new", true, true),
		onToken("p2", "old2", false, true),
		{PoolID: "p1"}, // no recorded token
	}
	got := CountOnDeactivatedTokens(workers)
	want := []DeactivatedTokenCount{{PoolID: "p1", TokenID: "old", Count: 2}, {PoolID: "p2", TokenID: "old2", Count: 1}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

type workerListerFunc func() []models.Worker

func (f workerListerFunc) ListWorkers(_ context.Context, _ *string) ([]models.Worker, error) {
	return f(), nil
}

func TestEnrollmentTokenCollector(t *testing.T) {
	c := &EnrollmentTokenCollector{Lister: workerListerFunc(func() []models.Worker {
		return []models.Worker{onToken("p1", "old", false, true), onToken("p1", "new", true, true)}
	})}
	ch := make(chan prometheus.Metric, 10)
	c.Collect(ch)
	close(ch)
	var lines []string
	for m := range ch {
		var out dto.Metric
		if err := m.Write(&out); err != nil {
			t.Fatal(err)
		}
		labels := map[string]string{}
		for _, l := range out.GetLabel() {
			labels[l.GetName()] = l.GetValue()
		}
		lines = append(lines, labels["pool_id"]+"/"+labels["enrollment_token_id"]+"="+strconv.FormatFloat(out.GetGauge().GetValue(), 'f', -1, 64))
	}
	if strings.Join(lines, ",") != "p1/old=1" {
		t.Fatalf("collected %v, want p1/old=1", lines)
	}
}
