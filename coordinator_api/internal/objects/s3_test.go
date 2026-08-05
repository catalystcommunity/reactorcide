package objects

import "testing"

func TestS3ObjectStoreLogicalKey(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		physical string
		want     string
	}{
		{name: "no configured prefix", physical: "telemetry/v1/job/metrics/0.json", want: "telemetry/v1/job/metrics/0.json"},
		{name: "configured prefix", prefix: "reactorcide/", physical: "reactorcide/telemetry/v1/job/metrics/0.json", want: "telemetry/v1/job/metrics/0.json"},
		{name: "similar text is not a prefix", prefix: "reactorcide/", physical: "other/reactorcide/log.json", want: "other/reactorcide/log.json"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &S3ObjectStore{prefix: test.prefix}
			if got := store.logicalKey(test.physical); got != test.want {
				t.Fatalf("logicalKey(%q) = %q, want %q", test.physical, got, test.want)
			}
		})
	}
}

func TestS3ObjectStoreListKeysRoundTripThroughFullKey(t *testing.T) {
	store := &S3ObjectStore{prefix: "tenant/reactorcide/"}
	physical := "tenant/reactorcide/telemetry/v1/job-a/logs/lease-a/stdout/0001.json"

	logical := store.logicalKey(physical)
	if got := store.fullKey(logical); got != physical {
		t.Fatalf("fullKey(logicalKey(%q)) = %q, want %q", physical, got, physical)
	}
}
