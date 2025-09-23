package test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// JobResponse represents the expected job response structure
type JobResponse struct {
	JobID       string            `json:"job_id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Status      string            `json:"status"`
	GitURL      string            `json:"git_url,omitempty"`
	GitRef      string            `json:"git_ref,omitempty"`
	SourceType  string            `json:"source_type"`
	SourcePath  string            `json:"source_path,omitempty"`
	CodeDir     string            `json:"code_dir"`
	JobDir      string            `json:"job_dir"`
	JobCommand  string            `json:"job_command"`
	RunnerImage string            `json:"runner_image"`
	QueueName   string            `json:"queue_name"`
	JobEnvVars  map[string]string `json:"job_env_vars,omitempty"`
	JobEnvFile  string            `json:"job_env_file,omitempty"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

// CreateJobRequest represents the request payload for creating a job
type CreateJobRequest struct {
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	GitURL         string            `json:"git_url,omitempty"`
	GitRef         string            `json:"git_ref,omitempty"`
	SourceType     string            `json:"source_type"`
	SourcePath     string            `json:"source_path,omitempty"`
	CodeDir        string            `json:"code_dir,omitempty"`
	JobDir         string            `json:"job_dir,omitempty"`
	JobCommand     string            `json:"job_command"`
	RunnerImage    string            `json:"runner_image,omitempty"`
	JobEnvVars     map[string]string `json:"job_env_vars,omitempty"`
	JobEnvFile     string            `json:"job_env_file,omitempty"`
	TimeoutSeconds *int              `json:"timeout_seconds,omitempty"`
	Priority       *int              `json:"priority,omitempty"`
	QueueName      string            `json:"queue_name,omitempty"`
}

// ListJobsResponse represents the response for listing jobs
type ListJobsResponse struct {
	Jobs   []JobResponse `json:"jobs"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

// createAuthTokenHeader creates an Authorization header with a real API token
func createAuthTokenHeader(ctx context.Context, tx *gorm.DB, userID string) (string, error) {
	// Create a real token value
	tokenValue := "test-api-token-" + userID

	// Use the same hash function that the auth middleware uses
	tokenHash := sha256.Sum256([]byte(tokenValue))

	// Create the token in the database
	dataUtils := &DataUtils{db: tx}
	_, err := dataUtils.CreateAPIToken(DataSetup{
		"UserID":    userID,
		"Name":      "Test Token",
		"TokenHash": tokenHash[:], // Convert [32]byte to []byte
		"IsActive":  true,
	})
	if err != nil {
		return "", err
	}

	return "Bearer " + tokenValue, nil
}

// TestJobsAPI tests the jobs API endpoints with authentication
func TestJobsAPI(t *testing.T) {
	t.Run("POST /api/v1/jobs creates a job", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test user
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "testuser",
				"Email":    "test@example.com",
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createAuthTokenHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			jobRequest := CreateJobRequest{
				Name:        "Test Job",
				Description: "A test job for API testing",
				SourceType:  "git",
				GitURL:      "https://github.com/test/repo.git",
				GitRef:      "main",
				JobCommand:  "echo 'Hello World'",
			}

			jsonData, err := json.Marshal(jobRequest)
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "POST", "/api/v1/jobs", bytes.NewBuffer(jsonData))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusCreated, rr.Code)

			var response JobResponse
			err = json.Unmarshal(rr.Body.Bytes(), &response)
			require.NoError(t, err)

			assert.Equal(t, "Test Job", response.Name)
			assert.Equal(t, "A test job for API testing", response.Description)
			assert.Equal(t, "git", response.SourceType)
			assert.Equal(t, "https://github.com/test/repo.git", response.GitURL)
			assert.Equal(t, "main", response.GitRef)
			assert.Equal(t, "echo 'Hello World'", response.JobCommand)
			assert.Equal(t, "submitted", response.Status)
			assert.NotEmpty(t, response.JobID)
		})
	})

	t.Run("POST /api/v1/jobs without auth returns 401", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()

			jobRequest := CreateJobRequest{
				Name:       "Test Job",
				SourceType: "git",
				GitURL:     "https://github.com/test/repo.git",
				JobCommand: "echo test",
			}

			jsonData, err := json.Marshal(jobRequest)
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "POST", "/api/v1/jobs", bytes.NewBuffer(jsonData))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			// No Authorization header

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusUnauthorized, rr.Code)
		})
	})

	t.Run("POST /api/v1/jobs with invalid request returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test user
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "testuser",
				"Email":    "test@example.com",
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createAuthTokenHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			// Missing required fields
			jobRequest := CreateJobRequest{
				Name: "Test Job",
				// Missing SourceType and JobCommand
			}

			jsonData, err := json.Marshal(jobRequest)
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "POST", "/api/v1/jobs", bytes.NewBuffer(jsonData))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusBadRequest, rr.Code)
		})
	})

	t.Run("GET /api/v1/jobs lists jobs", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test user
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "testuser",
				"Email":    "test@example.com",
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createAuthTokenHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			// Create test jobs
			job1, err := dataUtils.CreateJob(DataSetup{
				"UserID":     user.UserID,
				"Name":       "Job 1",
				"SourceType": "git",
				"JobCommand": "echo job1",
			})
			require.NoError(t, err)

			job2, err := dataUtils.CreateJob(DataSetup{
				"UserID":     user.UserID,
				"Name":       "Job 2",
				"SourceType": "copy",
				"JobCommand": "echo job2",
			})
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)

			var response ListJobsResponse
			err = json.Unmarshal(rr.Body.Bytes(), &response)
			require.NoError(t, err)

			assert.Len(t, response.Jobs, 2)
			assert.Equal(t, 2, response.Total)

			// Find our jobs in the response
			jobIDs := []string{job1.JobID, job2.JobID}
			responseJobIDs := []string{response.Jobs[0].JobID, response.Jobs[1].JobID}
			assert.ElementsMatch(t, jobIDs, responseJobIDs)
		})
	})

	t.Run("GET /api/v1/jobs/{job_id} returns specific job", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test user
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "testuser",
				"Email":    "test@example.com",
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createAuthTokenHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			// Create a test job
			job, err := dataUtils.CreateJob(DataSetup{
				"UserID":     user.UserID,
				"Name":       "Specific Job",
				"SourceType": "git",
				"JobCommand": "echo specific",
			})
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID, nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)

			var response JobResponse
			err = json.Unmarshal(rr.Body.Bytes(), &response)
			require.NoError(t, err)

			assert.Equal(t, job.JobID, response.JobID)
			assert.Equal(t, "Specific Job", response.Name)
		})
	})

	t.Run("GET /api/v1/jobs/{job_id} with non-existent ID returns 404", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test user
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "testuser",
				"Email":    "test@example.com",
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createAuthTokenHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/nonexistent-id", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusNotFound, rr.Code)
		})
	})

	t.Run("PUT /api/v1/jobs/{job_id}/cancel cancels a job", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test user
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "testuser",
				"Email":    "test@example.com",
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createAuthTokenHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			// Create a job that can be cancelled
			job, err := dataUtils.CreateJob(DataSetup{
				"UserID":     user.UserID,
				"Name":       "Cancellable Job",
				"Status":     "queued", // Can be cancelled
				"SourceType": "git",
				"JobCommand": "echo cancel-me",
			})
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "PUT", "/api/v1/jobs/"+job.JobID+"/cancel", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)

			var response JobResponse
			err = json.Unmarshal(rr.Body.Bytes(), &response)
			require.NoError(t, err)

			assert.Equal(t, "cancelled", response.Status)
		})
	})

	t.Run("DELETE /api/v1/jobs/{job_id} deletes a job", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test user
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "testuser",
				"Email":    "test@example.com",
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createAuthTokenHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			// Create a job to delete
			job, err := dataUtils.CreateJob(DataSetup{
				"UserID":     user.UserID,
				"Name":       "Job to Delete",
				"SourceType": "git",
				"JobCommand": "echo delete-me",
			})
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "DELETE", "/api/v1/jobs/"+job.JobID, nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusNoContent, rr.Code)

			// Verify job is deleted by trying to get it
			getReq, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID, nil)
			require.NoError(t, err)
			getReq.Header.Set("Authorization", authHeader)

			getRR := httptest.NewRecorder()
			mux.ServeHTTP(getRR, getReq)

			assert.Equal(t, http.StatusNotFound, getRR.Code)
		})
	})
}

// TestJobsAPIAuthorizationAndOwnership tests user isolation and authorization
func TestJobsAPIAuthorizationAndOwnership(t *testing.T) {
	t.Run("users can only see their own jobs", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create two different users
			user1, err := dataUtils.CreateUser(DataSetup{
				"Username": "user1",
				"Email":    "user1@example.com",
			})
			require.NoError(t, err)

			user2, err := dataUtils.CreateUser(DataSetup{
				"Username": "user2",
				"Email":    "user2@example.com",
			})
			require.NoError(t, err)

			// Create auth headers for both users
			authHeader1, err := createAuthTokenHeader(ctx, tx, user1.UserID)
			require.NoError(t, err)

			authHeader2, err := createAuthTokenHeader(ctx, tx, user2.UserID)
			require.NoError(t, err)

			// Create a job for user1
			job1, err := dataUtils.CreateJob(DataSetup{
				"UserID":     user1.UserID,
				"Name":       "User1 Job",
				"SourceType": "git",
				"JobCommand": "echo user1",
			})
			require.NoError(t, err)

			// User1 should see their job
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader1)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)

			var response1 ListJobsResponse
			err = json.Unmarshal(rr.Body.Bytes(), &response1)
			require.NoError(t, err)
			assert.Len(t, response1.Jobs, 1)
			assert.Equal(t, job1.JobID, response1.Jobs[0].JobID)

			// User2 should see no jobs
			req2, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs", nil)
			require.NoError(t, err)
			req2.Header.Set("Authorization", authHeader2)

			rr2 := httptest.NewRecorder()
			mux.ServeHTTP(rr2, req2)

			assert.Equal(t, http.StatusOK, rr.Code)

			var response2 ListJobsResponse
			err = json.Unmarshal(rr2.Body.Bytes(), &response2)
			require.NoError(t, err)
			assert.Len(t, response2.Jobs, 0)
		})
	})

	t.Run("users cannot access other users' jobs", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create two different users
			user1, err := dataUtils.CreateUser(DataSetup{
				"Username": "user1",
				"Email":    "user1@example.com",
			})
			require.NoError(t, err)

			user2, err := dataUtils.CreateUser(DataSetup{
				"Username": "user2",
				"Email":    "user2@example.com",
			})
			require.NoError(t, err)

			// Create auth header for user2
			authHeader2, err := createAuthTokenHeader(ctx, tx, user2.UserID)
			require.NoError(t, err)

			// Create a job for user1
			job1, err := dataUtils.CreateJob(DataSetup{
				"UserID":     user1.UserID,
				"Name":       "User1 Job",
				"SourceType": "git",
				"JobCommand": "echo user1",
			})
			require.NoError(t, err)

			// User2 tries to access User1's job
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job1.JobID, nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader2)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusForbidden, rr.Code)
		})
	})

	t.Run("users cannot delete other users' jobs", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create two different users
			user1, err := dataUtils.CreateUser(DataSetup{
				"Username": "user1",
				"Email":    "user1@example.com",
			})
			require.NoError(t, err)

			user2, err := dataUtils.CreateUser(DataSetup{
				"Username": "user2",
				"Email":    "user2@example.com",
			})
			require.NoError(t, err)

			// Create auth header for user2
			authHeader2, err := createAuthTokenHeader(ctx, tx, user2.UserID)
			require.NoError(t, err)

			// Create a job for user1
			job1, err := dataUtils.CreateJob(DataSetup{
				"UserID":     user1.UserID,
				"Name":       "User1 Job",
				"SourceType": "git",
				"JobCommand": "echo user1",
			})
			require.NoError(t, err)

			// User2 tries to delete User1's job
			req, err := http.NewRequestWithContext(ctx, "DELETE", "/api/v1/jobs/"+job1.JobID, nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader2)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusForbidden, rr.Code)
		})
	})

	t.Run("users cannot cancel other users' jobs", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create two different users
			user1, err := dataUtils.CreateUser(DataSetup{
				"Username": "user1",
				"Email":    "user1@example.com",
			})
			require.NoError(t, err)

			user2, err := dataUtils.CreateUser(DataSetup{
				"Username": "user2",
				"Email":    "user2@example.com",
			})
			require.NoError(t, err)

			// Create auth header for user2
			authHeader2, err := createAuthTokenHeader(ctx, tx, user2.UserID)
			require.NoError(t, err)

			// Create a job for user1
			job1, err := dataUtils.CreateJob(DataSetup{
				"UserID":     user1.UserID,
				"Name":       "User1 Job",
				"SourceType": "git",
				"JobCommand": "echo user1",
			})
			require.NoError(t, err)

			// User2 tries to cancel User1's job
			req, err := http.NewRequestWithContext(ctx, "PUT", "/api/v1/jobs/"+job1.JobID+"/cancel", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader2)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusForbidden, rr.Code)
		})
	})
}

// TestJobsAPIValidation tests input validation and edge cases
func TestJobsAPIValidation(t *testing.T) {
	t.Run("invalid JSON returns 400", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test user
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "testuser",
				"Email":    "test@example.com",
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createAuthTokenHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			req, err := http.NewRequestWithContext(ctx, "POST", "/api/v1/jobs", strings.NewReader("{invalid json"))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusBadRequest, rr.Code)
		})
	})

	t.Run("unsupported HTTP methods return 405", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test user
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "testuser",
				"Email":    "test@example.com",
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createAuthTokenHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			methods := []string{"PATCH", "OPTIONS"}
			for _, method := range methods {
				req, err := http.NewRequestWithContext(ctx, method, "/api/v1/jobs", nil)
				require.NoError(t, err)
				req.Header.Set("Authorization", authHeader)

				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, req)

				assert.Equal(t, http.StatusMethodNotAllowed, rr.Code,
					"Method %s should return 405", method)
			}
		})
	})

	t.Run("invalid job ID format in URL", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			mux := GetTestMux()
			dataUtils := &DataUtils{db: tx}

			// Create a test user
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "testuser",
				"Email":    "test@example.com",
			})
			require.NoError(t, err)

			// Create auth header
			authHeader, err := createAuthTokenHeader(ctx, tx, user.UserID)
			require.NoError(t, err)

			// Test with empty job ID (should return 400 due to path validation)
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusBadRequest, rr.Code)
		})
	})
}
