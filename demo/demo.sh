#!/usr/bin/env bash
set -euo pipefail

# ─── Paths ────────────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CLI="$REPO_ROOT/coordinator_api/reactorcide"
CONFIG_FILE="$SCRIPT_DIR/demo-config.yaml"

# ─── Colors & Output ─────────────────────────────────────────────────────────
BOLD='\033[1m'
DIM='\033[2m'
CYAN='\033[36m'
GREEN='\033[32m'
YELLOW='\033[33m'
RED='\033[31m'
RESET='\033[0m'

step()  { echo -e "\n${BOLD}${CYAN}>>> $*${RESET}"; }
info()  { echo -e "    ${DIM}$*${RESET}"; }
ok()    { echo -e "    ${GREEN}$*${RESET}"; }
warn()  { echo -e "    ${YELLOW}$*${RESET}"; }
err()   { echo -e "    ${RED}$*${RESET}" >&2; }

# ─── Config File Loading ─────────────────────────────────────────────────────
# Reads demo-config.yaml (simple key: value YAML) and exports values as env
# vars. Env vars already set take precedence (config file is defaults only).
load_config() {
    if [[ ! -f "$CONFIG_FILE" ]]; then
        return
    fi
    info "Loading config from demo-config.yaml"

    # Map of yaml key -> env var name
    local -A key_map=(
        [api_url]=REACTORCIDE_API_URL
        [registry]=DEMO_REGISTRY
        [registry_user]=REGISTRY_USER
        [image]=DEMO_IMAGE
        [tag]=DEMO_TAG
        [backend]=REACTORCIDE_BACKEND
    )

    while IFS= read -r line; do
        # Skip comments and blank lines
        [[ "$line" =~ ^[[:space:]]*# ]] && continue
        [[ -z "${line// /}" ]] && continue

        # Parse "key: value" (strip quotes)
        local key value
        key="$(echo "$line" | sed -n 's/^\([a-z_]*\):.*/\1/p')"
        value="$(echo "$line" | sed -n 's/^[a-z_]*:[[:space:]]*"\{0,1\}\([^"]*\)"\{0,1\}[[:space:]]*$/\1/p')"

        [[ -z "$key" ]] && continue

        local env_var="${key_map[$key]:-}"
        [[ -z "$env_var" ]] && continue

        # Only set if not already in environment
        if [[ -z "${!env_var:-}" ]]; then
            export "$env_var=$value"
        fi
    done < "$CONFIG_FILE"
}

# ─── Container Backend ────────────────────────────────────────────────────────
# Returns the backend to use: explicit config, or auto-detected from the host.
resolve_backend() {
    if [[ -n "${REACTORCIDE_BACKEND:-}" ]]; then
        echo "$REACTORCIDE_BACKEND"
        return
    fi
    # Auto-detect: prefer docker if the daemon is reachable, otherwise containerd
    if command -v docker &>/dev/null && docker info &>/dev/null; then
        echo "docker"
    elif [[ -S /run/containerd/containerd.sock ]] || [[ -S /run/user/$(id -u)/containerd-rootless/containerd.sock ]]; then
        echo "containerd"
    else
        echo "docker"  # fall back, will fail with a clear error from the CLI
    fi
}

# Returns the user-facing pull command (docker or nerdctl) for check-image
resolve_runtime() {
    local backend
    backend="$(resolve_backend)"
    case "$backend" in
        containerd) echo "nerdctl" ;;
        *)          echo "docker" ;;
    esac
}

# Checks if the container runtime socket needs elevated privileges.
# Returns "sudo" if needed, empty string otherwise.
needs_sudo() {
    local backend
    backend="$(resolve_backend)"

    case "$backend" in
        containerd)
            # Rootless containerd doesn't need sudo
            if [[ -S "/run/user/$(id -u)/containerd-rootless/containerd.sock" ]]; then
                return
            fi
            # System containerd socket — check if current user can access it
            local sock="/run/containerd/containerd.sock"
            if [[ -S "$sock" ]] && ! [[ -r "$sock" && -w "$sock" ]]; then
                echo "sudo"
            fi
            ;;
        docker)
            # Docker socket — check if current user can access it
            local sock="/var/run/docker.sock"
            if [[ -S "$sock" ]] && ! [[ -r "$sock" && -w "$sock" ]]; then
                echo "sudo"
            fi
            ;;
    esac
}

