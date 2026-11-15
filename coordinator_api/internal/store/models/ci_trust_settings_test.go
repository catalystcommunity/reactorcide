package models

import (
	"strings"
	"testing"
)

func TestEffectiveTrust(t *testing.T) {
	defaults := EffectiveTrust(nil, nil)
	if len(defaults.RunGate) != 0 || defaults.GatedRunAuthority != GatedRunNoSecrets || defaults.OverrideProfile != DefaultOverrideProfile ||
		defaults.GatedRunProfile() != GatedRunNoSecretsProfile {
		t.Fatalf("defaults: %+v", defaults)
	}
	normal := GatedRunNormal
	orgReviews := []RequiredReview{{ID: "org-review", Accept: []string{"repository_write"}}}
	projectReviews := []RequiredReview{{ID: "project-review", Accept: []string{SubjectNonAuthor}}}
	org := &CITrustSettings{RunGate: []string{RunGateForks}, GatedRunAuthority: &normal, RequiredReviews: &orgReviews}
	project := &CITrustSettings{RunGate: []string{}, RequiredReviews: &projectReviews}
	effective := EffectiveTrust(org, project)
	if len(effective.RunGate) != 0 {
		t.Fatal("an empty project run gate replaces the organization gate")
	}
	if effective.GatedRunAuthority != GatedRunNormal || effective.GatedRunProfile() != "" {
		t.Fatal("a nil project field inherits the organization value")
	}
	if len(effective.RequiredReviews) != 2 || effective.RequiredReviews[0].Scope != "org" || effective.RequiredReviews[1].Scope != "project" {
		t.Fatalf("required reviews add: %+v", effective.RequiredReviews)
	}
}

func TestCITrustSettingsValidate(t *testing.T) {
	bad := "sometimes"
	for name, settings := range map[string]CITrustSettings{
		"gate":                {RunGate: []string{"everyone"}},
		"authority":           {GatedRunAuthority: &bad},
		"approver":            {RunApprovers: []string{"anyone"}},
		"review id":           {RequiredReviews: &[]RequiredReview{{ID: "../x", Accept: []string{"repository_write"}}}},
		"no accept":           {RequiredReviews: &[]RequiredReview{{ID: "x"}}},
		"duplicate":           {RequiredReviews: &[]RequiredReview{{ID: "x", Accept: []string{"repository_write"}}, {ID: "x", Accept: []string{"repository_write"}}}},
		"path":                {RequiredReviews: &[]RequiredReview{{ID: "x", Accept: []string{"repository_write"}, Paths: []string{"../etc"}}}},
		"non_author approver": {OverrideApprovers: []string{SubjectNonAuthor}},
	} {
		if err := settings.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	good := CITrustSettings{RunGate: []string{RunGateForks, RunGateOutsideContributors},
		RequiredReviews: &[]RequiredReview{{ID: "trust", Accept: []string{SubjectNonAuthor, "reactorcide_group:maintainers"}, Count: 2, Paths: []string{"crypto/**"}}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLooserThan(t *testing.T) {
	org := EffectiveTrust(&CITrustSettings{RunGate: []string{RunGateForks}, RunApprovers: []string{"project_owner"}}, nil)
	stricter := EffectiveTrust(&CITrustSettings{RunGate: []string{RunGateForks, RunGateFirstTimeContributors}, RunApprovers: []string{"project_owner"}}, nil)
	if reasons := LooserThan(stricter, org, nil); len(reasons) != 0 {
		t.Fatalf("a stricter project was refused: %v", reasons)
	}
	normal := GatedRunNormal
	looser := EffectiveTrust(&CITrustSettings{RunGate: []string{}, GatedRunAuthority: &normal, RunApprovers: []string{"project_owner", "repository_write"}}, nil)
	reasons := strings.Join(LooserThan(looser, org, nil), "; ")
	for _, want := range []string{"removes run gate condition forks", "normal profile", "adds run approvers repository_write"} {
		if !strings.Contains(reasons, want) {
			t.Errorf("missing %q in %q", want, reasons)
		}
	}
}
