package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	JobID            string     `json:"job_id"`
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	Status           string     `json:"status"`
	LastError        string     `json:"last_error,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	SourceURL        string     `json:"source_url,omitempty"`
	SourceRef        string     `json:"source_ref,omitempty"`
	SourceType       string     `json:"source_type"`
	SourcePath       string     `json:"source_path,omitempty"`
	CodeDir          string     `json:"code_dir"`
	JobDir           string     `json:"job_dir"`
	JobCommand       string     `json:"job_command"`
	RunnerImage      string     `json:"runner_image"`
	QueueName        string     `json:"queue_name"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	ExitCode         *int       `json:"exit_code,omitempty"`
	Priority         int        `json:"priority"`
	ParentJobID      *string    `json:"parent_job_id,omitempty"`
	ProjectID        *string    `json:"project_id,omitempty"`
	WorkflowID       *string    `json:"workflow_id,omitempty"`
	WorkflowNodeName string     `json:"workflow_node_name,omitempty"`
}

// ListJobsResponse matches the coordinator API's list response
type ListJobsResponse struct {
	Jobs   []JobResponse `json:"jobs"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

type WorkflowSummary struct {
	WorkflowID      string     `json:"workflow_id"`
	Kind            string     `json:"kind"`
	Name            string     `json:"name"`
	Status          string     `json:"status"`
	ProjectID       *string    `json:"project_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	QueueName       string     `json:"queue_name"`
	VCSRepo         string     `json:"vcs_repo,omitempty"`
	PRNumber        *int       `json:"pr_number,omitempty"`
	CommitSHA       string     `json:"commit_sha,omitempty"`
	JobCount        int        `json:"job_count"`
	RunningCount    int        `json:"running_count"`
	CompletedCount  int        `json:"completed_count"`
	FailedCount     int        `json:"failed_count"`
	SkippedCount    int        `json:"skipped_count"`
	LooseJobID      *string    `json:"loose_job_id,omitempty"`
	LooseJobExit    *int       `json:"loose_job_exit,omitempty"`
	DecisionSummary string     `json:"decision_summary,omitempty"`
}

type ListWorkflowsResponse struct {
	Workflows []WorkflowSummary `json:"workflows"`
	Total     int               `json:"total"`
	Limit     int               `json:"limit"`
	Offset    int               `json:"offset"`
}

type ProjectResponse struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	RepoURL   string `json:"repo_url"`
	Enabled   bool   `json:"enabled"`
}

type ListProjectsResponse struct {
	Projects []ProjectResponse `json:"projects"`
	Total    int               `json:"total"`
	Limit    int               `json:"limit"`
	Offset   int               `json:"offset"`
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
	values := url.Values{}
	values.Set("limit", fmt.Sprintf("%d", limit))
	values.Set("offset", fmt.Sprintf("%d", offset))
	if status != "" {
		values.Set("status", status)
	}
	path := "/api/v1/jobs?" + values.Encode()
	return c.ListJobsPath(path)
}

func (c *APIClient) ListJobsForWorkflow(workflowID string, limit, offset int) (*ListJobsResponse, error) {
	values := url.Values{}
	values.Set("limit", fmt.Sprintf("%d", limit))
	values.Set("offset", fmt.Sprintf("%d", offset))
	values.Set("workflow_id", workflowID)
	return c.ListJobsPath("/api/v1/jobs?" + values.Encode())
}

func (c *APIClient) ListJobsPath(path string) (*ListJobsResponse, error) {
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

func (c *APIClient) ListWorkflows(limit, offset int, status, projectID string) (*ListWorkflowsResponse, error) {
	values := url.Values{}
	values.Set("limit", fmt.Sprintf("%d", limit))
	values.Set("offset", fmt.Sprintf("%d", offset))
	if status != "" {
		values.Set("status", status)
	}
	if projectID != "" {
		values.Set("project_id", projectID)
	}
	body, statusCode, err := c.doRequest("GET", "/api/v1/workflows?"+values.Encode())
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", statusCode, string(body))
	}
	var result ListWorkflowsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return &result, nil
}

func (c *APIClient) GetWorkflow(workflowID string) (*WorkflowSummary, error) {
	body, statusCode, err := c.doRequest("GET", "/api/v1/workflows/"+workflowID)
	if err != nil {
		return nil, err
	}
	if statusCode == http.StatusNotFound {
		return nil, fmt.Errorf("workflow not found: %s", workflowID)
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", statusCode, string(body))
	}
	var result WorkflowSummary
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return &result, nil
}

func (c *APIClient) ListProjects(limit, offset int) (*ListProjectsResponse, error) {
	path := fmt.Sprintf("/api/v1/projects?limit=%d&offset=%d", limit, offset)
	body, statusCode, err := c.doRequest("GET", path)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", statusCode, string(body))
	}
	var result ListProjectsResponse
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
