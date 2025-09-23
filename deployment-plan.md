# Reactorcide Deployment Plan - HT0-Bastion Production Deployment

## Deployment Overview

This plan outlines the step-by-step process to deploy Reactorcide to the HT0-Bastion VM with:
- **Domain**: reactorcide.todandlorna.local
- **Components**: Runnerlib (job runner), Coordinator API, PostgreSQL, MinIO/Storage
- **Goal**: Fully functional CI/CD system accessible via web API

## Prerequisites

### System Requirements
- **VM**: HT0-Bastion with Ubuntu 22.04 or similar
- **Domain**: reactorcide.todandlorna.local (DNS configured)
- **Container Runtime**: Docker or nerdctl installed
- **Python**: 3.9+ with pip
- **Go**: 1.21+ (for building coordinator API)
- **PostgreSQL**: 14+ (or SQLite for simpler setup)
- **Storage**: 20GB+ for logs/artifacts

### Network Requirements
- **Ports**:
  - 80/443 (HTTP/HTTPS via reverse proxy)
  - 8080 (Coordinator API - internal)
  - 5432 (PostgreSQL - internal)
  - 9000/9001 (MinIO - internal)
- **Firewall**: Allow HTTP/HTTPS from external, restrict internal services

## Phase 1: Infrastructure Setup (Day 1)

### Step 1.1: Prepare VM Access
```bash
# Create SSH key pair if needed
ssh-keygen -t ed25519 -f ~/.ssh/ht0-bastion -C "reactorcide@ht0-bastion"

# Copy public key to VM
ssh-copy-id -i ~/.ssh/ht0-bastion.pub user@ht0-bastion

# Test connection
ssh -i ~/.ssh/ht0-bastion user@ht0-bastion
```

### Step 1.2: Install System Dependencies
```bash
# On HT0-Bastion
sudo apt update && sudo apt upgrade -y

# Install core dependencies
sudo apt install -y \
  curl wget git vim \
  build-essential python3-pip python3-venv \
  postgresql postgresql-client \
  nginx certbot python3-certbot-nginx \
  docker.io docker-compose

# Add user to docker group
sudo usermod -aG docker $USER
# Log out and back in for group change to take effect
```

### Step 1.3: Install Go (for building coordinator API)
```bash
# Download and install Go
wget https://go.dev/dl/go1.23.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.23.0.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
go version
```

### Step 1.4: Setup PostgreSQL Database
```bash
# Create database and user
sudo -u postgres psql << EOF
CREATE USER reactorcide WITH PASSWORD 'secure_password_here';
CREATE DATABASE reactorcide_db OWNER reactorcide;
GRANT ALL PRIVILEGES ON DATABASE reactorcide_db TO reactorcide;
EOF

# Enable PostgreSQL to accept local connections
sudo vim /etc/postgresql/14/main/postgresql.conf
# Ensure: listen_addresses = 'localhost'

sudo systemctl restart postgresql
```

### Step 1.5: Setup Storage (Option A: MinIO)
```bash
# Install MinIO
wget https://dl.min.io/server/minio/release/linux-amd64/minio
chmod +x minio
sudo mv minio /usr/local/bin/

# Create MinIO data directory
sudo mkdir -p /data/minio
sudo chown $USER:$USER /data/minio

# Create MinIO service
sudo tee /etc/systemd/system/minio.service << EOF
[Unit]
Description=MinIO
After=network.target

[Service]
User=$USER
Group=$USER
Environment="MINIO_ROOT_USER=minioadmin"
Environment="MINIO_ROOT_PASSWORD=secure_password_here"
ExecStart=/usr/local/bin/minio server /data/minio --console-address ":9001"
Restart=always

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now minio

# Create buckets
export MC_HOST_minio=http://minioadmin:secure_password_here@localhost:9000
mc mb minio/reactorcide-logs
mc mb minio/reactorcide-artifacts
```

### Step 1.5: Setup Storage (Option B: Filesystem)
```bash
# Simpler option - just use filesystem
sudo mkdir -p /data/reactorcide/{logs,artifacts,workspace}
sudo chown -R $USER:$USER /data/reactorcide
```

## Phase 2: Deploy Runnerlib (Day 1)

