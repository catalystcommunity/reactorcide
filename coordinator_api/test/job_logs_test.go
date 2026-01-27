package test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestJobLogsAPI tests the job logs API endpoint
func TestJobLogsAPI(t *testing.T) {
	t.Run("GET /api/v1/jobs/{job_id}/logs returns stdout logs", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			// Reset app mux to pick up the new object store
			handlers.ResetAppMux()
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

			// Store logs in the memory object store
			stdoutContent := "Hello from stdout!\nLine 2\nLine 3"
			stdoutKey := "logs/" + job.JobID + "/stdout.log"
			err = memStore.Put(ctx, stdoutKey, bytes.NewReader([]byte(stdoutContent)), "text/plain")
			require.NoError(t, err)

			// Request logs with stream=stdout
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs?stream=stdout", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, "text/plain; charset=utf-8", rr.Header().Get("Content-Type"))
			assert.Equal(t, stdoutContent, rr.Body.String())
		})
	})

	t.Run("GET /api/v1/jobs/{job_id}/logs returns stderr logs", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			// Reset app mux to pick up the new object store
			handlers.ResetAppMux()
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

			// Store stderr logs in the memory object store
			stderrContent := "Error: Something went wrong\nStack trace here"
			stderrKey := "logs/" + job.JobID + "/stderr.log"
			err = memStore.Put(ctx, stderrKey, bytes.NewReader([]byte(stderrContent)), "text/plain")
			require.NoError(t, err)

			// Request logs with stream=stderr
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs?stream=stderr", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, "text/plain; charset=utf-8", rr.Header().Get("Content-Type"))
			assert.Equal(t, stderrContent, rr.Body.String())
		})
	})

	t.Run("GET /api/v1/jobs/{job_id}/logs returns combined logs by default", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			// Reset app mux to pick up the new object store
			handlers.ResetAppMux()
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

			// Store both stdout and stderr logs
			stdoutContent := "Output line 1\nOutput line 2"
			stderrContent := "Warning: something happened"
			stdoutKey := "logs/" + job.JobID + "/stdout.log"
			stderrKey := "logs/" + job.JobID + "/stderr.log"

			err = memStore.Put(ctx, stdoutKey, bytes.NewReader([]byte(stdoutContent)), "text/plain")
			require.NoError(t, err)
			err = memStore.Put(ctx, stderrKey, bytes.NewReader([]byte(stderrContent)), "text/plain")
			require.NoError(t, err)

			// Request logs without stream parameter (should return combined)
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, "text/plain; charset=utf-8", rr.Header().Get("Content-Type"))

			// Combined output should contain both with separator
			responseBody := rr.Body.String()
			assert.Contains(t, responseBody, "Output line 1")
			assert.Contains(t, responseBody, "Output line 2")
			assert.Contains(t, responseBody, "--- stderr ---")
			assert.Contains(t, responseBody, "Warning: something happened")
		})
	})

	t.Run("GET /api/v1/jobs/{job_id}/logs returns 404 when no logs exist", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			// Reset app mux to pick up the new object store
			handlers.ResetAppMux()
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
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			// Reset app mux to pick up the new object store
			handlers.ResetAppMux()
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
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			// Reset app mux to pick up the new object store
			handlers.ResetAppMux()
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
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			// Reset app mux to pick up the new object store
			handlers.ResetAppMux()
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
			stdoutContent := "User1's logs"
			stdoutKey := "logs/" + job.JobID + "/stdout.log"
			err = memStore.Put(ctx, stdoutKey, bytes.NewReader([]byte(stdoutContent)), "text/plain")
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
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			// Reset app mux to pick up the new object store
			handlers.ResetAppMux()
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
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			// Reset app mux to pick up the new object store
			handlers.ResetAppMux()
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

			// Only store stdout logs (no stderr)
			stdoutContent := "Output without errors"
			stdoutKey := "logs/" + job.JobID + "/stdout.log"
			err = memStore.Put(ctx, stdoutKey, bytes.NewReader([]byte(stdoutContent)), "text/plain")
			require.NoError(t, err)

			// Request combined logs (default)
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", authHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, stdoutContent, rr.Body.String())
			assert.NotContains(t, rr.Body.String(), "--- stderr ---")
		})
	})
}

// TestJobLogsAdminAccess tests that admins can access any user's logs
func TestJobLogsAdminAccess(t *testing.T) {
	t.Run("admin can access other user's job logs", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			// Create and set up a memory object store for this test
			memStore := objects.NewMemoryObjectStore()
			handlers.SetObjectStore(memStore)
			defer handlers.SetObjectStore(nil)

			// Reset app mux to pick up the new object store
			handlers.ResetAppMux()
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

			// Store logs for regular user's job
			stdoutContent := "Regular user's logs"
			stdoutKey := "logs/" + job.JobID + "/stdout.log"
			err = memStore.Put(ctx, stdoutKey, bytes.NewReader([]byte(stdoutContent)), "text/plain")
			require.NoError(t, err)

			// Admin requests logs for regular user's job
			req, err := http.NewRequestWithContext(ctx, "GET", "/api/v1/jobs/"+job.JobID+"/logs", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", adminAuthHeader)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, stdoutContent, rr.Body.String())
		})
	})
}
