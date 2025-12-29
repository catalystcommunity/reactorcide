// +build integration

package worker

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDockerRunner_Integration_FullLifecycle tests the complete job lifecycle
func TestDockerRunner_Integration_FullLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create a DockerRunner
	runner, err := NewDockerRunner()
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}

	ctx := context.Background()

	// Create a temporary workspace directory
	workspaceDir, err := os.MkdirTemp("", "docker-runner-test-*")
	if err != nil {
		t.Fatalf("Failed to create workspace: %v", err)
	}
	defer os.RemoveAll(workspaceDir)

	// Create a test file in the workspace
	testFile := filepath.Join(workspaceDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("Hello from test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Configure a simple job
	config := &JobConfig{
		Image:          "alpine:latest",
		Command:        []string{"sh", "-c", "echo 'Starting job' && cat /job/test.txt && echo 'Job complete'"},
		Env:            map[string]string{"TEST_VAR": "test-value"},
		WorkspaceDir:   workspaceDir,
		TimeoutSeconds: 30,
		JobID:          "test-job-123",
		QueueName:      "test-queue",
	}

	// Step 1: Spawn the job
	t.Log("Spawning job container")
	containerID, err := runner.SpawnJob(ctx, config)
	if err != nil {
		t.Fatalf("Failed to spawn job: %v", err)
	}
	if containerID == "" {
		t.Fatal("Expected non-empty container ID")
	}
	t.Logf("Container spawned: %s", containerID)

	// Ensure cleanup happens
	defer func() {
		t.Log("Cleaning up container")
		if err := runner.Cleanup(ctx, containerID); err != nil {
			t.Errorf("Failed to cleanup: %v", err)
		}
	}()

	// Step 2: Stream logs
	t.Log("Streaming logs")
	stdout, stderr, err := runner.StreamLogs(ctx, containerID)
	if err != nil {
		t.Fatalf("Failed to stream logs: %v", err)
	}
	defer stdout.Close()
	defer stderr.Close()

	// Read logs in background
	outputDone := make(chan string)
	go func() {
		data, _ := io.ReadAll(stdout)
		outputDone <- string(data)
	}()

	// Step 3: Wait for completion
	t.Log("Waiting for completion")
	exitCode, err := runner.WaitForCompletion(ctx, containerID)
	if err != nil {
		t.Fatalf("Failed to wait for completion: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", exitCode)
	}
	t.Logf("Job completed with exit code: %d", exitCode)

	// Verify output
	select {
	case output := <-outputDone:
		t.Logf("Job output: %s", output)
		if !strings.Contains(output, "Starting job") {
			t.Error("Expected output to contain 'Starting job'")
		}
		if !strings.Contains(output, "Hello from test") {
			t.Error("Expected output to contain 'Hello from test' (from mounted file)")
		}
		if !strings.Contains(output, "Job complete") {
			t.Error("Expected output to contain 'Job complete'")
		}
	case <-time.After(5 * time.Second):
		t.Error("Timeout waiting for output")
	}
}

// TestDockerRunner_Integration_FailingJob tests handling of a job that fails
func TestDockerRunner_Integration_FailingJob(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	runner, err := NewDockerRunner()
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}

	ctx := context.Background()

	workspaceDir, err := os.MkdirTemp("", "docker-runner-fail-test-*")
	if err != nil {
		t.Fatalf("Failed to create workspace: %v", err)
	}
	defer os.RemoveAll(workspaceDir)

	// Configure a job that will fail
	config := &JobConfig{
		Image:        "alpine:latest",
		Command:      []string{"sh", "-c", "echo 'This will fail' && exit 42"},
		WorkspaceDir: workspaceDir,
		JobID:        "fail-job-123",
		QueueName:    "test-queue",
	}

	containerID, err := runner.SpawnJob(ctx, config)
	if err != nil {
		t.Fatalf("Failed to spawn job: %v", err)
	}
	defer runner.Cleanup(ctx, containerID)

	// Wait for completion - should get non-zero exit code
	exitCode, err := runner.WaitForCompletion(ctx, containerID)
	if err != nil {
		t.Fatalf("Failed to wait for completion: %v", err)
	}

	if exitCode != 42 {
		t.Errorf("Expected exit code 42, got %d", exitCode)
	}
	t.Logf("Job failed as expected with exit code: %d", exitCode)
}

// TestDockerRunner_Integration_ResourceLimits tests setting resource limits
func TestDockerRunner_Integration_ResourceLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	runner, err := NewDockerRunner()
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}

	ctx := context.Background()

	workspaceDir, err := os.MkdirTemp("", "docker-runner-limits-test-*")
	if err != nil {
		t.Fatalf("Failed to create workspace: %v", err)
	}
	defer os.RemoveAll(workspaceDir)

	config := &JobConfig{
		Image:        "alpine:latest",
		Command:      []string{"sh", "-c", "echo 'Testing resource limits' && sleep 1"},
		WorkspaceDir: workspaceDir,
		CPULimit:     "0.5",  // 0.5 CPU cores
		MemoryLimit:  "128Mi", // 128 MiB
		JobID:        "limits-job-123",
		QueueName:    "test-queue",
	}

	containerID, err := runner.SpawnJob(ctx, config)
	if err != nil {
		t.Fatalf("Failed to spawn job with limits: %v", err)
	}
	defer runner.Cleanup(ctx, containerID)

	// Job should complete successfully even with limits
	exitCode, err := runner.WaitForCompletion(ctx, containerID)
	if err != nil {
		t.Fatalf("Failed to wait for completion: %v", err)
	}

	if exitCode != 0 {
		t.Errorf("Expected exit code 0 with resource limits, got %d", exitCode)
	}
	t.Log("Job completed successfully with resource limits")
}

