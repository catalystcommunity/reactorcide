package test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/cmd"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	// Container holds the postgres container for cleanup
	postgresContainer *postgres.PostgresContainer
)

// TestMain for all tests in the test package
func TestMain(m *testing.M) {
	// Parse flags to check for -short mode
	flag.Parse()

	// Skip integration tests in short mode
	if testing.Short() {
		fmt.Println("Skipping database integration tests in short mode")
		os.Exit(0)
	}

	// Start postgres container using testcontainers-go
	ctx := context.Background()
	var err error

	fmt.Println("Starting PostgreSQL container for tests...")
	postgresContainer, err = postgres.Run(ctx,
		"postgres:17",
		postgres.WithDatabase("testpg"),
		postgres.WithUsername("devuser"),
		postgres.WithPassword("devpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		fmt.Printf("Failed to start postgres container: %v\n", err)
		os.Exit(1)
	}

	// Get the container's connection string
	connStr, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Printf("Failed to get connection string: %v\n", err)
		terminateContainer(ctx)
		os.Exit(1)
	}

	fmt.Printf("PostgreSQL container started: %s\n", connStr)

	// Configure test database URI
	os.Setenv("TEST_DB_URI", connStr)

	// Also set the main DB_URI for migrations
	config.DbUri = connStr
	os.Setenv("DB_URI", connStr)

	// Configure test environment to never commit transactions
	os.Setenv("COMMIT_ON_SUCCESS", "false")
	// Reload config to pick up the environment variable
	config.CommitOnSuccess = false

	// Run migrations
	err = cmd.RunMigrations()
	if err != nil {
		fmt.Printf("Failed to run migrations: %v\n", err)
		terminateContainer(ctx)
		os.Exit(1)
	}

	// Initialize the test database connections
	initTestDB()
	if initErr != nil {
		fmt.Printf("Failed to initialize test database: %v\n", initErr)
		terminateContainer(ctx)
		os.Exit(1)
	}

	// Run the tests
	code := m.Run()

	// Clean up the database connection
	if cleanupFunc != nil {
		cleanupFunc()
	}

	// Terminate the container
	terminateContainer(ctx)

	// Exit with the test status code
	os.Exit(code)
}

// terminateContainer cleans up the postgres container
func terminateContainer(ctx context.Context) {
	if postgresContainer != nil {
		fmt.Println("Terminating PostgreSQL container...")
		if err := postgresContainer.Terminate(ctx); err != nil {
			fmt.Printf("Failed to terminate postgres container: %v\n", err)
		}
	}
}
