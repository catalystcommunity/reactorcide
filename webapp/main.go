package main

import (
	"os"

	"github.com/catalystcommunity/reactorcide/webapp/cmd"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

func main() {
	app := &cli.App{
		Name:  "reactorcide-web",
		Usage: "Reactorcide Web UI",
		Commands: []*cli.Command{
			cmd.ServeCommand,
		},
	}
	err := app.Run(os.Args)
	if err != nil {
		logrus.WithError(err).Fatal("runtime error")
	}
}
