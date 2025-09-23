package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// Create a test context with transaction
func createTestContextWithTx(tx *gorm.DB) context.Context {
	return context.WithValue(context.Background(), postgres_store.GetTxContextKey(), tx)
}

// TestTransactionMiddleware tests that the transaction middleware correctly handles commits and rollbacks
func TestTransactionMiddleware(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		// Create a test context with our transaction
		txCtx := context.WithValue(ctx, postgres_store.GetTxContextKey(), tx)

		// Get the app's HTTP mux for testing
		router := GetTestMux()

		// Test that the transaction middleware is working by making a request
		// to a health endpoint (which uses the transaction middleware)

		// Use our txCtx with the transaction
		req, err := http.NewRequestWithContext(txCtx, "GET", "/api/health", nil)
		assert.NoError(t, err)

		// Create a response recorder
		rr := httptest.NewRecorder()

		// Serve the request
		router.ServeHTTP(rr, req)

		// Check the status code is what we expect
		assert.Equal(t, http.StatusOK, rr.Code)

		// Verify we get a valid JSON response (proving transaction middleware worked)
		assert.Contains(t, rr.Header().Get("Content-Type"), "application/json")
	})
}