# Builds the command prefix for running commands that need the container socket.
# Uses "sudo -E" to preserve the caller's full environment (secrets password,
# registry vars, etc.) since sudo strips env vars by default.
build_sudo_prefix() {
    local sudo_prefix
    sudo_prefix="$(needs_sudo)"
    if [[ -n "$sudo_prefix" ]]; then
        echo "sudo -E"
    fi
}

# ─── Tooling Validation ──────────────────────────────────────────────────────
check_tools() {
    local missing=0

    # Reactorcide CLI
    if [[ ! -x "$CLI" ]]; then
        err "reactorcide CLI not found at: $CLI"
        echo "    Build it: cd $REPO_ROOT/coordinator_api && go build -o reactorcide ."
        missing=1
    fi

    # git (needed for source checkout in jobs)
    if ! command -v git &>/dev/null; then
        err "git is not installed."
        missing=1
    fi

    # Container runtime — need at least one of nerdctl or docker for local runs
    if ! command -v nerdctl &>/dev/null && ! command -v docker &>/dev/null; then
        err "Neither nerdctl nor docker found. A container runtime is required for local job execution."
        missing=1
    fi

    # curl (for remote API calls, also used internally by CLI)
    if ! command -v curl &>/dev/null; then
        err "curl is not installed."
        missing=1
    fi

    if [[ "$missing" -eq 1 ]]; then
        echo ""
        err "Missing required tools. Install them and try again."
        exit 1
    fi

    ok "All required tools available."
}

# ─── Env Checks ──────────────────────────────────────────────────────────────
check_password() {
    if [[ -z "${REACTORCIDE_SECRETS_PASSWORD:-}" ]]; then
        err "REACTORCIDE_SECRETS_PASSWORD is not set."
        echo ""
        echo "  Set it with the password you want to use for local secrets encryption:"
        echo ""
        echo "    export REACTORCIDE_SECRETS_PASSWORD=\"your-password-here\""
        echo ""
        exit 1
    fi
}

check_api_url() {
    if [[ -z "${REACTORCIDE_API_URL:-}" ]]; then
        err "REACTORCIDE_API_URL is not set (and not in demo-config.yaml)."
        echo ""
        echo "  Either set it directly:"
        echo ""
        echo "    export REACTORCIDE_API_URL=\"http://your-vm-host:6080\""
        echo ""
        echo "  Or add it to $CONFIG_FILE (copy from demo-config.example.yaml)."
        echo ""
        exit 1
    fi
}

check_cli() {
    if [[ ! -x "$CLI" ]]; then
        err "Reactorcide CLI not found at: $CLI"
        echo "  Build it first: cd $REPO_ROOT/coordinator_api && go build -o reactorcide ."
        exit 1
    fi
}

# ─── Remote Job Helpers ──────────────────────────────────────────────────────
# Runs "reactorcide submit --wait", captures the job ID, and fetches logs
# after completion. All submit args are passed through.
# Usage: submit_and_show_logs [submit args...]
submit_and_show_logs() {
    local token
    token=$("$CLI" secrets get demo/api api_token)

    local output
    output=$("$CLI" submit \
        --api-url "$REACTORCIDE_API_URL" \
        --token "$token" \
        --wait \
        "$@" 2>&1) || true

    echo "$output"

    # Extract job ID from submit output (line like "  Job ID: <uuid>")
    local job_id
    job_id=$(echo "$output" | grep "Job ID:" | head -1 | awk '{print $NF}')

    if [[ -z "$job_id" ]]; then
        warn "Could not extract job ID from submit output"
        return 1
    fi

    # Fetch and display logs
    echo ""
    step "Job logs ($job_id)"
    local logs
    logs=$("$CLI" logs \
        --api-url "$REACTORCIDE_API_URL" \
        --token "$token" \
        "$job_id" 2>&1) || true

    if [[ -n "$logs" ]]; then
        # Logs come back as JSON array — extract message fields for readable output.
        # If jq is available use it, otherwise try a simple sed fallback.
        if command -v jq &>/dev/null; then
            echo "$logs" | jq -r '.[] | .message' 2>/dev/null || echo "$logs"
        else
            # Fallback: pull "message":"..." values with sed
            echo "$logs" | sed 's/},/}\n/g' | sed -n 's/.*"message":"\([^"]*\)".*/\1/p'
        fi
    else
        warn "No logs available (job may have failed before producing output)"
    fi
}

