package test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// LogEntry matches the structure in job_handler.go
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Stream    string `json:"stream"`
	Level     string `json:"level,omitempty"`
	Message   string `json:"message"`
}

// TestJobLogsAPI tests the job logs API endpoint
func TestJobLogsAPI(t *testing.T) {
	t.Run("GET /api/v1/jobs/{job_id}/logs returns stdout logs", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			// Reset app mux first, then set up a memory object store for this test
			handlers.ResetAppMux()
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			mux := handlers.GetAppMux()

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
				"Name":       "Test Job with Logs",
				"SourceType": "git",
				"JobCommand": "echo hello",
				"Status":     "completed",
			})
			require.NoError(t, err)

			// Store logs in the memory object store as JSON array
			logEntries := []LogEntry{
				{Timestamp: "2024-01-01T00:00:00Z", Stream: "stdout", Message: "Hello from stdout!"},
				{Timestamp: "2024-01-01T00:00:01Z", Stream: "stdout", Message: "Line 2"},
				{Timestamp: "2024-01-01T00:00:02Z", Stream: "stdout", Message: "Line 3"},
			}
			stdoutContent, err := json.Marshal(logEntries)
			require.NoError(t, err)

			stdoutKey := "logs/" + job.JobID + "/stdout.json"
			err = memStore.Put(ctx, stdoutKey, bytes.NewReader(stdoutContent), "application/json")
			require.NoError(t, err)

			// Request logs with stream=stdout
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs?stream=stdout", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

			// Parse response and verify content
			var responseEntries []LogEntry
			err = json.Unmarshal(rr.Body.Bytes(), &responseEntries)
			require.NoError(t, err)
			assert.Len(t, responseEntries, 3)
			assert.Equal(t, "Hello from stdout!", responseEntries[0].Message)
		})
	})

	t.Run("GET /api/v1/jobs/{job_id}/logs returns stderr logs", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			// Reset app mux first, then set up a memory object store for this test
			handlers.ResetAppMux()
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			mux := handlers.GetAppMux()

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
				"Name":       "Test Job with Stderr",
				"SourceType": "git",
				"JobCommand": "echo error >&2",
				"Status":     "failed",
			})
			require.NoError(t, err)

			// Store stderr logs in the memory object store as JSON array
			logEntries := []LogEntry{
				{Timestamp: "2024-01-01T00:00:00Z", Stream: "stderr", Level: "error", Message: "Error: Something went wrong"},
				{Timestamp: "2024-01-01T00:00:01Z", Stream: "stderr", Level: "error", Message: "Stack trace here"},
			}
			stderrContent, err := json.Marshal(logEntries)
			require.NoError(t, err)

			stderrKey := "logs/" + job.JobID + "/stderr.json"
			err = memStore.Put(ctx, stderrKey, bytes.NewReader(stderrContent), "application/json")
			require.NoError(t, err)

			// Request logs with stream=stderr
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs?stream=stderr", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

			// Parse response and verify content
			var responseEntries []LogEntry
			err = json.Unmarshal(rr.Body.Bytes(), &responseEntries)
			require.NoError(t, err)
			assert.Len(t, responseEntries, 2)
			assert.Equal(t, "Error: Something went wrong", responseEntries[0].Message)
		})
	})

	t.Run("GET /api/v1/jobs/{job_id}/logs returns combined logs by default", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			// Reset app mux first, then set up a memory object store for this test
			handlers.ResetAppMux()
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			mux := handlers.GetAppMux()

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
				"Name":       "Test Job Combined",
				"SourceType": "git",
				"JobCommand": "echo test",
				"Status":     "completed",
			})
			require.NoError(t, err)

			// Store both stdout and stderr logs as JSON arrays
			stdoutEntries := []LogEntry{
				{Timestamp: "2024-01-01T00:00:00Z", Stream: "stdout", Message: "Output line 1"},
				{Timestamp: "2024-01-01T00:00:02Z", Stream: "stdout", Message: "Output line 2"},
			}
			stderrEntries := []LogEntry{
				{Timestamp: "2024-01-01T00:00:01Z", Stream: "stderr", Level: "warn", Message: "Warning: something happened"},
			}

			stdoutContent, err := json.Marshal(stdoutEntries)
			require.NoError(t, err)
			stderrContent, err := json.Marshal(stderrEntries)
			require.NoError(t, err)

			stdoutKey := "logs/" + job.JobID + "/stdout.json"
			stderrKey := "logs/" + job.JobID + "/stderr.json"

			err = memStore.Put(ctx, stdoutKey, bytes.NewReader(stdoutContent), "application/json")
			require.NoError(t, err)
			err = memStore.Put(ctx, stderrKey, bytes.NewReader(stderrContent), "application/json")
			require.NoError(t, err)

			// Request logs without stream parameter (should return combined)
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

			// Parse response and verify combined content sorted by timestamp
			var responseEntries []LogEntry
			err = json.Unmarshal(rr.Body.Bytes(), &responseEntries)
			require.NoError(t, err)
			assert.Len(t, responseEntries, 3)
			// Should be sorted by timestamp
			assert.Equal(t, "Output line 1", responseEntries[0].Message)
			assert.Equal(t, "Warning: something happened", responseEntries[1].Message)
			assert.Equal(t, "Output line 2", responseEntries[2].Message)
		})
	})

	t.Run("GET /api/v1/jobs/{job_id}/logs returns 404 when no logs exist", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			// Reset app mux first, then set up a memory object store for this test
			handlers.ResetAppMux()
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			mux := handlers.GetAppMux()

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

			// Create a test job (no logs stored)
			job, err := dataUtils.CreateJob(DataSetup{
				"UserID":     user.UserID,
				"Name":       "Job Without Logs",
				"SourceType": "git",
				"JobCommand": "echo test",
				"Status":     "submitted",
			})
			require.NoError(t, err)

			// Request logs
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusNotFound, rr.Code)
		})
	})

	t.Run("GET /api/v1/jobs/{job_id}/logs returns 404 for non-existent job", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			// Reset app mux first, then set up a memory object store for this test
			handlers.ResetAppMux()
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			mux := handlers.GetAppMux()

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

			// Request logs for non-existent job
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/nonexistent-job-id/logs", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusNotFound, rr.Code)
		})
	})

	t.Run("GET /api/v1/jobs/{job_id}/logs returns 401 without auth", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			// Reset app mux first, then set up a memory object store for this test
			handlers.ResetAppMux()
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			mux := handlers.GetAppMux()

			dataUtils := &DataUtils{db: tx}

			// Create a test user and job
			user, err := dataUtils.CreateUser(DataSetup{
				"Username": "testuser",
				"Email":    "test@example.com",
			})
			require.NoError(t, err)

			job, err := dataUtils.CreateJob(DataSetup{
				"UserID":     user.UserID,
				"Name":       "Test Job",
				"SourceType": "git",
				"JobCommand": "echo test",
			})
			require.NoError(t, err)

			// Request logs without auth header
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs", nil)
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusUnauthorized, rr.Code)
		})
	})

	t.Run("GET /api/v1/jobs/{job_id}/logs returns 403 for other user's job", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			// Reset app mux first, then set up a memory object store for this test
			handlers.ResetAppMux()
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			mux := handlers.GetAppMux()

			dataUtils := &DataUtils{db: tx}

			// Create two users
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
			job, err := dataUtils.CreateJob(DataSetup{
				"UserID":     user1.UserID,
				"Name":       "User1's Job",
				"SourceType": "git",
				"JobCommand": "echo test",
			})
			require.NoError(t, err)

			// Store logs for user1's job
			logEntries := []LogEntry{
				{Timestamp: "2024-01-01T00:00:00Z", Stream: "stdout", Message: "User1's logs"},
			}
			stdoutContent, err := json.Marshal(logEntries)
			require.NoError(t, err)

			stdoutKey := "logs/" + job.JobID + "/stdout.json"
			err = memStore.Put(ctx, stdoutKey, bytes.NewReader(stdoutContent), "application/json")
			require.NoError(t, err)

			// User2 tries to access User1's job logs
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader2)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusForbidden, rr.Code)
		})
	})

	t.Run("GET /api/v1/jobs/{job_id}/logs returns 400 for invalid stream parameter", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			// Reset app mux first, then set up a memory object store for this test
			handlers.ResetAppMux()
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			mux := handlers.GetAppMux()

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
				"Name":       "Test Job",
				"SourceType": "git",
				"JobCommand": "echo test",
			})
			require.NoError(t, err)

			// Request logs with invalid stream parameter
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs?stream=invalid", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusBadRequest, rr.Code)
		})
	})

	t.Run("GET /api/v1/jobs/{job_id}/logs returns only stdout when stderr is missing", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			// Reset app mux first, then set up a memory object store for this test
			handlers.ResetAppMux()
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			mux := handlers.GetAppMux()

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
				"Name":       "Test Job Stdout Only",
				"SourceType": "git",
				"JobCommand": "echo test",
				"Status":     "completed",
			})
			require.NoError(t, err)

			// Only store stdout logs (no stderr) as JSON array
			logEntries := []LogEntry{
				{Timestamp: "2024-01-01T00:00:00Z", Stream: "stdout", Message: "Output without errors"},
			}
			stdoutContent, err := json.Marshal(logEntries)
			require.NoError(t, err)

			stdoutKey := "logs/" + job.JobID + "/stdout.json"
			err = memStore.Put(ctx, stdoutKey, bytes.NewReader(stdoutContent), "application/json")
			require.NoError(t, err)

			// Request combined logs (default)
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)

			// Parse response and verify content
			var responseEntries []LogEntry
			err = json.Unmarshal(rr.Body.Bytes(), &responseEntries)
			require.NoError(t, err)
			assert.Len(t, responseEntries, 1)
			assert.Equal(t, "Output without errors", responseEntries[0].Message)
		})
	})
}

