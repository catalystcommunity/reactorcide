package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
)

// Rollups, alert rules, and SLOs (docs/observability.md). Every command
// works with the admin API token.

func obsOrgBase(ctx *cli.Context) string {
	return "/api/v1/organizations/" + url.PathEscape(ctx.String("org"))
}

func obsPct(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f%%", *v*100)
}

func obsSecs(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.0fs", *v)
}

type sloTarget struct {
	SLI              string  `json:"sli" yaml:"sli"`
	Lane             string  `json:"lane" yaml:"lane"`
	Objective        float64 `json:"objective" yaml:"objective"`
	ThresholdSeconds float64 `json:"threshold_seconds" yaml:"threshold_seconds"`
	WindowDays       int     `json:"window_days" yaml:"window_days"`
	Builtin          bool    `json:"builtin" yaml:"builtin"`
}

type sloStatus struct {
	SLI                  string   `json:"sli" yaml:"sli"`
	Lane                 string   `json:"lane" yaml:"lane"`
	Objective            float64  `json:"objective" yaml:"objective"`
	ThresholdSeconds     float64  `json:"threshold_seconds" yaml:"threshold_seconds"`
	WindowDays           int      `json:"window_days" yaml:"window_days"`
	Total                float64  `json:"total" yaml:"total"`
	Good                 float64  `json:"good" yaml:"good"`
	Compliance           *float64 `json:"compliance,omitempty" yaml:"compliance,omitempty"`
	P95Seconds           *float64 `json:"p95_seconds,omitempty" yaml:"p95_seconds,omitempty"`
	ErrorBudgetRemaining float64  `json:"error_budget_remaining" yaml:"error_budget_remaining"`
	BudgetState          string   `json:"budget_state" yaml:"budget_state"`
	Measures             string   `json:"measures" yaml:"measures"`
}

type alertRule struct {
	RuleID             string   `json:"rule_id" yaml:"rule_id"`
	Name               string   `json:"name" yaml:"name"`
	Scope              string   `json:"scope" yaml:"scope"`
	ProjectID          *string  `json:"project_id,omitempty" yaml:"project_id,omitempty"`
	WorkflowSecurityID string   `json:"workflow_security_id,omitempty" yaml:"workflow_security_id,omitempty"`
	OwnerGroup         string   `json:"owner_group,omitempty" yaml:"owner_group,omitempty"`
	Condition          string   `json:"condition" yaml:"condition"`
	RefClassBucket     string   `json:"ref_class_bucket" yaml:"ref_class_bucket"`
	Threshold          float64  `json:"threshold" yaml:"threshold"`
	Factor             float64  `json:"factor" yaml:"factor"`
	WindowHours        int      `json:"window_hours" yaml:"window_hours"`
	BaselineDays       int      `json:"baseline_days" yaml:"baseline_days"`
	MinRuns            int      `json:"min_runs" yaml:"min_runs"`
	Enabled            bool     `json:"enabled" yaml:"enabled"`
	State              string   `json:"state" yaml:"state"`
	LastValue          *float64 `json:"last_value,omitempty" yaml:"last_value,omitempty"`
	LastBaseline       *float64 `json:"last_baseline,omitempty" yaml:"last_baseline,omitempty"`
	LastRuns           int      `json:"last_runs" yaml:"last_runs"`
	LastEvaluatedAt    *string  `json:"last_evaluated_at,omitempty" yaml:"last_evaluated_at,omitempty"`
}

func ruleTarget(r alertRule) string {
	switch r.Scope {
	case "workflow":
		return r.WorkflowSecurityID
	case "owner_group":
		return r.OwnerGroup
	}
	if r.ProjectID != nil {
		return *r.ProjectID
	}
	return "-"
}

func ruleValue(r alertRule) string {
	if r.Condition == "pass_rate_below" {
		return obsPct(r.LastValue)
	}
	return obsSecs(r.LastValue)
}

func renderRules(w *tabwriter.Writer, rules []alertRule) {
	fmt.Fprintln(w, "NAME\tSCOPE\tTARGET\tCONDITION\tSTATE\tVALUE\tRUNS\tENABLED")
	for _, r := range rules {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%t\n", r.Name, r.Scope, ruleTarget(r), r.Condition, r.State, ruleValue(r), r.LastRuns, r.Enabled)
	}
}

func alertRuleFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "description"},
		&cli.StringFlag{Name: "ref-class", Usage: "ref class bucket: default, tag, pr, schedule, manual, other; \"all\" for every bucket (create default: default)"},
		&cli.Float64Flag{Name: "threshold", Usage: "pass_rate_below: fire below this pass rate (0-1, default 0.9)"},
		&cli.Float64Flag{Name: "factor", Usage: "duration_regression: fire when p95 > baseline p95 x factor (default 1.5)"},
		&cli.IntFlag{Name: "window-hours", Usage: "evaluation window (default 24)"},
		&cli.IntFlag{Name: "baseline-days", Usage: "duration_regression baseline before the window (default 7)"},
		&cli.IntFlag{Name: "min-runs", Usage: "fewer runs is no data (default 5)"},
		&cli.Float64Flag{Name: "resolve-margin", Usage: "hysteresis margin (default 0.05)"},
		&cli.IntFlag{Name: "fire-after", Usage: "breaches in a row to fire (default 2)"},
		&cli.IntFlag{Name: "resolve-after", Usage: "recoveries in a row to resolve (default 2)"},
	}
}

func alertRuleBody(ctx *cli.Context, body map[string]interface{}) {
	if ctx.IsSet("description") {
		body["description"] = ctx.String("description")
	}
	if ctx.IsSet("ref-class") {
		v := ctx.String("ref-class")
		if v == "all" {
			v = ""
		}
		body["ref_class_bucket"] = v
	}
	for flag, key := range map[string]string{"threshold": "threshold", "factor": "factor", "resolve-margin": "resolve_margin"} {
		if ctx.IsSet(flag) {
			body[key] = ctx.Float64(flag)
		}
	}
	for flag, key := range map[string]string{"window-hours": "window_hours", "baseline-days": "baseline_days", "min-runs": "min_runs",
		"fire-after": "fire_after", "resolve-after": "resolve_after"} {
		if ctx.IsSet(flag) {
			body[key] = ctx.Int(flag)
		}
	}
}

// AlertsCommand manages pipeline owner alert rules.
var AlertsCommand = &cli.Command{
	Name:  "alerts",
	Usage: "Manage pipeline owner alert rules (pass rate and duration; infrastructure failures never count)",
	Subcommands: []*cli.Command{
		{
			Name: "list", Usage: "List the alert rules of an organization and their state",
			Flags: append(apiFlags(), orgFlag(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Rules []alertRule `json:"rules" yaml:"rules"`
				}
				if err := client.doJSON(http.MethodGet, obsOrgBase(ctx)+"/alert-rules", nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) { renderRules(w, response.Rules) })
			},
		},
		{
			Name: "show", ArgsUsage: "<name>", Usage: "Show a rule and its recent transitions",
			Flags: append(apiFlags(), orgFlag(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "alerts show --org <org> <name>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Rule        alertRule `json:"rule" yaml:"rule"`
					Transitions []struct {
						State      string   `json:"state" yaml:"state"`
						Value      *float64 `json:"value,omitempty" yaml:"value,omitempty"`
						Runs       int      `json:"runs" yaml:"runs"`
						OccurredAt string   `json:"occurred_at" yaml:"occurred_at"`
					} `json:"transitions" yaml:"transitions"`
				}
				if err := client.doJSON(http.MethodGet, obsOrgBase(ctx)+"/alert-rules/"+url.PathEscape(ctx.Args().First()), nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					renderRules(w, []alertRule{response.Rule})
					fmt.Fprintln(w, "\nTRANSITION\tVALUE\tRUNS\tAT")
					for _, t := range response.Transitions {
						v := "-"
						if t.Value != nil {
							v = strconv.FormatFloat(*t.Value, 'f', 3, 64)
						}
						fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", t.State, v, t.Runs, t.OccurredAt)
					}
				})
			},
		},
		{
			Name: "create", ArgsUsage: "<name>",
			Usage: "Create a rule. Notifications go to outbound event subscriptions as alert.firing and alert.resolved",
			Flags: append(append(apiFlags(), orgFlag(), formatFlag(),
				&cli.StringFlag{Name: "scope", Required: true, Usage: "workflow, project, or owner_group"},
				&cli.StringFlag{Name: "project", Usage: "project ID or name (workflow and project scope; optional for owner_group)"},
				&cli.StringFlag{Name: "workflow", Usage: "workflow security ID (workflow scope)"},
				&cli.StringFlag{Name: "owner-group", Usage: "owner group (owner_group scope)"},
				&cli.StringFlag{Name: "condition", Required: true, Usage: "pass_rate_below or duration_regression"},
				&cli.BoolFlag{Name: "disabled"}), alertRuleFlags()...),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "alerts create --org <org> --scope <scope> --condition <condition> <name>"); err != nil {
					return err
				}
				body := map[string]interface{}{"name": ctx.Args().First(), "scope": ctx.String("scope"), "project": ctx.String("project"),
					"workflow_security_id": ctx.String("workflow"), "owner_group": ctx.String("owner-group"), "condition": ctx.String("condition")}
				if ctx.Bool("disabled") {
					body["enabled"] = false
				}
				alertRuleBody(ctx, body)
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var rule alertRule
				if err := client.doJSON(http.MethodPost, obsOrgBase(ctx)+"/alert-rules", body, http.StatusCreated, &rule); err != nil {
					return err
				}
				return render(ctx.String("format"), rule, func(w *tabwriter.Writer) { renderRules(w, []alertRule{rule}) })
			},
		},
		{
			Name: "update", ArgsUsage: "<name>", Usage: "Change a rule (the scope cannot change)",
			Flags: append(append(apiFlags(), orgFlag(), formatFlag(),
				&cli.StringFlag{Name: "condition"}, &cli.StringFlag{Name: "enabled", Usage: "true or false"}), alertRuleFlags()...),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "alerts update --org <org> <name>"); err != nil {
					return err
				}
				body := map[string]interface{}{}
				if ctx.IsSet("condition") {
					body["condition"] = ctx.String("condition")
				}
				enabled, err := optionalBoolFlag(ctx, "enabled")
				if err != nil {
					return err
				}
				if enabled != nil {
					body["enabled"] = *enabled
				}
				alertRuleBody(ctx, body)
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var rule alertRule
				if err := client.doJSON(http.MethodPatch, obsOrgBase(ctx)+"/alert-rules/"+url.PathEscape(ctx.Args().First()), body, http.StatusOK, &rule); err != nil {
					return err
				}
				return render(ctx.String("format"), rule, func(w *tabwriter.Writer) { renderRules(w, []alertRule{rule}) })
			},
		},
		{
			Name: "delete", ArgsUsage: "<name>", Usage: "Delete a rule",
			Flags: append(apiFlags(), orgFlag()),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "alerts delete --org <org> <name>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				if err := client.doJSON(http.MethodDelete, obsOrgBase(ctx)+"/alert-rules/"+url.PathEscape(ctx.Args().First()), nil, http.StatusNoContent, nil); err != nil {
					return err
				}
				fmt.Println("Done")
				return nil
			},
		},
	},
}

