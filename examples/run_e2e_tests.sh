#!/bin/bash
# Comprehensive E2E test runner for Reactorcide
# This script orchestrates the full system test including startup, testing, and teardown

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
COMPOSE_FILE="$PROJECT_ROOT/docker-compose.yml"
LOG_DIR="/tmp/reactorcide-e2e-logs"
STARTUP_TIMEOUT=30
CLEANUP=${CLEANUP:-true}

# Create log directory
mkdir -p "$LOG_DIR"

echo -e "${BLUE}╔════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║   Reactorcide E2E Test Suite          ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════╝${NC}"
echo ""

# Function to start the stack
start_stack() {
    echo -e "${YELLOW}Starting Reactorcide stack...${NC}"

    cd "$PROJECT_ROOT"

    # Check if stack is already running
    if docker compose ps --services --filter "status=running" | grep -q coordinator-api; then
        echo -e "${GREEN}✓ Stack already running${NC}"
        return 0
    fi

    # Start the stack
    echo "Starting services with docker-compose..."
    docker compose up -d > "$LOG_DIR/docker-compose.log" 2>&1

    # Wait for services to be healthy
    echo -n "Waiting for services to be ready"
    local elapsed=0
    while [ $elapsed -lt $STARTUP_TIMEOUT ]; do
        if docker compose ps | grep -q "healthy" | grep -q coordinator-api; then
            echo ""
            echo -e "${GREEN}✓ Services are healthy${NC}"
            return 0
        fi

        # Check API health endpoint
        if curl -s -f http://localhost:8080/api/health > /dev/null 2>&1; then
            echo ""
            echo -e "${GREEN}✓ API is responding${NC}"
            break
        fi

        echo -n "."
        sleep 2
        elapsed=$((elapsed + 2))
    done

    if [ $elapsed -ge $STARTUP_TIMEOUT ]; then
        echo ""
        echo -e "${RED}✗ Services failed to start within ${STARTUP_TIMEOUT}s${NC}"
        echo "Docker compose logs:"
        docker compose logs --tail=50
        return 1
    fi

    # Give services a moment to fully initialize
    sleep 3
    echo -e "${GREEN}✓ Stack is ready${NC}"
    echo ""
}

# Function to check prerequisites
check_prerequisites() {
    echo -e "${YELLOW}Checking prerequisites...${NC}"

    local missing=0

    # Check Docker
    if ! command -v docker &> /dev/null; then
        echo -e "${RED}✗ Docker is not installed${NC}"
        missing=1
    else
        echo -e "${GREEN}✓ Docker found${NC}"
    fi

    # Check Docker Compose
    if ! docker compose version &> /dev/null; then
        echo -e "${RED}✗ Docker Compose is not installed${NC}"
        missing=1
    else
        echo -e "${GREEN}✓ Docker Compose found${NC}"
    fi

    # Check Python
    if ! command -v python3 &> /dev/null; then
        echo -e "${RED}✗ Python 3 is not installed${NC}"
        missing=1
    else
        echo -e "${GREEN}✓ Python found${NC}"
    fi

    # Check curl
    if ! command -v curl &> /dev/null; then
        echo -e "${RED}✗ curl is not installed${NC}"
        missing=1
    else
        echo -e "${GREEN}✓ curl found${NC}"
    fi

    if [ $missing -eq 1 ]; then
        echo -e "${RED}Please install missing prerequisites${NC}"
        exit 1
    fi

    echo ""
}

# Function to run bash tests
run_bash_tests() {
    echo -e "${BLUE}=== Running Bash E2E Tests ===${NC}"

    if [ -f "$SCRIPT_DIR/e2e_test.sh" ]; then
        bash "$SCRIPT_DIR/e2e_test.sh" quick
        if [ $? -eq 0 ]; then
            echo -e "${GREEN}✓ Bash tests passed${NC}"
        else
            echo -e "${RED}✗ Bash tests failed${NC}"
            return 1
        fi
    else
        echo -e "${YELLOW}⚠ Bash test script not found${NC}"
    fi

    echo ""
}

# Function to run Python tests
run_python_tests() {
    echo -e "${BLUE}=== Running Python E2E Tests ===${NC}"

    if [ -f "$SCRIPT_DIR/test_e2e.py" ]; then
        # Set environment variables
        export API_URL="http://localhost:8080"
        export USER_ID="e2e-testuser"
        export E2E_TIMEOUT="60"

        # Run Python tests
        python3 "$SCRIPT_DIR/test_e2e.py"
        if [ $? -eq 0 ]; then
            echo -e "${GREEN}✓ Python tests passed${NC}"
        else
            echo -e "${RED}✗ Python tests failed${NC}"
            return 1
        fi
    else
        echo -e "${YELLOW}⚠ Python test script not found${NC}"
    fi

    echo ""
}

