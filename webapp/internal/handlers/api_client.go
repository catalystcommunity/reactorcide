package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/catalystcommunity/reactorcide/webapp/internal/config"
)

// APIClient handles communication with the coordinator API
type APIClient struct {
	httpClient *http.Client
}

func NewAPIClient() *APIClient {
	return &APIClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// JobResponse matches the coordinator API's job response format
type JobResponse struct {
	JobID       string     `json:"job_id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	SourceURL   string     `json:"source_url,omitempty"`
	SourceRef   string     `json:"source_ref,omitempty"`
	SourceType  string     `json:"source_type"`
	SourcePath  string     `json:"source_path,omitempty"`
	CodeDir     string     `json:"code_dir"`
	JobDir      string     `json:"job_dir"`
	JobCommand  string     `json:"job_command"`
	RunnerImage string     `json:"runner_image"`
	QueueName   string     `json:"queue_name"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	Priority    int        `json:"priority"`
	ParentJobID *string    `json:"parent_job_id,omitempty"`
}

// ListJobsResponse matches the coordinator API's list response
type ListJobsResponse struct {
	Jobs   []JobResponse `json:"jobs"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

// LogEntry matches the coordinator API's log format
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Stream    string `json:"stream"`
	Level     string `json:"level,omitempty"`
	Message   string `json:"message"`
}

func (c *APIClient) doRequest(method, path string) ([]byte, int, error) {
	url := config.APIUrl + path
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	if config.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+config.APIToken)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response: %w", err)
	}

	return body, resp.StatusCode, nil
}

// ListJobs fetches jobs from the coordinator API
func (c *APIClient) ListJobs(limit, offset int, status string) (*ListJobsResponse, error) {
	path := fmt.Sprintf("/api/v1/jobs?limit=%d&offset=%d", limit, offset)
	if status != "" {
		path += "&status=" + status
	}

	body, statusCode, err := c.doRequest("GET", path)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", statusCode, string(body))
	}

	var result ListJobsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return &result, nil
}

// GetJob fetches a single job by ID
func (c *APIClient) GetJob(jobID string) (*JobResponse, error) {
	body, statusCode, err := c.doRequest("GET", "/api/v1/jobs/"+jobID)
	if err != nil {
		return nil, err
	}
	if statusCode == http.StatusNotFound {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", statusCode, string(body))
	}

	var result JobResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return &result, nil
}

// GetJobLogs fetches logs for a job
func (c *APIClient) GetJobLogs(jobID, stream string) ([]LogEntry, error) {
	if stream == "" {
		stream = "combined"
	}

	path := fmt.Sprintf("/api/v1/jobs/%s/logs?stream=%s", jobID, stream)
	body, statusCode, err := c.doRequest("GET", path)
	if err != nil {
		return nil, err
	}
	if statusCode == http.StatusNotFound {
		return nil, nil // no logs yet
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", statusCode, string(body))
	}

	var entries []LogEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("parsing logs: %w", err)
	}
	return entries, nil
}
