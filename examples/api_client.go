package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// ReactorcideClient is a simple client for the Reactorcide API
type ReactorcideClient struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

// NewReactorcideClient creates a new API client
func NewReactorcideClient(baseURL, token string) *ReactorcideClient {
	return &ReactorcideClient{
		BaseURL: baseURL,
		Token:   token,
		Client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Job represents a job in the system
type Job struct {
	JobID       string            `json:"job_id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Status      string            `json:"status"`
	GitURL      string            `json:"git_url,omitempty"`
	GitRef      string            `json:"git_ref,omitempty"`
	SourceType  string            `json:"source_type"`
	JobCommand  string            `json:"job_command"`
	RunnerImage string            `json:"runner_image,omitempty"`
	JobEnvVars  map[string]string `json:"job_env_vars,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// CreateJobRequest represents the request to create a new job
type CreateJobRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	GitURL      string            `json:"git_url,omitempty"`
	GitRef      string            `json:"git_ref,omitempty"`
	SourceType  string            `json:"source_type"`
	SourcePath  string            `json:"source_path,omitempty"`
	JobCommand  string            `json:"job_command"`
	RunnerImage string            `json:"runner_image,omitempty"`
	JobEnvVars  map[string]string `json:"job_env_vars,omitempty"`
	QueueName   string            `json:"queue_name,omitempty"`
}

// doRequest performs an HTTP request with authentication
func (c *ReactorcideClient) doRequest(method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewBuffer(jsonBody)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)

	return c.Client.Do(req)
}

// CheckHealth checks the API health
func (c *ReactorcideClient) CheckHealth() error {
	resp, err := c.doRequest("GET", "/api/v1/health", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("health check failed: %s", body)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	fmt.Printf("API Health: %+v\n", result)
	return nil
}

// CreateJob creates a new job that will be submitted to Corndogs
func (c *ReactorcideClient) CreateJob(req *CreateJobRequest) (*Job, error) {
	resp, err := c.doRequest("POST", "/api/v1/jobs", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create job: %s", body)
	}

	var job Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, err
	}

	return &job, nil
}

// GetJob retrieves a job by ID
func (c *ReactorcideClient) GetJob(jobID string) (*Job, error) {
	resp, err := c.doRequest("GET", "/api/v1/jobs/"+jobID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get job: %s", body)
	}

	var job Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, err
	}

	return &job, nil
}

// ListJobs lists all jobs
func (c *ReactorcideClient) ListJobs() ([]Job, error) {
	resp, err := c.doRequest("GET", "/api/v1/jobs", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to list jobs: %s", body)
	}

	var result struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Jobs, nil
}

// CancelJob cancels a running job
func (c *ReactorcideClient) CancelJob(jobID string) error {
	resp, err := c.doRequest("PUT", "/api/v1/jobs/"+jobID+"/cancel", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to cancel job: %s", body)
	}

	return nil
}

// DeleteJob deletes a job
func (c *ReactorcideClient) DeleteJob(jobID string) error {
	resp, err := c.doRequest("DELETE", "/api/v1/jobs/"+jobID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete job: %s", body)
	}

	return nil
}

// WaitForJob waits for a job to complete or fail
func (c *ReactorcideClient) WaitForJob(jobID string, timeout time.Duration) (*Job, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		job, err := c.GetJob(jobID)
		if err != nil {
			return nil, err
		}

		// Check if job is in a terminal state
		switch job.Status {
		case "completed", "failed", "cancelled":
			return job, nil
		}

		// Wait before polling again
		time.Sleep(2 * time.Second)
	}

	return nil, fmt.Errorf("timeout waiting for job %s", jobID)
}

func main() {
	// Example usage
	// Note: You need to set these environment variables or update the values
	apiURL := os.Getenv("REACTORCIDE_API_URL")
	if apiURL == "" {
		apiURL = "http://localhost:8080"
	}

	apiToken := os.Getenv("REACTORCIDE_API_TOKEN")
	if apiToken == "" {
		fmt.Println("Please set REACTORCIDE_API_TOKEN environment variable")
		fmt.Println("To create a token for testing, see the test examples in coordinator_api/test/")
		os.Exit(1)
	}

	client := NewReactorcideClient(apiURL, apiToken)

	// Check API health
	fmt.Println("Checking API health...")
	if err := client.CheckHealth(); err != nil {
		fmt.Printf("Health check failed: %v\n", err)
		os.Exit(1)
	}

	// Create a simple echo job
	fmt.Println("\nCreating a job...")
	job, err := client.CreateJob(&CreateJobRequest{
		Name:        "Example Echo Job",
		Description: "A simple job that echoes a message",
		SourceType:  "git",
		GitURL:      "https://github.com/example/repo.git",
		GitRef:      "main",
		JobCommand:  "echo 'Hello from Reactorcide!'",
		RunnerImage: "alpine:latest",
		JobEnvVars: map[string]string{
			"EXAMPLE_VAR": "example_value",
		},
		QueueName: "default",
	})

	if err != nil {
		fmt.Printf("Failed to create job: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created job: %s (status: %s)\n", job.JobID, job.Status)
	fmt.Println("Job has been submitted to Corndogs queue for processing")

	// List jobs
	fmt.Println("\nListing jobs...")
	jobs, err := client.ListJobs()
	if err != nil {
		fmt.Printf("Failed to list jobs: %v\n", err)
	} else {
		for _, j := range jobs {
			fmt.Printf("- %s: %s (status: %s)\n", j.JobID, j.Name, j.Status)
		}
	}

	// Wait for job completion (with timeout)
	fmt.Printf("\nWaiting for job %s to complete...\n", job.JobID)
	completedJob, err := client.WaitForJob(job.JobID, 5*time.Minute)
	if err != nil {
		fmt.Printf("Error waiting for job: %v\n", err)
	} else {
		fmt.Printf("Job completed with status: %s\n", completedJob.Status)
	}

	// Clean up - delete the job
	fmt.Printf("\nDeleting job %s...\n", job.JobID)
	if err := client.DeleteJob(job.JobID); err != nil {
		fmt.Printf("Failed to delete job: %v\n", err)
	} else {
		fmt.Println("Job deleted successfully")
	}
}
