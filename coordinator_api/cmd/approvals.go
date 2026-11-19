package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

// prCommandSubcommand is one pull request command. It is the same as the PR
// comment "/reactorcide NAME", with the same authorization and effects.
func prCommandSubcommand(name, usage string, label bool) *cli.Command {
	flags := []cli.Flag{policyProjectFlag(),
		&cli.IntFlag{Name: "pr", Required: true, Usage: "Pull request number"},
		&cli.StringFlag{Name: "head-sha", Usage: "Refuse the command when this is not the current head"},
		formatFlag()}
	if label {
		flags = append(flags, &cli.StringFlag{Name: "label", Usage: "Count only for the required review with this ID"})
	}
	takesShifts := name == "retry" || name == "rerun"
	if takesShifts {
		flags = append(flags, shiftFlags()...)
	}
	return &cli.Command{Name: name, Usage: usage, Flags: flags, Action: func(ctx *cli.Context) error {
		client, projectID, err := policyClientAndProject(ctx)
		if err != nil {
			return err
		}
		request := csilapi.PrCommandRequest{ProjectId: projectID, PrNumber: int64(ctx.Int("pr")), Command: name,
			HeadSha: optionalText(ctx.String("head-sha"))}
		if label {
			request.Label = optionalText(ctx.String("label"))
		}
		if takesShifts {
			shifts, err := shiftsFromFlags(ctx)
			if err != nil {
				return err
			}
			request.Shifts = csilShifts(shifts)
		}
		resp, err := client.PrCommand(ctx.Context, request)
		if err != nil {
			return err
		}
		return render(ctx.String("format"), resp, func(w *tabwriter.Writer) {
			fmt.Fprintln(w, resp.Message)
			for _, action := range resp.Recorded {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", action.ApprovalId, action.Kind, action.Action, action.Person)
			}
		})
	}}
}

var ApprovalsCommand = &cli.Command{
	Name:  "approvals",
	Usage: "Approve, accept, revoke, retry, or rerun a pull request head",
	Description: "Each command is the same as the pull request comment /reactorcide COMMAND. " +
		"You act as your Reactorcide user, or as your linked VCS account. An approval binds to the current head SHA.",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		prCommandSubcommand("approve-ci", "Let a head that the run gate holds run on trusted base CI", false),
		prCommandSubcommand("override-ci", "Let the head's own CI run", false),
		prCommandSubcommand("accept", "Accept shared responsibility for the change (counts toward required reviews)", true),
		prCommandSubcommand("revoke", "Withdraw your approvals for the head, for every role you have", false),
		prCommandSubcommand("retry", "Retry the failed and cancelled jobs of the head's workflows, with optional shifts", false),
		prCommandSubcommand("rerun", "Run every workflow of the head again; with shifts, run the last evaluated workflows again with their recorded authority", false),
		{
			Name:  "show",
			Usage: "Show the approval actions and required reviews of the current head",
			Flags: []cli.Flag{policyProjectFlag(), &cli.IntFlag{Name: "pr", Required: true}, formatFlag()},
			Action: func(ctx *cli.Context) error {
				client, projectID, err := policyClientAndProject(ctx)
				if err != nil {
					return err
				}
				resp, err := client.GetPrApprovals(ctx.Context, csilapi.GetPrApprovalsRequest{ProjectId: projectID, PrNumber: int64(ctx.Int("pr"))})
				if err != nil {
					return err
				}
				return render(ctx.String("format"), resp, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "HEAD\t%s\n", resp.HeadSha)
					if resp.ReviewsState != nil {
						fmt.Fprintf(w, "REVIEWS\t%s\t%s\n", *resp.ReviewsState, textOrDash(resp.ReviewsSummary))
					}
					fmt.Fprintln(w, "\nREVIEW\tSCOPE\tAPPLIES\tACCEPTED\tSATISFIED")
					for _, review := range resp.Reviews {
						fmt.Fprintf(w, "%s\t%s\t%t\t%d of %d\t%t\n", review.Id, review.Scope, review.Applies,
							len(review.Accepted), review.Required, review.Satisfied)
					}
					fmt.Fprintln(w, "\nTIME\tKIND\tACTION\tLABEL\tPERSON\tCHANNEL\tSUBJECTS")
					for _, action := range resp.Actions {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", action.CreatedAt, action.Kind, action.Action,
							textOrDash(action.Label), action.Person, action.Channel, strings.Join(action.Subjects, ","))
					}
				})
			},
		},
	},
}

func trustScopeFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "org", Required: true, Usage: "Organization name"},
		&cli.StringFlag{Name: "project", Usage: "Project ID, name, or repository URL; omit for the organization settings"},
	}
}

func trustClientAndScope(ctx *cli.Context) (*csilapi.ReactorcideUiClient, *string, error) {
	if strings.TrimSpace(ctx.String("project")) == "" {
		client, err := managementClient(ctx)
		return client, nil, err
	}
	client, projectID, err := policyClientAndProject(ctx)
	if err != nil {
		return nil, nil, err
	}
	return client, &projectID, nil
}

// TrustCommand manages the run gate, override, and required-review settings.
var TrustCommand = &cli.Command{
	Name:  "trust",
	Usage: "Manage run gate, override, and required-review settings",
	Description: "A project field that is absent inherits the organization value. Organization required reviews " +
		"apply to every project; a project adds its own. A project owner can make a project more strict; only an " +
		"organization administrator can make it less strict.",
	Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name:  "get",
			Usage: "Show the stored settings and the settings in force",
			Flags: append(trustScopeFlags(), &cli.StringFlag{Name: "format", Value: "yaml", Usage: "Output format: yaml, json"}),
			Action: func(ctx *cli.Context) error {
				client, projectID, err := trustClientAndScope(ctx)
				if err != nil {
					return err
				}
				resp, err := client.GetCiTrustSettings(ctx.Context, csilapi.GetCiTrustSettingsRequest{Organization: ctx.String("org"), ProjectId: projectID})
				if err != nil {
					return err
				}
				return render(ctx.String("format"), resp, func(*tabwriter.Writer) {})
			},
		},
		{
			Name:  "set",
			Usage: "Replace the stored settings with a YAML file",
			Description: "The file has the fields run_gate, gated_run_authority, run_approvers, override_approvers, " +
				"override_profile, and required_reviews. An absent field inherits (project) or uses the default (organization).",
			Flags: append(trustScopeFlags(), &cli.StringFlag{Name: "file", Required: true, Usage: "Settings YAML file"}),
			Action: func(ctx *cli.Context) error {
				data, err := os.ReadFile(ctx.String("file"))
				if err != nil {
					return err
				}
				var settings csilapi.CiTrustSettings
				decoder := yaml.NewDecoder(strings.NewReader(string(data)))
				decoder.KnownFields(true)
				if err := decoder.Decode(&settings); err != nil {
					return fmt.Errorf("reading %s: %w", ctx.String("file"), err)
				}
				client, projectID, err := trustClientAndScope(ctx)
				if err != nil {
					return err
				}
				resp, err := client.PutCiTrustSettings(ctx.Context, csilapi.PutCiTrustSettingsRequest{Organization: ctx.String("org"),
					ProjectId: projectID, Settings: settings})
				if err != nil {
					return err
				}
				return render("yaml", resp, func(*tabwriter.Writer) {})
			},
		},
	},
}
