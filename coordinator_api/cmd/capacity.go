package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/backlog"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resourcehistory"
	"github.com/urfave/cli/v2"
)

// BacklogCommand shows GET /api/v1/backlog: the waiting work of each queue
// (docs/workers.md "Backlog and autoscaling").
var BacklogCommand = &cli.Command{
	Name:  "backlog",
	Usage: "Show the waiting jobs of each queue",
	Description: "Without --org the token needs workers:read or workers:manage for all organizations. " +
		"With --org it needs one of them for that organization, and only its queues show.",
	Flags: append(apiFlags(), formatFlag(), &cli.StringFlag{Name: "org", Usage: "Only the queues of this organization"}),
	Action: func(ctx *cli.Context) error {
		client, err := newAPIClient(ctx)
		if err != nil {
			return err
		}
		path := "/api/v1/backlog"
		if org := strings.TrimSpace(ctx.String("org")); org != "" {
			path += "?org=" + url.QueryEscape(org)
		}
		var snapshot backlog.Snapshot
		if err := client.doJSON(http.MethodGet, path, nil, http.StatusOK, &snapshot); err != nil {
			return err
		}
		return render(ctx.String("format"), snapshot, func(w *tabwriter.Writer) { renderBacklog(w, snapshot) })
	},
}

func renderBacklog(w *tabwriter.Writer, snapshot backlog.Snapshot) {
	fmt.Fprintln(w, "QUEUE\tWORKER CLASS\tCHARACTERISTICS\tWAITING\tOLDEST (S)\tCPU (M)\tMEMORY")
	for _, queue := range snapshot.Queues {
		name := queue.DisplayName
		if name == "" {
			name = queue.QueueUUID
		}
		if !queue.Known {
			name += " (no queue row)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%s\n", name, dashIfEmpty(queue.WorkerClass), characteristicsText(queue.Characteristics),
			queue.Count, queue.OldestAgeSeconds, queue.CPURequestMillicores, bytesText(queue.MemoryLimitBytes))
	}
	fmt.Fprintf(w, "TOTAL\t\t\t%d\t%d\t%d\t%s\n", snapshot.Totals.Count, snapshot.Totals.OldestAgeSeconds,
		snapshot.Totals.CPURequestMillicores, bytesText(snapshot.Totals.MemoryLimitBytes))
}

func characteristicsText(values map[string]string) string {
	if len(values) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(values))
	for key, value := range values {
		parts = append(parts, key+"="+value)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func dashIfEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func bytesText(value int64) string {
	const mi = 1024 * 1024
	if value <= 0 {
		return "0"
	}
	return strconv.FormatInt((value+mi-1)/mi, 10) + "Mi"
}

// resourceSuggestion is the body of GET /api/v1/jobs/{id}/resource-suggestion.
type resourceSuggestion struct {
	JobID     string  `json:"job_id" yaml:"job_id"`
	ProjectID *string `json:"project_id" yaml:"project_id"`
	Name      string  `json:"name" yaml:"name"`
	resourcehistory.Suggestion
}

// jobsResourceSuggestionCommand is "jobs resource-suggestion <job-id>".
func jobsResourceSuggestionCommand() *cli.Command {
	return &cli.Command{
		Name:      "resource-suggestion",
		Usage:     "Suggest cpu_request and memory_limit from the measured runs of a job (p95)",
		ArgsUsage: "<job-id>",
		Flags: append(apiFlags(), formatFlag(),
			&cli.IntFlag{Name: "runs", Usage: "How many of the newest runs to read (default 50)"}),
		Action: func(ctx *cli.Context) error {
			jobID, client, err := jobTarget(ctx)
			if err != nil {
				return err
			}
			path := "/api/v1/jobs/" + url.PathEscape(jobID) + "/resource-suggestion"
			if ctx.IsSet("runs") {
				path += "?limit=" + strconv.Itoa(ctx.Int("runs"))
			}
			var suggestion resourceSuggestion
			if err := client.doJSON(http.MethodGet, path, nil, http.StatusOK, &suggestion); err != nil {
				return err
			}
			return render(ctx.String("format"), suggestion, func(w *tabwriter.Writer) {
				fmt.Fprintf(w, "job\t%s (%s)\n", suggestion.Name, suggestion.JobID)
				fmt.Fprintf(w, "runs\t%d (needs %d)\n", suggestion.Runs, suggestion.MinRuns)
				if !suggestion.Sufficient {
					fmt.Fprintf(w, "suggestion\tnot enough measured runs\n")
					return
				}
				fmt.Fprintf(w, "cpu_p95\t%dm\n", suggestion.CPUP95Millicores)
				fmt.Fprintf(w, "memory_peak_p95\t%s\n", bytesText(suggestion.MemoryPeakP95Bytes))
				fmt.Fprintf(w, "oom_runs\t%d\n", suggestion.OOMRuns)
				fmt.Fprintf(w, "cpu_request\t%s\n", suggestion.CPURequest)
				fmt.Fprintf(w, "memory_limit\t%s\n", suggestion.MemoryLimit)
			})
		},
	}
}
