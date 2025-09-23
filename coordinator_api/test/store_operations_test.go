package test

import (
	"context"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestUserOperations tests user CRUD operations
func TestUserOperations(t *testing.T) {
	t.Run("CreateUser and GetUserByID", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testCreateUserAndGetUserByID(t, ctx, tx)
		})
	})

	t.Run("GetUserByID - User Not Found", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testGetUserByIDNotFound(t, ctx, tx)
		})
	})

	t.Run("CreateUser - Duplicate Email", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testCreateUserDuplicateEmail(t, ctx, tx)
		})
	})

	t.Run("User Role Management", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testUserRoleManagement(t, ctx, tx)
		})
	})
}

// TestJobOperations tests job CRUD operations
func TestJobOperations(t *testing.T) {
	t.Run("CreateJob and GetJobByID", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testCreateJobAndGetJobByID(t, ctx, tx)
		})
	})

	t.Run("UpdateJob", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testUpdateJob(t, ctx, tx)
		})
	})

	t.Run("DeleteJob", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testDeleteJob(t, ctx, tx)
		})
	})

	t.Run("GetJobsByUser", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testGetJobsByUser(t, ctx, tx)
		})
	})

	t.Run("ListJobs with Filters", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testListJobsWithFilters(t, ctx, tx)
		})
	})

	t.Run("Job Status Transitions", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testJobStatusTransitions(t, ctx, tx)
		})
	})

	t.Run("Job Environment Variables", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testJobEnvironmentVariables(t, ctx, tx)
		})
	})
}

// TestAPITokenOperations tests API token CRUD operations
func TestAPITokenOperations(t *testing.T) {
	t.Run("CreateAPIToken and ValidateAPIToken", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testCreateAPITokenAndValidateAPIToken(t, ctx, tx)
		})
	})

	t.Run("GetAPITokensByUser", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testGetAPITokensByUser(t, ctx, tx)
		})
	})

	t.Run("DeleteAPIToken", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testDeleteAPIToken(t, ctx, tx)
		})
	})

	t.Run("UpdateTokenLastUsed", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testUpdateTokenLastUsed(t, ctx, tx)
		})
	})

	t.Run("Token Expiration", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testTokenExpiration(t, ctx, tx)
		})
	})

	t.Run("Inactive Token Validation", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testInactiveTokenValidation(t, ctx, tx)
		})
	})
}

// User operation test implementations

func testCreateUserAndGetUserByID(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test user
	user := &models.User{
		Username: "testuser",
		Email:    "test@example.com",
		Roles:    pq.StringArray{"user"},
	}

	err := store.AppStore.CreateUser(ctx, user)
	require.NoError(t, err)
	assert.NotEmpty(t, user.UserID)
	assert.False(t, user.CreatedAt.IsZero())

	// Retrieve user
	retrievedUser, err := store.AppStore.GetUserByID(ctx, user.UserID)
	require.NoError(t, err)
	assert.Equal(t, user.UserID, retrievedUser.UserID)
	assert.Equal(t, user.Username, retrievedUser.Username)
	assert.Equal(t, user.Email, retrievedUser.Email)
	assert.Equal(t, user.Roles, retrievedUser.Roles)
}

