package cmd

import (
	"fmt"
	"text/tabwriter"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/urfave/cli/v2"
)

// ScheduleCommand manages coordinator-managed schedules. A schedule points at
// a workflow definition in the repository on a branch; the coordinator fires
// it at cron times.
var ScheduleCommand = &cli.Command{
	Name:    "schedules",
	Aliases: []string{"schedule"},
	Usage:   "Manage scheduled workflow runs",
	Flags:   apiFlags(),
	Subcommands: []*cli.Command{
		{
			Name:  "list",
			Usage: "List the schedules of a project",
			Flags: []cli.Flag{policyProjectFlag(), formatFlag()},
			Action: func(ctx *cli.Context) error {
				client, projectID, err := policyClientAndProject(ctx)
				if err != nil {
					return err
				}
				resp, err := client.ListSchedules(ctx.Context, csilapi.ListSchedulesRequest{ProjectId: projectID})
				if err != nil {
					return err
				}
				return render(ctx.String("format"), resp.Schedules, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "ID\tNAME\tBRANCH\tWORKFLOW\tCRON\tTIMEZONE\tENABLED\tNEXT FIRE\tLAST ERROR")
					for _, s := range resp.Schedules {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%t\t%s\t%s\n",
							s.ScheduleId, s.Name, s.Branch, s.WorkflowRef, s.Cron, s.Timezone, s.Enabled, textOrDash(s.NextFireAt), s.LastError)
					}
				})
			},
		},
		{
			Name:  "create",
			Usage: "Create a schedule that runs a workflow definition on a branch",
			Flags: []cli.Flag{
				policyProjectFlag(),
				&cli.StringFlag{Name: "name", Required: true, Usage: "Schedule name, unique in the project"},
				&cli.StringFlag{Name: "branch", Required: true, Usage: "Branch whose head commit runs"},
				&cli.StringFlag{Name: "workflow", Required: true, Usage: "Workflow ID, or a file path under .reactorcide/workflows/"},
				&cli.StringFlag{Name: "cron", Required: true, Usage: "Five-field cron expression or a descriptor such as @daily"},
				&cli.StringFlag{Name: "timezone", Value: "UTC", Usage: "IANA timezone the cron expression is read in"},
				&cli.BoolFlag{Name: "disabled", Usage: "Create the schedule disabled"},
				formatFlag(),
			},
			Action: func(ctx *cli.Context) error {
				client, projectID, err := policyClientAndProject(ctx)
				if err != nil {
					return err
				}
				enabled := !ctx.Bool("disabled")
				timezone := ctx.String("timezone")
				resp, err := client.CreateSchedule(ctx.Context, csilapi.CreateScheduleRequest{
					ProjectId: projectID, Name: ctx.String("name"), Branch: ctx.String("branch"),
					WorkflowRef: ctx.String("workflow"), Cron: ctx.String("cron"), Timezone: &timezone, Enabled: &enabled,
				})
				if err != nil {
					return err
				}
				return printSchedule(ctx.String("format"), resp.Schedule)
			},
		},
		{
			Name:      "update",
			Usage:     "Change a schedule. Only the flags you give change.",
			ArgsUsage: "<schedule-id>",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "name"},
				&cli.StringFlag{Name: "branch"},
				&cli.StringFlag{Name: "workflow", Usage: "Workflow ID, or a file path under .reactorcide/workflows/"},
				&cli.StringFlag{Name: "cron"},
				&cli.StringFlag{Name: "timezone"},
				formatFlag(),
			},
			Action: func(ctx *cli.Context) error {
				id, err := scheduleIDArg(ctx, "update")
				if err != nil {
					return err
				}
				req := csilapi.UpdateScheduleRequest{ScheduleId: id}
				if ctx.IsSet("name") {
					req.Name = stringPtr(ctx.String("name"))
				}
				if ctx.IsSet("branch") {
					req.Branch = stringPtr(ctx.String("branch"))
				}
				if ctx.IsSet("workflow") {
					req.WorkflowRef = stringPtr(ctx.String("workflow"))
				}
				if ctx.IsSet("cron") {
					req.Cron = stringPtr(ctx.String("cron"))
				}
				if ctx.IsSet("timezone") {
					req.Timezone = stringPtr(ctx.String("timezone"))
				}
				return updateSchedule(ctx, req)
			},
		},
		{
			Name:      "enable",
			Usage:     "Enable a schedule. The next fire is planned from now; missed times are not run.",
			ArgsUsage: "<schedule-id>",
			Flags:     []cli.Flag{formatFlag()},
			Action:    func(ctx *cli.Context) error { return setScheduleEnabled(ctx, "enable", true) },
		},
		{
			Name:      "disable",
			Usage:     "Disable a schedule",
			ArgsUsage: "<schedule-id>",
			Flags:     []cli.Flag{formatFlag()},
			Action:    func(ctx *cli.Context) error { return setScheduleEnabled(ctx, "disable", false) },
		},
		{
			Name:      "delete",
			Usage:     "Delete a schedule and its fire history",
			ArgsUsage: "<schedule-id>",
			Action: func(ctx *cli.Context) error {
				id, err := scheduleIDArg(ctx, "delete")
				if err != nil {
					return err
				}
				client, err := managementClient(ctx)
				if err != nil {
					return err
				}
				if _, err := client.DeleteSchedule(ctx.Context, csilapi.DeleteScheduleRequest{ScheduleId: id}); err != nil {
					return err
				}
				fmt.Printf("Schedule %s deleted\n", id)
				return nil
			},
		},
		{
			Name:      "fires",
			Usage:     "List the newest fires of a schedule",
			ArgsUsage: "<schedule-id>",
			Flags:     []cli.Flag{&cli.IntFlag{Name: "limit", Value: 20, Usage: "Number of fires (1-200)"}, formatFlag()},
			Action: func(ctx *cli.Context) error {
				id, err := scheduleIDArg(ctx, "fires")
				if err != nil {
					return err
				}
				client, err := managementClient(ctx)
				if err != nil {
					return err
				}
				limit := int64(ctx.Int("limit"))
				resp, err := client.ListScheduleFires(ctx.Context, csilapi.ListScheduleFiresRequest{ScheduleId: id, Limit: &limit})
				if err != nil {
					return err
				}
				return render(ctx.String("format"), resp.Fires, func(w *tabwriter.Writer) {
					fmt.Fprintln(w, "FIRE TIME\tSTATUS\tEVENT\tERROR")
					for _, f := range resp.Fires {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", f.FireTime, f.Status, textOrDash(f.EventId), f.Error)
					}
				})
			},
		},
	},
}

