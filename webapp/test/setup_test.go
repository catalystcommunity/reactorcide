package test

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	webConfig "github.com/catalystcommunity/reactorcide/webapp/internal/config"
	webHandlers "github.com/catalystcommunity/reactorcide/webapp/internal/handlers"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const testUserID = "550e8400-e29b-41d4-a716-446655440000"

var (
	postgresContainer *postgres.PostgresContainer
	apiBaseURL        string
	webBaseURL        string
	testToken         string
	apiCmd            *exec.Cmd
	testDB            *sql.DB
	connStr           string
)

func TestMain(m *testing.M) {
	flag.Parse()

	if testing.Short() {
		fmt.Println("Skipping integration tests in short mode")
		os.Exit(0)
	}

	ctx := context.Background()

	// Start PostgreSQL
	fmt.Println("Starting PostgreSQL container for webapp integration tests...")
	var err error
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

	connStr, err = postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Printf("Failed to get connection string: %v\n", err)
		cleanup(ctx)
		os.Exit(1)
	}
	fmt.Printf("PostgreSQL: %s\n", connStr)

	// Open a direct DB connection for inserting test data
	testDB, err = sql.Open("postgres", connStr)
	if err != nil {
		fmt.Printf("Failed to open DB: %v\n", err)
		cleanup(ctx)
		os.Exit(1)
	}

	// Build the coordinator API binary
	coordDir := findCoordinatorDir()
	fmt.Println("Building coordinator API binary...")
	buildCmd := exec.Command("go", "build", "-o", "/tmp/reactorcide-test", ".")
	buildCmd.Dir = coordDir
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		fmt.Printf("Failed to build coordinator API: %v\n", err)
		cleanup(ctx)
		os.Exit(1)
	}

	// Run migrations
	fmt.Println("Running migrations...")
	migrateCmd := exec.Command("/tmp/reactorcide-test", "migrate")
	migrateCmd.Env = append(os.Environ(),
		"REACTORCIDE_DB_URI="+connStr,
		"DB_URI="+connStr,
	)
	migrateCmd.Stdout = os.Stdout
	migrateCmd.Stderr = os.Stderr
	if err := migrateCmd.Run(); err != nil {
		fmt.Printf("Failed to run migrations: %v\n", err)
		cleanup(ctx)
		os.Exit(1)
	}

	// Create a test token
	fmt.Println("Creating test token...")
	tokenCmd := exec.Command("/tmp/reactorcide-test", "token", "create", "--name", "webapp-test-token")
	tokenCmd.Env = append(os.Environ(),
		"REACTORCIDE_DB_URI="+connStr,
		"DB_URI="+connStr,
		"REACTORCIDE_DEFAULT_USER_ID="+testUserID,
		"REACTORCIDE_COMMIT_ON_SUCCESS=true",
	)
	tokenOutput, err := tokenCmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Failed to create token: %v\nOutput: %s\n", err, string(tokenOutput))
		cleanup(ctx)
		os.Exit(1)
	}
	testToken = extractToken(string(tokenOutput))
	if testToken == "" {
		fmt.Printf("Failed to extract token from output: %s\n", string(tokenOutput))
		cleanup(ctx)
		os.Exit(1)
	}
	fmt.Printf("Test token: %s...\n", testToken[:min(16, len(testToken))])

	// Start coordinator API server
	apiPort := getFreePort()
	fmt.Printf("Starting coordinator API on port %d...\n", apiPort)
	apiCmd = exec.Command("/tmp/reactorcide-test", "serve", "--port", fmt.Sprintf("%d", apiPort))
	apiCmd.Env = append(os.Environ(),
		"REACTORCIDE_DB_URI="+connStr,
		"DB_URI="+connStr,
		"REACTORCIDE_DEFAULT_USER_ID="+testUserID,
		"REACTORCIDE_COMMIT_ON_SUCCESS=true",
		"REACTORCIDE_OBJECT_STORE_TYPE=memory",
	)
	apiCmd.Stdout = os.Stdout
	apiCmd.Stderr = os.Stderr
	if err := apiCmd.Start(); err != nil {
		fmt.Printf("Failed to start coordinator API: %v\n", err)
		cleanup(ctx)
		os.Exit(1)
	}
	apiBaseURL = fmt.Sprintf("http://localhost:%d", apiPort)

	if !waitForServer(apiBaseURL + "/api/v1/health") {
		fmt.Println("Coordinator API failed to start")
		cleanup(ctx)
		os.Exit(1)
	}

	// Start webapp server
	webConfig.APIUrl = apiBaseURL
	webConfig.APIToken = testToken
	webPort := getFreePort()
	webRouter := webHandlers.NewRouter()
	webServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", webPort),
		Handler: webRouter,
	}
	go webServer.ListenAndServe()
	webBaseURL = fmt.Sprintf("http://localhost:%d", webPort)

	if !waitForServer(webBaseURL + "/") {
		fmt.Println("Web server failed to start")
		cleanup(ctx)
		os.Exit(1)
	}

	fmt.Printf("API server: %s\n", apiBaseURL)
	fmt.Printf("Web server: %s\n", webBaseURL)

	code := m.Run()

	webServer.Close()
	cleanup(ctx)
	os.Exit(code)
}

func cleanup(ctx context.Context) {
	if apiCmd != nil && apiCmd.Process != nil {
		apiCmd.Process.Kill()
	}
	if testDB != nil {
		testDB.Close()
	}
	if postgresContainer != nil {
		fmt.Println("Terminating PostgreSQL container...")
		postgresContainer.Terminate(ctx)
	}
	os.Remove("/tmp/reactorcide-test")
}

func getFreePort() int {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		panic(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func waitForServer(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 50; i++ {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// findCoordinatorDir locates the coordinator_api directory
func findCoordinatorDir() string {
	dir, _ := os.Getwd()
	for {
		coordPath := dir + "/coordinator_api"
		if _, err := os.Stat(coordPath + "/main.go"); err == nil {
			return coordPath
		}
		parent := dir[:strings.LastIndex(dir, "/")]
		if parent == dir {
			break
		}
		dir = parent
	}
	return "../../coordinator_api"
}

// extractToken parses the token from the CLI output
func extractToken(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Token:") || strings.HasPrefix(line, "token:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// insertTestJob inserts a job directly into the database for testing
func insertTestJob(t *testing.T, name string) string {
	t.Helper()

	// Let the database generate the ULID via its default
	var jobID string
	err := testDB.QueryRow(`SELECT generate_ulid()`).Scan(&jobID)
	if err != nil {
		t.Fatalf("Failed to generate ULID: %v", err)
	}

	_, err = testDB.Exec(`INSERT INTO jobs (
		job_id, user_id, name, description, status, source_type, source_url, source_ref,
		job_command, runner_image, queue_name, priority, timeout_seconds,
		code_dir, job_dir, created_at, updated_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NOW(), NOW())`,
		jobID, testUserID, name, "Integration test job", "submitted",
		"git", "https://github.com/test/repo.git", "main",
		"echo hello", "alpine:latest", "reactorcide-jobs", 10, 3600,
		"/job/src", "/job/src",
	)
	if err != nil {
		t.Fatalf("Failed to insert test job: %v", err)
	}
	return jobID
}
