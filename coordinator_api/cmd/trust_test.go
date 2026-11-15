package cmd

import (
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"gopkg.in/yaml.v3"
)

// An empty list replaces the organization value; an absent field inherits it.
// The settings file must keep the two apart.
func TestTrustSettingsFileKeepsEmptyAndAbsentApart(t *testing.T) {
	file := `run_gate: []
gated_run_authority: no_secrets
required_reviews:
  - id: linkkeys-trust-anchors
    paths: ["crates/liblinkkeys/src/crypto.rs"]
    accept: [reactorcide_group:linkkeys-trust-maintainers, non_author]
    count: 1
`
	var settings csilapi.CiTrustSettings
	decoder := yaml.NewDecoder(strings.NewReader(file))
	decoder.KnownFields(true)
	if err := decoder.Decode(&settings); err != nil {
		t.Fatal(err)
	}
	if settings.RunGate == nil || len(settings.RunGate) != 0 {
		t.Fatalf("run_gate: [] must decode as an empty list, got %#v", settings.RunGate)
	}
	if settings.RunApprovers != nil {
		t.Fatal("an absent field must stay absent")
	}
	if len(settings.RequiredReviews) != 1 || *settings.RequiredReviews[0].Count != 1 || len(settings.RequiredReviews[0].Accept) != 2 {
		t.Fatalf("required reviews: %+v", settings.RequiredReviews)
	}
	bad := yaml.NewDecoder(strings.NewReader("run_gates: [forks]\n"))
	bad.KnownFields(true)
	if err := bad.Decode(&csilapi.CiTrustSettings{}); err == nil {
		t.Fatal("an unknown field must be refused")
	}
}
