package vcs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Job links (docs/builds-and-caches.md) in the PR comment entry of a job or a
// workflow. A store that has no links shows none.

func (u *JobStatusUpdater) jobLinks(jobIDs []string) map[string][]models.JobLink {
	if u == nil || u.store == nil || len(jobIDs) == 0 {
		return nil
	}
	linkStore, ok := u.store.(store.JobLinkStore)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	links, err := linkStore.ListJobLinksForJobs(ctx, jobIDs)
	if err != nil {
		if u.logger != nil {
			u.logger.WithError(err).Warn("Failed to read job links for the PR comment")
		}
		return nil
	}
	return links
}

// markdownLinkText escapes the characters that end a Markdown link text.
func markdownLinkText(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`, "|", `\|`, "\n", " ", "\r", " ")
	return replacer.Replace(value)
}

// markdownLinkURL keeps a URL inside the parentheses of a Markdown link.
func markdownLinkURL(value string) string {
	replacer := strings.NewReplacer("(", "%28", ")", "%29", " ", "%20", "<", "%3C", ">", "%3E")
	return replacer.Replace(value)
}

func renderLinkList(links []models.JobLink) string {
	parts := make([]string, 0, len(links))
	for _, link := range links {
		parts = append(parts, fmt.Sprintf("[%s](%s)", markdownLinkText(link.Name), markdownLinkURL(link.URL)))
	}
	return strings.Join(parts, " · ")
}

// renderWorkflowLinkLines lists the links of the jobs of the nodes.
func (u *JobStatusUpdater) renderWorkflowLinkLines(nodes []models.WorkflowNode) string {
	ids := []string{}
	for i := range nodes {
		if nodes[i].JobID != nil && *nodes[i].JobID != "" {
			ids = append(ids, *nodes[i].JobID)
		}
	}
	links := u.jobLinks(ids)
	if len(links) == 0 {
		return ""
	}
	var b strings.Builder
	for i := range nodes {
		node := &nodes[i]
		if node.JobID == nil || len(links[*node.JobID]) == 0 {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", escapeTableCell(workflowNodeDisplayName(node)), renderLinkList(links[*node.JobID]))
	}
	if b.Len() == 0 {
		return ""
	}
	return "\nLinks:\n" + b.String()
}

// renderJobLinkLine lists the links of one job.
func (u *JobStatusUpdater) renderJobLinkLine(job *models.Job) string {
	if job == nil {
		return ""
	}
	links := u.jobLinks([]string{job.JobID})[job.JobID]
	if len(links) == 0 {
		return ""
	}
	return "\n**Links:** " + renderLinkList(links)
}
