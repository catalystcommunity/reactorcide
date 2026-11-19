package vcs

import (
	"context"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/diagnosis"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcsreport"
)

// DiagnosisPullRequest is the pull request of a diagnosis target: from the
// workflow of the target, else from the VCS metadata of the job. ok is false
// when the target has no pull request.
func DiagnosisPullRequest(target *diagnosis.Resolved) (provider, repo string, number int, ok bool) {
	if target == nil {
		return "", "", 0, false
	}
	if wf := target.Workflow; wf != nil && wf.PRNumber != nil && *wf.PRNumber > 0 && wf.VCSProvider != "" && wf.VCSRepo != "" {
		return wf.VCSProvider, wf.VCSRepo, *wf.PRNumber, true
	}
	if target.Job != nil {
		metadata, err := MetadataFromJob(target.Job)
		if err == nil && metadata != nil && metadata.PRNumber > 0 && metadata.VCSProvider != "" && metadata.Repo != "" {
			return metadata.VCSProvider, metadata.Repo, metadata.PRNumber, true
		}
	}
	return "", "", 0, false
}

// NewDiagnosisPublisher returns the publisher that puts a diagnosis entry
// in the rolling pull request comment (the shared report of
// internal/vcsreport). reports is the store; a store without report targets
// publishes nothing. baseURL is the UI base for the "read all" link.
//
// The section is a separate report entry with its own key for each
// (target, author). A new post of the same author replaces the section. The
// report reconciler renders and writes the comment.
func NewDiagnosisPublisher(reports any, baseURL string) diagnosis.Publisher {
	return func(ctx context.Context, target *diagnosis.Resolved, report *models.DiagnosisReport) error {
		store, ok := reports.(workflowReportStore)
		if !ok || report == nil {
			return nil
		}
		provider, repo, number, ok := DiagnosisPullRequest(target)
		if !ok {
			return nil
		}
		reportTarget := &models.VCSReportTarget{OrgID: target.OrgID, ProjectID: target.ProjectID, Provider: provider,
			Repository: repo, TargetType: "pull_request", ExternalTargetID: fmt.Sprintf("%d", number),
			RootMarker: vcsreport.RootMarker, CurrentGeneration: 1}
		if err := store.UpsertVCSReportTarget(ctx, reportTarget); err != nil {
			return fmt.Errorf("creating diagnosis report target: %w", err)
		}
		title, status, body := diagnosis.CommentSection(report, target.SubjectName(), target.PageURL(strings.TrimSpace(baseURL)))
		entry := &models.VCSReportEntry{ReportTargetID: reportTarget.ReportTargetID, EntryKey: diagnosis.ReportEntryKey(report),
			WorkflowID: report.WorkflowID, Generation: reportTarget.CurrentGeneration, Status: status,
			StructuredState: models.JSONB{"title": title, "body": body, "kind": "diagnosis",
				"author_principal_id": report.AuthorPrincipalID, "author_kind": report.AuthorKind}}
		if err := store.UpsertVCSReportEntry(ctx, entry); err != nil {
			return fmt.Errorf("queueing diagnosis report: %w", err)
		}
		return nil
	}
}
