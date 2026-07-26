package characteristics

import "testing"

func TestSatisfies_Matrix(t *testing.T) {
	cases := []struct {
		name   string
		worker Characteristics
		queue  Characteristics
		want   bool
	}{
		{
			name:   "scalar match",
			worker: Characteristics{"os": StringValue("linux"), "arch": StringValue("amd64")},
			queue:  Characteristics{"os": StringValue("linux")},
			want:   true,
		},
		{
			name:   "scalar mismatch",
			worker: Characteristics{"os": StringValue("windows")},
			queue:  Characteristics{"os": StringValue("linux")},
			want:   false,
		},
		{
			name:   "list membership hit",
			worker: Characteristics{"os": StringValue("linux"), "zone": StringListValue{"a", "b", "c"}},
			queue:  Characteristics{"zone": StringValue("b")},
			want:   true,
		},
		{
			name:   "list membership miss",
			worker: Characteristics{"zone": StringListValue{"a", "b", "c"}},
			queue:  Characteristics{"zone": StringValue("z")},
			want:   false,
		},
		{
			name:   "int list membership hit",
			worker: Characteristics{"priority": IntListValue{1, 2, 3}},
			queue:  Characteristics{"priority": IntValue(2)},
			want:   true,
		},
		{
			name:   "bool list membership hit",
			worker: Characteristics{"flags": BoolListValue{true, false}},
			queue:  Characteristics{"flags": BoolValue(false)},
			want:   true,
		},
		{
			name:   "missing key on worker fails",
			worker: Characteristics{"os": StringValue("linux")},
			queue:  Characteristics{"arch": StringValue("amd64")},
			want:   false,
		},
		{
			name:   "type mismatch scalar string vs int",
			worker: Characteristics{"count": StringValue("1")},
			queue:  Characteristics{"count": IntValue(1)},
			want:   false,
		},
		{
			name:   "type mismatch int vs bool",
			worker: Characteristics{"flag": IntValue(1)},
			queue:  Characteristics{"flag": BoolValue(true)},
			want:   false,
		},
		{
			name:   "type mismatch against a list of a different element type",
			worker: Characteristics{"count": IntListValue{1, 2}},
			queue:  Characteristics{"count": StringValue("1")},
			want:   false,
		},
		{
			name:   "extra worker keys do not disqualify",
			worker: Characteristics{"os": StringValue("linux"), "arch": StringValue("amd64"), "gpu": BoolValue(true)},
			queue:  Characteristics{"os": StringValue("linux")},
			want:   true,
		},
		{
			name:   "empty queue is satisfied by any worker",
			worker: Characteristics{"os": StringValue("windows")},
			queue:  Characteristics{},
			want:   true,
		},
		{
			name:   "multi-key queue requires all keys to match",
			worker: Characteristics{"os": StringValue("linux"), "arch": StringValue("amd64")},
			queue:  Characteristics{"os": StringValue("linux"), "arch": StringValue("arm64")},
			want:   false,
		},
		{
			name:   "default queue os:linux satisfied by plain linux worker",
			worker: mustParseWorker(t, "linux", "amd64", nil),
			queue:  mustParseJob(t, map[string]any{}),
			want:   true,
		},
		{
			name:   "default queue os:linux NOT satisfied by macos worker",
			worker: mustParseWorker(t, "macos", "arm64", nil),
			queue:  mustParseJob(t, map[string]any{}),
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Satisfies(tc.worker, tc.queue)
			if got != tc.want {
				t.Fatalf("Satisfies(worker=%s, queue=%s) = %v, want %v",
					CanonicalString(tc.worker), CanonicalString(tc.queue), got, tc.want)
			}
		})
	}
}

func mustParseWorker(t *testing.T, os, arch string, custom []KV) Characteristics {
	t.Helper()
	c, err := ParseWorkerCharacteristics(os, arch, custom)
	if err != nil {
		t.Fatalf("ParseWorkerCharacteristics: %v", err)
	}
	return c
}

func mustParseJob(t *testing.T, raw map[string]any) Characteristics {
	t.Helper()
	c, err := ParseJobCharacteristics(raw)
	if err != nil {
		t.Fatalf("ParseJobCharacteristics: %v", err)
	}
	return c
}
