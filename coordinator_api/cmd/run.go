package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/urfave/cli/v2"
)

// RunCommand starts a new evaluated run on any ref of a project (a manual
// CI event). The coordinator resolves the commit and applies the normal
// policy of that ref. The run never gets the authority of an earlier run.
var RunCommand = &cli.Command{
	Name:  "run",
	Usage: "Start a new evaluated run on a branch, a tag, or a pull request head of a project",
	Description: "Give --ref (a branch name, refs/heads/NAME or refs/tags/NAME) or --pr. The coordinator resolves the " +
		"commit through the VCS API, records a manual CI event with you as its actor, and evaluates it with the normal " +
		"run gate, CI policy and execution profile of that ref. --workflow runs only that workflow (a workflow ID or a " +
		"path), without its on/paths filters. You need jobs:submit and the retry permission of the project.",
	Flags: append(apiFlags(),
		policyProjectFlag(),
		&cli.StringFlag{Name: "ref", Usage: "Branch name, refs/heads/NAME, or refs/tags/NAME"},
		&cli.IntFlag{Name: "pr", Usage: "Pull request number (runs its current head)"},
		&cli.StringFlag{Name: "workflow", Usage: "Run only this workflow (workflow ID or path)"},
		formatFlag(),
	),
	Action: func(ctx *cli.Context) error {
		ref := strings.TrimSpace(ctx.String("ref"))
		pr := ctx.Int("pr")
		if (ref == "") == (pr <= 0) {
			return fmt.Errorf("give exactly one of --ref and --pr")
		}
		client, projectID, err := policyClientAndProject(ctx)
		if err != nil {
			return err
		}
		request := csilapi.StartRunRequest{ProjectId: projectID, Ref: optionalText(ref), Workflow: optionalText(ctx.String("workflow"))}
		if pr > 0 {
			value := int64(pr)
			request.PrNumber = &value
		}
		resp, err := client.StartRun(ctx.Context, request)
		if err != nil {
			return err
		}
		return render(ctx.String("format"), resp, func(w *tabwriter.Writer) {
			fmt.Fprintf(w, "event_id\t%s\n", resp.EventId)
			fmt.Fprintf(w, "status\t%s\n", resp.Status)
			fmt.Fprintf(w, "ref\t%s\n", resp.Ref)
			fmt.Fprintf(w, "commit_sha\t%s\n", resp.CommitSha)
			if resp.EvalJobId != nil {
				fmt.Fprintf(w, "eval_job_id\t%s\n", *resp.EvalJobId)
			}
			if resp.Workflow != nil {
				fmt.Fprintf(w, "workflow\t%s\n", *resp.Workflow)
			}
		})
	},
}
