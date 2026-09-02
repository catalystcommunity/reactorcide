package cmd

import (
	"fmt"
	"net/http"

	"github.com/catalystcommunity/reactorcide/webapp/internal/config"
	"github.com/catalystcommunity/reactorcide/webapp/internal/handlers"
	"github.com/catalystcommunity/reactorcide/webapp/internal/transportsecurity"
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
		Value:       5080,
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
	&cli.BoolFlag{
		Name:        "allow-insecure-transport",
		Usage:       "Allow credentials and user sessions on a coordinator connection without TLS (development only)",
		Destination: &config.AllowInsecureTransport,
	},
	&cli.BoolFlag{
		Name:        "cookie-insecure",
		Usage:       "Disable the Secure flag on the session cookie (local http dev only)",
		EnvVars:     []string{"REACTORCIDE_WEB_COOKIE_INSECURE"},
		Destination: &config.WebCookieInsecure,
	},
}

func Serve() error {
	if err := transportsecurity.ValidateURL(config.APIUrl, config.AllowInsecureTransport, "web coordinator connection"); err != nil {
		return err
	}

	handler := handlers.NewRouter()

	logrus.Infof("Starting web UI on port %d", config.Port)
	logrus.Infof("Coordinator API: %s", config.APIUrl)

	err := http.ListenAndServe(fmt.Sprintf(":%d", config.Port), handler)
	logrus.WithError(err).Error("ListenAndServe exited")
	return err
}
