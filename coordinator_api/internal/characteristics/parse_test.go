package characteristics

import (
	"testing"
)

func TestParseJobCharacteristics_DefaultOS(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want Characteristics
	}{
		{
			name: "nil map gets default os",
			raw:  nil,
			want: Characteristics{"os": StringValue("linux")},
		},
		{
			name: "empty map gets default os",
			raw:  map[string]any{},
			want: Characteristics{"os": StringValue("linux")},
		},
		{
			name: "arch only gets default os appended",
			raw:  map[string]any{"arch": "amd64"},
			want: Characteristics{"os": StringValue("linux"), "arch": StringValue("amd64")},
		},
		{
			name: "explicit os windows is not overridden",
			raw:  map[string]any{"os": "windows"},
			want: Characteristics{"os": StringValue("windows")},
		},
		{
			name: "explicit os debian is not overridden (no normalization)",
			raw:  map[string]any{"os": "debian"},
			want: Characteristics{"os": StringValue("debian")},
		},
		{
			name: "nothing else is ever defaulted",
			raw:  map[string]any{"os": "windows", "gpu": true},
			want: Characteristics{"os": StringValue("windows"), "gpu": BoolValue(true)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseJobCharacteristics(tc.raw)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertCharacteristicsEqual(t, got, tc.want)
		})
	}
}

func TestParseJobCharacteristics_ScalarTypes(t *testing.T) {
	raw := map[string]any{
		"os":         "linux",
		"arch":       "amd64",
		"replicas":   3,
		"replicas64": int64(64),
		"gpu":        true,
		"cpu32":      int32(7),
		"cpuU64":     uint64(9),
	}
	got, err := ParseJobCharacteristics(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Characteristics{
		"os":         StringValue("linux"),
		"arch":       StringValue("amd64"),
		"replicas":   IntValue(3),
		"replicas64": IntValue(64),
		"gpu":        BoolValue(true),
		"cpu32":      IntValue(7),
		"cpuU64":     IntValue(9),
	}
	assertCharacteristicsEqual(t, got, want)
}

func TestParseJobCharacteristics_RejectsLists(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
	}{
		{"string list", map[string]any{"arch": []string{"amd64", "arm64"}}},
		{"int list", map[string]any{"count": []int{1, 2}}},
		{"int64 list", map[string]any{"count": []int64{1, 2}}},
		{"bool list", map[string]any{"flags": []bool{true, false}}},
		{"dynamic any list", map[string]any{"tags": []any{"a", "b"}}},
		{"empty list", map[string]any{"tags": []string{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseJobCharacteristics(tc.raw)
			if err == nil {
				t.Fatalf("expected error rejecting list value, got nil")
			}
		})
	}
}

func TestParseJobCharacteristics_RejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
	}{
		{"empty key", map[string]any{"": "x"}},
		{"float value", map[string]any{"cpu": 1.5}},
		{"nil value", map[string]any{"gpu": nil}},
		{"struct value", map[string]any{"weird": struct{ X int }{X: 1}}},
		{"map value", map[string]any{"weird": map[string]any{"a": 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseJobCharacteristics(tc.raw)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

func TestParseWorkerCharacteristics_RequiredFields(t *testing.T) {
	if _, err := ParseWorkerCharacteristics("", "amd64", nil); err == nil {
		t.Fatalf("expected error for empty os")
	}
	if _, err := ParseWorkerCharacteristics("linux", "", nil); err == nil {
		t.Fatalf("expected error for empty arch")
	}
}

func TestParseWorkerCharacteristics_NoNormalization(t *testing.T) {
	// Operator-supplied os/arch strings pass through verbatim: no
	// lowercasing, no capitalization fixups.
	got, err := ParseWorkerCharacteristics("Fedora", "ARM64v8", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Characteristics{"os": StringValue("Fedora"), "arch": StringValue("ARM64v8")}
	assertCharacteristicsEqual(t, got, want)
}

func TestParseWorkerCharacteristics_CustomScalarsAndLists(t *testing.T) {
	custom := []KV{
		{Key: "gpu", Value: true},
		{Key: "region", Value: "us-east"},
		{Key: "zones", Value: []string{"a", "b", "c"}},
		{Key: "priorities", Value: []int64{1, 2, 3}},
		{Key: "flags", Value: []bool{true, false}},
		{Key: "queue", Value: "not-special"},
	}
	got, err := ParseWorkerCharacteristics("linux", "amd64", custom)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Characteristics{
		"os":         StringValue("linux"),
		"arch":       StringValue("amd64"),
		"gpu":        BoolValue(true),
		"region":     StringValue("us-east"),
		"zones":      StringListValue{"a", "b", "c"},
		"priorities": IntListValue{1, 2, 3},
		"flags":      BoolListValue{true, false},
		"queue":      StringValue("not-special"),
	}
	assertCharacteristicsEqual(t, got, want)
}

func TestParseWorkerCharacteristics_DynamicAnyLists(t *testing.T) {
	custom := []KV{
		{Key: "zones", Value: []any{"a", "b"}},
		{Key: "priorities", Value: []any{int64(1), int64(2)}},
		{Key: "flags", Value: []any{true, false}},
	}
	got, err := ParseWorkerCharacteristics("linux", "amd64", custom)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v, ok := got["zones"].(StringListValue); !ok || len(v) != 2 {
		t.Fatalf("zones: got %#v", got["zones"])
	}
	if v, ok := got["priorities"].(IntListValue); !ok || len(v) != 2 {
		t.Fatalf("priorities: got %#v", got["priorities"])
	}
	if v, ok := got["flags"].(BoolListValue); !ok || len(v) != 2 {
		t.Fatalf("flags: got %#v", got["flags"])
	}
}

func TestParseWorkerCharacteristics_RejectsMixedLists(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"string+int", []any{"a", 1}},
		{"int+bool", []any{1, true}},
		{"bool+string", []any{true, "x"}},
		{"empty untyped list", []any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseWorkerCharacteristics("linux", "amd64", []KV{{Key: "mixed", Value: tc.value}})
			if err == nil {
				t.Fatalf("expected error for mixed/empty list, got nil")
			}
		})
	}
}

func TestParseWorkerCharacteristics_RejectsReservedAndDuplicateKeys(t *testing.T) {
	if _, err := ParseWorkerCharacteristics("linux", "amd64", []KV{{Key: "os", Value: "linux"}}); err == nil {
		t.Fatalf("expected error redefining os via custom")
	}
	if _, err := ParseWorkerCharacteristics("linux", "amd64", []KV{{Key: "arch", Value: "amd64"}}); err == nil {
		t.Fatalf("expected error redefining arch via custom")
	}
	if _, err := ParseWorkerCharacteristics("linux", "amd64", []KV{
		{Key: "gpu", Value: true},
		{Key: "gpu", Value: false},
	}); err == nil {
		t.Fatalf("expected error for duplicate custom key")
	}
	if _, err := ParseWorkerCharacteristics("linux", "amd64", []KV{{Key: "", Value: "x"}}); err == nil {
		t.Fatalf("expected error for empty custom key")
	}
}

func assertCharacteristicsEqual(t *testing.T, got, want Characteristics) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Fatalf("missing key %q in got: %v", k, got)
		}
		if gv.Kind() != wv.Kind() || gv.String() != wv.String() {
			t.Fatalf("key %q: got %v, want %v", k, gv, wv)
		}
	}
}
