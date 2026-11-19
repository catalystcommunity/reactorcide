package cmd

import (
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/urfave/cli/v2"
)

var ServeCommand = &cli.Command{
	Name:  "serve",
	Usage: "Run the Server",
	Flags: flags,
	Action: func(ctx *cli.Context) error {
		return Serve()
	},
}

var flags = []cli.Flag{
	&cli.StringFlag{
		Name:        "db-uri",
		Aliases:     []string{"db"},
		Value:       "postgresql://devuser:devpass@monodemo-postgresql:5432/monodemopg?sslmode=disable",
		Usage:       "The uri to use to connect to the postgres db",
		Destination: &config.DbUri,
		EnvVars:     []string{"REACTORCIDE_DB_URI", "DB_URI"},
	},
	&cli.IntFlag{
		Name:        "port",
		Aliases:     []string{"gp"},
		Value:       6080,
		Usage:       "Port to expose the web API on",
		EnvVars:     []string{"REACTORCIDE_PORT", "PORT"},
		Destination: &config.Port,
	},
	&cli.BoolFlag{
		Name: "allow-insecure-transport",
		Usage: "Development only: permit HTTP for federation issuer JWKS and discovery fetches, " +
			"an HTTP OIDC issuer URL, and HTTP outbound event webhooks. Never use it in production.",
		Destination: &config.AllowInsecureTransport,
	},
}
