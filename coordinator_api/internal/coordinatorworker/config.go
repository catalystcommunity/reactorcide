// Package coordinatorworker is the coordinator-mediated worker run loop
// (WORKERS_PLAN.md Wave 3, P3a): Register -> RequestJob -> run via the
// existing internal/worker.JobRunner -> stream logs to the coordinator via
// AppendLogs -> ReportResult, with a concurrent Heartbeat that renews the
// worker session and applies cancel/kill directives. It is deliberately
// additive: cmd/worker.go and internal/worker/corndogs_worker.go (the
// legacy direct-corndogs path) are untouched here -- wiring this package
// into the worker binary and removing the legacy path is P3b.
//
// VCS checkout credentials (WORKERS_PLAN.md Wave 3 P3c): when a lease
// carries a coordinator-resolved vcs_auth (see
// internal/workerapi/vcs_auth.go for how the coordinator decides whether to
// populate it), runLease (see lease.go/vcs_auth.go) writes per-job git
// credential material into the lease's ephemeral workspace and registers
// the token with the masker before spawning, mirroring the deleted
// internal/worker/vcs_checkout_auth.go's approach exactly, just fed from the
// lease's already-resolved (url, username, token) tuple instead of
// resolving credentials itself.
package coordinatorworker

import (
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
)

// Config configures a single coordinator-mediated worker run loop instance.
// One Config/Run call corresponds to one worker process (one worker_key,
// one CSIL-RPC session at a time).
type Config struct {
	// CoordinatorURL is the coordinator's base URL, e.g.
	// "https://coordinator.example.com". The CSIL-RPC path
	// ("/csil/v1/rpc") is appended by the transport.
	CoordinatorURL string

	// EnrollmentToken is the durable, per-pool credential presented to
	// Register. It is never logged and never persisted by this package --
	// the caller (P3b's cmd/worker.go) owns how it is sourced/stored.
	EnrollmentToken string

	// WorkerKey is a stable, worker-provided identifier (generated and
	// persisted locally by the caller, like a host GUID) that Register
	// upserts the coordinator's workers row by. Required.
	WorkerKey string

	// Hostname is optional identifying metadata sent on Register.
	Hostname string

	// OS and Arch are this worker's characteristic values, taken verbatim
	// (no normalization -- WORKERS_PLAN.md "Characteristics & matching").
	// Both are required; auto-detection is the caller's responsibility.
	OS   string
	Arch string

	// Custom is this worker's free-form custom characteristics. Values must
	// be a Go string/int-family/bool scalar, or a homogeneous slice of one
	// of those (see toCharacteristicValue).
	Custom []characteristics.KV

	// WorkerVersion is optional identifying metadata sent on Register.
	WorkerVersion string

	// ContainerRuntime selects the JobRunner backend ("docker",
	// "containerd", "kubernetes", "auto", or "" which behaves like "auto").
	// See internal/worker.NewJobRunner.
	ContainerRuntime string

	// Concurrency bounds how many leases this worker runs at once. Values
	// less than 1 are treated as 1.
	Concurrency int

	// WorkspaceRoot is the base directory under which each lease's ephemeral
	// job workspace is created (bind-mounted into the job container as /job).
	// When the worker itself runs in a container and drives a host container
	// runtime (docker-out-of-docker / a shared containerd socket), this MUST
	// be a path that resolves identically inside the worker and on the host
	// (e.g. a `-v /tmp/reactorcide-jobs:/tmp/reactorcide-jobs` bind mount) --
	// otherwise the host runtime cannot stat the bind-mount source and
	// container creation fails. Empty means the OS temp dir (correct for a
	// bare-metal worker and for run-local, where there is no namespace split).
	WorkspaceRoot string

	// DataDir holds the worker identity and its same-instance telemetry spool.
	DataDir string

	// MetricsInterval controls CPU and memory sampling. TelemetrySendInterval
	// controls batch flushes. Non-positive values use safe defaults.
	MetricsInterval       time.Duration
	TelemetrySendInterval time.Duration

	// VMImagePrefetch lists OCI VM image references to pull before this worker
	// registers with the coordinator.
	VMImagePrefetch []string

	// VMImageMaxUnused controls cache retention. The worker prunes once at
	// startup and then at VMImagePruneInterval.
	VMImageMaxUnused     time.Duration
	VMImagePruneInterval time.Duration
}

// defaultHeartbeatInterval is used only if Register's response carries a
// non-positive heartbeat_interval (shouldn't happen against a well-behaved
// coordinator, but keeps the heartbeat loop's ticker construction safe).
const defaultHeartbeatInterval = 30 * time.Second

// noLeaseBackoff is the fixed pause between RequestJob calls that return
// has_lease=false ("nothing to do right now" is the overwhelmingly common
// response and not itself an error, so a short fixed backoff -- not an
// escalating one -- keeps the worker responsive to new work).
const noLeaseBackoff = 2 * time.Second

// reconnectBackoffMin/Max bound the escalating backoff applied when a
// coordinator call fails outright (network error, non-2xx, undecodable
// response, or a ServiceError other than the ones RequestJob already turns
// into has_lease=false). Escalating (vs. noLeaseBackoff's fixed pause)
// because a failing coordinator connection is the case where hammering it
// is actually harmful.
const (
	reconnectBackoffMin = 1 * time.Second
	reconnectBackoffMax = 30 * time.Second
)

func nextBackoff(cur, maxBackoff time.Duration) time.Duration {
	next := cur * 2
	if next > maxBackoff {
		next = maxBackoff
	}
	return next
}
