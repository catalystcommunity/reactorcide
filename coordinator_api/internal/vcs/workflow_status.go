package vcs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type workflowReportStore interface {
	UpsertVCSReportTarget(context.Context, *models.VCSReportTarget) error
	UpsertVCSReportEntry(context.Context, *models.VCSReportEntry) error
}

// UpdateWorkflowStatus publishes the single aggregate workflow check and
// rolling PR comment used for dynamic workflow jobs.
func (u *JobStatusUpdater) UpdateWorkflowStatus(ctx context.Context, wf *models.WorkflowInstance, nodes []models.WorkflowNode) error {
	if wf == nil || wf.VCSProvider == "" || wf.VCSRepo == "" || wf.CommitSHA == "" {
		return nil
	}
	provider := Provider(wf.VCSProvider)
	client := u.getClientForWorkflow(ctx, wf, provider)
	if client == nil {
		u.logger.WithField("provider", provider).Debug("No VCS client available for workflow")
		return nil
	}
	contextName := wf.StatusContext
	if contextName == "" {
		contextName = "Reactorcide Jobs"
	}
	update := StatusUpdate{
		SHA:         wf.CommitSHA,
		State:       u.mapWorkflowStatusToVCSStatus(wf.Status),
		TargetURL:   u.getWorkflowURL(wf.WorkflowID),
		Description: u.getWorkflowStatusDescription(wf, nodes),
		Context:     contextName,
	}
	if err := client.UpdateCommitStatus(ctx, wf.VCSRepo, update); err != nil {
		return fmt.Errorf("updating workflow commit status: %w", err)
	}
	if wf.PRNumber != nil && *wf.PRNumber > 0 {
		if reports, ok := u.store.(workflowReportStore); ok {
			target := &models.VCSReportTarget{
				OrgID: wf.OrgID, ProjectID: wf.ProjectID, Provider: wf.VCSProvider,
				Repository: wf.VCSRepo, TargetType: "pull_request",
				ExternalTargetID: fmt.Sprintf("%d", *wf.PRNumber),
				RootMarker:       "<!-- reactorcide:report:v1 -->", CurrentGeneration: 1,
			}
			if err := reports.UpsertVCSReportTarget(ctx, target); err != nil {
				return fmt.Errorf("creating workflow report target: %w", err)
			}
			key := wf.WorkflowSecurityID
			if key == "" {
				key = wf.WorkflowID
			}
			state := models.JSONB{"title": wf.Name, "body": u.renderWorkflowCommentBody(wf, nodes, "")}
			entry := &models.VCSReportEntry{ReportTargetID: target.ReportTargetID, EntryKey: key,
				WorkflowID: &wf.WorkflowID, Generation: target.CurrentGeneration, Status: wf.Status, StructuredState: state}
			if err := reports.UpsertVCSReportEntry(ctx, entry); err != nil {
				return fmt.Errorf("queueing workflow report: %w", err)
			}
			return nil
		}
		marker := wf.CommentMarker
		if marker == "" {
			marker = fmt.Sprintf("<!-- reactorcide:workflows:%s -->", wf.CommitSHA)
		}
		body := u.renderWorkflowCommentBody(wf, nodes, marker)
		if err := client.UpsertPRCommentByMarker(ctx, wf.VCSRepo, *wf.PRNumber, marker, body); err != nil {
			u.logger.WithError(err).WithFields(map[string]interface{}{
				"repo":        wf.VCSRepo,
				"pr_number":   *wf.PRNumber,
				"workflow_id": wf.WorkflowID,
			}).Warn("Failed to update workflow PR comment")
		}
	}
	return nil
}

func (u *JobStatusUpdater) getClientForWorkflow(ctx context.Context, wf *models.WorkflowInstance, provider Provider) Client {
	if client := u.getProjectClient(ctx, wf.ProjectID, provider); client != nil {
		return client
	}
	if client, ok := u.vcsClients[provider]; ok {
		return client
	}
	return nil
}

func (u *JobStatusUpdater) mapWorkflowStatusToVCSStatus(status string) StatusState {
	switch status {
	case "evaluating", "running":
		return StatusPending
	case "success", "skipped":
		return StatusSuccess
	case "failed":
		return StatusFailure
	default:
		return StatusPending
	}
}

