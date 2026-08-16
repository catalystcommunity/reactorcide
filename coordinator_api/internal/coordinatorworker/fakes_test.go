package coordinatorworker

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// fakeClient is an in-memory stand-in for a coordinator, satisfying the
// unexported client interface this package's run loop depends on. It
// mirrors internal/corndogs.MockClient's Func-field-plus-call-log shape.
type fakeClient struct {
	mu sync.Mutex

	RegisterFunc          func(ctx context.Context, enrollmentToken string, info csilapi.WorkerInfo) (csilapi.RegisterResponse, error)
	RequestJobFunc        func(ctx context.Context, characteristics csilapi.WorkerCharacteristics) (csilapi.RequestJobResponse, error)
	HeartbeatFunc         func(ctx context.Context, status string, runningLeases []csilapi.RunningLease) (csilapi.HeartbeatResponse, error)
	AppendLogsFunc        func(ctx context.Context, leaseID, stream, chunk string) (csilapi.AppendLogsResponse, error)
	AppendLogBatchFunc    func(ctx context.Context, req csilapi.AppendLogBatchRequest) (csilapi.AppendLogBatchResponse, error)
	AppendMetricBatchFunc func(ctx context.Context, req csilapi.AppendMetricBatchRequest) (csilapi.AppendMetricBatchResponse, error)
	ReportResultFunc      func(ctx context.Context, leaseID string, exitCode int, status string, errMsg string) (csilapi.ReportResultResponse, error)

	RegisterCalls     []csilapi.WorkerInfo
	RequestJobCalls   []csilapi.WorkerCharacteristics
	HeartbeatCalls    []heartbeatCall
	AppendLogsCalls   []appendLogsCall
	ReportResultCalls []reportResultCall
}

type heartbeatCall struct {
	Status        string
	RunningLeases []csilapi.RunningLease
}

type appendLogsCall struct {
	LeaseID string
	Stream  string
	Chunk   string
}

type reportResultCall struct {
	LeaseID  string
	ExitCode int
	Status   string
	ErrMsg   string
}

func (f *fakeClient) Register(ctx context.Context, enrollmentToken string, info csilapi.WorkerInfo) (csilapi.RegisterResponse, error) {
	f.mu.Lock()
	f.RegisterCalls = append(f.RegisterCalls, info)
	f.mu.Unlock()
	if f.RegisterFunc != nil {
		return f.RegisterFunc(ctx, enrollmentToken, info)
	}
	return csilapi.RegisterResponse{WorkerSession: "test-session", WorkerId: "test-worker-id", HeartbeatInterval: 1}, nil
}

func (f *fakeClient) RequestJob(ctx context.Context, characteristics csilapi.WorkerCharacteristics) (csilapi.RequestJobResponse, error) {
	f.mu.Lock()
	f.RequestJobCalls = append(f.RequestJobCalls, characteristics)
	f.mu.Unlock()
	if f.RequestJobFunc != nil {
		return f.RequestJobFunc(ctx, characteristics)
	}
	return csilapi.RequestJobResponse{HasLease: false}, nil
}

func (f *fakeClient) Heartbeat(ctx context.Context, status string, runningLeases []csilapi.RunningLease) (csilapi.HeartbeatResponse, error) {
	f.mu.Lock()
	f.HeartbeatCalls = append(f.HeartbeatCalls, heartbeatCall{Status: status, RunningLeases: runningLeases})
	f.mu.Unlock()
	if f.HeartbeatFunc != nil {
		return f.HeartbeatFunc(ctx, status, runningLeases)
	}
	return csilapi.HeartbeatResponse{}, nil
}

func (f *fakeClient) AppendLogs(ctx context.Context, leaseID, stream, chunk string) (csilapi.AppendLogsResponse, error) {
	f.mu.Lock()
	f.AppendLogsCalls = append(f.AppendLogsCalls, appendLogsCall{LeaseID: leaseID, Stream: stream, Chunk: chunk})
	f.mu.Unlock()
	if f.AppendLogsFunc != nil {
		return f.AppendLogsFunc(ctx, leaseID, stream, chunk)
	}
	return csilapi.AppendLogsResponse{Ok: true}, nil
}

func (f *fakeClient) AppendLogBatch(ctx context.Context, req csilapi.AppendLogBatchRequest) (csilapi.AppendLogBatchResponse, error) {
	f.mu.Lock()
	for _, entry := range req.Entries {
		f.AppendLogsCalls = append(f.AppendLogsCalls, appendLogsCall{LeaseID: req.LeaseId, Stream: req.Stream, Chunk: entry.Message + "\n"})
	}
	f.mu.Unlock()
	if f.AppendLogBatchFunc != nil {
		return f.AppendLogBatchFunc(ctx, req)
	}
	return csilapi.AppendLogBatchResponse{Ok: true, AcceptedSequence: req.Sequence}, nil
}

func (f *fakeClient) AppendMetricBatch(ctx context.Context, req csilapi.AppendMetricBatchRequest) (csilapi.AppendMetricBatchResponse, error) {
	if f.AppendMetricBatchFunc != nil {
		return f.AppendMetricBatchFunc(ctx, req)
	}
	return csilapi.AppendMetricBatchResponse{Ok: true, AcceptedSequence: req.Sequence}, nil
}