# ─── Steps ────────────────────────────────────────────────────────────────────

do_validate() {
    step "Validating demo environment"
    check_tools
    check_password

    if [[ -f "$CONFIG_FILE" ]]; then
        ok "Config file found: demo-config.yaml"
    else
        warn "No demo-config.yaml found. Using environment variables only."
        echo "    Copy the example:  cp $SCRIPT_DIR/demo-config.example.yaml $CONFIG_FILE"
    fi

    # Show resolved values
    info "Resolved configuration:"
    echo "    REACTORCIDE_SECRETS_PASSWORD = (set)"
    echo "    REACTORCIDE_API_URL         = ${REACTORCIDE_API_URL:-(not set)}"
    echo "    REACTORCIDE_BACKEND         = $(resolve_backend) ($(if [[ -n "${REACTORCIDE_BACKEND:-}" ]]; then echo "from config"; else echo "auto-detected"; fi))$(if [[ -n "$(needs_sudo)" ]]; then echo ", sudo -E required"; fi)"
    echo "    DEMO_REGISTRY               = ${DEMO_REGISTRY:-(not set)}"
    echo "    REGISTRY_USER               = ${REGISTRY_USER:-(not set)}"
    echo "    DEMO_IMAGE                  = ${DEMO_IMAGE:-(not set)}"
    echo "    DEMO_TAG                    = ${DEMO_TAG:-(not set)}"
    echo ""
    ok "Validation passed."
}

do_setup() {
    step "Setting up demo secrets"
    check_password
    check_cli

    # Initialize secrets storage (idempotent — will fail silently if already init'd)
    info "Initializing secrets storage..."
    "$CLI" secrets init 2>/dev/null || true
    ok "Secrets storage ready."

    # Store the API token
    info "Storing API token at demo/api:api_token"
    if [[ -n "${REACTORCIDE_API_TOKEN:-}" ]]; then
        info "Using REACTORCIDE_API_TOKEN from environment."
        echo -n "$REACTORCIDE_API_TOKEN" | "$CLI" secrets set --stdin demo/api api_token
    else
        echo ""
        echo -e "  ${YELLOW}No REACTORCIDE_API_TOKEN env var found.${RESET}"
        echo "  Enter your Reactorcide VM API token (input will be hidden):"
        echo ""
        "$CLI" secrets set demo/api api_token
    fi
    ok "API token stored at demo/api:api_token"

    # Store a test secret to demonstrate masking
    info "Storing test secret at demo/test:secret"
    echo -n "super-secret-demo-value" | "$CLI" secrets set --stdin demo/test secret
    ok "Test secret stored at demo/test:secret"

    echo ""
    ok "Setup complete! Available steps:"
    echo "    $0 hello-local     Run a hello-world job locally"
    echo "    $0 hello-remote    Submit a hello-world job to the coordinator"
    echo "    $0 build-local     Build the demo site image locally"
    echo "    $0 build-remote    Submit the demo site build to the coordinator"
    echo "    $0 reset           Clean up demo secrets"
}

do_hello_local() {
    step "Running hello-demo job locally"
    check_password
    check_cli

    info "This runs a simple Alpine container that prints a greeting"
    info "and demonstrates secret masking in output."
    echo ""

    local backend sudo_prefix
    backend="$(resolve_backend)"
    sudo_prefix="$(build_sudo_prefix)"

    info "Job file: demo/jobs/hello-demo.yaml"
    info "Backend:  $backend"
    [[ -n "$sudo_prefix" ]] && info "Using sudo -E (container socket requires elevated privileges)"
    info "Command:  reactorcide run-local --backend $backend --job-dir ./ demo/jobs/hello-demo.yaml"
    echo ""

    $sudo_prefix "$CLI" run-local \
        --backend "$backend" \
        --job-dir "$REPO_ROOT" \
        "$SCRIPT_DIR/jobs/hello-demo.yaml"
}

do_hello_remote() {
    step "Submitting hello-demo job to coordinator"
    check_password
    check_api_url
    check_cli

    info "This submits the same job to your remote Reactorcide coordinator."
    info "The coordinator will schedule it on a worker."
    echo ""

    info "API URL: $REACTORCIDE_API_URL"
    info "Command: reactorcide submit --wait demo/jobs/hello-demo.yaml"
    echo ""

    submit_and_show_logs "$SCRIPT_DIR/jobs/hello-demo.yaml"
}

