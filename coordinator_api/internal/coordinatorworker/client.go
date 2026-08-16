package coordinatorworker

import (
	"context"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// client is the subset of *workerclient.Client this run loop needs. It
// exists so unit tests can substitute a fake coordinator (in-process, or an
// httptest.Server-backed *workerclient.Client) without depending on
// workerclient's HTTP carrier. *workerclient.Client satisfies this
// interface structurally -- see run.go's Run, which wires the real one.
type client interface {
	Register(ctx context.Context, enrollmentToken string, info csilapi.WorkerInfo) (csilapi.RegisterResponse, error)
	RequestJob(ctx context.Context, characteristics csilapi.WorkerCharacteristics) (csilapi.RequestJobResponse, error)
	Heartbeat(ctx context.Context, status string, runningLeases []csilapi.RunningLease) (csilapi.HeartbeatResponse, error)
	AppendLogs(ctx context.Context, leaseID, stream, chunk string) (csilapi.AppendLogsResponse, error)
	AppendLogBatch(ctx context.Context, req csilapi.AppendLogBatchRequest) (csilapi.AppendLogBatchResponse, error)
	AppendMetricBatch(ctx context.Context, req csilapi.AppendMetricBatchRequest) (csilapi.AppendMetricBatchResponse, error)
	ReportResult(ctx context.Context, leaseID string, exitCode int, status string, errMsg string) (csilapi.ReportResultResponse, error)
	ReportResultWithOutput(ctx context.Context, leaseID string, exitCode int, status string, errMsg, workflowOutput string) (csilapi.ReportResultResponse, error)
}

// workerInfo builds the WorkerInfo sent on Register from cfg.
func workerInfo(cfg Config) (csilapi.WorkerInfo, error) {
	custom, err := toCustomCharacteristics(cfg.Custom)
	if err != nil {
		return csilapi.WorkerInfo{}, err
	}
	info := csilapi.WorkerInfo{
		WorkerKey: cfg.WorkerKey,
		Os:        cfg.OS,
		Arch:      cfg.Arch,
		Custom:    custom,
	}
	if cfg.Hostname != "" {
		info.Hostname = &cfg.Hostname
	}
	if cfg.WorkerVersion != "" {
		info.WorkerVersion = &cfg.WorkerVersion
	}
	return info, nil
}

// workerCharacteristics builds the WorkerCharacteristics sent on every
// RequestJob poll from cfg.
func workerCharacteristics(cfg Config) (csilapi.WorkerCharacteristics, error) {
	custom, err := toCustomCharacteristics(cfg.Custom)
	if err != nil {
		return csilapi.WorkerCharacteristics{}, err
	}
	return csilapi.WorkerCharacteristics{Os: cfg.OS, Arch: cfg.Arch, Custom: custom}, nil
}

func toCustomCharacteristics(kvs []characteristics.KV) ([]csilapi.CustomCharacteristic, error) {
	out := make([]csilapi.CustomCharacteristic, len(kvs))
	for i, kv := range kvs {
		val, err := toCharacteristicValue(kv.Value)
		if err != nil {
			return nil, fmt.Errorf("custom characteristic %q: %w", kv.Key, err)
		}
		out[i] = csilapi.CustomCharacteristic{Key: kv.Key, Value: val}
	}
	return out, nil
}

// toCharacteristicValue converts a characteristics.KV value (any Go
// string/int-family/bool scalar, or a homogeneous slice of one of those)
// into the exact Go types csilapi.CharacteristicValue's wire codec switches
// on: string, int64, bool, []string, []int64, []bool (see
// csilEncCharacteristicValue in internal/workerclient/csilapi/codec.gen.go).
// That generated switch silently encodes any other underlying type as a
// CBOR null, so this mirrors internal/characteristics.classifyScalar's
// accepted Go integer kinds -- a worker configured with a plain `int` (the
// common case from Go code or a parsed config file) round-trips correctly
// instead of vanishing on the wire.
func toCharacteristicValue(v any) (csilapi.CharacteristicValue, error) {
	switch t := v.(type) {
	case string, bool, int64, []string, []bool, []int64:
		return t, nil
	case int:
		return int64(t), nil
	case int8:
		return int64(t), nil
	case int16:
		return int64(t), nil
	case int32:
		return int64(t), nil
	case uint:
		return int64(t), nil
	case uint8:
		return int64(t), nil
	case uint16:
		return int64(t), nil
	case uint32:
		return int64(t), nil
	case uint64:
		return int64(t), nil
	case []int:
		out := make([]int64, len(t))
		for i, e := range t {
			out[i] = int64(e)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported custom characteristic value type %T (expected string, int, bool, or a homogeneous list of those)", v)
	}
}
