package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/urfave/cli/v2"
)

// SubmitCommand submits a job to a remote Reactorcide coordinator API
var SubmitCommand = &cli.Command{
	Name:      "submit",
	Usage:     "Submit a job to a remote Reactorcide coordinator",
	ArgsUsage: "<job-file>",
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
		&cli.StringSliceFlag{
			Name:    "overlay",
			Aliases: []string{"o"},
			Usage:   "Overlay file(s) to merge with job definition (can be repeated)",
		},
		&cli.BoolFlag{
			Name:  "allow-secret-overrides",
			Usage: "Allow overlay files to override secret references with plaintext",
		},
		&cli.BoolFlag{
			Name:    "wait",
			Aliases: []string{"w"},
			Usage:   "Wait for job to complete and show final status",
		},
		&cli.IntFlag{
			Name:  "poll-interval",
			Value: 5,
			Usage: "Polling interval in seconds when using --wait",
		},
	},
	Action: submitAction,
}

// CreateJobRequest is the API request structure for creating a job
type CreateJobRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	// Source configuration
	SourceURL  string `json:"source_url,omitempty"`
	SourceRef  string `json:"source_ref,omitempty"`
	SourceType string `json:"source_type"`
	SourcePath string `json:"source_path,omitempty"`

	// CI Source configuration (trusted CI pipeline code)
	CISourceType string `json:"ci_source_type,omitempty"`
	CISourceURL  string `json:"ci_source_url,omitempty"`
	CISourceRef  string `json:"ci_source_ref,omitempty"`

	// Runnerlib configuration
	CodeDir     string `json:"code_dir,omitempty"`
	JobDir      string `json:"job_dir,omitempty"`
	JobCommand  string `json:"job_command"`
	RunnerImage string `json:"runner_image,omitempty"`

	// Environment configuration
	JobEnvVars map[string]string `json:"job_env_vars,omitempty"`
	JobEnvFile string            `json:"job_env_file,omitempty"`

	// Execution settings
	TimeoutSeconds *int   `json:"timeout_seconds,omitempty"`
	Priority       *int   `json:"priority,omitempty"`
	QueueName      string `json:"queue_name,omitempty"`
}

// JobResponse is the API response structure for job operations
type JobResponse struct {
	JobID       string    `json:"job_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Source info
	SourceURL  string `json:"source_url,omitempty"`
	SourceRef  string `json:"source_ref,omitempty"`
	SourceType string `json:"source_type"`
	SourcePath string `json:"source_path,omitempty"`

	// Execution info
	TimeoutSeconds int        `json:"timeout_seconds"`
	Priority       int        `json:"priority"`
	QueueName      string     `json:"queue_name"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ExitCode       *int       `json:"exit_code,omitempty"`
}

