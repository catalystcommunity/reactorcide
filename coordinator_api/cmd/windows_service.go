package cmd

import (
	"fmt"
	"os"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/windowsservice"
	"github.com/urfave/cli/v2"
)

var WindowsServiceCommand = &cli.Command{
	Name:  "windows-service",
	Usage: "Manage the native Windows worker service",
	Subcommands: []*cli.Command{
		{
			Name:  "install",
			Usage: "Install the worker service",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "config", Required: true, Usage: "Path to the worker service JSON file"},
				&cli.StringFlag{Name: "executable", Usage: "Path to reactorcide.exe (default: this executable)"},
			},
			Action: func(ctx *cli.Context) error {
				executable := ctx.String("executable")
				if executable == "" {
					var err error
					executable, err = os.Executable()
					if err != nil {
						return fmt.Errorf("find current executable: %w", err)
					}
				}
				return windowsservice.Install(executable, ctx.String("config"))
			},
		},
		{Name: "start", Usage: "Start the worker service", Action: func(*cli.Context) error { return windowsservice.Start() }},
		{Name: "stop", Usage: "Stop the worker service", Action: func(*cli.Context) error { return windowsservice.Stop() }},
		{
			Name:  "restart",
			Usage: "Restart the worker service",
			Action: func(*cli.Context) error {
				if err := windowsservice.Stop(); err != nil {
					return err
				}
				return windowsservice.Start()
			},
		},
		{
			Name:  "status",
			Usage: "Show the worker service state",
			Action: func(ctx *cli.Context) error {
				status, err := windowsservice.Status()
				if err == nil {
					fmt.Fprintln(ctx.App.Writer, status)
				}
				return err
			},
		},
		{Name: "uninstall", Usage: "Remove the stopped worker service", Action: func(*cli.Context) error { return windowsservice.Uninstall() }},
		{Name: "run", Hidden: true, Flags: []cli.Flag{&cli.StringFlag{Name: "config"}}},
	},
}
