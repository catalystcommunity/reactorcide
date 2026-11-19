package worker

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker/vmrunner"
)

// A job with egress control must not start on a VM worker whose host
// forwarder does not send flows to the egress proxy with job credentials.
func TestVMAdapterRefusesEgressWithoutProxy(t *testing.T) {
	t.Setenv(vmrunner.EgressProxyEnv, "")
	adapter := &vmRunnerAdapter{}
	_, err := adapter.SpawnJob(context.Background(), &JobConfig{JobID: "j", Image: "x", Command: []string{"true"}, Egress: testEgressConfig()})
	if err == nil || !strings.Contains(err.Error(), vmrunner.EgressProxyEnv) {
		t.Fatalf("err = %v", err)
	}
}
