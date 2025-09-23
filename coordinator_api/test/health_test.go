package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// HealthResponse represents the expected health check response structure
type HealthResponse struct {
	Status       string                 `json:"status"`
	Verification map[string]interface{} `json:"verification"`
}

// TestHealthEndpoint tests the /api/health endpoint using the actual app router
func TestHealthEndpoint(t *testing.T) {
	// Get the actual application mux through the test wrapper
	mux := GetTestMux()

	t.Run("GET /api/health returns 200 OK", func(t *testing.T) {
		// Create request
		req, err := http.NewRequest("GET", "/api/health", nil)
		require.NoError(t, err)

		// Create response recorder
		rr := httptest.NewRecorder()

		// Serve the request using the actual app router
		mux.ServeHTTP(rr, req)

		// Check status code
		assert.Equal(t, http.StatusOK, rr.Code)

		// Check content type
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		// Parse response
		var response HealthResponse
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)

		// Verify response structure
		assert.Equal(t, "OK", response.Status)
		assert.NotNil(t, response.Verification)

		// Verify verification fields exist
		_, hasVerified := response.Verification["verified"]
		assert.True(t, hasVerified, "response should include 'verified' field")

		_, hasUserAuth := response.Verification["user_authenticated"]
		assert.True(t, hasUserAuth, "response should include 'user_authenticated' field")
	})

	t.Run("GET /api/health without authentication", func(t *testing.T) {
		// Create request without authentication
		req, err := http.NewRequest("GET", "/api/health", nil)
		require.NoError(t, err)

		// Create response recorder
		rr := httptest.NewRecorder()

		// Serve the request
		mux.ServeHTTP(rr, req)

		// Check status code (should still be OK since health doesn't require auth)
		assert.Equal(t, http.StatusOK, rr.Code)

		// Parse response
		var response HealthResponse
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)

		// Verify no user authentication
		assert.Equal(t, false, response.Verification["user_authenticated"])

		// user_id should not be present since no user is authenticated
		_, hasUserID := response.Verification["user_id"]
		assert.False(t, hasUserID, "response should not include 'user_id' when not authenticated")
	})

	t.Run("POST /api/health returns method not allowed", func(t *testing.T) {
		// Create POST request
		req, err := http.NewRequest("POST", "/api/health", nil)
		require.NoError(t, err)

		// Create response recorder
		rr := httptest.NewRecorder()

		// Serve the request
		mux.ServeHTTP(rr, req)

		// Check status code
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("PUT /api/health returns method not allowed", func(t *testing.T) {
		// Create PUT request
		req, err := http.NewRequest("PUT", "/api/health", nil)
		require.NoError(t, err)

		// Create response recorder
		rr := httptest.NewRecorder()

		// Serve the request
		mux.ServeHTTP(rr, req)

		// Check status code
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("DELETE /api/health returns method not allowed", func(t *testing.T) {
		// Create DELETE request
		req, err := http.NewRequest("DELETE", "/api/health", nil)
		require.NoError(t, err)

		// Create response recorder
		rr := httptest.NewRecorder()

		// Serve the request
		mux.ServeHTTP(rr, req)

		// Check status code
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})
}

// TestHealthV1Endpoint tests the /api/v1/health endpoint using the actual app router
func TestHealthV1Endpoint(t *testing.T) {
	// Get the actual application mux through the test wrapper
	mux := GetTestMux()

	t.Run("GET /api/v1/health returns 200 OK", func(t *testing.T) {
		// Create request
		req, err := http.NewRequest("GET", "/api/v1/health", nil)
		require.NoError(t, err)

		// Create response recorder
		rr := httptest.NewRecorder()

		// Serve the request
		mux.ServeHTTP(rr, req)

		// Check status code
		assert.Equal(t, http.StatusOK, rr.Code)

		// Check content type
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		// Parse response
		var response HealthResponse
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)

		// Verify response structure (should be identical to v0)
		assert.Equal(t, "OK", response.Status)
		assert.NotNil(t, response.Verification)

		// Verify verification fields exist
		_, hasVerified := response.Verification["verified"]
		assert.True(t, hasVerified, "response should include 'verified' field")

		_, hasUserAuth := response.Verification["user_authenticated"]
		assert.True(t, hasUserAuth, "response should include 'user_authenticated' field")
	})

	t.Run("GET /api/v1/health without authentication", func(t *testing.T) {
		// Create request without authentication
		req, err := http.NewRequest("GET", "/api/v1/health", nil)
		require.NoError(t, err)

		// Create response recorder
		rr := httptest.NewRecorder()

		// Serve the request
		mux.ServeHTTP(rr, req)

		// Check status code (should still be OK since health doesn't require auth)
		assert.Equal(t, http.StatusOK, rr.Code)

		// Parse response
		var response HealthResponse
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)

		// Verify no user authentication
		assert.Equal(t, false, response.Verification["user_authenticated"])

		// user_id should not be present since no user is authenticated
		_, hasUserID := response.Verification["user_id"]
		assert.False(t, hasUserID, "response should not include 'user_id' when not authenticated")
	})

	t.Run("POST /api/v1/health returns method not allowed", func(t *testing.T) {
		// Create POST request
		req, err := http.NewRequest("POST", "/api/v1/health", nil)
		require.NoError(t, err)

		// Create response recorder
		rr := httptest.NewRecorder()

		// Serve the request
		mux.ServeHTTP(rr, req)

		// Check status code
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("Both health endpoints return identical responses", func(t *testing.T) {
		// Test v0 endpoint
		reqV0, err := http.NewRequest("GET", "/api/health", nil)
		require.NoError(t, err)
		rrV0 := httptest.NewRecorder()
		mux.ServeHTTP(rrV0, reqV0)

		// Test v1 endpoint
		reqV1, err := http.NewRequest("GET", "/api/v1/health", nil)
		require.NoError(t, err)
		rrV1 := httptest.NewRecorder()
		mux.ServeHTTP(rrV1, reqV1)

		// Both should return 200
		assert.Equal(t, http.StatusOK, rrV0.Code)
		assert.Equal(t, http.StatusOK, rrV1.Code)

		// Parse both responses
		var responseV0, responseV1 HealthResponse
		err = json.Unmarshal(rrV0.Body.Bytes(), &responseV0)
		require.NoError(t, err)
		err = json.Unmarshal(rrV1.Body.Bytes(), &responseV1)
		require.NoError(t, err)

		// Responses should be identical
		assert.Equal(t, responseV0.Status, responseV1.Status)
		assert.Equal(t, responseV0.Verification["verified"], responseV1.Verification["verified"])
		assert.Equal(t, responseV0.Verification["user_authenticated"], responseV1.Verification["user_authenticated"])
	})
}

// TestHealthEndpointRouting tests that the health endpoints are properly configured in the app router
func TestHealthEndpointRouting(t *testing.T) {
	// This test validates that our health endpoints are actually configured in the app router
	// and not just returning 404s

	mux := GetTestMux()

	t.Run("health endpoints are properly routed", func(t *testing.T) {
		testCases := []struct {
			name string
			path string
		}{
			{"v0 health endpoint", "/api/health"},
			{"v1 health endpoint", "/api/v1/health"},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				req, err := http.NewRequest("GET", tc.path, nil)
				require.NoError(t, err)

				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, req)

				// Should not return 404 (which would indicate route not configured)
				assert.NotEqual(t, http.StatusNotFound, rr.Code,
					"endpoint %s should be configured in router", tc.path)

				// Should return 200 for GET requests
				assert.Equal(t, http.StatusOK, rr.Code,
					"endpoint %s should return 200 for GET requests", tc.path)
			})
		}
	})

	t.Run("invalid health paths return 404", func(t *testing.T) {
		invalidPaths := []string{
			"/api/health/invalid",
			"/api/v1/health/invalid",
			"/api/v2/health",
			"/health", // without /api prefix
		}

		for _, path := range invalidPaths {
			req, err := http.NewRequest("GET", path, nil)
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			// Invalid paths should return 404
			assert.Equal(t, http.StatusNotFound, rr.Code,
				"invalid path %s should return 404", path)
		}
	})
}

// TestHealthEndpointMiddleware tests that health endpoints go through the expected middleware
func TestHealthEndpointMiddleware(t *testing.T) {
	// Health endpoints should go through transaction middleware but not auth middleware
	// We can test this by verifying the endpoint works without authentication

	mux := GetTestMux()

	t.Run("health endpoints work without authentication", func(t *testing.T) {
		// Both health endpoints should work without any authentication headers
		endpoints := []string{"/api/health", "/api/v1/health"}

		for _, endpoint := range endpoints {
			req, err := http.NewRequest("GET", endpoint, nil)
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			// Should return 200, not 401 (which would indicate auth middleware blocking)
			assert.Equal(t, http.StatusOK, rr.Code,
				"endpoint %s should work without authentication", endpoint)

			// Should return valid JSON response
			var response HealthResponse
			err = json.Unmarshal(rr.Body.Bytes(), &response)
			assert.NoError(t, err, "endpoint %s should return valid JSON", endpoint)
		}
	})
}