func (u *JobStatusUpdater) getWorkflowStatusDescription(wf *models.WorkflowInstance, nodes []models.WorkflowNode) string {
	total := len(nodes)
	if total == 0 {
		return "Workflow evaluated with no jobs to run"
	}
	done := 0
	for _, node := range nodes {
		if workflowNodeTerminal(node.Status) {
			done++
		}
	}
	switch wf.Status {
	case "success":
		return fmt.Sprintf("Workflow passed (%d jobs)", total)
	case "skipped":
		return "Workflow skipped"
	case "failed":
		return "Workflow failed"
	default:
		return fmt.Sprintf("Workflow running (%d/%d done)", done, total)
	}
}

func (u *JobStatusUpdater) renderWorkflowCommentBody(wf *models.WorkflowInstance, nodes []models.WorkflowNode, marker string) string {
	shortSHA := wf.CommitSHA
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}
	name := wf.Name
	if name == "" {
		name = "Reactorcide Jobs"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## %s for commit `%s`\n\n", name, shortSHA)
	if wf.CIOrigin != "" && wf.ExecutionProfile != "" {
		fmt.Fprintf(&b, "CI origin `%s` · profile `%s`\n\n", wf.CIOrigin, wf.ExecutionProfile)
	}
	b.WriteString("| Job | Status | Duration | Reason |\n")
	b.WriteString("|-----|--------|----------|--------|\n")
	for i := range nodes {
		node := &nodes[i]
		fmt.Fprintf(
			&b,
			"| %s | %s | %s | %s |\n",
			escapeTableCell(workflowNodeDisplayName(node)),
			escapeTableCell(renderWorkflowNodeStatus(node.Status)),
			escapeTableCell(renderWorkflowNodeDuration(node)),
			escapeTableCell(node.DecisionReason),
		)
	}
	if authority := renderNodeAuthorityLines(nodes); authority != "" {
		b.WriteString(authority)
	}
	fmt.Fprintf(&b, "\n<sub>Updated %s · %s</sub>\n", time.Now().UTC().Format(time.RFC3339), marker)
	return b.String()
}

// renderNodeAuthorityLines lists the effective authority of every node that
// carries a policy-controlled override, so an operator can see the node CI
// origin and profile without environment values.
func renderNodeAuthorityLines(nodes []models.WorkflowNode) string {
	var b strings.Builder
	for i := range nodes {
		node := &nodes[i]
		if !node.HasAuthorityOverride() {
			continue
		}
		shortSHA := node.CISHA
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		fmt.Fprintf(&b, "- `%s`: CI `%s@%s` · profile `%s`\n",
			workflowNodeDisplayName(node), node.CIOrigin, shortSHA, node.ExecutionProfile)
	}
	if b.Len() == 0 {
		return ""
	}
	return "\nPolicy-controlled node authority:\n" + b.String()
}

func workflowNodeDisplayName(node *models.WorkflowNode) string {
	if node.DisplayName != "" {
		return node.DisplayName
	}
	if node.Name != "" {
		return node.Name
	}
	return node.NodeID
}

func renderWorkflowNodeStatus(status string) string {
	switch status {
	case "pending":
		return "⏳ pending"
	case "waiting":
		return "⏳ waiting"
	case "submitted":
		return "⏳ submitted"
	case "running":
		return "🟡 running"
	case "completed":
		return "✅ succeeded"
	case "failed":
		return "❌ failed"
	case "skipped":
		return "⏭️ skipped"
	case "cancelled":
		return "⚠️ cancelled"
	case "timeout":
		return "⏱️ timed out"
	default:
		if status == "" {
			return "❓ unknown"
		}
		return "❓ " + status
	}
}

func renderWorkflowNodeDuration(node *models.WorkflowNode) string {
	if node.CompletedAt != nil && node.LastSuccessfulDurationMs != nil {
		return fmt.Sprintf("%.3fs", float64(*node.LastSuccessfulDurationMs)/1000)
	}
	if !workflowNodeTerminal(node.Status) && node.LastSuccessfulDurationMs != nil {
		return fmt.Sprintf("est %.3fs", float64(*node.LastSuccessfulDurationMs)/1000)
	}
	return "-"
}

func workflowNodeTerminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "timeout" || status == "skipped"
}

func (u *JobStatusUpdater) getWorkflowURL(workflowID string) string {
	if u.baseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/app/workflows/%s", u.baseURL, workflowID)
}
