package workerapi

import (
	"context"
	"unicode/utf8"

	"github.com/catalystcommunity/app-utils-go/logging"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imageref"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// Limits of a heartbeat image set report. A report over a limit is not
// stored. The worker sends the report again on its next heartbeat.
const (
	maxImageReportDigests   = 1024
	maxImageReportDetailLen = 2048
)

// imageSetReportState converts a heartbeat image set report to the worker
// image state that the store records. ok is false when the report is not
// valid: an unknown state, a negative version, a digest that is not
// "sha256:<64 lowercase hex>", or too many digests. A long detail is cut.
func imageSetReportState(report *csilapi.ImageSetReport) (models.WorkerImageState, bool) {
	state := models.WorkerImageState{State: report.State, Version: report.Version}
	switch report.State {
	case models.WorkerImageStateUpdating, models.WorkerImageStateUpdated, models.WorkerImageStateFailed:
	default:
		return state, false
	}
	if report.Version < 0 || len(report.ReadyDigests) > maxImageReportDigests {
		return state, false
	}
	seen := make(map[string]bool, len(report.ReadyDigests))
	state.ReadyDigests = make([]string, 0, len(report.ReadyDigests))
	for _, digest := range report.ReadyDigests {
		if !imageref.ValidDigest(digest) {
			return state, false
		}
		if !seen[digest] {
			seen[digest] = true
			state.ReadyDigests = append(state.ReadyDigests, digest)
		}
	}
	if report.Detail != nil {
		state.Detail = cutReportDetail(*report.Detail, maxImageReportDetailLen)
	}
	if len(report.Failed) > maxImageReportDigests {
		return state, false
	}
	failedSeen := make(map[string]bool, len(report.Failed))
	for _, f := range report.Failed {
		if !imageref.ValidDigest(f.Digest) {
			return state, false
		}
		if failedSeen[f.Digest] {
			continue
		}
		failedSeen[f.Digest] = true
		state.Failed = append(state.Failed, models.ImageFailure{Digest: f.Digest, Permanent: f.Permanent, Detail: cutReportDetail(f.Detail, maxImageFailureDetailLen)})
	}
	return state, true
}

// maxImageFailureDetailLen limits the detail of one failure.
const maxImageFailureDetailLen = 512

func cutReportDetail(detail string, limit int) string {
	if len(detail) > limit {
		detail = detail[:limit]
		for !utf8.ValidString(detail) {
			detail = detail[:len(detail)-1]
		}
	}
	return detail
}

// recordImageSetReport stores the image state of a heartbeat in the worker
// image columns (migration 000068). It never fails the heartbeat: an
// invalid report or a store error is logged, and the next heartbeat sends
// the report again.
func (s *WorkerService) recordImageSetReport(ctx context.Context, wkr *models.Worker, report *csilapi.ImageSetReport) {
	images, ok := s.deps.Store.(store.WorkerImageStore)
	if !ok {
		return
	}
	state, valid := imageSetReportState(report)
	if !valid {
		logging.Log.WithField("worker_id", wkr.WorkerID).WithField("state", report.State).
			Warn("Worker sent an image set report that is not valid; not stored")
		return
	}
	if err := images.UpdateWorkerImageState(ctx, wkr.WorkerID, state); err != nil {
		logging.Log.WithError(err).WithField("worker_id", wkr.WorkerID).Warn("Failed to store worker image state")
	}
}
