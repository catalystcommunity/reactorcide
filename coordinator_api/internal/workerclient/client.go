package workerclient

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// Client is the typed worker-side entry point to the coordinator's
// ReactorcideWorker CSIL service. It wraps the generated
// csilapi.ReactorcideWorkerClient with a CSILRPCTransport and manages the
// worker session lifecycle: Register mints a session and stores it on the
// transport; every subsequent call (RequestJob/Heartbeat/AppendLogs/
// ReportResult) rides that stored session in the envelope "auth" field
// automatically, per WORKERS_PLAN.md's "Workers -- registration, auth,
// protocol" ("Rides the CSIL envelope auth field on every subsequent
// call"). Register itself carries the enrollment token in the request body,
// never in auth -- there is no session yet at that point.
type Client struct {
	transport *CSILRPCTransport
	raw       *csilapi.ReactorcideWorkerClient
}

// New returns a Client wired to a CSILRPCTransport at baseURL.
func New(baseURL string) *Client {
	return NewWithTransport(&CSILRPCTransport{BaseURL: baseURL})
}

// NewWithTransport returns a Client wired to a caller-supplied
// CSILRPCTransport, for tests or non-default HTTP client / header setups
// (e.g. pointing BaseURL at an httptest.Server).
func NewWithTransport(transport *CSILRPCTransport) *Client {
	return &Client{transport: transport, raw: csilapi.NewReactorcideWorkerClient(transport)}
}

// Session returns the worker session token currently stored on the
// transport (empty before the first successful Register).
func (c *Client) Session() string {
	return c.transport.Session()
}

// SetSession overrides the stored session token directly -- used when a
// caller has persisted a still-valid session out of process, or by tests
// that want to simulate an already-registered worker without a live
// Register round trip. Ordinary usage never needs this; Register sets it.
func (c *Client) SetSession(token string) {
	c.transport.SetSession(token)
}

// Register presents the durable enrollment token and this worker's identity
// to the coordinator. On success it stores the returned worker session on
// the transport so every later call automatically authenticates as this
// worker; on failure the stored session (if any) is left untouched.
func (c *Client) Register(ctx context.Context, enrollmentToken string, info csilapi.WorkerInfo) (csilapi.RegisterResponse, error) {
	resp, err := c.raw.Register(ctx, csilapi.RegisterRequest{
		EnrollmentToken: enrollmentToken,
		WorkerInfo:      info,
	})
	if err != nil {
		return resp, err
	}
	c.transport.SetSession(resp.WorkerSession)
	return resp, nil
}

// RequestJob polls the coordinator for a lease, carrying this worker's full
// current characteristics so any coordinator pod can match statelessly.
// resp.HasLease is false (with a nil resp.Lease) when nothing was available
// within the coordinator's poll window -- not an error.
func (c *Client) RequestJob(ctx context.Context, characteristics csilapi.WorkerCharacteristics) (csilapi.RequestJobResponse, error) {
	return c.raw.RequestJob(ctx, csilapi.RequestJobRequest{WorkerCharacteristics: characteristics})
}

// Heartbeat reports the worker's status and the leases it currently holds,
// extending each lease's underlying corndogs task timeout and renewing the
// worker session server-side. The response's Directives tell the caller to
// cancel or kill specific leases.
func (c *Client) Heartbeat(ctx context.Context, status string, runningLeases []csilapi.RunningLease) (csilapi.HeartbeatResponse, error) {
	return c.raw.Heartbeat(ctx, csilapi.HeartbeatRequest{Status: status, RunningLeases: runningLeases})
}

// AppendLogs pushes one chunk of a lease's stdout/stderr (stream must be
// "stdout" or "stderr") to the coordinator, which accumulates chunks into
// the job's log object. chunk must already be secret-masked by the caller
// (see internal/coordinatorworker's masker usage) -- this method performs
// no masking of its own and never inspects chunk's contents beyond passing
// it through.
func (c *Client) AppendLogs(ctx context.Context, leaseID, stream, chunk string) (csilapi.AppendLogsResponse, error) {
	return c.raw.AppendLogs(ctx, csilapi.AppendLogsRequest{LeaseId: leaseID, Stream: stream, Chunk: chunk})
}

func (c *Client) AppendLogBatch(ctx context.Context, req csilapi.AppendLogBatchRequest) (csilapi.AppendLogBatchResponse, error) {
	return c.raw.AppendLogBatch(ctx, req)
}

func (c *Client) AppendMetricBatch(ctx context.Context, req csilapi.AppendMetricBatchRequest) (csilapi.AppendMetricBatchResponse, error) {
	return c.raw.AppendMetricBatch(ctx, req)
}

// ReportResult finalizes a lease's job with its terminal exit code/status.
// errMsg is optional context (e.g. a runner-side error string) and may be
// empty; it must never carry a secret value.
func (c *Client) ReportResult(ctx context.Context, leaseID string, exitCode int, status string, errMsg string) (csilapi.ReportResultResponse, error) {
	req := csilapi.ReportResultRequest{LeaseId: leaseID, ExitCode: int64(exitCode), Status: status}
	if errMsg != "" {
		req.Error = &errMsg
	}
	return c.raw.ReportResult(ctx, req)
}
