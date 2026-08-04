package jobtelemetry

import (
	"context"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// PruneBefore deletes telemetry for terminal jobs that completed before the
// cutoff. A lookup error keeps the data. This makes a database outage safe.
func PruneBefore(ctx context.Context, objectStore objects.ObjectStore, cutoff time.Time, getJob func(context.Context, string) (*models.Job, error)) (int, error) {
	jobIDs := map[string]bool{}
	for _, prefix := range []string{ObjectPrefix + "/", "logs/"} {
		infos, err := objectStore.List(ctx, prefix)
		if err != nil {
			return 0, err
		}
		for _, info := range infos {
			rest := strings.TrimPrefix(info.Key, prefix)
			if jobID, _, ok := strings.Cut(rest, "/"); ok && jobID != "" {
				jobIDs[jobID] = true
			}
		}
	}
	deleted := 0
	for jobID := range jobIDs {
		job, err := getJob(ctx, jobID)
		if err != nil || job == nil || job.CompletedAt == nil || !job.CompletedAt.Before(cutoff) {
			continue
		}
		if err := DeleteJob(ctx, objectStore, jobID); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}
