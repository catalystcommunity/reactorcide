package handlers

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
)

func TestParseApprovalComment(t *testing.T) {
	for body, want := range map[string]approvals.Command{
		"/reactorcide approve-ci":           {Name: approvals.CommandApproveCI},
		"  /reactorcide override-ci\n":      {Name: approvals.CommandOverrideCI},
		"/reactorcide accept":               {Name: approvals.CommandAccept},
		"/reactorcide accept trust-anchors": {Name: approvals.CommandAccept, Label: "trust-anchors"},
		"/reactorcide revoke":               {Name: approvals.CommandRevoke},
		"/reactorcide retry":                {Name: approvals.CommandRetry},
		"/reactorcide rerun":                {Name: approvals.CommandRerun},
	} {
		got, ok := approvals.ParseComment(body)
		if !ok || got != want {
			t.Errorf("%q: got %+v, %t; want %+v", body, got, ok, want)
		}
	}
	for _, body := range []string{
		"/reactorcide approve backend-tests pr-untrusted abc123",
		"/reactorcide approve-content trust",
		"/reactorcide approve-ci now",
		"/reactorcide accept ../x",
		"/reactorcide accept a b",
		"please /reactorcide approve-ci",
	} {
		if _, ok := approvals.ParseComment(body); ok {
			t.Errorf("accepted invalid command %q", body)
		}
	}
	if label := approvals.ParseReviewLabel("Looks right.\n/reactorcide accept trust-anchors\n"); label != "trust-anchors" {
		t.Errorf("review label = %q", label)
	}
}
