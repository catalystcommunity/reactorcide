package vcs

import (
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestRenderWorkflowNodeStatusIncludesEmoji(t *testing.T) {
	tests := map[string]string{
		"pending":   "⏳ pending",
		"waiting":   "⏳ waiting",
		"submitted": "⏳ submitted",
		"running":   "🟡 running",
		"completed": "✅ succeeded",
		"failed":    "❌ failed",
		"skipped":   "⏭️ skipped",
		"cancelled": "⚠️ cancelled",
		"timeout":   "⏱️ timed out",
		"":          "❓ unknown",
	}

	for status, want := range tests {
		t.Run(status, func(t *testing.T) {
			if got := renderWorkflowNodeStatus(status); got != want {
				t.Fatalf("renderWorkflowNodeStatus(%q) = %q, want %q", status, got, want)
			}
		})
	}
}

func TestRenderWorkflowCommentBodyIncludesStatusEmoji(t *testing.T) {
	updater := NewJobStatusUpdater()
	body := updater.renderWorkflowCommentBody(&models.WorkflowInstance{
		Name:      "Reactorcide Jobs",
		CommitSHA: "abcdef123456",
	}, []models.WorkflowNode{
		{Name: "eval", Status: "completed"},
		{Name: "test-go", Status: "failed", DecisionReason: "job failed"},
	}, "<!-- marker -->")

	for _, want := range []string{"✅ succeeded", "❌ failed"} {
		if !strings.Contains(body, want) {
			t.Fatalf("workflow comment should contain %q, got:\n%s", want, body)
		}
	}
}
