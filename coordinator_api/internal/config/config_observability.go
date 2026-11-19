package config

import "github.com/catalystcommunity/app-utils-go/env"

// Rollups, alerts, and SLOs (docs/observability.md).
var (
	// ObservabilityEnabled starts the rollup loop (lease
	// "observability-rollups"): rollups, alert evaluation, availability
	// heartbeats, and the job and eval Prometheus histograms.
	ObservabilityEnabled = env.GetEnvAsBoolOrDefault("REACTORCIDE_OBSERVABILITY_ENABLED", "true")
	// ObservabilityIntervalSeconds is the time between rollup passes.
	ObservabilityIntervalSeconds = env.GetEnvAsIntOrDefault("REACTORCIDE_OBSERVABILITY_INTERVAL_SECONDS", "60")
	// ObservabilityAlertIntervalSeconds is the time between alert evaluations.
	ObservabilityAlertIntervalSeconds = env.GetEnvAsIntOrDefault("REACTORCIDE_OBSERVABILITY_ALERT_INTERVAL_SECONDS", "300")
	// RollupHourlyRetentionDays keeps hourly rollups this many days.
	RollupHourlyRetentionDays = env.GetEnvAsIntOrDefault("REACTORCIDE_ROLLUP_HOURLY_RETENTION_DAYS", "35")
	// RollupDailyRetentionDays keeps daily rollups, heartbeats, and alert
	// transitions this many days.
	RollupDailyRetentionDays = env.GetEnvAsIntOrDefault("REACTORCIDE_ROLLUP_DAILY_RETENTION_DAYS", "400")
	// MetricsOwnerGroupLabelLimit is the number of distinct owner_group
	// label values. Later groups are "other".
	MetricsOwnerGroupLabelLimit = env.GetEnvAsIntOrDefault("REACTORCIDE_METRICS_OWNER_GROUP_LABEL_LIMIT", "20")
)