func scheduleIDArg(ctx *cli.Context, sub string) (string, error) {
	if ctx.NArg() != 1 || ctx.Args().First() == "" {
		return "", fmt.Errorf("usage: reactorcide schedules %s <schedule-id>", sub)
	}
	return ctx.Args().First(), nil
}

func setScheduleEnabled(ctx *cli.Context, sub string, enabled bool) error {
	id, err := scheduleIDArg(ctx, sub)
	if err != nil {
		return err
	}
	return updateSchedule(ctx, csilapi.UpdateScheduleRequest{ScheduleId: id, Enabled: &enabled})
}

func updateSchedule(ctx *cli.Context, req csilapi.UpdateScheduleRequest) error {
	client, err := managementClient(ctx)
	if err != nil {
		return err
	}
	resp, err := client.UpdateSchedule(ctx.Context, req)
	if err != nil {
		return err
	}
	return printSchedule(ctx.String("format"), resp.Schedule)
}

func printSchedule(format string, s csilapi.ScheduleSummary) error {
	return render(format, s, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "schedule_id\t%s\nname\t%s\nbranch\t%s\nworkflow\t%s\ncron\t%s\ntimezone\t%s\nenabled\t%t\nnext_fire_at\t%s\nlast_fire_at\t%s\nlast_error\t%s\n",
			s.ScheduleId, s.Name, s.Branch, s.WorkflowRef, s.Cron, s.Timezone, s.Enabled, textOrDash(s.NextFireAt), textOrDash(s.LastFireAt), s.LastError)
	})
}

func stringPtr(value string) *string { return &value }
