package characteristics

import "testing"

// TestSatisfiesLocationCharacteristics covers the standard region, zone, and
// cloud worker characteristics (docs/workers.md "Region, zone, and cloud").
// A job that does not require a location matches a worker in any location
// or in none, so work moves to other regions during a capacity stockout.
func TestSatisfiesLocationCharacteristics(t *testing.T) {
	east, err := ParseWorkerCharacteristics("linux", "amd64", []KV{
		{Key: "region", Value: "us-east-1"}, {Key: "zone", Value: "us-east-1a"}, {Key: "cloud", Value: "aws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	west, err := ParseWorkerCharacteristics("linux", "amd64", []KV{{Key: "region", Value: "us-west-2"}})
	if err != nil {
		t.Fatal(err)
	}
	unplaced, err := ParseWorkerCharacteristics("linux", "amd64", nil)
	if err != nil {
		t.Fatal(err)
	}
	anyRegion, err := ParseJobCharacteristics(map[string]any{"arch": "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	eastOnly, err := ParseJobCharacteristics(map[string]any{"region": "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	eastZone, err := ParseJobCharacteristics(map[string]any{"region": "us-east-1", "zone": "us-east-1b"})
	if err != nil {
		t.Fatal(err)
	}

	for name, worker := range map[string]Characteristics{"east": east, "west": west, "unplaced": unplaced} {
		if !Satisfies(worker, anyRegion) {
			t.Fatalf("worker %s must satisfy a job with no location requirement", name)
		}
	}
	if !Satisfies(east, eastOnly) || Satisfies(west, eastOnly) || Satisfies(unplaced, eastOnly) {
		t.Fatal("a region requirement must match only workers in that region")
	}
	if Satisfies(east, eastZone) {
		t.Fatal("a zone requirement must match only workers in that zone")
	}
}
