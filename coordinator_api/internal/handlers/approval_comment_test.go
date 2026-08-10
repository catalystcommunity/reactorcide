package handlers

import "testing"

func TestParseApprovalComment(t *testing.T) {
	command, ok := parseApprovalComment("/reactorcide approve backend-tests pr-untrusted abc123")
	if !ok || command.Workflow != "backend-tests" || command.Profile != "pr-untrusted" || command.PolicyRevision != "abc123" {
		t.Fatalf("unexpected command: %#v, %t", command, ok)
	}
	for _, body := range []string{
		"/reactorcide approve backend-tests",
		"/reactorcide approve backend-tests pr-untrusted abc123 extra",
		"please /reactorcide approve backend-tests pr-untrusted abc123",
	} {
		if _, accepted := parseApprovalComment(body); accepted {
			t.Fatalf("accepted invalid approval command %q", body)
		}
	}
}
