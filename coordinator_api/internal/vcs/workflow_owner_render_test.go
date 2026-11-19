package vcs

import (
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestWorkflowCommentShowsTrustedOwnerAndContact(t *testing.T) {
	group := "payments-ci"
	wf := &models.WorkflowInstance{Name: "Payments", CommitSHA: "abcdef123", OwnerGroup: &group,
		OwnerSubjects: []string{"vcs_user:github/alice"},
		OwnerContact:  &models.WorkflowContact{Channel: "#payments-ci", HelpURL: "https://wiki.example/pay", Email: "pay@example.com"}}
	body := NewJobStatusUpdater().renderWorkflowCommentBody(wf, nil, "m")
	want := "Owner `payments-ci`, `vcs_user:github/alice` · contact `#payments-ci` · email `pay@example.com` · [help](<https://wiki.example/pay>)"
	if !strings.Contains(body, want) {
		t.Fatalf("owner line missing:\n%s", body)
	}
}

func TestWorkflowCommentOwnerLineIsSafeAndOptional(t *testing.T) {
	if body := NewJobStatusUpdater().renderWorkflowCommentBody(&models.WorkflowInstance{Name: "X"}, nil, "m"); strings.Contains(body, "Owner") {
		t.Fatalf("an unknown owner must render no line:\n%s", body)
	}
	none := ""
	if line := renderWorkflowOwnerLine(models.WorkflowOwnership{Group: &none}); line != "" {
		t.Fatalf("no owner and no contact must render no line, got %q", line)
	}
	group := "pay"
	line := renderWorkflowOwnerLine(models.WorkflowOwnership{Group: &group,
		Contact: &models.WorkflowContact{Channel: "a`b|c", HelpURL: "javascript:alert(1)"}})
	if strings.Count(line, "`") != 4 || strings.Contains(line, "javascript") || strings.Contains(line, "|") {
		t.Fatalf("contact text must not escape inline code or add a link: %q", line)
	}
}
