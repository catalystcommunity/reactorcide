package handlers

import (
	"context"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/lease"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/observability"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
)

var observabilityOnce sync.Once

// startObservabilityOnce runs the rollup loop on the replica that holds the
// "observability-rollups" lease (docs/observability.md).
func startObservabilityOnce(appStore store.Store) {
	if !config.ObservabilityEnabled {
		return
	}
	obs, ok := appStore.(store.ObservabilityStore)
	if !ok {
		return
	}
	leases, ok := appStore.(store.LeaseStore)
	if !ok {
		return
	}
	observabilityOnce.Do(func() {
		observability.RegisterMetrics()
		day := 24 * time.Hour
		loop := &observability.Loop{
			Store: obs,
			Config: observability.Config{
				Interval:             time.Duration(config.ObservabilityIntervalSeconds) * time.Second,
				AlertEvery:           time.Duration(config.ObservabilityAlertIntervalSeconds) * time.Second,
				HourlyRetention:      time.Duration(config.RollupHourlyRetentionDays) * day,
				DailyRetention:       time.Duration(config.RollupDailyRetentionDays) * day,
				OwnerGroupLabelLimit: config.MetricsOwnerGroupLabelLimit,
			},
			Alerts:  &observability.Evaluator{Store: obs, Owners: appStore, UIBaseURL: config.VCSBaseURL},
			Observe: true,
		}
		go lease.NewManager(leases).Run(context.Background(), observability.LeaseName, loop.Run)
	})
}
