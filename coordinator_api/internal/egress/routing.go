package egress

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// SettingReader reads the egress control setting of a job
// (store.EgressControlStore).
type SettingReader interface {
	EffectiveEgressControl(ctx context.Context, orgID string, projectID *string, profile string) (bool, error)
}

// RouteJob adds the characteristic egress_proxy=true to the job when egress
// control is on for it (organization, project, or execution profile). The
// job queue then requires a worker with an egress proxy, so the job waits
// for such a worker instead of failing on a worker without one.
//
// Call it before the queue of the job is resolved. It is best effort: a
// store that cannot read the setting changes nothing, and the claim check
// of the coordinator (workerapi buildLeaseEgress) stays the authority. A
// job that has egress control only because of a proxy-delivered secret
// grant is not routed here; route it with a queue that requires the
// characteristic (docs/egress-proxy.md).
func RouteJob(ctx context.Context, value any, job *models.Job) {
	if job == nil {
		return
	}
	if v, ok := job.Characteristics[WorkerCharacteristic].(characteristics.BoolValue); ok && bool(v) {
		return
	}
	reader, ok := value.(SettingReader)
	if !ok {
		return
	}
	on, err := reader.EffectiveEgressControl(ctx, job.OwnershipOrgID(), job.ProjectID, job.ExecutionProfile)
	if err != nil || !on {
		return
	}
	if job.Characteristics == nil {
		job.Characteristics = characteristics.Characteristics{}
	}
	job.Characteristics[WorkerCharacteristic] = characteristics.BoolValue(true)
}
