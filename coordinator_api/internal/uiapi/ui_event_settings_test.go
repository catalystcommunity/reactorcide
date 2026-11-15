package uiapi

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func strp(value string) *string { return &value }

func TestApplyProjectEventSettings(t *testing.T) {
	project := &models.Project{DuplicateEvents: "inherit", DynamicJobs: "inherit"}
	changed, err := applyProjectEventSettings(project, strp("skip"), strp("deny"))
	if err != nil {
		t.Fatal(err)
	}
	if project.DuplicateEvents != "skip" || project.DynamicJobs != "deny" {
		t.Fatalf("project = %+v", project)
	}
	if len(changed) != 2 {
		t.Fatalf("audit details = %v", changed)
	}
	// No change: nothing to audit.
	changed, err = applyProjectEventSettings(project, strp("skip"), nil)
	if err != nil || changed != nil {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
	if _, err := applyProjectEventSettings(project, nil, strp("maybe")); err == nil {
		t.Fatal("an unknown dynamic_jobs value must be refused")
	}
	if _, err := applyProjectEventSettings(project, strp("never"), nil); err == nil {
		t.Fatal("an unknown duplicate_events value must be refused")
	}
}

func TestApplyOrgEventSettings(t *testing.T) {
	org := &models.Organization{DuplicateEvents: "run", DynamicJobs: "allow"}
	if _, err := applyOrgEventSettings(org, nil, strp("inherit")); err == nil {
		t.Fatal("an organization has no inherit")
	}
	if _, err := applyOrgEventSettings(org, strp("inherit"), nil); err == nil {
		t.Fatal("an organization has no inherit")
	}
	changed, err := applyOrgEventSettings(org, strp("skip"), strp("deny"))
	if err != nil || len(changed) != 2 || org.DynamicJobs != "deny" || org.DuplicateEvents != "skip" {
		t.Fatalf("org = %+v changed = %v err = %v", org, changed, err)
	}
	summary := orgSummary(org, nil)
	if summary.DynamicJobs == nil || *summary.DynamicJobs != "deny" || summary.DuplicateEvents == nil || *summary.DuplicateEvents != "skip" {
		t.Fatalf("summary = %+v", summary)
	}
}
