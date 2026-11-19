package resources

import "testing"

func TestParseStorage(t *testing.T) {
	request, limit, err := ParseStorage(map[string]any{"storage": map[string]any{"request": "10Gi", "limit": 20 * 1024 * 1024 * 1024}})
	if err != nil || request != "10Gi" || limit != "21474836480" {
		t.Fatalf("got %q %q %v", request, limit, err)
	}
	if request, limit, err := ParseStorage(map[string]any{"cpu": map[string]any{}}); err != nil || request != "" || limit != "" {
		t.Fatalf("no storage: %q %q %v", request, limit, err)
	}
	for _, raw := range []map[string]any{
		{"storage": "10Gi"},
		{"storage": map[string]any{"size": "1Gi"}},
		{"storage": map[string]any{"limit": "lots"}},
		{"storage": map[string]any{"request": "2Gi", "limit": "1Gi"}},
	} {
		if _, _, err := ParseStorage(raw); err == nil {
			t.Errorf("%v: expected an error", raw)
		}
	}
	// ParseResources accepts a storage block next to cpu and memory.
	if _, _, _, err := ParseResources(map[string]any{"storage": map[string]any{"limit": "1Gi"}}); err != nil {
		t.Fatalf("ParseResources refused storage: %v", err)
	}
}