do_build_local() {
    step "Building demo site image locally"
    check_password
    check_cli

    # Check for required registry env vars
    if [[ -z "${DEMO_REGISTRY:-}" ]]; then
        err "DEMO_REGISTRY is not set."
        echo ""
        echo "  Set the registry, image name, and tag for the demo site:"
        echo ""
        echo "    export DEMO_REGISTRY=\"docker.io\"             # or your registry"
        echo "    export DEMO_IMAGE=\"youruser/reactorcide-demo\" # image path"
        echo "    export DEMO_TAG=\"latest\"                      # image tag"
        echo "    export REGISTRY_USER=\"youruser\"               # registry username"
        echo ""
        echo "  You'll also need a registry password secret. Store it with:"
        echo ""
        echo "    $0 setup-registry"
        echo ""
        exit 1
    fi

    info "This builds a Pysocha static site, packages it with Caddy,"
    info "and pushes the image to $DEMO_REGISTRY/$DEMO_IMAGE:${DEMO_TAG:-latest}"
    echo ""

    local backend sudo_prefix
    backend="$(resolve_backend)"
    sudo_prefix="$(build_sudo_prefix)"

    export DEMO_TAG="${DEMO_TAG:-latest}"
    export DEMO_IMAGE="${DEMO_IMAGE:-reactorcide-demo}"

    info "Job file: demo/jobs/build-demo-site.yaml"
    info "Backend:  $backend"
    [[ -n "$sudo_prefix" ]] && info "Using sudo -E (container socket requires elevated privileges)"
    info "Command:  reactorcide run-local --backend $backend --job-dir ./ demo/jobs/build-demo-site.yaml"
    echo ""

    $sudo_prefix "$CLI" run-local \
        --backend "$backend" \
        --job-dir "$REPO_ROOT" \
        "$SCRIPT_DIR/jobs/build-demo-site.yaml"

    echo ""
    ok "Image built and pushed!"
    echo "    Pull it with: docker pull $DEMO_REGISTRY/${DEMO_IMAGE:-reactorcide-demo}:${DEMO_TAG:-latest}"
    echo "    Run it with:  docker run -p 8080:80 $DEMO_REGISTRY/${DEMO_IMAGE:-reactorcide-demo}:${DEMO_TAG:-latest}"
}

do_build_remote() {
    step "Submitting demo site build to coordinator"
    check_password
    check_api_url
    check_cli

    if [[ -z "${DEMO_REGISTRY:-}" ]]; then
        err "DEMO_REGISTRY is not set. See: $0 build-local  for required env vars."
        exit 1
    fi

    info "This submits the site build job to your remote coordinator."
    info "Target image: $DEMO_REGISTRY/${DEMO_IMAGE:-reactorcide-demo}:${DEMO_TAG:-latest}"
    echo ""

    info "API URL: $REACTORCIDE_API_URL"
    info "Command: reactorcide submit --wait demo/jobs/build-demo-site.yaml"
    echo ""

    export DEMO_TAG="${DEMO_TAG:-latest}"
    export DEMO_IMAGE="${DEMO_IMAGE:-reactorcide-demo}"

    submit_and_show_logs "$SCRIPT_DIR/jobs/build-demo-site.yaml"

    echo ""
    ok "Image built and pushed via coordinator!"
    echo "    Pull it with: docker pull $DEMO_REGISTRY/${DEMO_IMAGE:-reactorcide-demo}:${DEMO_TAG:-latest}"
}

do_check_image() {
    step "Checking if demo site image exists in registry"

    if [[ -z "${DEMO_REGISTRY:-}" ]]; then
        err "DEMO_REGISTRY is not set. Fill in demo-config.yaml or set env vars."
        exit 1
    fi

    local full_image="$DEMO_REGISTRY/${DEMO_IMAGE:-reactorcide-demo}:${DEMO_TAG:-latest}"
    local runtime sudo_prefix
    runtime="$(resolve_runtime)"
    sudo_prefix="$(build_sudo_prefix)"

    info "Attempting to pull: $full_image"
    info "Using: ${sudo_prefix:+sudo }$runtime"
    echo ""

    if $sudo_prefix $runtime pull "$full_image" 2>&1; then
        echo ""
        warn "Image already exists: $full_image"
        info "If you want a clean demo, remove it first:"
        echo "    ${sudo_prefix:+sudo }$runtime rmi $full_image"
    else
        echo ""
        ok "Image does not exist yet: $full_image"
        info "Run '$0 build-local' or '$0 build-remote' to create it."
    fi
}

