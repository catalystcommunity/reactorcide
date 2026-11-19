package config

import "github.com/catalystcommunity/app-utils-go/env"

// Diagnosis post limits (WP17, docs/diagnosis.md). The limit applies to each
// principal on each coordinator replica, so N replicas permit up to N times
// the rate. A rate of 0 turns the limit off.
var (
	// DiagnosisRateLimitPerMinute is the number of diagnosis posts a minute
	// that one principal can make.
	DiagnosisRateLimitPerMinute = env.GetEnvAsIntOrDefault("REACTORCIDE_DIAGNOSIS_RATE_LIMIT_PER_MINUTE", "30")
	// DiagnosisRateLimitBurst is the number of posts that one principal can
	// make at the same time before the rate applies.
	DiagnosisRateLimitBurst = env.GetEnvAsIntOrDefault("REACTORCIDE_DIAGNOSIS_RATE_LIMIT_BURST", "10")
)