func submitAction(ctx *cli.Context) error {
	if ctx.NArg() < 1 {
		return fmt.Errorf("usage: reactorcide submit <job-file>")
	}

	jobFile := ctx.Args().Get(0)
	apiURL := ctx.String("api-url")
	token := ctx.String("token")
	overlayFiles := ctx.StringSlice("overlay")
	allowSecretOverrides := ctx.Bool("allow-secret-overrides")
	wait := ctx.Bool("wait")
	pollInterval := ctx.Int("poll-interval")

	// Validate required flags
	if apiURL == "" {
		return fmt.Errorf("API URL is required (use --api-url or REACTORCIDE_API_URL)")
	}

	// Normalize API URL (remove trailing slash)
	apiURL = strings.TrimSuffix(apiURL, "/")

	// Load job specification with overlays
	spec, secretOverrides, err := worker.LoadJobSpecWithOverlays(jobFile, overlayFiles)
	if err != nil {
		return err
	}

	// Check for secret overrides
	if err := checkSecretOverrides(secretOverrides, allowSecretOverrides); err != nil {
		return err
	}

	// Resolve ${env:VAR_NAME} references from host environment
	spec.Environment = worker.ResolveEnvInMap(spec.Environment)

	// Resolve ${secret:path:key} references
	resolvedEnv, _, err := resolveJobSecrets(spec.Environment)
	if err != nil {
		return err
	}
	spec.Environment = resolvedEnv

	// Get API token
	if token == "" {
		token, err = promptForSecret("REACTORCIDE_API_TOKEN", "API token: ")
		if err != nil {
			return err
		}
	}

	if token == "" {
		return fmt.Errorf("API token is required (use --token or REACTORCIDE_API_TOKEN)")
	}

	// Build the API request
	req := specToCreateJobRequest(spec)

	// Submit the job
	fmt.Fprintf(os.Stderr, "Submitting job: %s\n", spec.Name)
	jobResp, err := submitJobToAPI(apiURL, token, req)
	if err != nil {
		return fmt.Errorf("failed to submit job: %w", err)
	}

	fmt.Println("Job submitted successfully!")
	fmt.Printf("  Job ID: %s\n", jobResp.JobID)
	fmt.Printf("  Status: %s\n", jobResp.Status)
	fmt.Printf("  Name:   %s\n", jobResp.Name)

	// Optionally wait for completion
	if wait {
		fmt.Println("\nWaiting for completion...")
		startTime := time.Now()

		finalResp, err := waitForJobCompletion(apiURL, token, jobResp.JobID, pollInterval)
		if err != nil {
			return fmt.Errorf("failed while waiting for job: %w", err)
		}

		elapsed := time.Since(startTime).Round(time.Second)

		fmt.Println()
		switch finalResp.Status {
		case "completed":
			fmt.Println("Job completed!")
		case "failed":
			fmt.Println("Job failed!")
		case "cancelled":
			fmt.Println("Job cancelled!")
		case "timeout":
			fmt.Println("Job timed out!")
		default:
			fmt.Printf("Job ended with status: %s\n", finalResp.Status)
		}

		if finalResp.ExitCode != nil {
			fmt.Printf("  Exit Code: %d\n", *finalResp.ExitCode)
		}
		fmt.Printf("  Duration:  %s\n", elapsed)

		// Return non-zero exit if job failed
		if finalResp.Status != "completed" {
			exitCode := 1
			if finalResp.ExitCode != nil {
				exitCode = *finalResp.ExitCode
			}
			return cli.Exit("", exitCode)
		}
	}

	return nil
}

// specToCreateJobRequest converts a JobSpec to a CreateJobRequest
func specToCreateJobRequest(spec *worker.JobSpec) *CreateJobRequest {
	req := &CreateJobRequest{
		Name:        spec.Name,
		JobCommand:  spec.Command,
		RunnerImage: spec.Image,
		JobEnvVars:  spec.Environment,
		JobDir:      spec.WorkingDir,
	}

	// Set timeout if specified
	if spec.TimeoutSeconds > 0 {
		req.TimeoutSeconds = &spec.TimeoutSeconds
	}

	// Set source configuration
	if spec.Source != nil {
		req.SourceType = spec.Source.Type
		req.SourceURL = spec.Source.URL
		req.SourceRef = spec.Source.Ref
		req.SourcePath = spec.Source.Path
	} else {
		// Default to "copy" if no source specified (local job)
		req.SourceType = "copy"
	}

	return req
}

// submitJobToAPI sends a job creation request to the coordinator API
func submitJobToAPI(apiURL, token string, req *CreateJobRequest) (*JobResponse, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", apiURL+"/api/v1/jobs", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var jobResp JobResponse
	if err := json.Unmarshal(body, &jobResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &jobResp, nil
}

// waitForJobCompletion polls the API until the job reaches a terminal state
func waitForJobCompletion(apiURL, token, jobID string, pollInterval int) (*JobResponse, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	lastStatus := ""

	for {
		req, err := http.NewRequest("GET", apiURL+"/api/v1/jobs/"+jobID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to get job status: %w", err)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
		}

		var jobResp JobResponse
		if err := json.Unmarshal(body, &jobResp); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}

		// Print status updates
		if jobResp.Status != lastStatus {
			fmt.Fprintf(os.Stderr, "  Status: %s\n", jobResp.Status)
			lastStatus = jobResp.Status
		}

		// Check for terminal states
		switch jobResp.Status {
		case "completed", "failed", "cancelled", "timeout":
			return &jobResp, nil
		}

		time.Sleep(time.Duration(pollInterval) * time.Second)
	}
}
