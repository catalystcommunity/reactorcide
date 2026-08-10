package vcsreport

import (
	"strings"
	"testing"
)

func TestRenderStableAndEscaped(t *testing.T) {
	body := Render([]Entry{{Key: "b", Title: "B", Status: "running", Generation: 2}, {Key: "a -->", Title: "A", Status: "failed", Generation: 2}}, 2, true)
	if strings.Index(body, "workflow:a-") > strings.Index(body, "workflow:b") {
		t.Fatal("entries are not stable")
	}
	if strings.Contains(body, "a -->:begin") {
		t.Fatal("unsafe marker was not escaped")
	}
}

func TestMergePreservesPrefixAndRebuildsMalformed(t *testing.T) {
	existing := "Owner note\n\n" + RootMarker + "\n<!-- reactorcide:workflow:x:begin -->"
	body, rebuilt := Merge(existing, []Entry{{Key: "x", Status: "success", Generation: 1}}, 1, true)
	if !rebuilt || !strings.HasPrefix(body, "Owner note") || !strings.Contains(body, "Warning") {
		t.Fatal("malformed report was not rebuilt safely")
	}
}

func TestGenerationCompletionRemovesStaleEntries(t *testing.T) {
	body := Render([]Entry{{Key: "old", Generation: 1}, {Key: "new", Generation: 2}}, 2, true)
	if strings.Contains(body, "workflow:old") || !strings.Contains(body, "workflow:new") {
		t.Fatal("generation filtering failed")
	}
}

func TestMergePreservesTextOutsideManagedSections(t *testing.T) {
	existing := "Owner prefix\n" + Render([]Entry{{Key: "old", Generation: 1}}, 1, true) + "\nOwner suffix\n"
	body, rebuilt := Merge(existing, []Entry{{Key: "new", Generation: 2}}, 2, true)
	if rebuilt || !strings.HasPrefix(body, "Owner prefix") || !strings.HasSuffix(body, "\nOwner suffix\n") {
		t.Fatalf("text outside the report was not preserved: %q", body)
	}
}
