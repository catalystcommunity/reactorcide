package test

import (
	"flag"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/cmd"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
)

// checkDatabaseConnectivity tests if the database is reachable
func checkDatabaseConnectivity(host string, port string) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// TestMain for all tests in the test package
func TestMain(m *testing.M) {
	// Parse flags to check for -short mode
	flag.Parse()

	// Skip integration tests in short mode
	if testing.Short() {
		fmt.Println("Skipping database integration tests in short mode")
		os.Exit(0)
	}

	// Check if database is reachable before attempting connection
	if !checkDatabaseConnectivity("localhost", "5432") {
		fmt.Println("Skipping database integration tests: PostgreSQL not available at localhost:5432")
		fmt.Println("To run these tests, start PostgreSQL with a 'testpg' database")
		os.Exit(0)
	}

	// Configure test database URI for the running container
	testDbUri := "postgresql://devuser:devpass@localhost:5432/testpg?sslmode=disable"
	os.Setenv("TEST_DB_URI", testDbUri)

	// Also set the main DB_URI for migrations
	config.DbUri = testDbUri
	os.Setenv("DB_URI", testDbUri)

	// Configure test environment to never commit transactions
	os.Setenv("COMMIT_ON_SUCCESS", "false")
	// Reload config to pick up the environment variable
	config.CommitOnSuccess = false

	// Simply run migrations before tests
	// This is safe because goose tracks applied migrations and won't rerun them
	err := cmd.RunMigrations()
	if err != nil {
		fmt.Printf("Skipping database integration tests: failed to run migrations: %v\n", err)
		fmt.Println("Ensure the 'testpg' database exists and is accessible")
		os.Exit(0)
	}

	// Initialize the test database connections
	initTestDB()
	if initErr != nil {
		fmt.Printf("Skipping database integration tests: failed to initialize: %v\n", initErr)
		os.Exit(0)
	}

	// Run the tests
	code := m.Run()

	// Clean up the database connection
	if cleanupFunc != nil {
		cleanupFunc()
	}

	// Exit with the test status code
	os.Exit(code)
}
