package vcs

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/testreports"
)

// Test results (WP15, docs/artifacts-and-tests.md) in the PR comment entry
// of a workflow or a job: the counts, then the failing tests with the first
// lines of their messages. A quarantined failure is shown as quarantined and
// does not make the summary fail. A store without test results shows none.

const (
	maxCommentFailedTests = 5
	maxCommentErrorLines  = 3
	maxCommentLineWidth   = 160
)

type jobTestReport struct {
	summary     models.JobTestSummary
	failures    []models.TestResult
	quarantined map[string]bool
}

func (u *JobStatusUpdater) jobTestReports(jobIDs []string) map[string]jobTestReport {
	if u == nil || u.store == nil || len(jobIDs) == 0 {
		return nil
	}
	results, ok := u.store.(store.TestResultStore)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summaries, err := results.ListJobTestSummaries(ctx, jobIDs)
	if err != nil {
		if u.logger != nil {
			u.logger.WithError(err).Warn("Failed to read test summaries for the PR comment")
		}
		return nil
	}
	out := map[string]jobTestReport{}
	quarantinesByProject := map[string]map[string]bool{}
	for jobID, summary := range summaries {
		report := jobTestReport{summary: summary, quarantined: map[string]bool{}}
		if summary.Failed+summary.Errored > 0 {
			failures, err := results.ListJobTestResults(ctx, jobID, []string{models.TestStatusError, models.TestStatusFail}, 50)
			if err == nil {
				report.failures = failures
			}
			if len(failures) > 0 && failures[0].ProjectID != nil {
				projectID := *failures[0].ProjectID
				active, seen := quarantinesByProject[projectID]
				if !seen {
					active = map[string]bool{}
					if st, ok := u.store.(store.TestQuarantineStore); ok {
						if rows, err := st.ListTestQuarantines(ctx, projectID, false, time.Now().UTC()); err == nil {
							for _, row := range rows {
								active[row.TestID] = true
							}
						}
					}
					quarantinesByProject[projectID] = active
				}
				report.quarantined = active
			}
		}
		out[jobID] = report
	}
	return out
}

// renderTestCounts is the one-line test summary.
func renderTestCounts(summary models.JobTestSummary) string {
	failures := summary.Failed + summary.Errored
	icon := "✅"
	if summary.UnquarantinedFailures() > 0 {
		icon = "❌"
	} else if failures > 0 {
		icon = "🟨"
	}
	parts := []string{fmt.Sprintf("%d passed", summary.Passed)}
	if failures > 0 {
		failed := fmt.Sprintf("%d failed", failures)
		if summary.QuarantinedFailures > 0 {
			failed += fmt.Sprintf(" (%d quarantined)", summary.QuarantinedFailures)
		}
		parts = append(parts, failed)
	}
	if summary.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", summary.Skipped))
	}
	if summary.Flaky > 0 {
		parts = append(parts, fmt.Sprintf("%d flaky", summary.Flaky))
	}
	line := fmt.Sprintf("%s %d tests: %s", icon, summary.Total, strings.Join(parts, ", "))
	if summary.QuarantineOverride {
		line += " · the node passed: every failure is quarantined"
	}
	if summary.Truncated || len(summary.ParseErrors) > 0 {
		line += " · some report data was not read"
	}
	return line
}

var backtickRun = regexp.MustCompile("`{3,}")

// codeBlockText keeps untrusted text inside a fenced code block.
func codeBlockText(value string) string {
	return backtickRun.ReplaceAllString(value, "'''")
}

// inlineCodeText keeps untrusted text inside inline code.
func inlineCodeText(value string) string {
	value = strings.NewReplacer("`", "'", "\n", " ", "\r", " ").Replace(value)
	if len([]rune(value)) > 200 {
		value = string([]rune(value)[:200]) + "…"
	}
	return value
}

func testDisplayName(result models.TestResult) string {
	parts := []string{}
	for _, part := range []string{result.Classname, result.Name} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return result.Suite
	}
	return strings.Join(parts, " › ")
}

// renderTestFailures lists the failing tests: the failures that are not
// quarantined first.
func renderTestFailures(report jobTestReport, indent string) string {
	if len(report.failures) == 0 {
		return ""
	}
	ordered := make([]models.TestResult, 0, len(report.failures))
	for _, f := range report.failures {
		if !report.quarantined[f.TestID] {
			ordered = append(ordered, f)
		}
	}
	for _, f := range report.failures {
		if report.quarantined[f.TestID] {
			ordered = append(ordered, f)
		}
	}
	var b strings.Builder
	for i, f := range ordered {
		if i >= maxCommentFailedTests {
			fmt.Fprintf(&b, "%s- … and %d more\n", indent, len(ordered)-maxCommentFailedTests)
			break
		}
		label := "failed"
		if f.Status == models.TestStatusError {
			label = "error"
		}
		if report.quarantined[f.TestID] {
			label += ", quarantined"
		}
		location := ""
		if f.File != "" {
			location = " at `" + inlineCodeText(f.File)
			if f.Line != nil {
				location += fmt.Sprintf(":%d", *f.Line)
			}
			location += "`"
		}
		fmt.Fprintf(&b, "%s- `%s` (%s)%s\n", indent, inlineCodeText(testDisplayName(f)), label, location)
		if lines := testreports.FirstLines(f.Message, maxCommentErrorLines, maxCommentLineWidth); len(lines) > 0 {
			fmt.Fprintf(&b, "%s  ```\n", indent)
			for _, line := range lines {
				fmt.Fprintf(&b, "%s  %s\n", indent, codeBlockText(line))
			}
			fmt.Fprintf(&b, "%s  ```\n", indent)
		}
	}
	return b.String()
}

// renderWorkflowTestLines lists the test results of the jobs of the nodes.
func (u *JobStatusUpdater) renderWorkflowTestLines(nodes []models.WorkflowNode) string {
	ids := []string{}
	for i := range nodes {
		if nodes[i].JobID != nil && *nodes[i].JobID != "" {
			ids = append(ids, *nodes[i].JobID)
		}
	}
	reports := u.jobTestReports(ids)
	if len(reports) == 0 {
		return ""
	}
	var b strings.Builder
	for i := range nodes {
		node := &nodes[i]
		if node.JobID == nil {
			continue
		}
		report, ok := reports[*node.JobID]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", escapeTableCell(workflowNodeDisplayName(node)), renderTestCounts(report.summary))
		b.WriteString(renderTestFailures(report, "  "))
	}
	if b.Len() == 0 {
		return ""
	}
	return "\nTests:\n" + b.String()
}

// renderJobTestLine shows the test results of one job.
func (u *JobStatusUpdater) renderJobTestLine(job *models.Job) string {
	if job == nil {
		return ""
	}
	report, ok := u.jobTestReports([]string{job.JobID})[job.JobID]
	if !ok {
		return ""
	}
	return "\n**Tests:** " + renderTestCounts(report.summary) + "\n" + renderTestFailures(report, "")
}
