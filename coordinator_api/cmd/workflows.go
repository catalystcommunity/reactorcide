package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"text/tabwriter"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/urfave/cli/v2"
)

var WorkflowsCommand = &cli.Command{
	Name:  "workflows",
	Usage: "List and control workflows on a Reactorcide coordinator",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name:  "list",
			Usage: "List workflows",
			Flags: append(append(apiFlags(), paginationFlags()...),
				formatFlag(),
				&cli.StringFlag{Name: "status", Usage: "Filter by workflow status"},
				&cli.StringFlag{Name: "project-id", Usage: "Filter by project ID"},
				&cli.StringFlag{Name: "user-id", Usage: "Filter by owning user ID (admins only)"},
			),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				query := pagedQuery(ctx, map[string]string{
					"status":     ctx.String("status"),
					"project_id": ctx.String("project-id"),
					"user_id":    ctx.String("user-id"),
				})
				var resp listWorkflowsResponse
				if err := client.doJSON(http.MethodGet, "/api/v1/workflows"+query, nil, http.StatusOK, &resp); err != nil {
					return err
				}
				return render(ctx.String("format"), resp.Workflows, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "WORKFLOW ID\tNAME\tSTATUS\tJOBS\tRUNNING\tFAILED\tCREATED")
					for _, wf := range resp.Workflows {
						fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
							wf.WorkflowID, wf.Name, wf.Status, wf.JobCount,
							wf.RunningCount, wf.FailedCount, wf.CreatedAt.Format(time.RFC3339))
					}
				})
			},
		},
		{
			Name:      "get",
			Usage:     "Get a workflow by ID",
			ArgsUsage: "<workflow-id>",
			Flags:     append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				workflowID, client, err := workflowTarget(ctx)
				if err != nil {
					return err
				}
				var summary models.WorkflowSummary
				if err := client.doJSON(http.MethodGet, "/api/v1/workflows/"+url.PathEscape(workflowID), nil, http.StatusOK, &summary); err != nil {
					return err
				}
				return renderWorkflow(ctx.String("format"), &summary)
			},
		},
		{
			Name:      "cancel",
			Usage:     "Cancel a workflow and its non-terminal jobs",
			ArgsUsage: "<workflow-id>",
			Flags:     append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				workflowID, client, err := workflowTarget(ctx)
				if err != nil {
					return err
				}
				var instance models.WorkflowInstance
				if err := client.doJSON(http.MethodPut, "/api/v1/workflows/"+url.PathEscape(workflowID)+"/cancel", nil, http.StatusOK, &instance); err != nil {
					return err
				}
				return renderWorkflowInstance(ctx.String("format"), &instance)
			},
		},
		{
			Name:      "retry",
			Usage:     "Retry a workflow as a new instance, leaving the original for history",
			ArgsUsage: "<workflow-id>",
			Flags:     append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				workflowID, client, err := workflowTarget(ctx)
				if err != nil {
					return err
				}
				var instance models.WorkflowInstance
				if err := client.doJSON(http.MethodPost, "/api/v1/workflows/"+url.PathEscape(workflowID)+"/retry", nil, http.StatusCreated, &instance); err != nil {
					return err
				}
				return renderWorkflowInstance(ctx.String("format"), &instance)
			},
		},
		{
			Name:      "retry-unsuccessful",
			Usage:     "Retry the failed and cancelled jobs of a workflow in place",
			ArgsUsage: "<workflow-id>",
			Flags:     append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				workflowID, client, err := workflowTarget(ctx)
				if err != nil {
					return err
				}
				var resp retryUnsuccessfulResponse
				if err := client.doJSON(http.MethodPost, "/api/v1/workflows/"+url.PathEscape(workflowID)+"/retry-unsuccessful", nil, http.StatusOK, &resp); err != nil {
					return err
				}
				// A partial success returns 200 with both fields set, so
				// report the retried jobs before surfacing the error.
				if err := render(ctx.String("format"), resp.Jobs, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "JOB ID\tNAME\tSTATUS")
					for _, job := range resp.Jobs {
						fmt.Fprintf(w, "%s\t%s\t%s\n", job.JobID, job.Name, job.Status)
					}
				}); err != nil {
					return err
				}
				if resp.Error != "" {
					return fmt.Errorf("some jobs could not be retried: %s", resp.Error)
				}
				return nil
			},
		},
	},
}

type listWorkflowsResponse struct {
	Workflows []models.WorkflowSummary `json:"workflows"`
	Total     int                      `json:"total"`
	Limit     int                      `json:"limit"`
	Offset    int                      `json:"offset"`
}

type retryUnsuccessfulResponse struct {
	Jobs  []jobSummary `json:"jobs"`
	Error string       `json:"error,omitempty"`
}

func workflowTarget(ctx *cli.Context) (string, *apiClient, error) {
	if ctx.NArg() < 1 {
		return "", nil, fmt.Errorf("usage: reactorcide workflows %s <workflow-id>", ctx.Command.Name)
	}
	client, err := newAPIClient(ctx)
	if err != nil {
		return "", nil, err
	}
	return ctx.Args().Get(0), client, nil
}

func renderWorkflow(format string, wf *models.WorkflowSummary) error {
	return render(format, wf, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "workflow_id\t%s\n", wf.WorkflowID)
		fmt.Fprintf(w, "name\t%s\n", wf.Name)
		fmt.Fprintf(w, "kind\t%s\n", wf.Kind)
		fmt.Fprintf(w, "status\t%s\n", wf.Status)
		fmt.Fprintf(w, "queue_name\t%s\n", wf.QueueName)
		fmt.Fprintf(w, "jobs\t%d\n", wf.JobCount)
		fmt.Fprintf(w, "running\t%d\n", wf.RunningCount)
		fmt.Fprintf(w, "completed\t%d\n", wf.CompletedCount)
		fmt.Fprintf(w, "failed\t%d\n", wf.FailedCount)
		fmt.Fprintf(w, "skipped\t%d\n", wf.SkippedCount)
		fmt.Fprintf(w, "created_at\t%s\n", wf.CreatedAt.Format(time.RFC3339))
		fmt.Fprintf(w, "completed_at\t%s\n", timeOrDash(wf.CompletedAt))
		if wf.VCSRepo != "" {
			fmt.Fprintf(w, "vcs_repo\t%s\n", wf.VCSRepo)
		}
		if wf.CommitSHA != "" {
			fmt.Fprintf(w, "commit_sha\t%s\n", wf.CommitSHA)
		}
	})
}

// renderWorkflowInstance prints the workflow row returned by the cancel and
// retry endpoints, which respond with the instance rather than the
// job-count summary that list and get return.
func renderWorkflowInstance(format string, wf *models.WorkflowInstance) error {
	return render(format, wf, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "workflow_id\t%s\n", wf.WorkflowID)
		fmt.Fprintf(w, "name\t%s\n", wf.Name)
		fmt.Fprintf(w, "status\t%s\n", wf.Status)
		fmt.Fprintf(w, "queue_name\t%s\n", wf.QueueName)
		fmt.Fprintf(w, "created_at\t%s\n", wf.CreatedAt.Format(time.RFC3339))
		fmt.Fprintf(w, "completed_at\t%s\n", timeOrDash(wf.CompletedAt))
		if wf.LastError != "" {
			fmt.Fprintf(w, "last_error\t%s\n", wf.LastError)
		}
	})
}