### Step 2.1: Clone Repository
```bash
cd ~
git clone https://github.com/catalystcommunity/reactorcide.git
cd reactorcide
```

### Step 2.2: Install Runnerlib
```bash
cd runnerlib

# Create virtual environment
python3 -m venv venv
source venv/bin/activate

# Install dependencies
pip install -e .
pip install click pyyaml pydantic

# Test installation
python -m runnerlib --help
```

### Step 2.3: Configure Runnerlib
```bash
# Create config directory
mkdir -p ~/.reactorcide/{jobs,secrets,logs,workspace}

# Create runnerlib config
cat > ~/.reactorcide/runnerlib-config.yaml << EOF
runtime: docker
workspace_dir: /home/$USER/.reactorcide/workspace
log_dir: /home/$USER/.reactorcide/logs
secrets_dir: /home/$USER/.reactorcide/secrets

defaults:
  timeout: 3600
  cleanup: true

security:
  allow_privileged: false
  allow_host_network: false
  read_only_source: true
EOF

# Create secrets template
cat > ~/.reactorcide/secrets/.env << EOF
# Add your secrets here
# GITHUB_TOKEN=
# NPM_TOKEN=
# API_KEY=
EOF

chmod 600 ~/.reactorcide/secrets/.env
```

### Step 2.4: Test Runnerlib
```bash
# Test basic execution
python -m runnerlib run \
  --image alpine:latest \
  --command "echo 'Runnerlib test successful'" \
  --dry-run

# Test with actual container
python -m runnerlib run \
  --image alpine:latest \
  --command "uname -a"
```

## Phase 3: Build and Deploy Coordinator API (Day 1-2)

### Step 3.1: Build Coordinator API
```bash
cd ~/reactorcide

# Build the binary
cd coordinator_api
go mod download
go build -o coordinator-api .

# Test the binary
./coordinator-api --help
```

### Step 3.2: Configure Coordinator API
```bash
# Create production config
cat > ~/reactorcide/config.production.yaml << EOF
database:
  url: postgres://reactorcide:secure_password_here@localhost:5432/reactorcide_db?sslmode=disable

object_storage:
  type: filesystem  # or 's3' if using MinIO
  base_path: /data/reactorcide
  # For MinIO:
  # endpoint: http://localhost:9000
  # access_key: minioadmin
  # secret_key: secure_password_here
  # bucket_logs: reactorcide-logs
  # bucket_artifacts: reactorcide-artifacts

api:
  port: 8080
  host: 0.0.0.0
  jwt_secret: $(openssl rand -hex 32)
  log_level: info

worker:
  concurrency: 2
  poll_interval: 5s
  timeout: 3600s

runnerlib:
  runner_image: ""
  git_ref: main
  code_dir: /job/code
  job_dir: /job
EOF
```

### Step 3.3: Run Database Migrations
```bash
cd ~/reactorcide/coordinator_api

# Set environment variables
export DB_URI="postgres://reactorcide:secure_password_here@localhost:5432/reactorcide_db?sslmode=disable"

# Run migrations
./coordinator-api migrate
```

### Step 3.4: Create Systemd Service for Coordinator API
```bash
sudo tee /etc/systemd/system/reactorcide-api.service << EOF
[Unit]
Description=Reactorcide Coordinator API
After=network.target postgresql.service

[Service]
Type=simple
User=$USER
Group=$USER
WorkingDirectory=/home/$USER/reactorcide
Environment="DB_URI=postgres://reactorcide:secure_password_here@localhost:5432/reactorcide_db?sslmode=disable"
Environment="PORT=8080"
Environment="OBJECT_STORE_BASE_PATH=/data/reactorcide"
Environment="DEFAULT_USER_ID=550e8400-e29b-41d4-a716-446655440000"
ExecStart=/home/$USER/reactorcide/coordinator_api/coordinator-api serve
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

# Enable and start the service
sudo systemctl daemon-reload
sudo systemctl enable --now reactorcide-api

# Check status
sudo systemctl status reactorcide-api
curl http://localhost:8080/api/health
```

## Phase 4: Configure Reverse Proxy (Day 2)

