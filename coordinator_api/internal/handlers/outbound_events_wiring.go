package handlers

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/lease"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/outboundevents"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
)

var (
	outboundEventsOnce  sync.Once
	outboxRetentionOnce sync.Once
)

// startOutboxRetentionOnce deletes old outbox rows every hour, dispatched or
// not, on every replica. It runs also when the dispatcher is off
// (REACTORCIDE_OUTBOUND_EVENTS_ENABLED=false, or no Corndogs), so the outbox
// cannot grow without a limit. The DELETE statements are idempotent.
func startOutboxRetentionOnce(appStore store.Store) {
	pruner, ok := appStore.(outboundevents.OutboxPruner)
	if !ok || config.OutboundEventsRetentionDays <= 0 {
		return
	}
	retention := time.Duration(config.OutboundEventsRetentionDays) * 24 * time.Hour
	outboxRetentionOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				outboundevents.PruneOutbox(context.Background(), pruner, time.Now().UTC().Add(-retention))
				<-ticker.C
			}
		}()
	})
}

// buildOutboundEventsManager returns the subscription manager, or nil when
// the store has no outbound event support. Without master keys the manager
// refuses to store signing secrets.
func buildOutboundEventsManager(appStore store.Store, keys *secrets.MasterKeyManager) (*outboundevents.Manager, map[string]outboundevents.Sink) {
	events, ok := appStore.(store.OutboundEventStore)
	if !ok {
		return nil, nil
	}
	sinks, err := outboundevents.NewSinks(outboundevents.RuntimeConfig{
		AllowInsecure:   config.AllowInsecureTransport,
		AllowedNetworks: config.OutboundEventsAllowedNetworks,
		Timeout:         time.Duration(config.OutboundEventsTimeoutSeconds) * time.Second,
	})
	if err != nil {
		log.Printf("WARNING: outbound events disabled: REACTORCIDE_OUTBOUND_EVENTS_ALLOWED_NETWORKS: %v", err)
		return nil, nil
	}
	manager := &outboundevents.Manager{Store: events, Sinks: sinks}
	if lookup, ok := appStore.(outboundevents.PrincipalLookup); ok {
		manager.Principals = lookup
	}
	if keys != nil {
		manager.Crypto = keys
	}
	return manager, sinks
}

// startOutboundEventsOnce starts the dispatcher on the replica that holds
// the "outbound-events" lease, and the delivery loop on every replica. Both
// need Corndogs with the internal queue; without it, events stay in the
// outbox (and the lag gauge grows) until a coordinator with Corndogs runs.
func startOutboundEventsOnce(appStore store.Store, client corndogs.ClientInterface, keys *secrets.MasterKeyManager, sinks map[string]outboundevents.Sink) {
	if !config.OutboundEventsEnabled || sinks == nil {
		return
	}
	events, ok := appStore.(store.OutboundEventStore)
	if !ok {
		return
	}
	leases, ok := appStore.(store.LeaseStore)
	if !ok {
		return
	}
	concrete, ok := client.(*corndogs.Client)
	if !ok || concrete == nil {
		return
	}
	outboundEventsOnce.Do(func() {
		queue := concrete.InternalQueue()
		dispatcher := &outboundevents.Dispatcher{
			Store: events, Queue: queue, UIBaseURL: config.VCSBaseURL,
			Retention: time.Duration(config.OutboundEventsRetentionDays) * 24 * time.Hour,
			Lag:       time.Duration(config.OutboundEventsLagMillis) * time.Millisecond,
		}
		go lease.NewManager(leases).Run(context.Background(), outboundevents.LeaseName, dispatcher.Run)
		loop := &outboundevents.DeliveryLoop{
			Store: events, Queue: queue, Sinks: sinks, MaxAttempts: config.OutboundEventsMaxAttempts,
			Backoff: outboundevents.DefaultBackoff, Workers: config.OutboundEventsDeliveryWorkers,
		}
		if keys != nil {
			loop.Secrets = keys
		}
		go loop.Run(context.Background())
	})
}