do_setup_registry() {
    step "Storing registry password"
    check_password
    check_cli

    if [[ -n "${REACTORCIDE_REGISTRY_PASSWORD:-}" ]]; then
        info "Using REACTORCIDE_REGISTRY_PASSWORD from environment."
        echo -n "$REACTORCIDE_REGISTRY_PASSWORD" | "$CLI" secrets set --stdin demo/registry password
    else
        echo ""
        echo -e "  ${YELLOW}No REACTORCIDE_REGISTRY_PASSWORD env var found.${RESET}"
        echo "  Enter the password for your container registry (input will be hidden):"
        echo ""
        "$CLI" secrets set demo/registry password
    fi
    ok "Registry password stored at demo/registry:password"
}

do_setup_remote() {
    step "Syncing demo secrets to coordinator API"
    check_password
    check_api_url
    check_cli

    local token
    token=$("$CLI" secrets get demo/api api_token)

    info "API URL: $REACTORCIDE_API_URL"
    info "This pushes local demo secrets to the remote coordinator so"
    info "that remote jobs can resolve \${secret:...} references."
    echo ""

    # Sync demo/test:secret
    local test_secret
    test_secret=$("$CLI" secrets get demo/test secret 2>/dev/null || true)
    if [[ -n "$test_secret" ]]; then
        local status
        status=$(curl -s -o /dev/null -w "%{http_code}" -X PUT \
            "$REACTORCIDE_API_URL/api/v1/secrets/value?path=demo/test&key=secret" \
            -H "Authorization: Bearer $token" \
            -H "Content-Type: application/json" \
            -d "$(printf '{"value":"%s"}' "$test_secret")")
        if [[ "$status" == "200" ]]; then
            ok "Synced demo/test:secret"
        else
            err "Failed to sync demo/test:secret (HTTP $status)"
        fi
    else
        warn "demo/test:secret not found locally, skipping (run setup first)"
    fi

    # Sync demo/registry:password
    local reg_pass
    reg_pass=$("$CLI" secrets get demo/registry password 2>/dev/null || true)
    if [[ -n "$reg_pass" ]]; then
        local status
        status=$(curl -s -o /dev/null -w "%{http_code}" -X PUT \
            "$REACTORCIDE_API_URL/api/v1/secrets/value?path=demo/registry&key=password" \
            -H "Authorization: Bearer $token" \
            -H "Content-Type: application/json" \
            -d "$(printf '{"value":"%s"}' "$reg_pass")")
        if [[ "$status" == "200" ]]; then
            ok "Synced demo/registry:password"
        else
            err "Failed to sync demo/registry:password (HTTP $status)"
        fi
    else
        warn "demo/registry:password not found locally, skipping (run setup-registry first)"
    fi

    echo ""
    ok "Remote secrets sync complete."
}

do_setup_all() {
    do_setup
    do_setup_registry
    do_setup_remote
}

do_reset() {
    step "Resetting demo"
    check_password
    check_cli

    # Delete registry image if config is available
    if [[ -n "${DEMO_REGISTRY:-}" ]]; then
        local image="${DEMO_IMAGE:-reactorcide-demo}"
        local tag="${DEMO_TAG:-latest}"
        local full_image="$DEMO_REGISTRY/$image:$tag"

        info "Deleting image from registry: $full_image"

        local reg_pass reg_user
        reg_pass=$("$CLI" secrets get demo/registry password 2>/dev/null || true)
        reg_user="${REGISTRY_USER:-}"

        if [[ -n "$reg_pass" ]]; then
            local digest
            digest=$(curl -s -I \
                -u "$reg_user:$reg_pass" \
                -H "Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json" \
                "https://$DEMO_REGISTRY/v2/$image/manifests/$tag" 2>&1 \
                | grep -i "docker-content-digest" \
                | tr -d '\r' \
                | awk '{print $2}')

            if [[ -n "$digest" ]]; then
                local status
                status=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE \
                    -u "$reg_user:$reg_pass" \
                    "https://$DEMO_REGISTRY/v2/$image/manifests/$digest")

                if [[ "$status" == "202" ]]; then
                    ok "Image deleted from registry: $full_image"
                else
                    warn "Could not delete image (HTTP $status) — may not exist or registry denied delete"
                fi
            else
                warn "Image not found in registry (may already be deleted)"
            fi
        else
            warn "No registry password available, skipping image deletion"
        fi
    else
        warn "DEMO_REGISTRY not set, skipping image deletion"
    fi

    # Clean up secrets
    info "Removing demo/test:secret..."
    "$CLI" secrets delete demo/test secret 2>/dev/null || true

    info "Removing demo/registry:password..."
    "$CLI" secrets delete demo/registry password 2>/dev/null || true

    info "Keeping demo/api:api_token (use 'reset-all' to remove it too)"
    echo ""
    ok "Demo reset complete. API token preserved."
}

