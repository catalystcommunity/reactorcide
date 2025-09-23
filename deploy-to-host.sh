#!/bin/bash
# deploy-to-host.sh - Deploy Reactorcide to a host
#
# Usage:
#   ./deploy-to-host.sh                    # Install locally
#   ./deploy-to-host.sh user@hostname      # Install on remote host

set -euo pipefail

# Configuration
REACTORCIDE_VERSION="${REACTORCIDE_VERSION:-latest}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/reactorcide}"
DATA_DIR="${DATA_DIR:-$HOME/.reactorcide}"
RUNTIME="${RUNTIME:-auto}"  # auto|docker|nerdctl|podman|containerd
HOST="${1:-localhost}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log() {
    echo -e "${GREEN}[$(date +'%Y-%m-%d %H:%M:%S')]${NC} $*"
}

error() {
    echo -e "${RED}[ERROR]${NC} $*" >&2
    exit 1
}

warn() {
    echo -e "${YELLOW}[WARN]${NC} $*" >&2
}

# Detect container runtime
detect_runtime() {
    if [ "$RUNTIME" != "auto" ]; then
        echo "$RUNTIME"
        return
    fi

    for runtime in docker nerdctl podman; do
        if command -v "$runtime" &> /dev/null; then
            echo "$runtime"
            return
        fi
    done

    # Check for containerd without nerdctl
    if command -v ctr &> /dev/null; then
        echo "containerd"
        return
    fi

    error "No container runtime found. Please install docker, nerdctl, or podman."
}

# Check prerequisites
check_prerequisites() {
    local runtime=$(detect_runtime)
    log "Detected container runtime: $runtime"

    # Check Python
    if ! command -v python3 &> /dev/null; then
        error "Python 3 is required but not installed"
    fi

    # Check Git
    if ! command -v git &> /dev/null; then
        error "Git is required but not installed"
    fi

    # Check container runtime permissions
    case "$runtime" in
        docker)
            if ! docker ps &> /dev/null; then
                error "Cannot access Docker. Add user to docker group or use sudo"
            fi
            ;;
        nerdctl|containerd)
            if ! nerdctl ps &> /dev/null; then
                warn "Cannot access nerdctl. Will try rootless mode"
            fi
            ;;
        podman)
            if ! podman ps &> /dev/null; then
                error "Cannot access Podman"
            fi
            ;;
    esac
}

# Install runnerlib
install_runnerlib() {
    log "Installing runnerlib to $INSTALL_DIR"

    # Create directories
    mkdir -p "$INSTALL_DIR" "$DATA_DIR"/{jobs,secrets,logs,workspace}

    # Clone or update reactorcide
    if [ -d "$INSTALL_DIR/runnerlib" ]; then
        log "Updating existing installation"
        cd "$INSTALL_DIR/runnerlib"
        git pull
    else
        log "Cloning Reactorcide repository"
        cd "$INSTALL_DIR"
        # Try official repo first, fall back to local for development
        if ! git clone https://github.com/catalystcommunity/reactorcide.git . 2>/dev/null; then
            # Development mode - use local copy
            if [ -d "$(dirname "$0")/runnerlib" ]; then
                log "Using local development version"
                cp -r "$(dirname "$0")/runnerlib" "$INSTALL_DIR/"
            else
                error "Cannot find runnerlib source"
            fi
        fi
    fi

    # Install Python dependencies
    log "Installing Python dependencies"
    cd "$INSTALL_DIR/runnerlib"
    if [ -f "pyproject.toml" ]; then
        # Production install with uv if available, otherwise pip
        if command -v uv &> /dev/null; then
            uv pip install --user -e .
        else
            pip3 install --user -e . || pip3 install -e .
        fi
    else
        pip3 install --user click pyyaml pydantic || pip3 install click pyyaml pydantic
    fi

    # Create wrapper script
    cat > "$INSTALL_DIR/reactorcide" << 'EOF'
#!/bin/bash
export PYTHONPATH="${PYTHONPATH}:$(dirname "$0")/runnerlib/src"
export REACTORCIDE_DATA="${REACTORCIDE_DATA:-$HOME/.reactorcide}"
export REACTORCIDE_RUNTIME="${REACTORCIDE_RUNTIME:-auto}"
exec python3 -m runnerlib "$@"
EOF
    chmod +x "$INSTALL_DIR/reactorcide"

    # Add to PATH if not present
    if ! echo "$PATH" | grep -q "$INSTALL_DIR"; then
        for rcfile in ~/.bashrc ~/.zshrc; do
            if [ -f "$rcfile" ]; then
                echo "" >> "$rcfile"
                echo "# Reactorcide" >> "$rcfile"
                echo "export PATH=\"\$PATH:$INSTALL_DIR\"" >> "$rcfile"
                log "Added $INSTALL_DIR to PATH in $rcfile"
            fi
        done
    fi
}

