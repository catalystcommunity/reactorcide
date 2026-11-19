package uiapi

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	workercsilapi "github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// opsTestWorker satisfies the ReactorcideWorker interface so that the
// dispatcher mounts the worker service. The test never calls it.
type opsTestWorker struct{}

func (opsTestWorker) Register(context.Context, workercsilapi.RegisterRequest) (workercsilapi.RegisterResponse, error) {
	return workercsilapi.RegisterResponse{}, nil
}
func (opsTestWorker) RequestJob(context.Context, workercsilapi.RequestJobRequest) (workercsilapi.RequestJobResponse, error) {
	return workercsilapi.RequestJobResponse{}, nil
}
func (opsTestWorker) Heartbeat(context.Context, workercsilapi.HeartbeatRequest) (workercsilapi.HeartbeatResponse, error) {
	return workercsilapi.HeartbeatResponse{}, nil
}
func (opsTestWorker) AppendLogs(context.Context, workercsilapi.AppendLogsRequest) (workercsilapi.AppendLogsResponse, error) {
	return workercsilapi.AppendLogsResponse{}, nil
}
func (opsTestWorker) AppendLogBatch(context.Context, workercsilapi.AppendLogBatchRequest) (workercsilapi.AppendLogBatchResponse, error) {
	return workercsilapi.AppendLogBatchResponse{}, nil
}
func (opsTestWorker) AppendMetricBatch(context.Context, workercsilapi.AppendMetricBatchRequest) (workercsilapi.AppendMetricBatchResponse, error) {
	return workercsilapi.AppendMetricBatchResponse{}, nil
}
func (opsTestWorker) ReportResult(context.Context, workercsilapi.ReportResultRequest) (workercsilapi.ReportResultResponse, error) {
	return workercsilapi.ReportResultResponse{}, nil
}
func (opsTestWorker) UploadArtifact(context.Context, workercsilapi.UploadArtifactRequest) (workercsilapi.UploadArtifactResponse, error) {
	return workercsilapi.UploadArtifactResponse{}, nil
}

var (
	csilServiceStart = regexp.MustCompile(`^\s*service\s+([A-Za-z0-9_]+)\s*\{`)
	csilServiceOp    = regexp.MustCompile(`^\s*([a-z0-9][a-z0-9-]*)\s*:`)
)

// csilServiceOps reads a .csil file and returns the operation names of each
// service block, keyed by service name.
func csilServiceOps(t *testing.T, path string) map[string][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	out := map[string][]string{}
	current := ""
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.Index(line, ";"); i >= 0 {
			line = line[:i]
		}
		if current == "" {
			if m := csilServiceStart.FindStringSubmatch(line); m != nil {
				current = m[1]
				out[current] = nil
			}
			continue
		}
		if strings.Contains(line, "}") {
			current = ""
			continue
		}
		if m := csilServiceOp.FindStringSubmatch(line); m != nil {
			out[current] = append(out[current], m[1])
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return out
}

// TestDispatcher_EveryCSILOperationIsRegistered makes sure that each
// operation the CSIL files declare has a dispatcher entry, and that the
// dispatcher has no entry the CSIL files do not declare. An operation with
// no entry is an "unknown op" transport error for every caller, which the
// generated clients and the UI cannot detect at build time.
func TestDispatcher_EveryCSILOperationIsRegistered(t *testing.T) {
	csilDir := filepath.Join("..", "..", "csil")
	want := map[string][]string{}
	for _, file := range []string{"reactorcide-ui.csil", "reactorcide-worker.csil"} {
		for svc, ops := range csilServiceOps(t, filepath.Join(csilDir, file)) {
			want[svc] = append(want[svc], ops...)
		}
	}
	for _, svc := range []string{"ReactorcideAuth", "ReactorcideUi", "ReactorcideWorker"} {
		if len(want[svc]) == 0 {
			t.Fatalf("CSIL parse found no operations for service %s", svc)
		}
	}

	h := NewHandlerWithWorker(NewStubAuth(), NewStubUi(), opsTestWorker{})

	for svc, ops := range want {
		registered := h.ops[svc]
		for _, op := range ops {
			if _, ok := registered[op]; !ok {
				t.Errorf("CSIL operation %s/%s has no dispatcher entry", svc, op)
			}
		}
	}
	for svc, registered := range h.ops {
		declared := map[string]bool{}
		for _, op := range want[svc] {
			declared[op] = true
		}
		var extra []string
		for op := range registered {
			if !declared[op] {
				extra = append(extra, op)
			}
		}
		sort.Strings(extra)
		for _, op := range extra {
			t.Errorf("dispatcher entry %s/%s is not declared in the CSIL files", svc, op)
		}
	}
}
