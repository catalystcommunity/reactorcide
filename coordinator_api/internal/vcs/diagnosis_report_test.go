package vcs

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/diagnosis"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcsreport"
)

// diagnosisReportFake records report targets and entries.
type diagnosisReportFake struct {
	targets []models.VCSReportTarget
	entries map[string]models.VCSReportEntry
}

func (f *diagnosisReportFake) UpsertVCSReportTarget(ctx context.Context, target *models.VCSReportTarget) error {
	target.ReportTargetID = "target-1"
	f.targets = append(f.targets, *target)
	return nil
}

func (f *diagnosisReportFake) UpsertVCSReportEntry(ctx context.Context, entry *models.VCSReportEntry) error {
	if f.entries == nil {
		f.entries = map[string]models.VCSReportEntry{}
	}
	f.entries[entry.EntryKey] = *entry
	return nil
}

func TestDiagnosisPublisherWritesALabelledSection(t *testing.T) {
	fake := &diagnosisReportFake{}
	publish := NewDiagnosisPublisher(fake, "https://ci.example.com")
	pr := 42
	wf := &models.WorkflowInstance{WorkflowID: "wf-1", Name: "ci", PRNumber: &pr, VCSProvider: "github", VCSRepo: "acme/app"}
	job := &models.Job{JobID: "job-1", Name: "build"}
	target := &diagnosis.Resolved{TargetType: models.DiagnosisTargetJob, Job: job, Workflow: wf, NodeName: "build", OrgID: "org-a"}
	jobID, wfID := "job-1", "wf-1"
	report := &models.DiagnosisReport{TargetType: models.DiagnosisTargetJob, JobID: &jobID, WorkflowID: &wfID,
		AuthorPrincipalID: "svc-1", AuthorKind: "service", AuthorDisplayName: "Diag Bot", Title: "Flaky test",
		Body: "<b>retry</b> it @maintainers"}
	if err := publish(context.Background(), target, report); err != nil {
		t.Fatal(err)
	}
	if len(fake.targets) != 1 || fake.targets[0].ExternalTargetID != "42" || fake.targets[0].Repository != "acme/app" ||
		fake.targets[0].RootMarker != vcsreport.RootMarker {
		t.Fatalf("targets = %+v", fake.targets)
	}
	entry, ok := fake.entries["diagnosis-job-1-svc-1"]
	if !ok {
		t.Fatalf("entries = %+v", fake.entries)
	}
	title, _ := entry.StructuredState["title"].(string)
	body, _ := entry.StructuredState["body"].(string)
	if entry.Status != "by Diag Bot (service)" || !strings.Contains(title, "Diagnosis of build: Flaky test") {
		t.Fatalf("entry = %+v", entry)
	}
	if strings.Contains(body, "<b>") || strings.Contains(body, "@maintainers") {
		t.Fatalf("the section is sanitized: %q", body)
	}

	// A second post of the same author replaces the same key.
	report.Title = "Not flaky: a real bug"
	if err := publish(context.Background(), target, report); err != nil {
		t.Fatal(err)
	}
	if len(fake.entries) != 1 {
		t.Fatalf("one section for each (target, author): %+v", fake.entries)
	}
	rendered := vcsreport.Render([]vcsreport.Entry{{Key: entry.EntryKey, Title: fake.entries[entry.EntryKey].StructuredState["title"].(string),
		Status: fake.entries[entry.EntryKey].Status, Body: fake.entries[entry.EntryKey].StructuredState["body"].(string), Generation: 1}}, 1, true)
	if !strings.Contains(rendered, "Not flaky: a real bug — by Diag Bot (service)") {
		t.Fatalf("rendered:\n%s", rendered)
	}
}

func TestDiagnosisPublisherSkipsATargetWithNoPullRequest(t *testing.T) {
	fake := &diagnosisReportFake{}
	publish := NewDiagnosisPublisher(fake, "")
	target := &diagnosis.Resolved{TargetType: models.DiagnosisTargetJob, Job: &models.Job{JobID: "job-1"}, OrgID: "org-a"}
	if err := publish(context.Background(), target, &models.DiagnosisReport{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(fake.targets) != 0 {
		t.Fatal("a job with no pull request writes no report")
	}
	// The PR of a job outside a workflow comes from its VCS metadata.
	job := &models.Job{JobID: "job-2", Notes: `{"vcs_provider":"github","repo":"acme/app","pr_number":7,"commit_sha":"abc"}`}
	provider, repo, number, ok := DiagnosisPullRequest(&diagnosis.Resolved{Job: job})
	if !ok || provider != "github" || repo != "acme/app" || number != 7 {
		t.Fatalf("metadata PR: %s %s %d %v", provider, repo, number, ok)
	}
}