// TestJobLogsAdminAccess tests that admins can access any user's logs
func TestJobLogsAdminAccess(t *testing.T) {
	t.Run("admin can access other user's job logs", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			// Reset app mux first, then set up a memory object store for this test
			handlers.ResetAppMux()
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			mux := handlers.GetAppMux()

			dataUtils := &DataUtils{db: tx}

			// Create a regular user
			regularUser, err := dataUtils.CreateUser(DataSetup{
				"Username": "regularuser",
				"Email":    "regular@example.com",
			})
			require.NoError(t, err)

			// Create an admin user
			adminUser, err := dataUtils.CreateUser(DataSetup{
				"Username": "adminuser",
				"Email":    "admin@example.com",
				"Roles":    []string{"admin"},
			})
			require.NoError(t, err)

			// Create auth header for admin
			tokenValue := "test-api-token-" + adminUser.UserID
			tokenHash := sha256.Sum256([]byte(tokenValue))
			_, err = dataUtils.CreateAPIToken(DataSetup{
				"UserID":    adminUser.UserID,
				"Name":      "Admin Token",
				"TokenHash": tokenHash[:],
				"IsActive":  true,
			})
			require.NoError(t, err)
			adminAuthHeader := "Bearer " + tokenValue

			// Create a job for regular user
			job, err := dataUtils.CreateJob(DataSetup{
				"UserID":     regularUser.UserID,
				"Name":       "Regular User's Job",
				"SourceType": "git",
				"JobCommand": "echo test",
				"Status":     "completed",
			})
			require.NoError(t, err)

			// Store logs for regular user's job as JSON array
			logEntries := []LogEntry{
				{Timestamp: "2024-01-01T00:00:00Z", Stream: "stdout", Message: "Regular user's logs"},
			}
			stdoutContent, err := json.Marshal(logEntries)
			require.NoError(t, err)

			stdoutKey := "logs/" + job.JobID + "/stdout.json"
			err = memStore.Put(ctx, stdoutKey, bytes.NewReader(stdoutContent), "application/json")
			require.NoError(t, err)

			// Admin requests logs for regular user's job
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", adminAuthHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)

			// Parse response and verify content
			var responseEntries []LogEntry
			err = json.Unmarshal(rr.Body.Bytes(), &responseEntries)
			require.NoError(t, err)
			assert.Len(t, responseEntries, 1)
			assert.Equal(t, "Regular user's logs", responseEntries[0].Message)
		})
	})
}