do_reset_all() {
    # Run normal reset first (deletes registry image + local test/registry secrets)
    do_reset

    step "Removing remote secrets and API token"
    check_password
    check_cli

    # Delete remote secrets if API is reachable
    if [[ -n "${REACTORCIDE_API_URL:-}" ]]; then
        local token
        token=$("$CLI" secrets get demo/api api_token 2>/dev/null || true)

        if [[ -n "$token" ]]; then
            info "Deleting demo/test:secret from coordinator..."
            curl -s -o /dev/null -X DELETE \
                "$REACTORCIDE_API_URL/api/v1/secrets/value?path=demo/test&key=secret" \
                -H "Authorization: Bearer $token" 2>/dev/null || true

            info "Deleting demo/registry:password from coordinator..."
            curl -s -o /dev/null -X DELETE \
                "$REACTORCIDE_API_URL/api/v1/secrets/value?path=demo/registry&key=password" \
                -H "Authorization: Bearer $token" 2>/dev/null || true

            ok "Remote secrets deleted."
        else
            warn "No API token available, skipping remote secret cleanup"
        fi
    else
        warn "REACTORCIDE_API_URL not set, skipping remote secret cleanup"
    fi

    # Remove the API token last (needed it above)
    info "Removing demo/api:api_token..."
    "$CLI" secrets delete demo/api api_token 2>/dev/null || true

    echo ""
    ok "All demo secrets removed (local and remote)."
}

# ─── Usage ────────────────────────────────────────────────────────────────────

usage() {
    echo ""
    echo -e "${BOLD}Reactorcide Demo${RESET}"
    echo ""
    echo "Usage: $0 <step>"
    echo ""
    echo "Steps (run in order for the full demo):"
    echo ""
    echo "  validate         Check all required tools and show config"
    echo "  setup            Initialize secrets and store API token"
    echo "  setup-registry   Store container registry password"
    echo "  setup-remote     Sync demo secrets to coordinator API (for remote jobs)"
    echo "  hello-local      Run hello-world job locally (run-local)"
    echo "  hello-remote     Submit hello-world job to coordinator (submit)"
    echo "  check-image      Try to pull the demo image (should fail before build)"
    echo "  build-local      Build & push demo site image locally"
    echo "  build-remote     Submit demo site build to coordinator"
    echo "  reset            Clean up demo secrets (keeps API token)"
    echo "  reset-all        Clean up ALL demo secrets"
    echo ""
    echo "Configuration:"
    echo ""
    echo "  Copy demo-config.example.yaml to demo-config.yaml and fill in your"
    echo "  values. The config file sets defaults; env vars always take precedence."
    echo ""
    echo "  REACTORCIDE_SECRETS_PASSWORD must always be set as an env var (never in config)."
    echo ""
}

# ─── Main ─────────────────────────────────────────────────────────────────────

# Load config file defaults before dispatching (env vars take precedence)
load_config

case "${1:-}" in
    validate)       do_validate ;;
    setup)          do_setup ;;
    setup-all)      do_setup_all ;;
    setup-registry) do_setup_registry ;;
    setup-remote)   do_setup_remote ;;
    hello-local)    do_hello_local ;;
    hello-remote)   do_hello_remote ;;
    check-image)    do_check_image ;;
    build-local)    do_build_local ;;
    build-remote)   do_build_remote ;;
    reset)          do_reset ;;
    reset-all)      do_reset_all ;;
    *)              usage ;;
esac
