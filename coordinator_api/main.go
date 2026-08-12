package main

import (
	"context"
	"os"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/cmd"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/windowsservice"
	"github.com/urfave/cli/v2"
)

func main() {
	handled, err := windowsservice.RunIfService(func(ctx context.Context, arguments []string) error {
		return newApp().RunContext(ctx, append([]string{"reactorcide"}, arguments...))
	})
	if handled {
		if err != nil {
			logging.Log.WithError(err).Error("Windows service stopped")
			os.Exit(1)
		}
		return
	}
	app := newApp()
	// Let flags follow positional arguments: see cmd.NormalizeArgs.
	err = app.Run(cmd.NormalizeArgs(app, os.Args))
	if err != nil {
		// log fatal so we exit with the proper exit code, this is important for containerized deployment health checks
		logging.Log.WithError(err).Fatal("runtime error")
	}
}

func newApp() *cli.App {
	return &cli.App{
		Name:  "reactorcide",
		Usage: "Reactorcide CI/CD system",
		Commands: []*cli.Command{
			cmd.ServeCommand,
			cmd.MigrateCommand,
			cmd.WorkerCommand,
			cmd.HealthCheckCommand,
			cmd.TokenCommand,
			cmd.OrgsCommand,
			cmd.PolicyCommand,
			cmd.ProfilesCommand,
			cmd.ApprovalsCommand,
			cmd.SecretsCommand,
			cmd.SecretGrantsCommand,
			cmd.RunLocalCommand,
			cmd.SubmitCommand,
			cmd.JobsCommand,
			cmd.WorkflowsCommand,
			cmd.ProjectsCommand,
			cmd.LogsCommand,
			cmd.VMImageCommand,
			cmd.WindowsServiceCommand,
		},
	}
}