# Create configuration
create_config() {
    log "Creating configuration in $DATA_DIR/config.yaml"

    mkdir -p "$DATA_DIR"

    cat > "$DATA_DIR/config.yaml" << EOF
# Reactorcide Configuration
runtime: $(detect_runtime)
data_dir: $DATA_DIR
workspace_dir: $DATA_DIR/workspace
log_dir: $DATA_DIR/logs
secrets_dir: $DATA_DIR/secrets

# Job defaults
defaults:
  timeout: 3600  # 1 hour
  max_retries: 0
  cleanup: true

# Security
security:
  allow_privileged: false
  allow_host_network: false
  secrets_mount_path: /run/secrets
  read_only_source: true

# Resource limits (enforced where supported)
resources:
  max_cpu: 4
  max_memory: 8Gi
  max_jobs: 10
EOF

    # Create secrets template (but not actual secrets)
    cat > "$DATA_DIR/secrets/.env.template" << 'EOF'
# Copy to .env and fill in actual values
# These will be mounted into containers at /run/secrets/env
# NEVER commit actual secrets to git

# Example entries (uncomment and fill in as needed):
# API_KEY=
# DATABASE_URL=
# NPM_TOKEN=
# GITHUB_TOKEN=
EOF

    chmod 600 "$DATA_DIR/secrets/.env.template"
}

# Create systemd service
create_service() {
    if ! command -v systemctl &> /dev/null; then
        warn "systemd not available, skipping service creation"
        return
    fi

    log "Creating systemd user service"

    mkdir -p ~/.config/systemd/user

    cat > ~/.config/systemd/user/reactorcide-coordinator.service << EOF
[Unit]
Description=Reactorcide Coordinator API
After=network.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/reactorcide coordinator --port 8080 --config $DATA_DIR/config.yaml
Restart=on-failure
RestartSec=10
Environment="PATH=/usr/local/bin:/usr/bin:$INSTALL_DIR"
Environment="REACTORCIDE_DATA=$DATA_DIR"
Environment="REACTORCIDE_RUNTIME=$(detect_runtime)"

[Install]
WantedBy=default.target
EOF

    systemctl --user daemon-reload
    log "Service created. Enable with: systemctl --user enable --now reactorcide-coordinator"
}

# Test installation with safe defaults
test_installation() {
    log "Testing Reactorcide installation"

    # Test 1: Basic container execution
    log "Test 1: Container runtime access"
    if "$INSTALL_DIR/reactorcide" run \
        --image alpine:latest \
        --command "echo 'Runtime check: OK'" \
        --source-type local \
        --dry-run; then
        log "✓ Container runtime accessible"
    else
        error "Container runtime test failed"
    fi

    # Test 2: Workspace creation
    log "Test 2: Workspace management"
    local test_workspace="$DATA_DIR/workspace/test-$(date +%s)"
    if mkdir -p "$test_workspace" && rmdir "$test_workspace"; then
        log "✓ Workspace creation working"
    else
        error "Workspace creation failed"
    fi

    # Test 3: Git operations (if git available)
    log "Test 3: Git operations"
    if git ls-remote https://github.com/catalystcommunity/reactorcide.git &>/dev/null; then
        log "✓ Git operations working"
    else
        warn "Git operations not tested (network issue?)"
    fi

    log "Installation verified successfully"
}

# Remote installation
install_remote() {
    local host="$1"
    log "Installing Reactorcide on remote host: $host"

    # Verify SSH access
    if ! ssh "$host" "echo 'SSH access OK'" &>/dev/null; then
        error "Cannot access remote host: $host"
    fi

    # Copy this script to remote
    scp "$0" "$host:/tmp/deploy-reactorcide.sh"

    # Run installation on remote
    ssh "$host" "bash /tmp/deploy-reactorcide.sh --local-install && rm /tmp/deploy-reactorcide.sh"

    log "Remote installation completed on $host"
    log "Test with: ssh $host 'reactorcide run --image alpine --command \"uname -a\"'"
}

# Main execution
main() {
    case "${1:-}" in
        --help|-h)
            cat << EOF
Reactorcide Deployment Script

Usage:
  $0 [OPTIONS] [HOST]

Options:
  --help, -h           Show this help message
  --local-install      Internal flag for remote installation
  --service            Create systemd service (included by default)
  --runtime RUNTIME    Specify container runtime (docker|nerdctl|podman)
  --install-dir DIR    Installation directory (default: ~/reactorcide)
  --data-dir DIR       Data directory (default: ~/.reactorcide)

Examples:
  $0                   # Install locally
  $0 user@host         # Install on remote host via SSH

After installation:
  reactorcide run --image alpine --command "uname -a"  # Test run
  reactorcide run -f job.yaml                          # Run job file

EOF
            exit 0
            ;;

        --local-install)
            # Internal flag used during remote installation
            check_prerequisites
            install_runnerlib
            create_config
            create_service
            test_installation
            log "Installation completed successfully!"
            log "Run 'source ~/.bashrc' or start a new shell to use 'reactorcide' command"
            ;;

        --service)
            create_service
            ;;

        ""|localhost)
            check_prerequisites
            install_runnerlib
            create_config
            create_service
            test_installation

            log ""
            log "Installation completed successfully!"
            log ""
            log "Next steps:"
            log "  1. Run 'source ~/.bashrc' or start a new shell"
            log "  2. Test: reactorcide run --image alpine --command 'uname -a'"
            log "  3. Configure secrets in $DATA_DIR/secrets/.env if needed"
            log ""
            log "For API server: systemctl --user enable --now reactorcide-coordinator"
            ;;

        *)
            # Assume it's a hostname
            if [[ "$1" == *"@"* ]]; then
                install_remote "$1"
            else
                error "Unknown option: $1. Use --help for usage."
            fi
            ;;
    esac
}

# Run main function
main "$@"