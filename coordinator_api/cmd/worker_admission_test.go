package cmd

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/admission"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
)

func TestAddStandardCharacteristics(t *testing.T) {
	got, err := addStandardCharacteristics(nil, map[string]string{"region": "us-east-1", "zone": "", "cloud": " onprem "})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (characteristics.KV{Key: "region", Value: "us-east-1"}) || got[1] != (characteristics.KV{Key: "cloud", Value: "onprem"}) {
		t.Fatalf("got %+v", got)
	}

	custom, err := gatherCustomCharacteristics(nil, []string{"REACTORCIDE_WORKER_CUSTOM_REGION=eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	// A custom "region" alone still works, as before.
	if same, err := addStandardCharacteristics(custom, map[string]string{}); err != nil || len(same) != 1 {
		t.Fatalf("got %+v, %v", same, err)
	}
	if _, err := addStandardCharacteristics(custom, map[string]string{"region": "us-east-1"}); err == nil {
		t.Fatal("a region set twice must fail")
	}
}

func TestParseAdmissionBudget(t *testing.T) {
	budget, err := parseAdmissionBudget("16", "64Gi")
	if err != nil {
		t.Fatal(err)
	}
	if budget != (admission.Resources{CPUMillicores: 16000, MemoryBytes: 64 * 1024 * 1024 * 1024}) {
		t.Fatalf("budget = %+v", budget)
	}
	if budget, err := parseAdmissionBudget("", ""); err != nil || budget != (admission.Resources{}) {
		t.Fatalf("empty budget = %+v, %v", budget, err)
	}
	for _, values := range [][2]string{{"0", ""}, {"many", ""}, {"", "lots"}} {
		if _, err := parseAdmissionBudget(values[0], values[1]); err == nil {
			t.Fatalf("parseAdmissionBudget(%q, %q) must fail", values[0], values[1])
		}
	}
}
