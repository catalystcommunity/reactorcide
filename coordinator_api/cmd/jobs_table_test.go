package cmd

import (
	"bytes"
	"strings"
	"testing"
	"text/tabwriter"
)

// TestJobTableShowsImageRetryOf: jobs get prints the image and, for a retry
// or a re-run that changed it, the earlier image.
func TestJobTableShowsImageRetryOf(t *testing.T) {
	var out bytes.Buffer
	w := tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
	writeJobTable(w, map[string]interface{}{"job_id": "j1", "status": "queued",
		"image_display": "win (r.example/win@sha256:new)", "image_retry_of": "win (r.example/win@sha256:old)"})
	_ = w.Flush()
	text := out.String()
	if !strings.Contains(text, "image_display") || !strings.Contains(text, "r.example/win@sha256:new") {
		t.Fatalf("no image_display in %q", text)
	}
	if !strings.Contains(text, "image_retry_of") || !strings.Contains(text, "r.example/win@sha256:old") {
		t.Fatalf("no image_retry_of in %q", text)
	}

	out.Reset()
	w = tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
	writeJobTable(w, map[string]interface{}{"job_id": "j2", "image_display": "alpine:3"})
	_ = w.Flush()
	if strings.Contains(out.String(), "image_retry_of") {
		t.Fatalf("image_retry_of printed for a job without it: %q", out.String())
	}
}
