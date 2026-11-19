package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
)

// refClassRule is a ref class rule as the REST API returns it.
type refClassRule struct {
	RuleID          string   `json:"rule_id" yaml:"rule_id"`
	ProjectID       *string  `json:"project_id,omitempty" yaml:"project_id,omitempty"`
	Name            string   `json:"name" yaml:"name"`
	Position        int      `json:"position" yaml:"position"`
	Description     string   `json:"description,omitempty" yaml:"description,omitempty"`
	EventTypes      []string `json:"event_types" yaml:"event_types,omitempty"`
	BranchGlob      string   `json:"branch_glob,omitempty" yaml:"branch_glob,omitempty"`
	TagGlob         string   `json:"tag_glob,omitempty" yaml:"tag_glob,omitempty"`
	BaseBranchGlob  string   `json:"base_branch_glob,omitempty" yaml:"base_branch_glob,omitempty"`
	IsPR            *bool    `json:"is_pr,omitempty" yaml:"is_pr,omitempty"`
	IsFork          *bool    `json:"is_fork,omitempty" yaml:"is_fork,omitempty"`
	IsDefaultBranch *bool    `json:"is_default_branch,omitempty" yaml:"is_default_branch,omitempty"`
	Classes         []string `json:"classes" yaml:"classes"`
	Enabled         bool     `json:"enabled" yaml:"enabled"`
}

type refClassRulesResponse struct {
	Rules     []refClassRule      `json:"rules" yaml:"rules"`
	Inherited []refClassRule      `json:"inherited,omitempty" yaml:"inherited,omitempty"`
	Builtin   map[string][]string `json:"builtin" yaml:"builtin"`
}

type refClassFacts struct {
	Source          string `json:"source,omitempty" yaml:"source,omitempty"`
	EventType       string `json:"event_type,omitempty" yaml:"event_type,omitempty"`
	Branch          string `json:"branch,omitempty" yaml:"branch,omitempty"`
	HeadBranch      string `json:"head_branch,omitempty" yaml:"head_branch,omitempty"`
	Tag             string `json:"tag,omitempty" yaml:"tag,omitempty"`
	BaseBranch      string `json:"base_branch,omitempty" yaml:"base_branch,omitempty"`
	DefaultBranch   string `json:"default_branch,omitempty" yaml:"default_branch,omitempty"`
	IsPR            bool   `json:"is_pr,omitempty" yaml:"is_pr,omitempty"`
	IsFork          bool   `json:"is_fork,omitempty" yaml:"is_fork,omitempty"`
	IsDefaultBranch bool   `json:"is_default_branch,omitempty" yaml:"is_default_branch,omitempty"`
}

type refClassExplanation struct {
	EventID       string        `json:"event_id,omitempty" yaml:"event_id,omitempty"`
	Facts         refClassFacts `json:"facts" yaml:"facts"`
	Classes       []string      `json:"classes" yaml:"classes"`
	StoredClasses []string      `json:"stored_classes,omitempty" yaml:"stored_classes,omitempty"`
	Reasons       []struct {
		Class    string `json:"class" yaml:"class"`
		RuleID   string `json:"rule_id" yaml:"rule_id"`
		RuleName string `json:"rule_name,omitempty" yaml:"rule_name,omitempty"`
		Scope    string `json:"scope" yaml:"scope"`
	} `json:"reasons" yaml:"reasons"`
	Rules []struct {
		Name    string   `json:"name" yaml:"name"`
		Scope   string   `json:"scope" yaml:"scope"`
		Enabled bool     `json:"enabled" yaml:"enabled"`
		Matched bool     `json:"matched" yaml:"matched"`
		Classes []string `json:"classes" yaml:"classes"`
	} `json:"rules" yaml:"rules"`
}

func refClassScopeFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "org", Usage: "organization name (organization rules)"},
		&cli.StringFlag{Name: "project", Usage: "project ID or name (project rules)"},
	}
}

// refClassBasePath returns the REST base path of the scope that the flags
// select: exactly one of --org and --project.
func refClassBasePath(ctx *cli.Context) (string, error) {
	org, project := strings.TrimSpace(ctx.String("org")), strings.TrimSpace(ctx.String("project"))
	switch {
	case org != "" && project != "":
		return "", fmt.Errorf("use --org or --project, not both")
	case org != "":
		return "/api/v1/organizations/" + url.PathEscape(org), nil
	case project != "":
		return "/api/v1/projects/" + url.PathEscape(project), nil
	}
	return "", fmt.Errorf("--org or --project is required")
}

func optionalBoolFlag(ctx *cli.Context, name string) (*bool, error) {
	if !ctx.IsSet(name) {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(ctx.String(name))) {
	case "true", "yes":
		value := true
		return &value, nil
	case "false", "no":
		value := false
		return &value, nil
	case "", "any":
		return nil, nil
	}
	return nil, fmt.Errorf("--%s must be true, false, or any", name)
}