// SLOCommand manages SLO targets and shows error budgets.
var SLOCommand = &cli.Command{
	Name:  "slo",
	Usage: "Show and set SLO targets and error budgets (eval latency, queue wait, infrastructure failure rate, availability)",
	Subcommands: []*cli.Command{
		{
			Name: "list", Usage: "List the SLO targets (stored and built-in)",
			Flags: append(apiFlags(), orgFlag(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					Targets []sloTarget `json:"targets" yaml:"targets"`
				}
				if err := client.doJSON(http.MethodGet, obsOrgBase(ctx)+"/slos", nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "SLI\tLANE\tOBJECTIVE\tTHRESHOLD\tWINDOW\tSOURCE")
					for _, t := range response.Targets {
						source := "stored"
						if t.Builtin {
							source = "built-in"
						}
						lane := t.Lane
						if lane == "" {
							lane = "(all)"
						}
						fmt.Fprintf(w, "%s\t%s\t%.3f%%\t%.0fs\t%dd\t%s\n", t.SLI, lane, t.Objective*100, t.ThresholdSeconds, t.WindowDays, source)
					}
				})
			},
		},
		{
			Name: "set", ArgsUsage: "<sli>", Usage: "Set a target: eval_latency, queue_wait, infra_failure_rate, or availability",
			Flags: append(apiFlags(), orgFlag(), formatFlag(),
				&cli.StringFlag{Name: "lane", Usage: "lane target (queue_wait and infra_failure_rate only)"},
				&cli.Float64Flag{Name: "objective", Required: true, Usage: "fraction of good events, for example 0.99"},
				&cli.Float64Flag{Name: "threshold-seconds", Usage: "latency limit of a good event (eval_latency, queue_wait)"},
				&cli.IntFlag{Name: "window-days", Usage: "window of the error budget (default 30)"}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "slo set --org <org> --objective <fraction> <sli>"); err != nil {
					return err
				}
				body := map[string]interface{}{"objective": ctx.Float64("objective"), "threshold_seconds": ctx.Float64("threshold-seconds"),
					"window_days": ctx.Int("window-days")}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				path := obsOrgBase(ctx) + "/slos/" + url.PathEscape(ctx.Args().First())
				if lane := ctx.String("lane"); lane != "" {
					path += "?lane=" + url.QueryEscape(lane)
				}
				var target sloTarget
				if err := client.doJSON(http.MethodPut, path, body, http.StatusOK, &target); err != nil {
					return err
				}
				return render(ctx.String("format"), target, func(w *tabwriter.Writer) {
					fmt.Fprintf(w, "%s\t%s\t%.3f%%\t%.0fs\t%dd\n", target.SLI, target.Lane, target.Objective*100, target.ThresholdSeconds, target.WindowDays)
				})
			},
		},
		{
			Name: "delete", ArgsUsage: "<sli>", Usage: "Delete a stored target (the built-in default applies again)",
			Flags: append(apiFlags(), orgFlag(), &cli.StringFlag{Name: "lane"}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "slo delete --org <org> [--lane <lane>] <sli>"); err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				path := obsOrgBase(ctx) + "/slos/" + url.PathEscape(ctx.Args().First())
				if lane := ctx.String("lane"); lane != "" {
					path += "?lane=" + url.QueryEscape(lane)
				}
				if err := client.doJSON(http.MethodDelete, path, nil, http.StatusNoContent, nil); err != nil {
					return err
				}
				fmt.Println("Done")
				return nil
			},
		},
		{
			Name: "status", Usage: "Show each SLI, its compliance, and the error budget that remains",
			Flags: append(apiFlags(), orgFlag(), formatFlag()),
			Action: func(ctx *cli.Context) error {
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response struct {
					SLOs []sloStatus `json:"slos" yaml:"slos"`
				}
				if err := client.doJSON(http.MethodGet, obsOrgBase(ctx)+"/slos/status", nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "SLI\tLANE\tOBJECTIVE\tCOMPLIANCE\tP95\tTOTAL\tBUDGET LEFT\tSTATE")
					for _, s := range response.SLOs {
						lane := s.Lane
						if lane == "" {
							lane = "-"
						}
						fmt.Fprintf(w, "%s\t%s\t%.3f%%\t%s\t%s\t%.0f\t%.1f%%\t%s\n", s.SLI, lane, s.Objective*100, obsPct(s.Compliance),
							obsSecs(s.P95Seconds), s.Total, s.ErrorBudgetRemaining*100, s.BudgetState)
					}
				})
			},
		},
	},
}

