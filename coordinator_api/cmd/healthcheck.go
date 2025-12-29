package cmd

import (
	"fmt"
	"net/http"
	"time"

	"github.com/urfave/cli/v2"
)

var HealthCheckCommand = &cli.Command{
	Name:  "healthcheck",
	Usage: "Check if the coordinator API is healthy (for container health checks)",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "url",
			Aliases: []string{"u"},
			Value:   "http://localhost:6080/api/v1/health",
			Usage:   "URL to check for health",
			EnvVars: []string{"REACTORCIDE_HEALTH_URL"},
		},
		&cli.IntFlag{
			Name:    "timeout",
			Aliases: []string{"t"},
			Value:   5,
			Usage:   "Timeout in seconds",
			EnvVars: []string{"REACTORCIDE_HEALTH_TIMEOUT"},
		},
	},
	Action: func(ctx *cli.Context) error {
		url := ctx.String("url")
		timeout := time.Duration(ctx.Int("timeout")) * time.Second

		client := &http.Client{
			Timeout: timeout,
		}

		resp, err := client.Get(url)
		if err != nil {
			return fmt.Errorf("health check failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("health check failed: status %d", resp.StatusCode)
		}

		return nil
	},
}
