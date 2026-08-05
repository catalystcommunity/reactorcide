package resources

import "testing"

func TestParseResources_ValidQuantities(t *testing.T) {
	raw := map[string]any{
		"cpu": map[string]any{
			"request": "500m",
			"limit":   "2",
		},
		"memory": map[string]any{
			"limit": "4Gi",
		},
	}

	cpuRequest, cpuLimit, memoryLimit, err := ParseResources(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cpuRequest != "500m" {
		t.Errorf("cpuRequest = %q, want %q", cpuRequest, "500m")
	}
	if cpuLimit != "2" {
		t.Errorf("cpuLimit = %q, want %q", cpuLimit, "2")
	}
	if memoryLimit != "4Gi" {
		t.Errorf("memoryLimit = %q, want %q", memoryLimit, "4Gi")
	}
}

func TestParseResources_NumericYAMLScalars(t *testing.T) {
	// YAML/JSON decoders hand back bare numeric quantities (e.g. `limit: 2`)
	// as int/float64, not string -- ParseResources must accept both.
	raw := map[string]any{
		"cpu": map[string]any{
			"request": 1,
			"limit":   2,
		},
	}

	cpuRequest, cpuLimit, _, err := ParseResources(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cpuRequest != "1" {
		t.Errorf("cpuRequest = %q, want %q", cpuRequest, "1")
	}
	if cpuLimit != "2" {
		t.Errorf("cpuLimit = %q, want %q", cpuLimit, "2")
	}
}

func TestParseResources_NilAndEmpty(t *testing.T) {
	cpuRequest, cpuLimit, memoryLimit, err := ParseResources(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cpuRequest != "" || cpuLimit != "" || memoryLimit != "" {
		t.Errorf("expected all-empty results for nil input, got (%q, %q, %q)", cpuRequest, cpuLimit, memoryLimit)
	}

	cpuRequest, cpuLimit, memoryLimit, err = ParseResources(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cpuRequest != "" || cpuLimit != "" || memoryLimit != "" {
		t.Errorf("expected all-empty results for empty map, got (%q, %q, %q)", cpuRequest, cpuLimit, memoryLimit)
	}
}

func TestParseResources_PartialSpec(t *testing.T) {
	// Only cpu.limit specified -- everything else stays "" (unspecified).
	raw := map[string]any{
		"cpu": map[string]any{
			"limit": "4",
		},
	}
	cpuRequest, cpuLimit, memoryLimit, err := ParseResources(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cpuRequest != "" {
		t.Errorf("cpuRequest = %q, want empty", cpuRequest)
	}
	if cpuLimit != "4" {
		t.Errorf("cpuLimit = %q, want %q", cpuLimit, "4")
	}
	if memoryLimit != "" {
		t.Errorf("memoryLimit = %q, want empty", memoryLimit)
	}
}

func TestParseResources_MemoryRequestRejected(t *testing.T) {
	raw := map[string]any{
		"memory": map[string]any{
			"request": "1Gi",
		},
	}
	_, _, _, err := ParseResources(raw)
	if err == nil {
		t.Fatal("expected error for memory.request, got nil")
	}
}

func TestParseResources_InvalidQuantityRejected(t *testing.T) {
	cases := []map[string]any{
		{"cpu": map[string]any{"limit": "not-a-quantity"}},
		{"cpu": map[string]any{"request": "4GB"}}, // cpu grammar has no byte-suffix forms
		{"cpu": map[string]any{"request": "-1"}},
		{"cpu": map[string]any{"limit": "-500m"}},
		{"memory": map[string]any{"limit": "lots"}},
		{"memory": map[string]any{"limit": "-4Gi"}},
	}
	for _, raw := range cases {
		if _, _, _, err := ParseResources(raw); err == nil {
			t.Errorf("expected error for %+v, got nil", raw)
		}
	}
}

func TestParseResources_MemoryGBAccepted(t *testing.T) {
	// GB/MB are not valid k8s quantity grammar but our own parser accepts
	// them -- this is the whole point of replacing the k8s parser.
	raw := map[string]any{
		"memory": map[string]any{"limit": "4GB"},
	}
	_, _, memoryLimit, err := ParseResources(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if memoryLimit != "4GB" {
		t.Errorf("memoryLimit = %q, want %q", memoryLimit, "4GB")
	}
}

func TestParseResources_UnknownFieldsRejected(t *testing.T) {
	cases := []map[string]any{
		{"gpu": map[string]any{"limit": "1"}},
		{"cpu": map[string]any{"weight": "1"}},
		{"memory": map[string]any{"max": "1Gi"}},
	}
	for _, raw := range cases {
		if _, _, _, err := ParseResources(raw); err == nil {
			t.Errorf("expected error for %+v, got nil", raw)
		}
	}
}

func TestParseResources_WrongShapeRejected(t *testing.T) {
	cases := []map[string]any{
		{"cpu": "2"},
		{"memory": "4Gi"},
	}
	for _, raw := range cases {
		if _, _, _, err := ParseResources(raw); err == nil {
			t.Errorf("expected error for %+v, got nil", raw)
		}
	}
}

func TestApplyDefaults(t *testing.T) {
	tests := []struct {
		name                                     string
		cpuRequest, cpuLimit, memoryLimit        string
		wantCPURequest, wantCPULimit, wantMemory string
	}{
		{
			name:           "all empty gets full defaults",
			wantCPURequest: DefaultCPURequest,
			wantCPULimit:   DefaultCPULimit,
			wantMemory:     DefaultMemoryLimit,
		},
		{
			name:           "explicit values pass through untouched",
			cpuRequest:     "500m",
			cpuLimit:       "4",
			memoryLimit:    "8Gi",
			wantCPURequest: "500m",
			wantCPULimit:   "4",
			wantMemory:     "8Gi",
		},
		{
			name:           "partial spec only fills the gaps",
			cpuLimit:       "3",
			wantCPURequest: DefaultCPURequest,
			wantCPULimit:   "3",
			wantMemory:     DefaultMemoryLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCPURequest, gotCPULimit, gotMemory := ApplyDefaults(tt.cpuRequest, tt.cpuLimit, tt.memoryLimit)
			if gotCPURequest != tt.wantCPURequest {
				t.Errorf("cpuRequest = %q, want %q", gotCPURequest, tt.wantCPURequest)
			}
			if gotCPULimit != tt.wantCPULimit {
				t.Errorf("cpuLimit = %q, want %q", gotCPULimit, tt.wantCPULimit)
			}
			if gotMemory != tt.wantMemory {
				t.Errorf("memoryLimit = %q, want %q", gotMemory, tt.wantMemory)
			}
		})
	}
}

func TestParseCPU_Valid(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"1", 1000},
		{"2", 2000},
		{"1.5", 1500},
		{"0.5", 500},
		{"500m", 500},
		{"0", 0},
		{"0m", 0},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseCPU(tt.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("ParseCPU(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseCPU_Invalid(t *testing.T) {
	cases := []string{"", "-1", "-500m", "not-a-quantity", "1.5m", "4GB", "m", "1x"}
	for _, in := range cases {
		if _, err := ParseCPU(in); err == nil {
			t.Errorf("ParseCPU(%q): expected error, got nil", in)
		}
	}
}

func TestParseMemory_Valid(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"4Gi", 4 * 1024 * 1024 * 1024},
		{"4GB", 4 * 1000 * 1000 * 1000},
		{"4G", 4 * 1000 * 1000 * 1000},
		{"512Mi", 512 * 1024 * 1024},
		{"512MB", 512 * 1000 * 1000},
		{"512M", 512 * 1000 * 1000},
		{"1Ki", 1024},
		{"1K", 1000},
		{"1KB", 1000},
		{"1Ti", 1024 * 1024 * 1024 * 1024},
		{"1TB", 1000 * 1000 * 1000 * 1000},
		{"1Pi", 1024 * 1024 * 1024 * 1024 * 1024},
		{"1PB", 1000 * 1000 * 1000 * 1000 * 1000},
		{"1048576", 1048576},
		{"0", 0},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseMemory(tt.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("ParseMemory(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseMemory_GBEqualsG(t *testing.T) {
	gb, err := ParseMemory("4GB")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g, err := ParseMemory("4G")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gb != g {
		t.Errorf("4GB = %d, 4G = %d, want equal", gb, g)
	}
	if gb != 4*1000*1000*1000 {
		t.Errorf("4GB = %d, want %d", gb, 4*1000*1000*1000)
	}

	gi, err := ParseMemory("4Gi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gi != 4*1024*1024*1024 {
		t.Errorf("4Gi = %d, want %d", gi, 4*1024*1024*1024)
	}
	if gi == gb {
		t.Errorf("4Gi (%d) should not equal 4GB (%d)", gi, gb)
	}
}

func TestParseMemory_Invalid(t *testing.T) {
	cases := []string{"", "-4Gi", "-1", "lots", "5B", "4gi", "4gb", "Gi", "GB"}
	for _, in := range cases {
		if _, err := ParseMemory(in); err == nil {
			t.Errorf("ParseMemory(%q): expected error, got nil", in)
		}
	}
}

func TestCPUMillicoresAndMemoryBytesWrappers(t *testing.T) {
	m, err := CPUMillicores("1.5")
	if err != nil || m != 1500 {
		t.Errorf("CPUMillicores(1.5) = (%d, %v), want (1500, nil)", m, err)
	}
	b, err := MemoryBytes("4GB")
	if err != nil || b != 4*1000*1000*1000 {
		t.Errorf("MemoryBytes(4GB) = (%d, %v), want (%d, nil)", b, err, 4*1000*1000*1000)
	}
}