// RollupsCommand reads rollups and dashboards.
var RollupsCommand = &cli.Command{
	Name:  "rollups",
	Usage: "Read job rollups and dashboards (pipeline, infrastructure, usage)",
	Subcommands: []*cli.Command{
		{
			Name: "show", Usage: "Show the totals of the job rollups that match the filters",
			Flags: append(apiFlags(), orgFlag(), formatFlag(),
				&cli.StringFlag{Name: "granularity", Value: "day", Usage: "hour or day"},
				&cli.IntFlag{Name: "days", Value: 7},
				&cli.StringFlag{Name: "project"}, &cli.StringFlag{Name: "workflow"}, &cli.StringFlag{Name: "owner-group"},
				&cli.StringFlag{Name: "lane"}, &cli.StringFlag{Name: "ref-class"}),
			Action: func(ctx *cli.Context) error {
				q := url.Values{"granularity": {ctx.String("granularity")}, "days": {strconv.Itoa(ctx.Int("days"))}}
				for flag, key := range map[string]string{"project": "project", "workflow": "workflow", "owner-group": "owner_group", "ref-class": "ref_class"} {
					if v := ctx.String(flag); v != "" {
						q.Set(key, v)
					}
				}
				if ctx.IsSet("lane") {
					q.Set("lane", ctx.String("lane"))
				}
				return dashboardRequest(ctx, "rollups", q)
			},
		},
		{
			Name: "dashboard", ArgsUsage: "<pipeline|infrastructure|usage|status>", Usage: "Read a dashboard as JSON or YAML",
			Flags: append(apiFlags(), orgFlag(), formatFlag(),
				&cli.IntFlag{Name: "days", Value: 30, Usage: "7, 30, or 90"},
				&cli.StringFlag{Name: "project"},
				&cli.StringFlag{Name: "attribution", Usage: "usage: owner (default) or actor"}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "rollups dashboard --org <org> <pipeline|infrastructure|usage|status>"); err != nil {
					return err
				}
				q := url.Values{"days": {strconv.Itoa(ctx.Int("days"))}}
				if v := ctx.String("project"); v != "" {
					q.Set("project", v)
				}
				if v := ctx.String("attribution"); v != "" {
					q.Set("attribution", v)
				}
				return dashboardRequest(ctx, ctx.Args().First(), q)
			},
		},
	},
}

func dashboardRequest(ctx *cli.Context, view string, q url.Values) error {
	client, err := newAPIClient(ctx)
	if err != nil {
		return err
	}
	var response map[string]interface{}
	if err := client.doJSON(http.MethodGet, obsOrgBase(ctx)+"/observability/"+url.PathEscape(view)+"?"+q.Encode(), nil, http.StatusOK, &response); err != nil {
		return err
	}
	format := ctx.String("format")
	if format == "" || format == "table" {
		format = "json"
	}
	return render(format, response, func(*tabwriter.Writer) {})
}