### Step 4.1: Configure Nginx
```bash
# Create Nginx site config
sudo tee /etc/nginx/sites-available/reactorcide << EOF
server {
    listen 80;
    server_name reactorcide.todandlorna.local;

    # API endpoints
    location /api/ {
        proxy_pass http://localhost:8080/api/;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;

        # WebSocket support (if needed)
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
    }

    # Health check endpoint
    location /health {
        proxy_pass http://localhost:8080/api/health;
    }

    # Webhook endpoint
    location /webhook {
        proxy_pass http://localhost:8080/webhook;
        proxy_set_header X-GitHub-Event \$http_x_github_event;
        proxy_set_header X-GitLab-Event \$http_x_gitlab_event;
    }

    # Default
    location / {
        return 200 '{"status": "Reactorcide CI/CD System"}';
        add_header Content-Type application/json;
    }
}
EOF

# Enable the site
sudo ln -s /etc/nginx/sites-available/reactorcide /etc/nginx/sites-enabled/
sudo nginx -t
sudo systemctl reload nginx
```

### Step 4.2: Setup SSL (Optional but Recommended)
```bash
# For self-signed certificate (development)
sudo openssl req -x509 -nodes -days 365 -newkey rsa:2048 \
  -keyout /etc/ssl/private/reactorcide.key \
  -out /etc/ssl/certs/reactorcide.crt \
  -subj "/CN=reactorcide.todandlorna.local"

# Update Nginx config to use SSL
sudo vim /etc/nginx/sites-available/reactorcide
# Add:
# listen 443 ssl;
# ssl_certificate /etc/ssl/certs/reactorcide.crt;
# ssl_certificate_key /etc/ssl/private/reactorcide.key;
```

## Phase 5: Create Worker Service (Day 2)

### Step 5.1: Create Worker Service
```bash
sudo tee /etc/systemd/system/reactorcide-worker.service << EOF
[Unit]
Description=Reactorcide Worker
After=network.target reactorcide-api.service

[Service]
Type=simple
User=$USER
Group=$USER
WorkingDirectory=/home/$USER/reactorcide
Environment="DB_URI=postgres://reactorcide:secure_password_here@localhost:5432/reactorcide_db?sslmode=disable"
Environment="PYTHONPATH=/home/$USER/reactorcide/runnerlib"
ExecStart=/home/$USER/reactorcide/coordinator_api/coordinator-api worker
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now reactorcide-worker
```

## Phase 6: Testing and Validation (Day 2-3)

### Step 6.1: Create Test Job Files
```bash
# Create test job directory
mkdir -p ~/reactorcide/test-jobs

# Simple test job
cat > ~/reactorcide/test-jobs/hello-world.yaml << EOF
name: hello-world-test
image: alpine:latest
command: |
  echo "Hello from Reactorcide!"
  echo "Hostname: \$(hostname)"
  echo "Date: \$(date)"
  ls -la /
EOF

# Build test job
cat > ~/reactorcide/test-jobs/build-test.yaml << EOF
name: build-test
source:
  type: git
  repo: https://github.com/example/test-repo.git
  ref: main
image: node:18
command: |
  cd /job/code
  npm install
  npm test
  npm run build
EOF
```

### Step 6.2: Test Job Submission via API
```bash
# Get API token (created during migration)
export DB_URI="postgres://reactorcide:secure_password_here@localhost:5432/reactorcide_db?sslmode=disable"
TOKEN=$(psql $DB_URI -t -c "SELECT token FROM api_tokens LIMIT 1;" | xargs)

# Submit job via API
curl -X POST http://reactorcide.todandlorna.local/api/jobs \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test-job",
    "image": "alpine:latest",
    "command": "echo \"API test successful\"",
    "source_type": "local"
  }'

# Check job status
curl http://reactorcide.todandlorna.local/api/jobs \
  -H "Authorization: Bearer $TOKEN"
```

### Step 6.3: Test Direct Runnerlib Execution
```bash
cd ~/reactorcide
source runnerlib/venv/bin/activate

# Test with local job file
python -m runnerlib run-job test-jobs/hello-world.yaml

# Test with git checkout
python -m runnerlib run \
  --source-type git \
  --source-repo https://github.com/example/test-repo.git \
  --source-ref main \
  --image node:18 \
  --command "npm test"
```

