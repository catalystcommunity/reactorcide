package vcs

import (
	"context"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// The "Reactorcide Checks" check run of a GitHub App project (WP11,
// docs/github-app.md). It has the same state and title as the commit
// status, and a summary with one row for each workflow, the failing jobs,
// their failure class, the first error lines, and line annotations from
// test reports.

const (
	maxSummaryFailedJobs   = 20
	maxSummaryTestFailures = 5
	maxSummaryAnnotations  = 50
)

type checkRunNodeStore interface {
	ListWorkflowNodes(ctx context.Context, workflowID string) ([]models.WorkflowNode, error)
}

// checkRunTestStore is the part of store.TestResultStore (WP15) the summary
// reads.
type checkRunTestStore interface {
	ListJobTestResults(ctx context.Context, jobID string, statuses []string, limit int) ([]models.TestResult, error)
}

// CheckRunFailedJob is one failed job of a workflow in the summary.
type CheckRunFailedJob struct {
	Workflow     string
	Name         string
	JobID        string
	Status       string
	FailureClass string
	FirstError   string
	Tests        []models.TestResult
}

// CheckRunDetails is the input of the summary.
type CheckRunDetails struct {
	Workflows  []EventChecksWorkflow
	FailedJobs []CheckRunFailedJob
	// NotAdmitted and NotAdmittedAdvisory name refused workflows.
	NotAdmitted         []string
	NotAdmittedAdvisory []string
	PolicyViolations    int
	BaseURL             string
}

// firstLine returns the first non-empty line of text, shortened.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 200 {
				line = line[:197] + "..."
			}
			return line
		}
	}
	return ""
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}

func workflowStatusLabel(wf EventChecksWorkflow) string {
	label := wf.Status
	if label == "" {
		label = "pending"
	}
	if wf.Advisory {
		label += " (advisory)"
	}
	return label
}

// RenderCheckRunSummary returns the markdown summary of the check run.
func RenderCheckRunSummary(result EventChecksResult, details CheckRunDetails) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n\n", markdownCell(result.Description))
	if len(details.Workflows) > 0 || len(details.NotAdmitted) > 0 || len(details.NotAdmittedAdvisory) > 0 {
		b.WriteString("| Workflow | Status |\n| --- | --- |\n")
		for _, wf := range details.Workflows {
			name := markdownCell(workflowLabel(wf.Name))
			if details.BaseURL != "" && wf.ID != "" {
				name = fmt.Sprintf("[%s](%s/app/workflows/%s)", name, details.BaseURL, wf.ID)
			}
			fmt.Fprintf(&b, "| %s | %s |\n", name, markdownCell(workflowStatusLabel(wf)))
		}
		for _, name := range details.NotAdmitted {
			fmt.Fprintf(&b, "| %s | not admitted |\n", markdownCell(workflowLabel(name)))
		}
		for _, name := range details.NotAdmittedAdvisory {
			fmt.Fprintf(&b, "| %s | not admitted (advisory) |\n", markdownCell(workflowLabel(name)))
		}
		b.WriteString("\n")
	}
	if details.PolicyViolations > 0 {
		fmt.Fprintf(&b, "The CI policy refused %d CI %s.\n\n", details.PolicyViolations,
			plural(details.PolicyViolations, "change", "changes"))
	}
	if len(details.FailedJobs) > 0 {
		b.WriteString("### Failed jobs\n\n")
		for _, job := range details.FailedJobs {
			name := markdownCell(job.Name)
			if details.BaseURL != "" && job.JobID != "" {
				name = fmt.Sprintf("[%s](%s/app/jobs/%s)", name, details.BaseURL, job.JobID)
			}
			line := fmt.Sprintf("- %s / %s: %s", markdownCell(workflowLabel(job.Workflow)), name, job.Status)
			if job.FailureClass != "" {
				line += fmt.Sprintf(" (`%s`)", job.FailureClass)
			}
			b.WriteString(line + "\n")
			if job.FirstError != "" {
				fmt.Fprintf(&b, "  - `%s`\n", strings.ReplaceAll(job.FirstError, "`", "'"))
			}
			for i, test := range job.Tests {
				if i == maxSummaryTestFailures {
					fmt.Fprintf(&b, "  - and %d more failed tests\n", len(job.Tests)-maxSummaryTestFailures)
					break
				}
				testName := test.Name
				if test.Classname != "" {
					testName = test.Classname + "." + test.Name
				}
				entry := fmt.Sprintf("  - test `%s` %s", strings.ReplaceAll(testName, "`", "'"), test.Status)
				if message := firstLine(test.Message); message != "" {
					entry += ": " + markdownCell(message)
				}
				b.WriteString(entry + "\n")
			}
		}
	}
	return b.String()
}

