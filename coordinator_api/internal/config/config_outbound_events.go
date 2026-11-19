package config

import "github.com/catalystcommunity/app-utils-go/env"

// Outbound events (docs/outbound-events.md). HTTP sinks need the
// --allow-insecure-transport command-line option; no environment variable
// can permit them.
var (
	// OutboundEventsEnabled starts the dispatcher and the delivery loop. The
	// outbox triggers record events in all cases.
	OutboundEventsEnabled = env.GetEnvAsBoolOrDefault("REACTORCIDE_OUTBOUND_EVENTS_ENABLED", "true")
	// OutboundEventsAllowedNetworks is a comma-separated list of CIDR
	// prefixes (or addresses) that a webhook can reach although they are
	// loopback, private, link-local, or reserved. Empty by default: only
	// public addresses.
	OutboundEventsAllowedNetworks = env.GetEnvOrDefault("REACTORCIDE_OUTBOUND_EVENTS_ALLOWED_NETWORKS", "")
	// OutboundEventsMaxAttempts is the attempt limit of a delivery when the
	// subscription does not set one. After it the delivery is dead_letter.
	OutboundEventsMaxAttempts = env.GetEnvAsIntOrDefault("REACTORCIDE_OUTBOUND_EVENTS_MAX_ATTEMPTS", "12")
	// OutboundEventsRetentionDays deletes dispatched events and their final
	// deliveries after this many days. 0 keeps them.
	OutboundEventsRetentionDays = env.GetEnvAsIntOrDefault("REACTORCIDE_OUTBOUND_EVENTS_RETENTION_DAYS", "14")
	// OutboundEventsDeliveryWorkers is the number of concurrent sends on
	// each coordinator replica.
	OutboundEventsDeliveryWorkers = env.GetEnvAsIntOrDefault("REACTORCIDE_OUTBOUND_EVENTS_DELIVERY_WORKERS", "4")
	// OutboundEventsLagMillis is how old an outbox row must be before the
	// dispatcher reads it, so that rows of one workflow deliver in the order
	// in which their transactions started. 0 uses the default (2000).
	OutboundEventsLagMillis = env.GetEnvAsIntOrDefault("REACTORCIDE_OUTBOUND_EVENTS_LAG_MS", "2000")
	// OutboundEventsTimeoutSeconds is the timeout of one webhook send.
	OutboundEventsTimeoutSeconds = env.GetEnvAsIntOrDefault("REACTORCIDE_OUTBOUND_EVENTS_TIMEOUT_SECONDS", "10")
)