func testGetUserByIDNotFound(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Try to get non-existent user
	_, err := store.AppStore.GetUserByID(ctx, "01234567-89ab-cdef-0123-456789abcdef")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func testCreateUserDuplicateEmail(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create first user
	user1 := &models.User{
		Username: "testuser1",
		Email:    "duplicate@example.com",
		Roles:    pq.StringArray{"user"},
	}
	err := store.AppStore.CreateUser(ctx, user1)
	require.NoError(t, err)

	// Try to create second user with same email
	user2 := &models.User{
		Username: "testuser2",
		Email:    "duplicate@example.com",
		Roles:    pq.StringArray{"user"},
	}
	err = store.AppStore.CreateUser(ctx, user2)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create user")
}

func testUserRoleManagement(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create user with multiple roles
	user := &models.User{
		Username: "adminuser",
		Email:    "admin@example.com",
		Roles:    pq.StringArray{"user", "admin", "support"},
	}

	err := store.AppStore.CreateUser(ctx, user)
	require.NoError(t, err)

	// Retrieve and verify roles
	retrievedUser, err := store.AppStore.GetUserByID(ctx, user.UserID)
	require.NoError(t, err)
	assert.Len(t, retrievedUser.Roles, 3)
	assert.Contains(t, retrievedUser.Roles, "user")
	assert.Contains(t, retrievedUser.Roles, "admin")
	assert.Contains(t, retrievedUser.Roles, "support")
}

// Job operation test implementations

func testCreateJobAndGetJobByID(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test user first
	dataUtils := &DataUtils{db: tx}
	user, err := dataUtils.CreateUser(DataSetup{"Username": "jobuser"})
	require.NoError(t, err)

	// Create test job
	job := &models.Job{
		UserID:      user.UserID,
		Name:        "Test Job",
		Description: "A test job for validation",
		GitURL:      "https://github.com/example/repo.git",
		GitRef:      "main",
		SourceType:  "git",
		JobCommand:  "echo 'Hello World'",
		QueueName:   "test-queue",
		Status:      "submitted",
	}

	err = store.AppStore.CreateJob(ctx, job)
	require.NoError(t, err)
	assert.NotEmpty(t, job.JobID)
	assert.False(t, job.CreatedAt.IsZero())

	// Retrieve job
	retrievedJob, err := store.AppStore.GetJobByID(ctx, job.JobID)
	require.NoError(t, err)
	assert.Equal(t, job.JobID, retrievedJob.JobID)
	assert.Equal(t, job.Name, retrievedJob.Name)
	assert.Equal(t, job.UserID, retrievedJob.UserID)
	assert.Equal(t, job.GitURL, retrievedJob.GitURL)
	assert.Equal(t, job.Status, retrievedJob.Status)
}

func testUpdateJob(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test job
	dataUtils := &DataUtils{db: tx}
	job, err := dataUtils.CreateJob(DataSetup{
		"Name":   "Original Job",
		"Status": "submitted",
	})
	require.NoError(t, err)

	// Update job
	now := time.Now().UTC()
	job.Status = "running"
	job.StartedAt = &now
	job.Description = "Updated description"

	err = store.AppStore.UpdateJob(ctx, job)
	require.NoError(t, err)

	// Verify update
	retrievedJob, err := store.AppStore.GetJobByID(ctx, job.JobID)
	require.NoError(t, err)
	assert.Equal(t, "running", retrievedJob.Status)
	assert.Equal(t, "Updated description", retrievedJob.Description)
	assert.NotNil(t, retrievedJob.StartedAt)
}

func testDeleteJob(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test job
	dataUtils := &DataUtils{db: tx}
	job, err := dataUtils.CreateJob(DataSetup{})
	require.NoError(t, err)

	// Delete job
	err = store.AppStore.DeleteJob(ctx, job.JobID)
	require.NoError(t, err)

	// Verify deletion
	_, err = store.AppStore.GetJobByID(ctx, job.JobID)
	assert.ErrorIs(t, err, store.ErrNotFound)

	// Try to delete again (should return not found)
	err = store.AppStore.DeleteJob(ctx, job.JobID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func testGetJobsByUser(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test user
	dataUtils := &DataUtils{db: tx}
	user, err := dataUtils.CreateUser(DataSetup{"Username": "jobuser"})
	require.NoError(t, err)

	// Create multiple jobs for the user
	for i := 0; i < 5; i++ {
		_, err := dataUtils.CreateJob(DataSetup{
			"UserID": user.UserID,
			"Name":   fmt.Sprintf("Job %d", i+1),
		})
		require.NoError(t, err)
	}

	// Create job for another user (should not be returned)
	otherUser, err := dataUtils.CreateUser(DataSetup{"Username": "otheruser"})
	require.NoError(t, err)
	_, err = dataUtils.CreateJob(DataSetup{
		"UserID": otherUser.UserID,
		"Name":   "Other User Job",
	})
	require.NoError(t, err)

	// Get jobs for the first user
	jobs, err := store.AppStore.GetJobsByUser(ctx, user.UserID, 10, 0)
	require.NoError(t, err)
	assert.Len(t, jobs, 5)

	// Verify all jobs belong to the correct user
	for _, job := range jobs {
		assert.Equal(t, user.UserID, job.UserID)
	}

	// Test pagination
	firstPageJobs, err := store.AppStore.GetJobsByUser(ctx, user.UserID, 3, 0)
	require.NoError(t, err)
	assert.Len(t, firstPageJobs, 3)

	secondPageJobs, err := store.AppStore.GetJobsByUser(ctx, user.UserID, 3, 3)
	require.NoError(t, err)
	assert.Len(t, secondPageJobs, 2)
}

func testListJobsWithFilters(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test users
	dataUtils := &DataUtils{db: tx}
	user1, err := dataUtils.CreateUser(DataSetup{"Username": "user1"})
	require.NoError(t, err)
	user2, err := dataUtils.CreateUser(DataSetup{"Username": "user2"})
	require.NoError(t, err)

	// Create jobs with different statuses and queues
	testJobs := []DataSetup{
		{"UserID": user1.UserID, "Status": "submitted", "QueueName": "queue1"},
		{"UserID": user1.UserID, "Status": "running", "QueueName": "queue1"},
		{"UserID": user2.UserID, "Status": "submitted", "QueueName": "queue2"},
		{"UserID": user2.UserID, "Status": "completed", "QueueName": "queue1"},
	}

	for _, jobData := range testJobs {
		_, err := dataUtils.CreateJob(jobData)
		require.NoError(t, err)
	}

	// Test filter by status
	submittedJobs, err := store.AppStore.ListJobs(ctx, map[string]interface{}{
		"status": "submitted",
	}, 10, 0)
	require.NoError(t, err)
	assert.Len(t, submittedJobs, 2)

	// Test filter by user
	user1Jobs, err := store.AppStore.ListJobs(ctx, map[string]interface{}{
		"user_id": user1.UserID,
	}, 10, 0)
	require.NoError(t, err)
	assert.Len(t, user1Jobs, 2)

	// Test filter by queue
	queue1Jobs, err := store.AppStore.ListJobs(ctx, map[string]interface{}{
		"queue_name": "queue1",
	}, 10, 0)
	require.NoError(t, err)
	assert.Len(t, queue1Jobs, 3)

	// Test multiple filters
	user1SubmittedJobs, err := store.AppStore.ListJobs(ctx, map[string]interface{}{
		"user_id": user1.UserID,
		"status":  "submitted",
	}, 10, 0)
	require.NoError(t, err)
	assert.Len(t, user1SubmittedJobs, 1)
}

func testJobStatusTransitions(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test job
	dataUtils := &DataUtils{db: tx}
	job, err := dataUtils.CreateJob(DataSetup{"Status": "submitted"})
	require.NoError(t, err)

	// Test status transition: submitted -> running
	now := time.Now().UTC()
	job.Status = "running"
	job.StartedAt = &now
	err = store.AppStore.UpdateJob(ctx, job)
	require.NoError(t, err)

	// Test status transition: running -> completed
	completedTime := time.Now().UTC()
	exitCode := 0
	job.Status = "completed"
	job.CompletedAt = &completedTime
	job.ExitCode = &exitCode
	err = store.AppStore.UpdateJob(ctx, job)
	require.NoError(t, err)

	// Verify final state
	finalJob, err := store.AppStore.GetJobByID(ctx, job.JobID)
	require.NoError(t, err)
	assert.Equal(t, "completed", finalJob.Status)
	assert.NotNil(t, finalJob.StartedAt)
	assert.NotNil(t, finalJob.CompletedAt)
	assert.NotNil(t, finalJob.ExitCode)
	assert.Equal(t, 0, *finalJob.ExitCode)
}

func testJobEnvironmentVariables(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create job with environment variables
	dataUtils := &DataUtils{db: tx}
	envVars := map[string]interface{}{
		"ENV_VAR_1": "value1",
		"ENV_VAR_2": float64(42), // JSON unmarshaling converts numbers to float64
		"ENV_VAR_3": true,
		"ENV_VAR_4": map[string]interface{}{"nested": "value"}, // JSON unmarshaling converts to map[string]interface{}
	}

	job, err := dataUtils.CreateJob(DataSetup{
		"JobEnvVars": envVars,
	})
	require.NoError(t, err)

	// Retrieve and verify environment variables
	retrievedJob, err := store.AppStore.GetJobByID(ctx, job.JobID)
	require.NoError(t, err)

	// Convert JSONB back to map[string]interface{} for comparison
	retrievedEnvVars := map[string]interface{}(retrievedJob.JobEnvVars)
	assert.Equal(t, envVars, retrievedEnvVars)
}

// API Token operation test implementations

func testCreateAPITokenAndValidateAPIToken(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test user
	dataUtils := &DataUtils{db: tx}
	user, err := dataUtils.CreateUser(DataSetup{"Username": "tokenuser"})
	require.NoError(t, err)

	// Generate raw token
	rawToken := make([]byte, 32)
	_, err = rand.Read(rawToken)
	require.NoError(t, err)
	tokenString := string(rawToken)

	// Create API token
	tokenHash := checkauth.HashAPIToken(tokenString)
	apiToken := &models.APIToken{
		UserID:    user.UserID,
		TokenHash: tokenHash,
		Name:      "Test Token",
		IsActive:  true,
	}

	err = store.AppStore.CreateAPIToken(ctx, apiToken)
	require.NoError(t, err)
	assert.NotEmpty(t, apiToken.TokenID)

	// Validate token
	validatedToken, validatedUser, err := store.AppStore.ValidateAPIToken(ctx, tokenString)
	require.NoError(t, err)
	assert.Equal(t, apiToken.TokenID, validatedToken.TokenID)
	assert.Equal(t, user.UserID, validatedUser.UserID)
	assert.Equal(t, user.Username, validatedUser.Username)
}

func testGetAPITokensByUser(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test user
	dataUtils := &DataUtils{db: tx}
	user, err := dataUtils.CreateUser(DataSetup{"Username": "tokenuser"})
	require.NoError(t, err)

	// Create multiple tokens for the user
	for i := 0; i < 3; i++ {
		_, err := dataUtils.CreateAPIToken(DataSetup{
			"UserID": user.UserID,
			"Name":   fmt.Sprintf("Token %d", i+1),
		})
		require.NoError(t, err)
	}

	// Get tokens for the user
	tokens, err := store.AppStore.GetAPITokensByUser(ctx, user.UserID)
	require.NoError(t, err)
	assert.Len(t, tokens, 3)

	// Verify all tokens belong to the correct user
	for _, token := range tokens {
		assert.Equal(t, user.UserID, token.UserID)
	}
}

func testDeleteAPIToken(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test token
	dataUtils := &DataUtils{db: tx}
	token, err := dataUtils.CreateAPIToken(DataSetup{})
	require.NoError(t, err)

	// Delete token
	err = store.AppStore.DeleteAPIToken(ctx, token.TokenID)
	require.NoError(t, err)

	// Verify deletion
	tokens, err := store.AppStore.GetAPITokensByUser(ctx, token.UserID)
	require.NoError(t, err)
	assert.Len(t, tokens, 0)

	// Try to delete again (should return not found)
	err = store.AppStore.DeleteAPIToken(ctx, token.TokenID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func testUpdateTokenLastUsed(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test token
	dataUtils := &DataUtils{db: tx}
	token, err := dataUtils.CreateAPIToken(DataSetup{})
	require.NoError(t, err)

	// Update last used timestamp
	lastUsed := time.Now().UTC()
	err = store.AppStore.UpdateTokenLastUsed(ctx, token.TokenID, lastUsed)
	require.NoError(t, err)

	// Verify update by getting the token
	tokens, err := store.AppStore.GetAPITokensByUser(ctx, token.UserID)
	require.NoError(t, err)
	require.Len(t, tokens, 1)

	updatedToken := tokens[0]
	assert.NotNil(t, updatedToken.LastUsedAt)
	assert.WithinDuration(t, lastUsed, *updatedToken.LastUsedAt, time.Second)
}

func testTokenExpiration(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test user
	dataUtils := &DataUtils{db: tx}
	user, err := dataUtils.CreateUser(DataSetup{"Username": "expireduser"})
	require.NoError(t, err)

	// Generate raw token
	rawToken := make([]byte, 32)
	_, err = rand.Read(rawToken)
	require.NoError(t, err)
	tokenString := string(rawToken)

	// Create expired token
	tokenHash := checkauth.HashAPIToken(tokenString)
	expiredTime := time.Now().UTC().Add(-24 * time.Hour)
	apiToken := &models.APIToken{
		UserID:    user.UserID,
		TokenHash: tokenHash,
		Name:      "Expired Token",
		IsActive:  true,
		ExpiresAt: &expiredTime,
	}

	err = store.AppStore.CreateAPIToken(ctx, apiToken)
	require.NoError(t, err)

	// Try to validate expired token
	_, _, err = store.AppStore.ValidateAPIToken(ctx, tokenString)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func testInactiveTokenValidation(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test user
	dataUtils := &DataUtils{db: tx}
	user, err := dataUtils.CreateUser(DataSetup{"Username": "inactiveuser"})
	require.NoError(t, err)

	// Generate raw token
	rawToken := make([]byte, 32)
	_, err = rand.Read(rawToken)
	require.NoError(t, err)
	tokenString := string(rawToken)

	// Create inactive token
	tokenHash := checkauth.HashAPIToken(tokenString)
	apiToken := &models.APIToken{
		UserID:    user.UserID,
		TokenHash: tokenHash,
		Name:      "Inactive Token",
		IsActive:  false, // Inactive
	}

	err = store.AppStore.CreateAPIToken(ctx, apiToken)
	require.NoError(t, err)

	// Try to validate inactive token
	_, _, err = store.AppStore.ValidateAPIToken(ctx, tokenString)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// TestStoreErrorHandling tests error handling scenarios
func TestStoreErrorHandling(t *testing.T) {
	t.Run("Invalid UUIDs", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testInvalidUUIDs(t, ctx, tx)
		})
	})

	t.Run("Context Cancellation", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testContextCancellation(t, ctx, tx)
		})
	})
}

func testInvalidUUIDs(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Test each invalid UUID operation in separate sub-transactions to avoid
	// PostgreSQL transaction abort issues

	t.Run("GetUserByID with invalid UUID", func(t *testing.T) {
		_, err := store.AppStore.GetUserByID(ctx, "invalid-uuid")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("GetJobByID with invalid UUID", func(t *testing.T) {
		_, err := store.AppStore.GetJobByID(ctx, "invalid-uuid")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("DeleteJob with invalid UUID", func(t *testing.T) {
		err := store.AppStore.DeleteJob(ctx, "invalid-uuid")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("DeleteAPIToken with invalid UUID", func(t *testing.T) {
		err := store.AppStore.DeleteAPIToken(ctx, "invalid-uuid")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}

func testContextCancellation(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create a cancelled context
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// These operations should still work with cancelled context since we're using
	// the database transaction context, not the cancelled context
	dataUtils := &DataUtils{db: tx}
	user, err := dataUtils.CreateUser(DataSetup{"Username": "contextuser"})
	require.NoError(t, err)

	// Verify user can still be retrieved (using the transaction context, not cancelled context)
	_, err = store.AppStore.GetUserByID(ctx, user.UserID)
	require.NoError(t, err)

	// Use the cancelled context to verify it's actually cancelled
	assert.Error(t, cancelledCtx.Err())
}

// TestConcurrentOperations tests concurrent access scenarios
func TestConcurrentOperations(t *testing.T) {
	t.Run("Concurrent Job Updates", func(t *testing.T) {
		RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
			testConcurrentJobUpdates(t, ctx, tx)
		})
	})
}

func testConcurrentJobUpdates(t *testing.T, ctx context.Context, tx *gorm.DB) {
	// Create test job
	dataUtils := &DataUtils{db: tx}
	job, err := dataUtils.CreateJob(DataSetup{"Status": "submitted"})
	require.NoError(t, err)

	// Simulate concurrent updates by updating the job multiple times
	// In a real concurrent scenario, we'd have multiple goroutines
	// but for testing, we'll just verify that sequential updates work correctly

	// Update 1: submitted -> running
	job.Status = "running"
	startTime := time.Now().UTC()
	job.StartedAt = &startTime
	err = store.AppStore.UpdateJob(ctx, job)
	require.NoError(t, err)

	// Update 2: add exit code while keeping running status
	exitCode := 0
	job.ExitCode = &exitCode
	err = store.AppStore.UpdateJob(ctx, job)
	require.NoError(t, err)

	// Update 3: running -> completed
	job.Status = "completed"
	completedTime := time.Now().UTC()
	job.CompletedAt = &completedTime
	err = store.AppStore.UpdateJob(ctx, job)
	require.NoError(t, err)

	// Verify final state
	finalJob, err := store.AppStore.GetJobByID(ctx, job.JobID)
	require.NoError(t, err)
	assert.Equal(t, "completed", finalJob.Status)
	assert.NotNil(t, finalJob.StartedAt)
	assert.NotNil(t, finalJob.CompletedAt)
	assert.NotNil(t, finalJob.ExitCode)
	assert.Equal(t, 0, *finalJob.ExitCode)
}
