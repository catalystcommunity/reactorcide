package cmd

import (
	"fmt"
	"net/http"

	"github.com/catalystcommunity/reactorcide/webapp/internal/config"
	"github.com/catalystcommunity/reactorcide/webapp/internal/handlers"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

var ServeCommand = &cli.Command{
	Name:  "serve",
	Usage: "Run the web UI server",
	Flags: flags,
	Action: func(ctx *cli.Context) error {
		return Serve()
	},
}

var flags = []cli.Flag{
	&cli.IntFlag{
		Name:        "port",
		Aliases:     []string{"p"},
		Value:       4080,
		Usage:       "Port to serve the web UI on",
		EnvVars:     []string{"REACTORCIDE_WEB_PORT", "PORT"},
		Destination: &config.Port,
	},
	&cli.StringFlag{
		Name:        "api-url",
		Value:       "http://localhost:6080",
		Usage:       "Base URL of the coordinator API",
		EnvVars:     []string{"REACTORCIDE_API_URL"},
		Destination: &config.APIUrl,
	},
	&cli.StringFlag{
		Name:        "api-token",
		Usage:       "Bearer token for coordinator API authentication",
		EnvVars:     []string{"REACTORCIDE_API_TOKEN"},
		Destination: &config.APIToken,
	},
}

func Serve() error {
	if config.APIToken == "" {
		logrus.Warn("No API token configured - API requests will fail. Set REACTORCIDE_API_TOKEN.")
	}

	handler := handlers.NewRouter()

	logrus.Infof("Starting web UI on port %d", config.Port)
	logrus.Infof("Coordinator API: %s", config.APIUrl)

	err := http.ListenAndServe(fmt.Sprintf(":%d", config.Port), handler)
	logrus.WithError(err).Error("ListenAndServe exited")
	return err
}