// CheckRunAnnotations returns line annotations for failed tests that name a
// file and a line.
func CheckRunAnnotations(details CheckRunDetails) []CheckRunAnnotation {
	var out []CheckRunAnnotation
	for _, job := range details.FailedJobs {
		for _, test := range job.Tests {
			if len(out) == maxSummaryAnnotations {
				return out
			}
			if test.File == "" || test.Line == nil || *test.Line <= 0 {
				continue
			}
			title := test.Name
			if test.Classname != "" {
				title = test.Classname + "." + test.Name
			}
			message := strings.TrimSpace(test.Message)
			if message == "" {
				message = "Test " + test.Status
			}
			out = append(out, CheckRunAnnotation{Path: test.File, StartLine: *test.Line, EndLine: *test.Line,
				Level: "failure", Title: title, Message: message})
		}
	}
	return out
}

// loadCheckRunDetails reads the failed jobs and their test failures. A
// missing optional store gives a summary with only the workflow table.
func (u *JobStatusUpdater) loadCheckRunDetails(ctx context.Context, input EventChecksInput) CheckRunDetails {
	details := CheckRunDetails{Workflows: input.Workflows, NotAdmitted: input.NotAdmitted,
		NotAdmittedAdvisory: input.NotAdmittedAdvisory, PolicyViolations: input.PolicyViolations, BaseURL: u.baseURL}
	nodes, ok := u.store.(checkRunNodeStore)
	if !ok || u.store == nil {
		return details
	}
	tests, _ := u.store.(checkRunTestStore)
	for _, wf := range input.Workflows {
		if wf.Status != "failed" && wf.Status != "cancelled" {
			continue
		}
		list, err := nodes.ListWorkflowNodes(ctx, wf.ID)
		if err != nil {
			continue
		}
		for _, node := range list {
			if len(details.FailedJobs) == maxSummaryFailedJobs {
				return details
			}
			if node.JobID == nil || *node.JobID == "" || (node.Status != "failed" && node.Status != "timeout") {
				continue
			}
			job, err := u.store.GetJobByID(ctx, *node.JobID)
			if err != nil || job == nil {
				continue
			}
			failed := CheckRunFailedJob{Workflow: wf.Name, Name: firstNonEmptyString(node.DisplayName, node.Name), JobID: job.JobID, Status: job.Status,
				FirstError: firstLine(firstNonEmptyString(job.FailureDetail, job.LastError))}
			if job.FailureClass != nil {
				failed.FailureClass = *job.FailureClass
			}
			if tests != nil {
				if results, err := tests.ListJobTestResults(ctx, job.JobID, []string{"fail", "error"}, 50); err == nil {
					failed.Tests = results
				}
			}
			details.FailedJobs = append(details.FailedJobs, failed)
		}
	}
	return details
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// eventCheckRun builds the "Reactorcide Checks" check run of an event.
func (u *JobStatusUpdater) eventCheckRun(ctx context.Context, event *models.CIEvent, input EventChecksInput, result EventChecksResult, target string) CheckRunUpdate {
	details := u.loadCheckRunDetails(ctx, input)
	update := CheckRunUpdate{Name: models.CIEventChecksContext, HeadSHA: event.CommitSHA, State: result.State,
		DetailsURL: target, ExternalID: event.EventID, Title: result.Description,
		Summary: RenderCheckRunSummary(result, details), Annotations: CheckRunAnnotations(details)}
	// The re-run path runs the head of a pull request again, so only a pull
	// request event has the button.
	if event.PRNumber != nil && result.State != StatusPending && result.State != StatusRunning {
		update.Actions = []CheckRunAction{{Label: "Re-run", Description: "Run every workflow of the head again",
			Identifier: RerunActionIdentifier}}
	}
	return update
}

// postEventChecks posts the aggregate: a check run when the client can post
// one (a GitHub App installation), else the commit status.
func (u *JobStatusUpdater) postEventChecks(ctx, publishCtx context.Context, client Client, event *models.CIEvent, input EventChecksInput, result EventChecksResult, target string) error {
	if publisher, ok := client.(CheckRunPublisher); ok {
		return publisher.PublishCheckRun(publishCtx, event.Repo, u.eventCheckRun(ctx, event, input, result, target))
	}
	return client.UpdateCommitStatus(publishCtx, event.Repo, StatusUpdate{
		SHA:         event.CommitSHA,
		State:       result.State,
		TargetURL:   target,
		Description: result.Description,
		Context:     models.CIEventChecksContext,
	})
}
