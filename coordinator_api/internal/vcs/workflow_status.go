package vcs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
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
	// A root workflow of a CI event counts toward the event's "Reactorcide
	// Checks" status. It posts a status of its own only when its definition
	// declares one (status.context), which the trigger processor stores in
	// StatusContext; otherwise StatusContext is empty for it.
	eventScoped := u.eventScoped(wf.CIEventID())
	contextName := wf.StatusContext
	if contextName == "" && !eventScoped {
		contextName = "Reactorcide Jobs"
	}
	// An advisory or quarantined workflow (docs/required-set.md) reports in
	// the PR comment only. It posts no status, also not its own
	// status.context (owner decision D26).
	classification := u.workflowClassification(ctx, wf, eventScoped)
	if !classification.Blocking() {
		contextName = ""
	}
	if contextName != "" && !u.statusesAllowed(ctx, wf.OrgID, wf.ProjectID) {
		// Report mode silent (docs/reporting-and-ownership.md).
		contextName = ""
	}
	if contextName != "" && contextName != models.CIEventChecksContext {
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
	}
	if eventScoped {
		if err := u.PublishEventChecks(ctx, *wf.CIEventID()); err != nil {
			u.logger.WithError(err).WithField("event_id", *wf.CIEventID()).Warn("Failed to update event checks status")
		}
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
			title, body := wf.Name, markAdvisory(u.renderWorkflowCommentBody(wf, nodes, ""), classification)
			if label := classification.Label(); label != "" {
				title = fmt.Sprintf("%s (%s)", wf.Name, label)
			}
			state := models.JSONB{"title": title, "body": body}
			entry := &models.VCSReportEntry{ReportTargetID: target.ReportTargetID, EntryKey: key,
				WorkflowID: &wf.WorkflowID, Generation: target.CurrentGeneration, Status: wf.Status, StructuredState: state}
			if err := reports.UpsertVCSReportEntry(ctx, entry); err != nil {
				return fmt.Errorf("queueing workflow report: %w", err)
			}
			return nil
		}
		if !u.commentsAllowed(ctx, wf.OrgID, wf.ProjectID) {
			return nil
		}
		marker := wf.CommentMarker
		if marker == "" {
			marker = fmt.Sprintf("<!-- reactorcide:workflows:%s -->", wf.CommitSHA)
		}
		body := markAdvisory(u.renderWorkflowCommentBody(wf, nodes, marker), classification)
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
	case "failed", "cancelled":
		// A cancelled workflow did not pass. Before, it fell to pending and
		// the status stayed pending for ever.
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
	case "cancelled":
		return "Workflow cancelled"
	case "failed":
		if reason := workflowAdmissionReason(nodes); reason != "" {
			return truncateWorkflowDescription("Workflow not admitted: "+reason, 140)
		}
		return "Workflow failed"
	default:
		return fmt.Sprintf("Workflow running (%d/%d done)", done, total)
	}
}

func workflowAdmissionReason(nodes []models.WorkflowNode) string {
	const prefix = "workflow not admitted:"
	for i := range nodes {
		reason := strings.TrimSpace(nodes[i].DecisionReason)
		if strings.HasPrefix(strings.ToLower(reason), prefix) {
			return strings.TrimSpace(reason[len(prefix):])
		}
	}
	return ""
}