func boolText(value *bool) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%t", *value)
}

func renderRefClassRules(w *tabwriter.Writer, scope string, rules []refClassRule) {
	for _, rule := range rules {
		match := []string{}
		if len(rule.EventTypes) > 0 {
			match = append(match, "events="+strings.Join(rule.EventTypes, ","))
		}
		for label, value := range map[string]string{"branch": rule.BranchGlob, "tag": rule.TagGlob, "base": rule.BaseBranchGlob} {
			if value != "" {
				match = append(match, label+"="+value)
			}
		}
		for label, value := range map[string]*bool{"pr": rule.IsPR, "fork": rule.IsFork, "default_branch": rule.IsDefaultBranch} {
			if value != nil {
				match = append(match, label+"="+boolText(value))
			}
		}
		sort.Strings(match)
		if len(match) == 0 {
			match = []string{"(every event)"}
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%t\t%s\t%s\n", scope, rule.Name, rule.Position, rule.Enabled, strings.Join(rule.Classes, ","), strings.Join(match, " "))
	}
}

// RefClassesCommand manages ref class rules and explains the classes of a
// ref (docs/ref-classes.md).
var RefClassesCommand = &cli.Command{
	Name:  "ref-classes",
	Usage: "Manage ref class rules and explain the ref classes of an event",
	Subcommands: []*cli.Command{
		{
			Name:  "rules",
			Usage: "Manage ref class rules of an organization or a project",
			Subcommands: []*cli.Command{
				{
					Name: "list", Usage: "List the rules (a project listing also shows the organization rules)",
					Flags: append(append(apiFlags(), refClassScopeFlags()...), formatFlag()),
					Action: func(ctx *cli.Context) error {
						base, err := refClassBasePath(ctx)
						if err != nil {
							return err
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						var response refClassRulesResponse
						if err := client.doJSON(http.MethodGet, base+"/ref-class-rules", nil, http.StatusOK, &response); err != nil {
							return err
						}
						return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
							fmt.Fprintln(w, "SCOPE\tNAME\tPOSITION\tENABLED\tCLASSES\tMATCH")
							names := make([]string, 0, len(response.Builtin))
							for name := range response.Builtin {
								names = append(names, name)
							}
							sort.Strings(names)
							for _, name := range names {
								fmt.Fprintf(w, "builtin\t%s\t-\ttrue\t%s\t(built-in)\n", strings.TrimPrefix(name, "builtin:"), strings.Join(response.Builtin[name], ","))
							}
							renderRefClassRules(w, "org", response.Inherited)
							scope := "org"
							if ctx.String("project") != "" {
								scope = "project"
							}
							renderRefClassRules(w, scope, response.Rules)
						})
					},
				},
				{
					Name: "set", ArgsUsage: "<name>", Usage: "Create or replace a rule that adds ref classes",
					Flags: append(append(apiFlags(), refClassScopeFlags()...), formatFlag(),
						&cli.StringSliceFlag{Name: "class", Required: true, Usage: "a class that the rule adds (repeat)"},
						&cli.StringSliceFlag{Name: "event", Usage: "an event type to match (repeat): push, tag_created, pull_request_opened, pull_request_updated, pull_request_merged, pull_request_closed, schedule, manual"},
						&cli.StringFlag{Name: "branch", Usage: "branch glob, for example main or release/*"},
						&cli.StringFlag{Name: "tag", Usage: "tag glob, for example v*"},
						&cli.StringFlag{Name: "base-branch", Usage: "pull request base branch glob"},
						&cli.StringFlag{Name: "pr", Usage: "true: only pull requests; false: never pull requests"},
						&cli.StringFlag{Name: "fork", Usage: "true: only fork pull requests; false: never fork pull requests"},
						&cli.StringFlag{Name: "default-branch", Usage: "true: only the default branch (push) or a pull request to it"},
						&cli.IntFlag{Name: "position", Usage: "order of the rule in its scope (lower first)"},
						&cli.StringFlag{Name: "description"},
						&cli.BoolFlag{Name: "disabled", Usage: "store the rule disabled"}),
					Action: func(ctx *cli.Context) error {
						if err := requireArgs(ctx, 1, "ref-classes rules set (--org <org> | --project <project>) --class <class> <name>"); err != nil {
							return err
						}
						base, err := refClassBasePath(ctx)
						if err != nil {
							return err
						}
						body := map[string]interface{}{
							"classes": ctx.StringSlice("class"), "event_types": ctx.StringSlice("event"),
							"branch_glob": ctx.String("branch"), "tag_glob": ctx.String("tag"), "base_branch_glob": ctx.String("base-branch"),
							"position": ctx.Int("position"), "description": ctx.String("description"), "enabled": !ctx.Bool("disabled"),
						}
						for flag, field := range map[string]string{"pr": "is_pr", "fork": "is_fork", "default-branch": "is_default_branch"} {
							value, err := optionalBoolFlag(ctx, flag)
							if err != nil {
								return err
							}
							if value != nil {
								body[field] = *value
							}
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						var rule refClassRule
						if err := client.doJSON(http.MethodPut, base+"/ref-class-rules/"+url.PathEscape(ctx.Args().First()), body, http.StatusOK, &rule); err != nil {
							return err
						}
						return render(ctx.String("format"), rule, func(w *tabwriter.Writer) {
							fmt.Fprintln(w, "NAME\tPOSITION\tENABLED\tCLASSES\tMATCH")
							renderRefClassRules(w, "", []refClassRule{rule})
						})
					},
				},
				{
					Name: "delete", ArgsUsage: "<name>", Usage: "Delete a rule",
					Flags: append(apiFlags(), refClassScopeFlags()...),
					Action: func(ctx *cli.Context) error {
						if err := requireArgs(ctx, 1, "ref-classes rules delete (--org <org> | --project <project>) <name>"); err != nil {
							return err
						}
						base, err := refClassBasePath(ctx)
						if err != nil {
							return err
						}
						client, err := newAPIClient(ctx)
						if err != nil {
							return err
						}
						if err := client.doJSON(http.MethodDelete, base+"/ref-class-rules/"+url.PathEscape(ctx.Args().First()), nil, http.StatusNoContent, nil); err != nil {
							return err
						}
						fmt.Println("Done")
						return nil
					},
				},
				refClassExplainCommand(),
			},
		},
		refClassExplainCommand(),
	},
}

func refClassExplainCommand() *cli.Command {
	return &cli.Command{
		Name:  "explain",
		Usage: "Show the ref classes of a stored event, or of the facts that you give",
		Flags: append(append(apiFlags(), refClassScopeFlags()...), formatFlag(),
			&cli.StringFlag{Name: "event-id", Usage: "a stored event: use its facts and show its stored classes"},
			&cli.StringFlag{Name: "source", Value: "webhook", Usage: "webhook, schedule, or manual"},
			&cli.StringFlag{Name: "event", Usage: "event type, for example push or pull_request_opened"},
			&cli.StringFlag{Name: "branch", Usage: "branch of a push, a schedule, or a manual submit"},
			&cli.StringFlag{Name: "head-branch", Usage: "head branch of a pull request (no rule matches it)"},
			&cli.StringFlag{Name: "tag", Usage: "tag name"},
			&cli.StringFlag{Name: "base-branch", Usage: "pull request base branch"},
			&cli.StringFlag{Name: "default-branch", Usage: "the default branch of the repository"},
			&cli.BoolFlag{Name: "fork", Usage: "the pull request comes from a fork"}),
		Action: func(ctx *cli.Context) error {
			base, err := refClassBasePath(ctx)
			if err != nil {
				return err
			}
			body := map[string]interface{}{
				"event_id": ctx.String("event-id"),
				"facts": refClassFacts{Source: ctx.String("source"), EventType: ctx.String("event"), Branch: ctx.String("branch"), HeadBranch: ctx.String("head-branch"),
					Tag: ctx.String("tag"), BaseBranch: ctx.String("base-branch"), DefaultBranch: ctx.String("default-branch"),
					IsFork: ctx.Bool("fork")},
			}
			client, err := newAPIClient(ctx)
			if err != nil {
				return err
			}
			var response refClassExplanation
			if err := client.doJSON(http.MethodPost, base+"/ref-classes/explain", body, http.StatusOK, &response); err != nil {
				return err
			}
			return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
				facts := response.Facts
				fmt.Fprintf(w, "facts\tsource=%s event=%s branch=%s head=%s tag=%s base=%s default=%s pr=%t fork=%t default_branch=%t\n",
					facts.Source, facts.EventType, facts.Branch, facts.HeadBranch, facts.Tag, facts.BaseBranch, facts.DefaultBranch,
					facts.IsPR, facts.IsFork, facts.IsDefaultBranch)
				fmt.Fprintf(w, "classes\t%s\n", strings.Join(response.Classes, ", "))
				if response.EventID != "" {
					fmt.Fprintf(w, "stored classes\t%s\n", strings.Join(response.StoredClasses, ", "))
				}
				for _, reason := range response.Reasons {
					fmt.Fprintf(w, "reason\t%s from %s rule %s\n", reason.Class, reason.Scope, reason.RuleName)
				}
				for _, rule := range response.Rules {
					fmt.Fprintf(w, "rule\t%s %s enabled=%t matched=%t classes=%s\n", rule.Scope, rule.Name, rule.Enabled, rule.Matched, strings.Join(rule.Classes, ","))
				}
			})
		},
	}
}
