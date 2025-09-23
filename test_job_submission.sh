#!/bin/bash
set -e

echo "=== Testing Reactorcide Job Submission to Corndogs ==="
echo ""

# For testing, we'll use a simple approach since we can't retrieve the hashed token
# In production, you'd have proper token management

# First, let's check if we can access the database directly to create a test token
echo "Creating test user and token in database..."
docker compose exec -T postgres psql -U devuser -d testpg <<EOF
-- Create a test user
INSERT INTO users (user_id, email, username, created_at, updated_at)
VALUES ('test-user-001', 'test@example.com', 'testuser', NOW(), NOW())
ON CONFLICT (user_id) DO NOTHING;

-- Create a test API token (unhashed for testing - DO NOT DO THIS IN PRODUCTION)
-- In production, tokens should be properly hashed
INSERT INTO api_tokens (token_id, user_id, name, token_hash, created_at, last_used_at)
VALUES ('test-token-001', 'test-user-001', 'Test Token', 'test-token-value', NOW(), NOW())
ON CONFLICT (token_id) DO UPDATE SET last_used_at = NOW();
EOF

echo ""
echo "Testing API health check..."
curl -s http://localhost:8080/api/v1/health | jq

echo ""
echo "Testing job submission (this will fail without proper auth, which is expected)..."
echo "Creating job payload..."

# Create a test job payload
cat > test_job.json <<EOF
{
  "name": "Test Job",
  "description": "Testing Corndogs integration",
  "config": {
    "image": "alpine:latest",
    "command": ["echo", "Hello from Reactorcide!"],
    "environment": {
      "TEST_VAR": "test_value"
    },
    "working_dir": "/job"
  },
  "metadata": {
    "source": "test_script",
    "test_run": "true"
  }
}
EOF

echo "Submitting job to API..."
# Note: This will fail because we need proper authentication
# The token_hash in the database needs to match the hash of the Authorization header
response=$(curl -s -X POST \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-token-value" \
  -d @test_job.json \
  http://localhost:8080/api/v1/jobs)

echo "Response:"
echo "$response" | jq . 2>/dev/null || echo "$response"

echo ""
echo "Checking if job was created in database..."
docker compose exec postgres psql -U devuser -d testpg -c "SELECT job_id, name, status, created_at FROM jobs ORDER BY created_at DESC LIMIT 5;"

echo ""
echo "Note: Job submission likely failed due to authentication. In production, you would:"
echo "1. Create users through proper registration flow"
echo "2. Generate API tokens with proper hashing"
echo "3. Use the actual token value (not the hash) in API requests"
echo ""
echo "For full testing, we need to implement a proper token generation endpoint or test helper."