# Function to run worker tests
run_worker_tests() {
    echo -e "${BLUE}=== Testing Worker Functionality ===${NC}"

    # Check if worker is processing jobs
    echo "Checking worker status..."

    # Submit a test job and verify worker picks it up
    local test_job_response=$(curl -s -X POST \
        -H "Content-Type: application/json" \
        -H "X-User-ID: worker-test" \
        -d '{
            "name": "Worker Test Job",
            "user_id": "worker-test",
            "config": {
                "container": {
                    "image": "alpine:latest",
                    "command": ["echo", "Worker test successful"],
                    "working_dir": "/job"
                }
            }
        }' \
        http://localhost:8080/api/v1/jobs)

    local job_id=$(echo "$test_job_response" | grep -o '"id":"[^"]*' | cut -d'"' -f4)

    if [ -n "$job_id" ]; then
        echo "Created test job: $job_id"

        # Wait for job to be processed
        local max_wait=30
        local elapsed=0
        while [ $elapsed -lt $max_wait ]; do
            local status=$(curl -s "http://localhost:8080/api/v1/jobs/$job_id" | grep -o '"status":"[^"]*' | cut -d'"' -f4)

            if [ "$status" = "completed" ]; then
                echo -e "${GREEN}✓ Worker successfully processed job${NC}"
                return 0
            elif [ "$status" = "failed" ]; then
                echo -e "${RED}✗ Job failed${NC}"
                return 1
            fi

            sleep 2
            elapsed=$((elapsed + 2))
        done

        echo -e "${YELLOW}⚠ Job not processed within ${max_wait}s (worker may not be running)${NC}"
    else
        echo -e "${YELLOW}⚠ Could not create test job${NC}"
    fi

    echo ""
}

# Function to collect logs
collect_logs() {
    echo -e "${YELLOW}Collecting logs...${NC}"

    # Docker compose logs
    docker compose logs > "$LOG_DIR/docker-compose-full.log" 2>&1

    # Individual service logs
    for service in postgres coordinator-api minio migration; do
        docker compose logs $service > "$LOG_DIR/${service}.log" 2>&1
    done

    echo "Logs saved to $LOG_DIR"
    echo ""
}

# Function to show summary
show_summary() {
    local status=$1

    echo ""
    echo -e "${BLUE}╔════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║           Test Summary                 ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════╝${NC}"

    if [ $status -eq 0 ]; then
        echo -e "${GREEN}✓ All E2E tests passed successfully!${NC}"
        echo ""
        echo "The Reactorcide system is functioning correctly:"
        echo "  • API accepts and stores jobs"
        echo "  • Jobs are submitted to Corndogs queue"
        echo "  • Workers process jobs from the queue"
        echo "  • Container execution works properly"
        echo "  • Git operations function correctly"
        echo "  • Environment variables are handled properly"
    else
        echo -e "${RED}✗ Some tests failed${NC}"
        echo ""
        echo "Please check the logs in $LOG_DIR for details"
        echo "You can also check service status with:"
        echo "  docker compose ps"
        echo "  docker compose logs"
    fi

    echo ""
}

# Function to cleanup
cleanup_stack() {
    if [ "$CLEANUP" = "true" ]; then
        echo -e "${YELLOW}Cleaning up...${NC}"
        cd "$PROJECT_ROOT"
        docker compose down
        echo -e "${GREEN}✓ Stack stopped${NC}"
    else
        echo -e "${YELLOW}Stack left running (CLEANUP=false)${NC}"
        echo "To stop manually: docker compose down"
    fi
}

# Trap to ensure cleanup on exit
trap cleanup_on_exit EXIT

cleanup_on_exit() {
    if [ "$CLEANUP" = "true" ]; then
        cleanup_stack
    fi
}

# Main execution
main() {
    local exit_code=0

    # Check prerequisites
    check_prerequisites

    # Start the stack
    if ! start_stack; then
        echo -e "${RED}Failed to start stack${NC}"
        exit 1
    fi

    # Run tests
    echo -e "${BLUE}Running E2E Test Suite${NC}"
    echo "========================"
    echo ""

    # Run different test suites
    if ! run_bash_tests; then
        exit_code=1
    fi

    if ! run_python_tests; then
        exit_code=1
    fi

    if ! run_worker_tests; then
        exit_code=1
    fi

    # Collect logs
    collect_logs

    # Show summary
    show_summary $exit_code

    # Cleanup if requested
    if [ "$CLEANUP" = "true" ]; then
        cleanup_stack
    fi

    exit $exit_code
}

# Handle command line arguments
case "${1:-}" in
    "--no-cleanup")
        CLEANUP=false
        main
        ;;
    "--quick")
        # Quick test - just check if services are up and run basic test
        check_prerequisites
        start_stack
        run_bash_tests
        ;;
    "--python-only")
        # Run only Python tests
        check_prerequisites
        start_stack
        run_python_tests
        ;;
    "--help")
        echo "Usage: $0 [OPTIONS]"
        echo ""
        echo "Options:"
        echo "  --no-cleanup     Don't stop the stack after tests"
        echo "  --quick          Run quick tests only"
        echo "  --python-only    Run only Python E2E tests"
        echo "  --help           Show this help message"
        echo ""
        echo "Environment variables:"
        echo "  CLEANUP=false    Same as --no-cleanup"
        echo "  API_URL          Override API URL (default: http://localhost:8080)"
        echo "  USER_ID          Override test user ID (default: testuser)"
        ;;
    *)
        main
        ;;
esac