### Step 6.4: Create Test Script
```bash
cat > ~/reactorcide/test-deployment.sh << 'EOF'
#!/bin/bash
set -e

echo "Testing Reactorcide Deployment..."

# Test 1: Health check
echo -n "1. API Health Check: "
if curl -sf http://localhost:8080/api/health > /dev/null; then
  echo "✓ PASS"
else
  echo "✗ FAIL"
  exit 1
fi

# Test 2: Runnerlib execution
echo -n "2. Runnerlib Execution: "
cd ~/reactorcide
source runnerlib/venv/bin/activate
if python -m runnerlib run --image alpine --command "true" 2>/dev/null; then
  echo "✓ PASS"
else
  echo "✗ FAIL"
  exit 1
fi

# Test 3: PostgreSQL connection
echo -n "3. Database Connection: "
if psql "postgres://reactorcide:secure_password_here@localhost:5432/reactorcide_db?sslmode=disable" -c "SELECT 1" > /dev/null 2>&1; then
  echo "✓ PASS"
else
  echo "✗ FAIL"
  exit 1
fi

# Test 4: Nginx proxy
echo -n "4. Nginx Proxy: "
if curl -sf http://reactorcide.todandlorna.local/health > /dev/null; then
  echo "✓ PASS"
else
  echo "✗ FAIL"
  exit 1
fi

echo ""
echo "All tests passed! Reactorcide is operational."
EOF

chmod +x ~/reactorcide/test-deployment.sh
~/reactorcide/test-deployment.sh
```

## Phase 7: Production Hardening (Day 3)

### Step 7.1: Security Hardening
```bash
# Firewall configuration
sudo ufw allow 22/tcp   # SSH
sudo ufw allow 80/tcp   # HTTP
sudo ufw allow 443/tcp  # HTTPS
sudo ufw --force enable

# Fail2ban for brute force protection
sudo apt install fail2ban
sudo systemctl enable --now fail2ban

# Secure secrets
chmod 600 ~/.reactorcide/secrets/.env
chmod 700 ~/.reactorcide/secrets
```

### Step 7.2: Monitoring Setup
```bash
# Install monitoring tools
sudo apt install htop iotop nethogs

# Create log rotation config
sudo tee /etc/logrotate.d/reactorcide << EOF
/home/$USER/.reactorcide/logs/*.log {
    daily
    rotate 14
    compress
    delaycompress
    missingok
    notifempty
    create 644 $USER $USER
}
EOF

# Create monitoring script
cat > ~/reactorcide/monitor.sh << 'EOF'
#!/bin/bash
# Simple monitoring script
while true; do
  STATUS=$(systemctl is-active reactorcide-api)
  if [ "$STATUS" != "active" ]; then
    echo "$(date): API service down, attempting restart"
    sudo systemctl restart reactorcide-api
  fi
  sleep 60
done
EOF
chmod +x ~/reactorcide/monitor.sh
```

### Step 7.3: Backup Configuration
```bash
# Create backup script
cat > ~/reactorcide/backup.sh << 'EOF'
#!/bin/bash
BACKUP_DIR="/backup/reactorcide/$(date +%Y%m%d)"
mkdir -p "$BACKUP_DIR"

# Backup database
pg_dump "postgres://reactorcide:secure_password_here@localhost:5432/reactorcide_db?sslmode=disable" \
  > "$BACKUP_DIR/database.sql"

# Backup configuration
cp -r ~/.reactorcide "$BACKUP_DIR/config"
cp ~/reactorcide/config.production.yaml "$BACKUP_DIR/"

# Backup logs
tar czf "$BACKUP_DIR/logs.tar.gz" ~/.reactorcide/logs/

echo "Backup completed: $BACKUP_DIR"
EOF

chmod +x ~/reactorcide/backup.sh

# Add to crontab for daily backups
(crontab -l 2>/dev/null; echo "0 2 * * * /home/$USER/reactorcide/backup.sh") | crontab -
```

## Phase 8: Integration with External Services (Day 3-4)