func (f *fakeClient) ReportResult(ctx context.Context, leaseID string, exitCode int, status string, errMsg string) (csilapi.ReportResultResponse, error) {
	return f.ReportResultWithOutput(ctx, leaseID, exitCode, status, errMsg, "")
}

func (f *fakeClient) ReportResultWithOutput(ctx context.Context, leaseID string, exitCode int, status string, errMsg, workflowOutput string) (csilapi.ReportResultResponse, error) {
	f.mu.Lock()
	f.ReportResultCalls = append(f.ReportResultCalls, reportResultCall{LeaseID: leaseID, ExitCode: exitCode, Status: status, ErrMsg: errMsg})
	f.mu.Unlock()
	if f.ReportResultFunc != nil {
		return f.ReportResultFunc(ctx, leaseID, exitCode, status, errMsg)
	}
	return csilapi.ReportResultResponse{Ok: true}, nil
}

func (f *fakeClient) snapshotAppendLogs() []appendLogsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]appendLogsCall, len(f.AppendLogsCalls))
	copy(out, f.AppendLogsCalls)
	return out
}

func (f *fakeClient) snapshotReportResults() []reportResultCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]reportResultCall, len(f.ReportResultCalls))
	copy(out, f.ReportResultCalls)
	return out
}

func (f *fakeClient) snapshotRequestJobCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.RequestJobCalls)
}

func (f *fakeClient) snapshotRegisterCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.RegisterCalls)
}

// fakeRunner is an in-memory stand-in for worker.JobRunner.
type fakeRunner struct {
	mu sync.Mutex

	SpawnErr  error
	SpawnID   string
	StreamErr error
	Stdout    io.ReadCloser
	Stderr    io.ReadCloser
	WaitExit  int
	WaitErr   error
	// WaitBlock, if non-nil, makes WaitForCompletion block until the
	// channel is closed (simulating a still-running container) -- tests
	// close it themselves, or StopFunc/CleanupFunc below can close it to
	// simulate a directive-driven termination unblocking the wait.
	WaitBlock chan struct{}
	StopFunc  func(id string, grace time.Duration) error
	// CleanupFunc, if set, runs on every Cleanup call (in addition to the
	// call being recorded) -- tests use it to close WaitBlock, simulating a
	// forced removal unblocking WaitForCompletion the way a real runner's
	// Cleanup would.
	CleanupFunc func(id string) error

	SpawnCalls    []*worker.JobConfig
	StopCalls     []fakeStopCall
	CleanupCalls  []string
	SampleOptions []worker.ResourceSampleOptions
}

type fakeStopCall struct {
	ID    string
	Grace time.Duration
}

func (f *fakeRunner) SpawnJob(ctx context.Context, config *worker.JobConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SpawnCalls = append(f.SpawnCalls, config)
	if f.SpawnErr != nil {
		return "", f.SpawnErr
	}
	id := f.SpawnID
	if id == "" {
		id = "fake-container-id"
	}
	return id, nil
}

func (f *fakeRunner) StreamLogs(ctx context.Context, jobID string) (io.ReadCloser, io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Stdout, f.Stderr, f.StreamErr
}

func (f *fakeRunner) WaitForCompletion(ctx context.Context, jobID string) (int, error) {
	f.mu.Lock()
	block := f.WaitBlock
	exit := f.WaitExit
	err := f.WaitErr
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return exit, err
}

func (f *fakeRunner) Stop(ctx context.Context, jobID string, grace time.Duration) error {
	f.mu.Lock()
	f.StopCalls = append(f.StopCalls, fakeStopCall{ID: jobID, Grace: grace})
	stopFunc := f.StopFunc
	f.mu.Unlock()
	if stopFunc != nil {
		return stopFunc(jobID, grace)
	}
	return nil
}

func (f *fakeRunner) Cleanup(ctx context.Context, jobID string) error {
	f.mu.Lock()
	f.CleanupCalls = append(f.CleanupCalls, jobID)
	cleanupFunc := f.CleanupFunc
	f.mu.Unlock()
	if cleanupFunc != nil {
		return cleanupFunc(jobID)
	}
	return nil
}

func (f *fakeRunner) SampleResources(ctx context.Context, jobID string, options worker.ResourceSampleOptions) (worker.ResourceSnapshot, error) {
	f.mu.Lock()
	f.SampleOptions = append(f.SampleOptions, options)
	f.mu.Unlock()
	return worker.ResourceSnapshot{}, nil
}

func (f *fakeRunner) snapshotSpawnCalls() []*worker.JobConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*worker.JobConfig, len(f.SpawnCalls))
	copy(out, f.SpawnCalls)
	return out
}

func (f *fakeRunner) snapshotStopCalls() []fakeStopCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeStopCall, len(f.StopCalls))
	copy(out, f.StopCalls)
	return out
}

func (f *fakeRunner) snapshotCleanupCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.CleanupCalls))
	copy(out, f.CleanupCalls)
	return out
}

var _ worker.JobRunner = (*fakeRunner)(nil)