// TestDockerRunner_Integration_EnvVars tests environment variable injection
func TestDockerRunner_Integration_EnvVars(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	runner, err := NewDockerRunner()
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}

	ctx := context.Background()

	workspaceDir, err := os.MkdirTemp("", "docker-runner-env-test-*")
	if err != nil {
		t.Fatalf("Failed to create workspace: %v", err)
	}
	defer os.RemoveAll(workspaceDir)

	config := &JobConfig{
		Image:   "alpine:latest",
		Command: []string{"sh", "-c", "echo VAR1=$VAR1 && echo VAR2=$VAR2"},
		Env: map[string]string{
			"VAR1": "value1",
			"VAR2": "value2",
		},
		WorkspaceDir: workspaceDir,
		JobID:        "env-job-123",
		QueueName:    "test-queue",
	}

	containerID, err := runner.SpawnJob(ctx, config)
	if err != nil {
		t.Fatalf("Failed to spawn job: %v", err)
	}
	defer runner.Cleanup(ctx, containerID)

	// Stream logs to verify env vars
	stdout, stderr, err := runner.StreamLogs(ctx, containerID)
	if err != nil {
		t.Fatalf("Failed to stream logs: %v", err)
	}
	defer stdout.Close()
	defer stderr.Close()

	outputDone := make(chan string)
	go func() {
		data, _ := io.ReadAll(stdout)
		outputDone <- string(data)
	}()

	exitCode, err := runner.WaitForCompletion(ctx, containerID)
	if err != nil {
		t.Fatalf("Failed to wait for completion: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", exitCode)
	}

	// Verify environment variables were set
	select {
	case output := <-outputDone:
		t.Logf("Output: %s", output)
		if !strings.Contains(output, "VAR1=value1") {
			t.Error("Expected output to contain 'VAR1=value1'")
		}
		if !strings.Contains(output, "VAR2=value2") {
			t.Error("Expected output to contain 'VAR2=value2'")
		}
	case <-time.After(5 * time.Second):
		t.Error("Timeout waiting for output")
	}
}

// TestDockerRunner_Integration_WorkspaceMount tests workspace directory mounting
func TestDockerRunner_Integration_WorkspaceMount(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	runner, err := NewDockerRunner()
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}

	ctx := context.Background()

	workspaceDir, err := os.MkdirTemp("", "docker-runner-mount-test-*")
	if err != nil {
		t.Fatalf("Failed to create workspace: %v", err)
	}
	defer os.RemoveAll(workspaceDir)

	// Create files in workspace
	if err := os.WriteFile(filepath.Join(workspaceDir, "input.txt"), []byte("test input"), 0644); err != nil {
		t.Fatalf("Failed to create input file: %v", err)
	}

	config := &JobConfig{
		Image: "alpine:latest",
		// Read input, write output
		Command:      []string{"sh", "-c", "cat /job/input.txt > /job/output.txt && echo 'success'"},
		WorkspaceDir: workspaceDir,
		JobID:        "mount-job-123",
		QueueName:    "test-queue",
	}

	containerID, err := runner.SpawnJob(ctx, config)
	if err != nil {
		t.Fatalf("Failed to spawn job: %v", err)
	}
	defer runner.Cleanup(ctx, containerID)

	exitCode, err := runner.WaitForCompletion(ctx, containerID)
	if err != nil {
		t.Fatalf("Failed to wait for completion: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", exitCode)
	}

	// Verify output file was created in workspace
	outputFile := filepath.Join(workspaceDir, "output.txt")
	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	if string(data) != "test input" {
		t.Errorf("Expected output 'test input', got '%s'", string(data))
	}
	t.Log("Workspace mount verified - container wrote to host filesystem")
}

// TestDockerRunner_Integration_Cancellation tests context cancellation
func TestDockerRunner_Integration_Cancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	runner, err := NewDockerRunner()
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}

	workspaceDir, err := os.MkdirTemp("", "docker-runner-cancel-test-*")
	if err != nil {
		t.Fatalf("Failed to create workspace: %v", err)
	}
	defer os.RemoveAll(workspaceDir)

	config := &JobConfig{
		Image:        "alpine:latest",
		Command:      []string{"sh", "-c", "sleep 30"}, // Long-running job
		WorkspaceDir: workspaceDir,
		JobID:        "cancel-job-123",
		QueueName:    "test-queue",
	}

	// Create a cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	containerID, err := runner.SpawnJob(ctx, config)
	if err != nil {
		t.Fatalf("Failed to spawn job: %v", err)
	}
	defer runner.Cleanup(context.Background(), containerID) // Use background context for cleanup

	// Cancel the context after a short delay
	go func() {
		time.Sleep(1 * time.Second)
		t.Log("Cancelling context")
		cancel()
	}()

	// Wait should be interrupted by cancellation
	_, err = runner.WaitForCompletion(ctx, containerID)
	if err == nil {
		t.Error("Expected error from cancelled context")
	}
	t.Logf("Context cancellation handled correctly: %v", err)
}
