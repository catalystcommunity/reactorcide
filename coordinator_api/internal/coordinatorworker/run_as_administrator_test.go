package coordinatorworker

import (
	"testing"

	serverapi "github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// TestRunLease_CarriesRunAsAdministrator guards lease -> JobConfig mapping
// of the optional run_as.administrator value.
func TestRunLease_CarriesRunAsAdministrator(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value *bool
	}{
		{"false", boolRef(false)},
		{"true", boolRef(true)},
		{"unset", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{}
			cfg := testConfig()
			cfg.DataDir = t.TempDir()
			runLease(&fakeClient{}, runner, csilapi.Lease{LeaseId: "l", JobId: "j", RunAsUser: "1001", RunAsAdministrator: tc.value}, newLeaseTracker(), cfg)
			calls := runner.snapshotSpawnCalls()
			if len(calls) != 1 {
				t.Fatalf("spawn calls = %d, want 1", len(calls))
			}
			got := calls[0].RunAsAdministrator
			switch {
			case tc.value == nil && got != nil:
				t.Fatalf("RunAsAdministrator = %v, want nil", *got)
			case tc.value != nil && (got == nil || *got != *tc.value):
				t.Fatalf("RunAsAdministrator = %v, want %v", got, *tc.value)
			case tc.value != nil && got == tc.value:
				t.Fatal("JobConfig must hold a copy of the lease value")
			}
		})
	}
}

// TestLeaseWire_RunAsAdministrator checks the coordinator encoder and the
// worker decoder agree on the optional field, and that an absent field
// decodes as nil.
func TestLeaseWire_RunAsAdministrator(t *testing.T) {
	no := false
	data := mustEncode(serverapi.EncodeLease(serverapi.Lease{LeaseId: "l", JobId: "j", RunAsAdministrator: &no}))
	got, err := csilapi.DecodeLease(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunAsAdministrator == nil || *got.RunAsAdministrator {
		t.Fatalf("decoded RunAsAdministrator = %v, want false", got.RunAsAdministrator)
	}
	got, err = csilapi.DecodeLease(mustEncode(serverapi.EncodeLease(serverapi.Lease{LeaseId: "l", JobId: "j"})))
	if err != nil {
		t.Fatalf("decode unset: %v", err)
	}
	if got.RunAsAdministrator != nil {
		t.Fatalf("unset decoded as %v, want nil", *got.RunAsAdministrator)
	}
}

// mustEncode returns the bytes of a generated encoder, or panics. A test
// fixture is always a valid value, so an error is a defect in the test.
func mustEncode(data []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return data
}
