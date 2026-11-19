package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
)

// priorityLane is a priority lane as the REST API returns it.
type priorityLane struct {
	LaneID      string   `json:"lane_id,omitempty" yaml:"lane_id,omitempty"`
	ProjectID   *string  `json:"project_id,omitempty" yaml:"project_id,omitempty"`
	Name        string   `json:"name" yaml:"name"`
	Position    int      `json:"position" yaml:"position"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Base        int      `json:"base" yaml:"base"`
	ClassesAny  []string `json:"classes_any" yaml:"classes_any,omitempty"`
	ClassesNone []string `json:"classes_none" yaml:"classes_none,omitempty"`
	EventTypes  []string `json:"event_types" yaml:"event_types,omitempty"`
	Draft       *bool    `json:"draft,omitempty" yaml:"draft,omitempty"`
	JobKind     string   `json:"job_kind,omitempty" yaml:"job_kind,omitempty"`
	Retry       bool     `json:"retry,omitempty" yaml:"retry,omitempty"`
	Enabled     bool     `json:"enabled" yaml:"enabled"`
	Scope       string   `json:"scope,omitempty" yaml:"scope,omitempty"`
	Replaces    string   `json:"replaces,omitempty" yaml:"replaces,omitempty"`
}

type priorityLanesResponse struct {
	Lanes     []priorityLane `json:"lanes" yaml:"lanes"`
	Inherited []priorityLane `json:"inherited,omitempty" yaml:"inherited,omitempty"`
	Builtin   []priorityLane `json:"builtin" yaml:"builtin"`
	Effective []priorityLane `json:"effective" yaml:"effective"`
}

type priorityShift struct {
	Source  string `json:"source" yaml:"source"`
	Group   string `json:"group,omitempty" yaml:"group,omitempty"`
	Value   int    `json:"value" yaml:"value"`
	Applied bool   `json:"applied" yaml:"applied"`
}

type priorityDecision struct {
	Lane         string          `json:"lane" yaml:"lane"`
	RuleID       string          `json:"rule_id" yaml:"rule_id"`
	Scope        string          `json:"scope" yaml:"scope"`
	Base         int             `json:"base" yaml:"base"`
	Shifts       []priorityShift `json:"shifts" yaml:"shifts"`
	Final        int             `json:"final" yaml:"final"`
	RetainedFrom string          `json:"retained_from,omitempty" yaml:"retained_from,omitempty"`
}

type priorityExplanation struct {
	Facts struct {
		RefClasses       []string `json:"ref_classes" yaml:"ref_classes"`
		EventType        string   `json:"event_type,omitempty" yaml:"event_type,omitempty"`
		EventSource      string   `json:"event_source,omitempty" yaml:"event_source,omitempty"`
		Draft            bool     `json:"draft" yaml:"draft"`
		JobKind          string   `json:"job_kind" yaml:"job_kind"`
		Retry            bool     `json:"retry" yaml:"retry"`
		ActorPrincipalID string   `json:"actor_principal_id,omitempty" yaml:"actor_principal_id,omitempty"`
		Requested        int      `json:"requested,omitempty" yaml:"requested,omitempty"`
	} `json:"facts" yaml:"facts"`
	Decision priorityDecision `json:"decision" yaml:"decision"`
	Lanes    []struct {
		Name    string `json:"name" yaml:"name"`
		Scope   string `json:"scope" yaml:"scope"`
		Base    int    `json:"base" yaml:"base"`
		Enabled bool   `json:"enabled" yaml:"enabled"`
		Matched bool   `json:"matched" yaml:"matched"`
		Match   string `json:"match" yaml:"match"`
	} `json:"lanes" yaml:"lanes"`
	JobID  string            `json:"job_id,omitempty" yaml:"job_id,omitempty"`
	Stored *priorityDecision `json:"stored,omitempty" yaml:"stored,omitempty"`
}

// laneMatchText is the match facts of a lane as one short text.
func laneMatchText(lane priorityLane) string {
	parts := []string{}
	if lane.JobKind != "" {
		parts = append(parts, "kind="+lane.JobKind)
	}
	if len(lane.ClassesAny) > 0 {
		parts = append(parts, "classes_any="+strings.Join(lane.ClassesAny, ","))
	}
	if len(lane.ClassesNone) > 0 {
		parts = append(parts, "classes_none="+strings.Join(lane.ClassesNone, ","))
	}
	if len(lane.EventTypes) > 0 {
		parts = append(parts, "events="+strings.Join(lane.EventTypes, ","))
	}
	if lane.Draft != nil {
		parts = append(parts, "draft="+boolText(lane.Draft))
	}
	if lane.Retry {
		parts = append(parts, "retry=true")
	}
	if len(parts) == 0 {
		return "(every job)"
	}
	return strings.Join(parts, " ")
}

func renderLanes(w *tabwriter.Writer, lanes []priorityLane, scope string) {
	for _, lane := range lanes {
		laneScope := lane.Scope
		if laneScope == "" {
			laneScope = scope
		}
		if lane.Replaces != "" {
			laneScope += " (replaces " + lane.Replaces + ")"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%t\t%s\n", laneScope, lane.Name, lane.Position, lane.Base, lane.Enabled, laneMatchText(lane))
	}
}

func shiftText(shifts []priorityShift) string {
	parts := []string{}
	for _, shift := range shifts {
		label := shift.Source
		if shift.Group != "" {
			label += ":" + shift.Group
		}
		text := fmt.Sprintf("%s %+d", label, shift.Value)
		if !shift.Applied {
			text += " (not used)"
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func laneScopeFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "org", Usage: "organization name (organization lanes)"},
		&cli.StringFlag{Name: "project", Usage: "project ID or name (project lanes)"},
	}
}

// LanesCommand manages priority lanes (docs/priority.md).
var LanesCommand = &cli.Command{
	Name:  "lanes",
	Usage: "Manage priority lanes and explain the priority of a job",
	Subcommands: []*cli.Command{
		{
			Name: "list", Usage: "List the effective lanes in evaluation order (built-in, organization and project lanes)",
			Flags: append(append(apiFlags(), laneScopeFlags()...), formatFlag()),
			Action: func(ctx *cli.Context) error {
				base, err := refClassBasePath(ctx)
				if err != nil {
					return err
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response priorityLanesResponse
				if err := client.doJSON(http.MethodGet, base+"/priority-lanes", nil, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "SCOPE\tNAME\tPOSITION\tBASE\tENABLED\tMATCH")
					renderLanes(w, response.Effective, "")
				})
			},
		},
		{
			Name: "set", ArgsUsage: "<name>",
			Usage: "Create or replace a lane. With the name of a built-in or organization lane and no match flags, only the base (and position) change",
			Flags: append(append(apiFlags(), laneScopeFlags()...), formatFlag(),
				&cli.IntFlag{Name: "base", Required: true, Usage: "base priority, -500 to 500 (higher runs sooner)"},
				&cli.IntFlag{Name: "position", Usage: "evaluation order (lower first; the first lane that matches wins)"},
				&cli.StringFlag{Name: "description"},
				&cli.StringSliceFlag{Name: "class", Usage: "match: the job has one of these ref classes (repeat)"},
				&cli.StringSliceFlag{Name: "not-class", Usage: "match: the job has none of these ref classes (repeat)"},
				&cli.StringSliceFlag{Name: "event", Usage: "match: the event type (repeat): push, tag_created, pull_request_opened, pull_request_updated, pull_request_merged, pull_request_closed, schedule, manual"},
				&cli.StringFlag{Name: "draft", Usage: "match: true for draft pull requests, false for ready ones"},
				&cli.StringFlag{Name: "kind", Usage: "match: eval or work"},
				&cli.BoolFlag{Name: "retry", Usage: "a lane for retries only"},
				&cli.BoolFlag{Name: "match-all", Usage: "a new lane that matches every job"},
				&cli.BoolFlag{Name: "disabled", Usage: "store the lane disabled (a disabled lane with a built-in name turns that lane off)"}),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "lanes set (--org <org> | --project <project>) --base <n> <name>"); err != nil {
					return err
				}
				base, err := refClassBasePath(ctx)
				if err != nil {
					return err
				}
				body := map[string]interface{}{"base": ctx.Int("base"), "enabled": !ctx.Bool("disabled")}
				if ctx.IsSet("position") {
					body["position"] = ctx.Int("position")
				}
				if ctx.IsSet("description") {
					body["description"] = ctx.String("description")
				}
				draft, err := optionalBoolFlag(ctx, "draft")
				if err != nil {
					return err
				}
				hasMatch := ctx.Bool("match-all") || ctx.IsSet("class") || ctx.IsSet("not-class") || ctx.IsSet("event") ||
					draft != nil || ctx.IsSet("kind") || ctx.Bool("retry")
				if hasMatch {
					match := map[string]interface{}{
						"classes_any": ctx.StringSlice("class"), "classes_none": ctx.StringSlice("not-class"),
						"event_types": ctx.StringSlice("event"), "job_kind": ctx.String("kind"), "retry": ctx.Bool("retry"),
					}
					if draft != nil {
						match["draft"] = *draft
					}
					body["match"] = match
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var lane priorityLane
				if err := client.doJSON(http.MethodPut, base+"/priority-lanes/"+url.PathEscape(ctx.Args().First()), body, http.StatusOK, &lane); err != nil {
					return err
				}
				return render(ctx.String("format"), lane, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "SCOPE\tNAME\tPOSITION\tBASE\tENABLED\tMATCH")
					scope := "org"
					if lane.ProjectID != nil {
						scope = "project"
					}
					renderLanes(w, []priorityLane{lane}, scope)
				})
			},
		},
		{
			Name: "delete", ArgsUsage: "<name>", Usage: "Delete a stored lane (a replaced built-in lane applies again)",
			Flags: append(apiFlags(), laneScopeFlags()...),
			Action: func(ctx *cli.Context) error {
				if err := requireArgs(ctx, 1, "lanes delete (--org <org> | --project <project>) <name>"); err != nil {
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
				if err := client.doJSON(http.MethodDelete, base+"/priority-lanes/"+url.PathEscape(ctx.Args().First()), nil, http.StatusNoContent, nil); err != nil {
					return err
				}
				fmt.Println("Done")
				return nil
			},
		},
		{
			Name:  "explain",
			Usage: "Show the lane and priority of a stored job, or of the facts that you give",
			Flags: append(append(apiFlags(), laneScopeFlags()...), formatFlag(),
				&cli.StringFlag{Name: "job-id", Usage: "a stored job: its facts now and its recorded decision"},
				&cli.StringSliceFlag{Name: "class", Usage: "a ref class of the job (repeat)"},
				&cli.StringFlag{Name: "event", Usage: "event type, for example push"},
				&cli.StringFlag{Name: "source", Usage: "event source: webhook, schedule, manual, retry, approval, rerun"},
				&cli.BoolFlag{Name: "draft", Usage: "a draft pull request"},
				&cli.StringFlag{Name: "kind", Value: "work", Usage: "eval or work"},
				&cli.BoolFlag{Name: "retry", Usage: "a retry"},
				&cli.StringFlag{Name: "actor", Usage: "principal UUID whose groups apply"},
				&cli.IntFlag{Name: "requested", Usage: "the priority that the job specification asks for"}),
			Action: func(ctx *cli.Context) error {
				base, err := refClassBasePath(ctx)
				if err != nil {
					return err
				}
				body := map[string]interface{}{
					"job_id": ctx.String("job-id"), "ref_classes": ctx.StringSlice("class"), "event_type": ctx.String("event"),
					"event_source": ctx.String("source"), "draft": ctx.Bool("draft"), "job_kind": ctx.String("kind"),
					"retry": ctx.Bool("retry"), "actor": ctx.String("actor"), "requested": ctx.Int("requested"),
				}
				client, err := newAPIClient(ctx)
				if err != nil {
					return err
				}
				var response priorityExplanation
				if err := client.doJSON(http.MethodPost, base+"/priority/explain", body, http.StatusOK, &response); err != nil {
					return err
				}
				return render(ctx.String("format"), response, func(w *tabwriter.Writer) {
					facts := response.Facts
					fmt.Fprintf(w, "facts\tclasses=%s event=%s source=%s draft=%t kind=%s retry=%t actor=%s requested=%d\n",
						strings.Join(facts.RefClasses, ","), facts.EventType, facts.EventSource, facts.Draft, facts.JobKind, facts.Retry,
						facts.ActorPrincipalID, facts.Requested)
					d := response.Decision
					fmt.Fprintf(w, "priority\t%d (lane %s, %s, base %d)\n", d.Final, d.Lane, d.Scope, d.Base)
					fmt.Fprintf(w, "shifts\t%s\n", shiftText(d.Shifts))
					if response.Stored != nil {
						s := response.Stored
						fmt.Fprintf(w, "recorded\t%d (lane %s, base %d, shifts %s)\n", s.Final, s.Lane, s.Base, shiftText(s.Shifts))
					} else if response.JobID != "" {
						fmt.Fprintln(w, "recorded\tnone (the job was made before lanes)")
					}
					for _, lane := range response.Lanes {
						fmt.Fprintf(w, "lane\t%s %s base=%d enabled=%t matched=%t %s\n", lane.Scope, lane.Name, lane.Base, lane.Enabled, lane.Matched, lane.Match)
					}
				})
			},
		},
	},
}

// groupsSetPriorityShiftCommand is `reactorcide groups set-priority-shift`.
func groupsSetPriorityShiftCommand() *cli.Command {
	return &cli.Command{
		Name: "set-priority-shift", ArgsUsage: "<group> <shift>",
		Usage: "Set the priority shift of a group, -50 to 50 (0 for none). The largest shift of the actor's groups applies to the work it starts",
		Flags: append(apiFlags(), &cli.StringFlag{Name: "org", Required: true}),
		Action: func(ctx *cli.Context) error {
			if err := requireArgs(ctx, 2, "groups set-priority-shift --org <org> <group> <shift>"); err != nil {
				return err
			}
			shift, err := strconv.Atoi(ctx.Args().Get(1))
			if err != nil {
				return fmt.Errorf("shift must be an integer: %w", err)
			}
			client, err := newAPIClient(ctx)
			if err != nil {
				return err
			}
			var response groupSummary
			if err := client.doJSON(http.MethodPut, groupsPath(ctx.String("org"), ctx.Args().First(), "priority-shift"), map[string]int{"shift": shift}, http.StatusOK, &response); err != nil {
				return err
			}
			fmt.Printf("Group %s priority shift: %+d\n", response.Name, response.PriorityShift)
			return nil
		},
	}
}

func init() {
	GroupsCommand.Subcommands = append(GroupsCommand.Subcommands, groupsSetPriorityShiftCommand())
}