### Step 8.1: GitHub Webhook Integration
```bash
# Configure GitHub webhook
# In GitHub repo settings, add webhook:
# - URL: https://reactorcide.todandlorna.local/webhook
# - Content type: application/json
# - Secret: generate with: openssl rand -hex 32
# - Events: Push, Pull Request

# Update coordinator API config with webhook secret
export VCS_GITHUB_SECRET="your-webhook-secret"
sudo systemctl restart reactorcide-api
```

### Step 8.2: Create Job Definitions Repository
```bash
# Create jobs repository
mkdir -p ~/reactorcide-jobs
cd ~/reactorcide-jobs
git init

# Create sample job definitions
cat > pr-test.yaml << EOF
name: pr-test
source:
  type: git
  repo: \${GITHUB_REPO}
  ref: \${GITHUB_SHA}
image: node:18
command: |
  cd /job/code
  npm ci
  npm test
timeout: 1800
EOF

cat > main-build.yaml << EOF
name: main-build
source:
  type: git
  repo: \${GITHUB_REPO}
  ref: main
image: node:18
command: |
  cd /job/code
  npm ci
  npm test
  npm run build
artifacts:
  - path: dist/
    name: build-output
timeout: 3600
EOF

git add .
git commit -m "Initial job definitions"
```

## Troubleshooting Guide

### Common Issues and Solutions

1. **Container runtime not accessible**
   ```bash
   # Check Docker service
   sudo systemctl status docker
   # Add user to docker group
   sudo usermod -aG docker $USER
   # Logout and login again
   ```

2. **PostgreSQL connection refused**
   ```bash
   # Check PostgreSQL is running
   sudo systemctl status postgresql
   # Check pg_hba.conf for local connections
   sudo vim /etc/postgresql/14/main/pg_hba.conf
   # Should have: local all all md5
   ```

3. **API not responding**
   ```bash
   # Check service logs
   sudo journalctl -u reactorcide-api -f
   # Check if port is listening
   sudo netstat -tlnp | grep 8080
   ```

4. **Jobs failing to execute**
   ```bash
   # Check worker logs
   sudo journalctl -u reactorcide-worker -f
   # Test runnerlib directly
   python -m runnerlib run --image alpine --command "echo test"
   ```

## Verification Checklist

- [ ] VM accessible via SSH
- [ ] Docker/container runtime working
- [ ] PostgreSQL database created and accessible
- [ ] MinIO or filesystem storage configured
- [ ] Runnerlib installed and tested
- [ ] Coordinator API built and running
- [ ] Database migrations completed
- [ ] Nginx reverse proxy configured
- [ ] Domain resolving correctly
- [ ] API health check passing
- [ ] Test job execution successful
- [ ] Worker processing jobs
- [ ] Logs being generated
- [ ] Backup script configured

## Next Steps After Deployment

1. **Configure Authentication**
   - Setup OAuth2/OIDC integration
   - Create user management system
   - Generate API tokens for services

2. **Setup CI/CD Pipelines**
   - Create job definitions for your projects
   - Configure webhooks for repositories
   - Setup branch protection rules

3. **Monitoring and Alerting**
   - Deploy Prometheus/Grafana
   - Configure alert rules
   - Setup log aggregation

4. **Scaling Considerations**
   - Add more workers for parallel execution
   - Consider Kubernetes deployment for scale
   - Implement job queuing with Corndogs

## Support and Maintenance

### Regular Maintenance Tasks
- Check disk usage: `df -h`
- Review logs: `sudo journalctl -u reactorcide-api --since="1 day ago"`
- Update system: `sudo apt update && sudo apt upgrade`
- Backup database: `~/reactorcide/backup.sh`

### Monitoring Commands
```bash
# Check all services
systemctl status reactorcide-api reactorcide-worker nginx postgresql

# View recent logs
sudo journalctl -u reactorcide-api -n 100

# Check resource usage
htop
docker stats
```

### Emergency Procedures
```bash
# Restart all services
sudo systemctl restart reactorcide-api reactorcide-worker

# Clear stuck jobs
psql $DB_URI -c "UPDATE jobs SET status='failed' WHERE status='running' AND updated_at < NOW() - INTERVAL '1 hour';"

# Full system restart
sudo systemctl stop reactorcide-worker reactorcide-api
sudo systemctl start postgresql nginx
sudo systemctl start reactorcide-api reactorcide-worker
```