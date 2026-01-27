package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v2"
)

// LogsCommand retrieves logs for a job from a remote Reactorcide coordinator API
var LogsCommand = &cli.Command{
	Name:      "logs",
	Usage:     "Get logs for a job from a remote Reactorcide coordinator",
	ArgsUsage: "<job-id>",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "api-url",
			Aliases: []string{"u"},
			Usage:   "Coordinator API URL (e.g., http://localhost:6080)",
			EnvVars: []string{"REACTORCIDE_API_URL"},
		},
		&cli.StringFlag{
			Name:    "token",
			Aliases: []string{"t"},
			Usage:   "API token for authentication",
			EnvVars: []string{"REACTORCIDE_API_TOKEN"},
		},
		&cli.StringFlag{
			Name:    "stream",
			Aliases: []string{"s"},
			Value:   "combined",
			Usage:   "Log stream to retrieve: stdout, stderr, or combined (default)",
		},
		&cli.StringFlag{
			Name:    "output",
			Aliases: []string{"o"},
			Usage:   "Output file (default: stdout)",
		},
	},
	Action: logsAction,
}

func logsAction(ctx *cli.Context) error {
	if ctx.NArg() < 1 {
		return fmt.Errorf("usage: reactorcide logs <job-id>")
	}

	jobID := ctx.Args().Get(0)
	apiURL := ctx.String("api-url")
	token := ctx.String("token")
	stream := ctx.String("stream")
	outputFile := ctx.String("output")

	// Validate required flags
	if apiURL == "" {
		return fmt.Errorf("API URL is required (use --api-url or REACTORCIDE_API_URL)")
	}

	// Normalize API URL (remove trailing slash)
	apiURL = strings.TrimSuffix(apiURL, "/")

	// Validate stream parameter
	if stream != "stdout" && stream != "stderr" && stream != "combined" {
		return fmt.Errorf("invalid stream value: %s (must be stdout, stderr, or combined)", stream)
	}

	// Get API token
	var err error
	if token == "" {
		token, err = promptForSecret("REACTORCIDE_API_TOKEN", "API token: ")
		if err != nil {
			return err
		}
	}

	if token == "" {
		return fmt.Errorf("API token is required (use --token or REACTORCIDE_API_TOKEN)")
	}

	// Fetch logs from API
	logs, err := fetchJobLogs(apiURL, token, jobID, stream)
	if err != nil {
		return fmt.Errorf("failed to fetch logs: %w", err)
	}

	// Output logs
	if outputFile != "" {
		if err := os.WriteFile(outputFile, logs, 0644); err != nil {
			return fmt.Errorf("failed to write logs to file: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Logs written to: %s\n", outputFile)
	} else {
		fmt.Print(string(logs))
	}

	return nil
}

// fetchJobLogs retrieves logs for a job from the coordinator API
func fetchJobLogs(apiURL, token, jobID, stream string) ([]byte, error) {
	url := fmt.Sprintf("%s/api/v1/jobs/%s/logs", apiURL, jobID)
	if stream != "" && stream != "combined" {
		url = fmt.Sprintf("%s?stream=%s", url, stream)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusNotFound:
		return nil, fmt.Errorf("job not found or no logs available for job: %s", jobID)
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("unauthorized: invalid or missing API token")
	case http.StatusForbidden:
		return nil, fmt.Errorf("forbidden: you don't have permission to access this job's logs")
	case http.StatusServiceUnavailable:
		return nil, fmt.Errorf("service unavailable: object store not configured on the server")
	default:
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}
}
