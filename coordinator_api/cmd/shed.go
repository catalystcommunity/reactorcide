package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

// shedActions mirrors models.ShedActions.
type shedActions struct {
	SkipDrafts       bool `json:"skip_drafts" yaml:"skip_drafts"`
	PauseSchedules   bool `json:"pause_schedules" yaml:"pause_schedules"`
	LowerPRPriority  bool `json:"lower_pr_priority" yaml:"lower_pr_priority"`
	SkipAdvisory     bool `json:"skip_advisory" yaml:"skip_advisory"`
	RefuseManualRuns bool `json:"refuse_manual_runs" yaml:"refuse_manual_runs"`
}

type shedActionsConfig struct {
	Degraded shedActions `json:"degraded" yaml:"degraded"`
	Critical shedActions `json:"critical" yaml:"critical"`
}

type shedState struct {
	GlobalLevel string            `json:"global_level" yaml:"global_level"`
	OrgLevel    string            `json:"org_level,omitempty" yaml:"org_level,omitempty"`
	Level       string            `json:"level" yaml:"level"`
	Source      string            `json:"source,omitempty" yaml:"source,omitempty"`
	Actions     shedActions       `json:"actions" yaml:"actions"`
	Config      shedActionsConfig `json:"config" yaml:"config"`
}

func shedPath(org string) string {
	if org == "" {
		return "/api/v1/shed"
	}
	return "/api/v1/organizations/" + url.PathEscape(org) + "/shed"
}

func renderShedState(ctx *cli.Context, state shedState) error {
	return render(ctx.String("format"), state, func(w *tabwriter.Writer) {
		fmt.Fprintln(w, "GLOBAL\tORG\tIN FORCE\tSOURCE\tSKIP DRAFTS\tPAUSE SCHEDULES\tLOWER PR PRIORITY\tSKIP ADVISORY\tREFUSE MANUAL RUNS")
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%t\t%t\t%t\t%t\t%t\n", state.GlobalLevel, state.OrgLevel, state.Level, state.Source,
			state.Actions.SkipDrafts, state.Actions.PauseSchedules, state.Actions.LowerPRPriority, state.Actions.SkipAdvisory, state.Actions.RefuseManualRuns)
	})
}

// ShedCommand reads and sets shed levels (overload protection).
var ShedCommand = &cli.Command{
	Name: "shed", Usage: "Read and set shed levels (overload protection)", Flags: apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name: "get", Usage: "Show the global shed level, or the level in force for an organization",
			Flags: append(apiFlags(), formatFlag(), &cli.StringFlag{Name: "org", Usage: "Organization name (default: the global level)"}),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var state shedState
				if err := client.doJSON(http.MethodGet, shedPath(ctx.String("org")), nil, http.StatusOK, &state); err != nil {
					return err
				}
				return renderShedState(ctx, state)
			},
		},
		{
			Name: "set", Usage: "Set the global shed level, or the level of an organization with --org",
			Flags: append(apiFlags(), formatFlag(),
				&cli.StringFlag{Name: "level", Required: true, Usage: "normal, degraded, or critical"},
				&cli.StringFlag{Name: "org", Usage: "Organization name (default: the global level)"},
				&cli.StringFlag{Name: "reason", Usage: "Reason for the audit log"}),
			Action: func(ctx *cli.Context) error {
				level := ctx.String("level")
				if level != "normal" && level != "degraded" && level != "critical" {
					return fmt.Errorf("--level must be normal, degraded, or critical")
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				body := map[string]string{"level": level}
				if reason := ctx.String("reason"); reason != "" {
					body["reason"] = reason
				}
				var state shedState
				if err := client.doJSON(http.MethodPut, shedPath(ctx.String("org")), body, http.StatusOK, &state); err != nil {
					return err
				}
				return renderShedState(ctx, state)
			},
		},
		{
			Name: "set-actions", Usage: "Set the actions of each shed level for an organization",
			Description: "The file has the keys degraded and critical. Each one has skip_drafts, pause_schedules, " +
				"lower_pr_priority, skip_advisory, and refuse_manual_runs. --reset returns to the built-in actions.",
			Flags: append(apiFlags(), formatFlag(),
				&cli.StringFlag{Name: "org", Required: true, Usage: "Organization name"},
				&cli.StringFlag{Name: "file", Usage: "YAML or JSON file with the actions"},
				&cli.BoolFlag{Name: "reset", Usage: "Use the built-in actions"},
				&cli.StringFlag{Name: "reason", Usage: "Reason for the audit log"}),
			Action: func(ctx *cli.Context) error {
				if ctx.Bool("reset") == (ctx.String("file") != "") {
					return fmt.Errorf("give --file or --reset")
				}
				body := map[string]any{}
				if ctx.Bool("reset") {
					body["reset_actions"] = true
				} else {
					data, err := os.ReadFile(ctx.String("file"))
					if err != nil {
						return fmt.Errorf("failed to read %s: %w", ctx.String("file"), err)
					}
					var config shedActionsConfig
					if err := yaml.Unmarshal(data, &config); err != nil {
						return fmt.Errorf("failed to parse %s: %w", ctx.String("file"), err)
					}
					body["actions"] = config
				}
				if reason := ctx.String("reason"); reason != "" {
					body["reason"] = reason
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var state shedState
				if err := client.doJSON(http.MethodPut, shedPath(ctx.String("org")), body, http.StatusOK, &state); err != nil {
					return err
				}
				return renderShedState(ctx, state)
			},
		},
	},
}