func truncateWorkflowDescription(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
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
	if line := renderWorkflowOwnerLine(wf.Ownership()); line != "" {
		b.WriteString(line + "\n\n")
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
	if links := u.renderWorkflowLinkLines(nodes); links != "" {
		b.WriteString(links)
	}
	if tests := u.renderWorkflowTestLines(nodes); tests != "" {
		b.WriteString(tests)
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

type workflowByIDStore interface {
	GetWorkflowInstance(ctx context.Context, workflowID string) (*models.WorkflowInstance, error)
}

// workflowClassification returns the required set state of the root
// workflow of wf for its event. A workflow outside a CI event, or a store
// that cannot tell, is required.
func (u *JobStatusUpdater) workflowClassification(ctx context.Context, wf *models.WorkflowInstance, eventScoped bool) requiredset.Classification {
	if !eventScoped || wf == nil || wf.CIEventID() == nil {
		return requiredset.Required
	}
	securityID := wf.WorkflowSecurityID
	if wf.ParentWorkflowID != nil && wf.RootWorkflowID != nil && *wf.RootWorkflowID != "" {
		// A child workflow has the state of its root workflow.
		workflows, ok := u.store.(workflowByIDStore)
		if !ok {
			return requiredset.Required
		}
		root, err := workflows.GetWorkflowInstance(ctx, *wf.RootWorkflowID)
		if err != nil || root == nil {
			return requiredset.Required
		}
		securityID = root.WorkflowSecurityID
	}
	events, ok := u.store.(store.CIEventStore)
	if !ok {
		return requiredset.Required
	}
	event, err := events.GetCIEvent(ctx, *wf.CIEventID())
	if err != nil {
		return requiredset.Required
	}
	return requiredset.ForEvent(ctx, u.store, event).Classify(securityID)
}

// markAdvisory adds the advisory line under the heading of a workflow
// comment body. A required workflow keeps the body as it is.
func markAdvisory(body string, classification requiredset.Classification) string {
	label := classification.Label()
	if label == "" {
		return body
	}
	note := "> **Advisory — not required.** This workflow is not in the required set of the project. Its result does not change `" +
		models.CIEventChecksContext + "`.\n\n"
	if classification.State == requiredset.StateQuarantined {
		note = "> **Quarantined — not required.** The quarantine ends"
		if classification.QuarantinedUntil != nil {
			note += " at " + classification.QuarantinedUntil.UTC().Format("2006-01-02 15:04 MST")
		}
		note += "; then the workflow is required again."
		if classification.QuarantineReason != "" {
			note += " Reason: " + escapeTableCell(classification.QuarantineReason) + "."
		}
		note += " Its result does not change `" + models.CIEventChecksContext + "`.\n\n"
	}
	if index := strings.Index(body, "\n\n"); index >= 0 {
		return body[:index+2] + note + body[index+2:]
	}
	return note + body
}

// renderWorkflowOwnerLine shows the owner and contact of the workflow (WP7),
// read from the trusted CI definition. It returns "" when the owner is
// unknown or the definition names no owner or contact.
func renderWorkflowOwnerLine(o models.WorkflowOwnership) string {
	if !o.Known() {
		return ""
	}
	parts := []string{}
	if o.HasOwner() {
		names := []string{}
		if group := o.GroupName(); group != "" {
			names = append(names, inlineCode(group))
		}
		for _, subject := range o.Subjects {
			names = append(names, inlineCode(subject))
		}
		parts = append(parts, "Owner "+strings.Join(names, ", "))
	}
	if o.Contact != nil {
		if o.Contact.Channel != "" {
			parts = append(parts, "contact "+inlineCode(o.Contact.Channel))
		}
		if o.Contact.Email != "" {
			parts = append(parts, "email "+inlineCode(o.Contact.Email))
		}
		if o.Contact.HelpURL != "" && safeLinkURL(o.Contact.HelpURL) {
			parts = append(parts, "[help](<"+o.Contact.HelpURL+">)")
		}
	}
	return strings.Join(parts, " · ")
}

// inlineCode renders value as Markdown inline code. A backtick or a line
// break cannot close the span: they become a space.
func inlineCode(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '`' || r == '\n' || r == '\r' || r == '|' {
			return ' '
		}
		return r
	}, value)
	return "`" + strings.TrimSpace(value) + "`"
}

// safeLinkURL reports whether value can go inside a Markdown <...> link.
func safeLinkURL(value string) bool {
	if !strings.HasPrefix(value, "https://") && !strings.HasPrefix(value, "http://") {
		return false
	}
	return !strings.ContainsAny(value, "<> \t\n\r`")
}
