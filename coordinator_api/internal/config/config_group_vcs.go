package config

import (
	"time"

	"github.com/catalystcommunity/app-utils-go/env"
)

// Groups linked to VCS teams (docs/principals.md, plan item B3).
var (
	// GroupVCSSyncInterval is the time between two scheduled syncs of one
	// linked team. "0" stops the sync loop: then only `groups sync` syncs.
	GroupVCSSyncInterval = groupVCSDuration(env.GetEnvOrDefault("REACTORCIDE_GROUP_VCS_SYNC_INTERVAL", "15m"), 15*time.Minute)
	// GroupVCSSyncMaxStale removes the synced members of a linked team when
	// no sync succeeded for this long.
	GroupVCSSyncMaxStale = groupVCSDuration(env.GetEnvOrDefault("REACTORCIDE_GROUP_VCS_SYNC_MAX_STALE", "24h"), 24*time.Hour)
)

func groupVCSDuration(value string, fallback time.Duration) time.Duration {
	if value == "0" {
		return 0
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}
