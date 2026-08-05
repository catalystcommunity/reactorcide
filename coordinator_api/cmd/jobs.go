package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"
)

// JobsCommand exposes the /api/v1/jobs endpoints. Job creation lives in the
// separate "submit" command, which also handles overlays and job-file parsing.
var JobsCommand = &cli.Command{
	Name:  "jobs",
	Usage: "List and control jobs on a Reactorcide coordinator",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name:  "list",
			Usage: "List jobs",
			Flags: append(append(apiFlags(), paginationFlags()...),
				formatFlag(),
				&cli.StringFlag{Name: "status", Usage: "Filter by job status"},
				&cli.StringFlag{Name: "queue-name", Usage: "Filter by queue name"},
				&cli.StringFlag{Name: "source-type", Usage: "Filter by source type"},
				&cli.StringFlag{Name: "project-id", Usage: "Filter by project ID"},
				&cli.StringFlag{Name: "workflow-id", Usage: "Filter by workflow ID"},
				&cli.StringFlag{Name: "user-id", Usage: "Filter by owning user ID (admins only)"},
			),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				query := pagedQuery(ctx, map[string]string{
					"status":      ctx.String("status"),
					"queue_name":  ctx.String("queue-name"),
					"source_type": ctx.String("source-type"),
					"project_id":  ctx.String("project-id"),
					"workflow_id": ctx.String("workflow-id"),
					"user_id":     ctx.String("user-id"),
				})
				var resp listJobsResponse
				if err := client.doJSON(http.MethodGet, "/api/v1/jobs"+query, nil, http.StatusOK, &resp); err != nil {
					return err
				}
				return render(ctx.String("format"), resp.Jobs, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "JOB ID\tNAME\tSTATUS\tQUEUE\tCREATED\tCOMPLETED")
					for _, job := range resp.Jobs {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
							job.JobID, job.Name, job.Status, job.QueueName,
							job.CreatedAt.Format(time.RFC3339), timeOrDash(job.CompletedAt))
					}
				})
			},
		},
		{
			Name:      "get",
			Usage:     "Get a job by ID",
			ArgsUsage: "<job-id>",
			Flags:     append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				return jobAction(ctx, http.MethodGet, "", http.StatusOK)
			},
		},
		{
			Name:      "cancel",
			Usage:     "Cancel a job (graceful, allows cleanup hooks to run)",
			ArgsUsage: "<job-id>",
			Flags:     append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				return jobAction(ctx, http.MethodPut, "/cancel", http.StatusOK)
			},
		},
		{
			Name:      "kill",
			Usage:     "Kill a job immediately (admin only, no cleanup grace period)",
			ArgsUsage: "<job-id>",
			Flags:     append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				return jobAction(ctx, http.MethodPost, "/kill", http.StatusOK)
			},
		},
		{
			Name:      "retry",
			Usage:     "Retry a job as a new job in the same workflow node",
			ArgsUsage: "<job-id>",
			Flags:     append(apiFlags(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				return jobAction(ctx, http.MethodPost, "/retry", http.StatusCreated)
			},
		},
		{
			Name:      "delete",
			Usage:     "Delete a job and its telemetry",
			ArgsUsage: "<job-id>",
			Flags:     apiFlags(),
			Action: func(ctx *cli.Context) error {
				jobID, client, err := jobTarget(ctx)
				if err != nil {
					return err
				}
				if err := client.doJSON(http.MethodDelete, "/api/v1/jobs/"+url.PathEscape(jobID), nil, http.StatusNoContent, nil); err != nil {
					return err
				}
				fmt.Printf("Job deleted: %s\n", jobID)
				return nil
			},
		},
		{
			Name:      "metrics",
			Usage:     "Get resource metrics for a job",
			ArgsUsage: "<job-id>",
			Flags: append(apiFlags(),
				formatFlag(),
				&cli.StringSliceFlag{Name: "metric", Usage: "Metric name to include (repeatable, default: all)"},
				&cli.StringFlag{Name: "from", Usage: "Start of the time range (RFC3339)"},
				&cli.StringFlag{Name: "to", Usage: "End of the time range (RFC3339)"},
				&cli.IntFlag{Name: "max-points", Usage: "Maximum points per metric series"},
			),
			Action: func(ctx *cli.Context) error {
				jobID, client, err := jobTarget(ctx)
				if err != nil {
					return err
				}
				values := url.Values{}
				for _, metric := range ctx.StringSlice("metric") {
					values.Add("metric", metric)
				}
				if from := ctx.String("from"); from != "" {
					values.Set("from", from)
				}
				if to := ctx.String("to"); to != "" {
					values.Set("to", to)
				}
				if maxPoints := ctx.Int("max-points"); maxPoints > 0 {
					values.Set("max_points", fmt.Sprint(maxPoints))
				}
				path := "/api/v1/jobs/" + url.PathEscape(jobID) + "/metrics"
				if len(values) > 0 {
					path += "?" + values.Encode()
				}
				var result map[string]interface{}
				if err := client.doJSON(http.MethodGet, path, nil, http.StatusOK, &result); err != nil {
					return err
				}
				format := ctx.String("format")
				if format == "table" {
					format = "json"
				}
				return render(format, result, nil)
			},
		},
	},
}

type listJobsResponse struct {
	Jobs   []jobSummary `json:"jobs"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// jobSummary is the subset of handlers.JobResponse that "jobs list" renders.
// Use "jobs get --format json" for the full job body.
type jobSummary struct {
	JobID       string     `json:"job_id" yaml:"job_id"`
	Name        string     `json:"name" yaml:"name"`
	Status      string     `json:"status" yaml:"status"`
	QueueName   string     `json:"queue_name" yaml:"queue_name"`
	CreatedAt   time.Time  `json:"created_at" yaml:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty" yaml:"completed_at,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty" yaml:"exit_code,omitempty"`
	WorkflowID  *string    `json:"workflow_id,omitempty" yaml:"workflow_id,omitempty"`
	ProjectID   *string    `json:"project_id,omitempty" yaml:"project_id,omitempty"`
	LastError   string     `json:"last_error,omitempty" yaml:"last_error,omitempty"`
}

func jobTarget(ctx *cli.Context) (string, *apiClient, error) {
	if ctx.NArg() < 1 {
		return "", nil, fmt.Errorf("usage: reactorcide jobs %s <job-id>", ctx.Command.Name)
	}
	client, err := newAPIClient(ctx)
	if err != nil {
		return "", nil, err
	}
	return ctx.Args().Get(0), client, nil
}

// jobAction runs a job endpoint that responds with the full job body and
// prints it. suffix is the sub-path after the job ID ("" for the job itself).
func jobAction(ctx *cli.Context, method, suffix string, expectedStatus int) error {
	jobID, client, err := jobTarget(ctx)
	if err != nil {
		return err
	}
	var job map[string]interface{}
	path := "/api/v1/jobs/" + url.PathEscape(jobID) + suffix
	if err := client.doJSON(method, path, nil, expectedStatus, &job); err != nil {
		return err
	}
	return render(ctx.String("format"), job, func(w *tabwriter.Writer) {
		for _, field := range []string{"job_id", "name", "status", "queue_name", "exit_code", "created_at", "started_at", "completed_at", "workflow_id", "last_error"} {
			if value, ok := job[field]; ok && value != nil {
				fmt.Fprintf(w, "%s\t%v\n", field, value)
			}
		}
	})
}
