#!/usr/bin/env python3
"""Deploy Reactorcide to Kubernetes cluster.

This script is invoked by runnerlib after source checkout and plugin execution.
Tools (helm, kubectl) are installed by the plugin_k8s_tools.py lifecycle hook.

Can be tested independently with required env vars:
    KUBECONFIG_CONTENT="..." REACTORCIDE_K8S_NAMESPACE=test python deploy_k8s.py --dry-run
"""
import argparse
import base64
from datetime import datetime
import json
import os
import socket
import struct
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from typing import Optional, Dict, Any

_SECRET_VALUES = set()


def log(msg: str) -> None:
    """Print log message."""
    print(f"[deploy] {msg}", flush=True)


def redact(value: str) -> str:
    """Redact known secret values from log output."""
    redacted = value
    for secret in sorted(_SECRET_VALUES, key=len, reverse=True):
        if secret:
            redacted = redacted.replace(secret, "***")
    return redacted


def run_cmd(cmd: str, check: bool = True, capture: bool = False, dry_run: bool = False) -> subprocess.CompletedProcess:
    """Run a shell command."""
    log(f"Running: {redact(cmd)}")
    if dry_run:
        log("  (dry-run - not executed)")
        return subprocess.CompletedProcess(args=cmd, returncode=0, stdout="", stderr="")
    return subprocess.run(cmd, shell=True, check=check, capture_output=capture, text=True)


def run_cmd_output(cmd: str, dry_run: bool = False) -> str:
    """Run a command and return its output."""
    if dry_run:
        log(f"Running (dry-run): {cmd}")
        return ""
    result = subprocess.run(cmd, shell=True, capture_output=True, text=True, check=True)
    return result.stdout.strip()


def register_secret(secret: str) -> None:
    """Register a secret value for masking via the runnerlib secrets socket."""
    if secret:
        _SECRET_VALUES.add(secret)

    socket_path = os.environ.get('REACTORCIDE_SECRETS_SOCKET')
    if not socket_path or not secret:
        return
    try:
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(5.0)
        sock.connect(socket_path)
        msg = json.dumps({'action': 'register', 'secrets': [secret]}).encode('utf-8')
        sock.send(struct.pack('!I', len(msg)))
        sock.send(msg)
        sock.close()
    except Exception as e:
        log(f"ERROR: Failed to register secret for masking: {e}")
        pass


def setup_path() -> None:
    """Add ~/.local/bin to PATH (where plugin installs tools)."""
    local_bin = Path.home() / ".local" / "bin"
    os.environ["PATH"] = f"{local_bin}:{os.environ.get('PATH', '')}"


def read_config() -> Dict[str, Any]:
    """Read deployment config from environment variables."""
    return {
        'namespace': os.environ.get('REACTORCIDE_K8S_NAMESPACE', 'reactorcide'),
        'release': os.environ.get('REACTORCIDE_HELM_RELEASE', 'reactorcide'),
        'image_tag': os.environ.get('REACTORCIDE_IMAGE_TAG', ''),
        'helm_values': os.environ.get('REACTORCIDE_HELM_VALUES', ''),
        'registry_server': os.environ.get('REACTORCIDE_REGISTRY_SERVER', '').strip(),
        'registry_pull_secret_name': os.environ.get('REACTORCIDE_REGISTRY_PULL_SECRET_NAME', '').strip(),
        'registry_username': os.environ.get('REACTORCIDE_REGISTRY_USERNAME', '').strip(),
        'registry_password': os.environ.get('REACTORCIDE_REGISTRY_PASSWORD', '').strip(),
        'db_uri': os.environ.get('REACTORCIDE_DB_URI', ''),
        'provision_postgres': os.environ.get('REACTORCIDE_PROVISION_POSTGRES', 'false').lower() == 'true',
        'postgres_team': os.environ.get('REACTORCIDE_POSTGRES_TEAM', 'reactorcide'),
        'postgres_version': os.environ.get('REACTORCIDE_POSTGRES_VERSION', '18'),
        'postgres_size': os.environ.get('REACTORCIDE_POSTGRES_SIZE', '5Gi'),
        'postgres_instances': os.environ.get('REACTORCIDE_POSTGRES_INSTANCES', '1'),
        'deploy_corndogs': os.environ.get('REACTORCIDE_DEPLOY_CORNDOGS', 'false').lower() == 'true',
        'corndogs_url': os.environ.get('REACTORCIDE_CORNDOGS_URL', ''),
        'object_store_type': os.environ.get('REACTORCIDE_OBJECT_STORE_TYPE', 's3'),
        's3_endpoint': os.environ.get('REACTORCIDE_S3_ENDPOINT', 'http://seaweedfs-s3.seaweedfs.svc.cluster.local:8333'),
        's3_bucket': os.environ.get('REACTORCIDE_S3_BUCKET', 'reactorcide'),
        's3_region': os.environ.get('REACTORCIDE_S3_REGION', 'us-east-1'),
        's3_access_key': os.environ.get('REACTORCIDE_S3_ACCESS_KEY', '').strip(),
        's3_secret_key': os.environ.get('REACTORCIDE_S3_SECRET_KEY', '').strip(),
        'gcs_bucket': os.environ.get('REACTORCIDE_GCS_BUCKET', 'reactorcide'),
        'gateway_enabled': os.environ.get('REACTORCIDE_GATEWAY_ENABLED', 'false').lower() == 'true',
        'gateway_domains': os.environ.get('REACTORCIDE_GATEWAY_DOMAINS', ''),
        'gateway_name': os.environ.get('REACTORCIDE_GATEWAY_NAME', ''),
        'gateway_namespace': os.environ.get('REACTORCIDE_GATEWAY_NAMESPACE', ''),
        'gateway_section': os.environ.get('REACTORCIDE_GATEWAY_SECTION', 'https'),
        'default_org': os.environ.get('REACTORCIDE_DEFAULT_ORG', 'default'),
        # Secret name for the bootstrap API token.
        'api_token_secret_name': os.environ.get('REACTORCIDE_API_TOKEN_SECRET_NAME', 'base-reactorcide-api-token'),
        'vcs_enabled': os.environ.get('REACTORCIDE_VCS_ENABLED', 'false').lower() == 'true',
        'vcs_base_url': os.environ.get('REACTORCIDE_VCS_BASE_URL', ''),
        'kubeconfig_content': os.environ.get('KUBECONFIG_CONTENT', ''),
        # Worker: coordinator-mediated. These reference a
        # Kubernetes Secret the operator manages out-of-band -- never the
        # enrollment token value itself. See docs/workers.md.
        'worker_coordinator_url': os.environ.get('REACTORCIDE_WORKER_COORDINATOR_URL', ''),
        'worker_enrollment_token_secret': os.environ.get('REACTORCIDE_WORKER_ENROLLMENT_TOKEN_SECRET', ''),
        'worker_enrollment_token_key': os.environ.get('REACTORCIDE_WORKER_ENROLLMENT_TOKEN_KEY', ''),
    }


def setup_kubeconfig(config: Dict[str, Any], dry_run: bool = False) -> None:
    """Setup kubeconfig from environment."""
    if not config['kubeconfig_content']:
        raise RuntimeError("KUBECONFIG_CONTENT not set")

    kube_dir = Path.home() / ".kube"
    kube_dir.mkdir(parents=True, exist_ok=True)
    kube_config = kube_dir / "config"

    if not dry_run:
        kube_config.write_text(config['kubeconfig_content'])
        kube_config.chmod(0o600)

    os.environ["KUBECONFIG"] = str(kube_config)
    log("Kubeconfig configured")


def verify_cluster(dry_run: bool = False) -> None:
    """Verify cluster connection."""
    log("Verifying cluster connection...")
    run_cmd("kubectl cluster-info --request-timeout=10s", dry_run=dry_run)


def create_namespace(namespace: str, dry_run: bool = False) -> None:
    """Create namespace if it doesn't exist."""
    log(f"Creating namespace: {namespace}")
    run_cmd(f"kubectl create namespace {namespace} --dry-run=client -o yaml | kubectl apply -f -", dry_run=dry_run)


def apply_registry_pull_secret(config: Dict[str, Any], dry_run: bool = False) -> None:
    """Create or update the registry pull Secret without command arguments."""
    server = config.get('registry_server', '')
    secret_name = config.get('registry_pull_secret_name', '')
    username = config.get('registry_username', '')
    password = config.get('registry_password', '')
    configured = [server, secret_name, username, password]

    if not any(configured):
        return
    if not all(configured):
        raise RuntimeError("Registry pull configuration is incomplete")

    register_secret(username)
    register_secret(password)
    auth = base64.b64encode(f"{username}:{password}".encode()).decode()
    docker_config = {
        "auths": {
            server: {
                "username": username,
                "password": password,
                "auth": auth,
            }
        }
    }
    manifest = {
        "apiVersion": "v1",
        "kind": "Secret",
        "metadata": {
            "name": secret_name,
            "namespace": config['namespace'],
        },
        "type": "kubernetes.io/dockerconfigjson",
        "data": {
            ".dockerconfigjson": base64.b64encode(
                json.dumps(docker_config, separators=(",", ":")).encode()
            ).decode(),
        },
    }

    if dry_run:
        log(f"Would apply registry pull Secret: {secret_name}")
        return

    log(f"Applying registry pull Secret: {secret_name}")
    subprocess.run(
        ["kubectl", "apply", "-f", "-"],
        input=json.dumps(manifest),
        text=True,
        check=True,
        capture_output=True,
    )


def provision_postgres(config: Dict[str, Any], dry_run: bool = False) -> str:
    """Provision PostgreSQL via Zalando operator.

    Returns:
        The Reactorcide database URI.
    """
    log("Provisioning PostgreSQL via Zalando operator")

    namespace = config['namespace']
    release = config['release']

    # Build PostgreSQL manifest
    users_spec = """    reactorcide:
      - superuser
      - createdb"""
    databases_spec = """    reactorcide: reactorcide"""

    manifest = f"""apiVersion: acid.zalan.do/v1
kind: postgresql
metadata:
  name: {release}-postgres
  namespace: {namespace}
spec:
  teamId: "{config['postgres_team']}"
  volume:
    size: {config['postgres_size']}
  numberOfInstances: {config['postgres_instances']}
  users:
{users_spec}
  databases:
{databases_spec}
  postgresql:
    version: "{config['postgres_version']}"
"""

    if dry_run:
        log("Would apply PostgreSQL manifest:")
        log(manifest)
    else:
        # Apply the manifest
        proc = subprocess.run(
            ["kubectl", "apply", "-f", "-"],
            input=manifest,
            text=True,
            check=True,
            capture_output=True
        )
        log(proc.stdout if proc.stdout else "PostgreSQL manifest applied")

    # Wait for PostgreSQL to be ready
    log("Waiting for PostgreSQL to be ready...")
    pg_host = f"{release}-postgres.{namespace}.svc.cluster.local"

    if not dry_run:
        for i in range(1, 61):
            try:
                status = run_cmd_output(
                    f"kubectl get postgresql {release}-postgres -n {namespace} -o jsonpath='{{.status.PostgresClusterStatus}}'"
                )
                if status == "Running":
                    log("PostgreSQL is running")
                    break
                log(f"Waiting for PostgreSQL... ({i}/60) status: {status}")
            except subprocess.CalledProcessError:
                log(f"Waiting for PostgreSQL... ({i}/60)")
            time.sleep(5)
        else:
            raise RuntimeError("Timed out waiting for PostgreSQL")

    # Get reactorcide credentials
    pg_secret = f"reactorcide.{release}-postgres.credentials.postgresql.acid.zalan.do"
    log(f"Retrieving reactorcide credentials from secret: {pg_secret}")

    if not dry_run:
        for i in range(1, 31):
            try:
                run_cmd(f"kubectl get secret {pg_secret} -n {namespace}", check=True, capture=True)
                break
            except subprocess.CalledProcessError:
                log(f"Waiting for credentials secret... ({i}/30)")
                time.sleep(2)
        else:
            raise RuntimeError("Timed out waiting for credentials secret")

        pg_user = run_cmd_output(
            f"kubectl get secret {pg_secret} -n {namespace} -o jsonpath='{{.data.username}}' | base64 -d"
        )
        pg_pass = run_cmd_output(
            f"kubectl get secret {pg_secret} -n {namespace} -o jsonpath='{{.data.password}}' | base64 -d"
        )

        db_uri = f"postgresql://{pg_user}:{pg_pass}@{pg_host}:5432/reactorcide?sslmode=require"

        register_secret(pg_pass)
        register_secret(db_uri)
    else:
        db_uri = f"postgresql://reactorcide:***@{pg_host}:5432/reactorcide?sslmode=require"

    log("Database URI configured from Zalando PostgreSQL")

    return db_uri


def deploy_corndogs(config: Dict[str, Any], dry_run: bool = False) -> str:
    """Deploy Corndogs to the cluster.

    Returns:
        The Corndogs URL.
    """
    log("Deploying Corndogs")

    namespace = config['namespace']
    repo_root = Path(__file__).resolve().parents[2]
    chart_path = repo_root / "helm_chart" / "charts" / "corndogs"
    if not chart_path.exists():
        raise RuntimeError(f"Corndogs chart not found: {chart_path}")

    run_cmd(
        f"kubectl delete deployment corndogs -n {namespace} --ignore-not-found --wait=true",
        dry_run=dry_run,
    )

    cmd = (
        f"helm upgrade --install corndogs {chart_path} "
        f"--namespace {namespace} "
        f"--set replicaCount=1 "
        f"--set storage.backend=file "
        f"--set postgresql.enabled=false "
        f"--set podSecurityContext.fsGroup=10001 "
        f"--set securityContext.runAsNonRoot=true "
        f"--set securityContext.runAsUser=10001 "
        f"--set securityContext.runAsGroup=10001 "
    )
    if config.get('registry_pull_secret_name'):
        cmd += (
            "--set imagePullSecrets[0].name="
            f"{config['registry_pull_secret_name']} "
        )
    cmd += "--wait --timeout 5m"
    run_cmd(cmd, dry_run=dry_run)

    # corndogs 0.7.0 RPC is raw TCP (CSIL StreamCarrier), NOT HTTP -- the address
    # must be scheme-less "host:port" or the coordinator's TCP dial fails with
    # "too many colons in address". (No "http://" prefix.)
    corndogs_url = f"corndogs.{namespace}.svc.cluster.local:5080"
    log(f"Corndogs deployed: {corndogs_url}")
    return corndogs_url


def build_helm_values(config: Dict[str, Any], db_uri: str, corndogs_url: str) -> list:
    """Build helm value arguments."""
    args = []

    # Write inline helm values to temp file if provided
    if config['helm_values']:
        log("Using inline helm values from overlay")
        values_file = Path("/tmp/values-overlay.yaml")
        values_file.write_text(config['helm_values'])
        args.extend(["-f", str(values_file)])

    pull_secret_name = config.get('registry_pull_secret_name', '')
    if pull_secret_name:
        args.extend(["--set", f"imagePullSecrets[0].name={pull_secret_name}"])
        args.extend(["--set", f"worker.jobImagePullSecrets[0]={pull_secret_name}"])

    # Database
    register_secret(db_uri)
    args.extend(["--set", f"postgres.uri={db_uri}"])

    # Subcharts always disabled in production
    args.extend(["--set", "postgresql.enabled=false"])
    args.extend(["--set", "corndogs.enabled=false"])

    # Corndogs URL
    if corndogs_url:
        args.extend(["--set", f"corndogs.baseUrl={corndogs_url}"])

    # Web UI - enable by default when deploying
    args.extend(["--set", "web.enabled=true"])
    args.extend(["--set", "web.apiTokenSecret.name=" + config.get('api_token_secret_name', 'base-reactorcide-api-token')])
    args.extend(["--set", "web.apiTokenSecret.key=token"])

    # Image tag
    if config['image_tag']:
        args.extend(["--set", f"app.image.tag={config['image_tag']}"])
        args.extend(["--set", f"worker.image.tag={config['image_tag']}"])
        args.extend(["--set", f"web.image.tag={config['image_tag']}"])

    # Object storage
    args.extend(["--set", f"objectStore.type={config['object_store_type']}"])

    if config['object_store_type'] == 's3':
        register_secret(config['s3_access_key'])
        register_secret(config['s3_secret_key'])
        args.extend(["--set", f"objectStore.bucket={config['s3_bucket']}"])
        args.extend(["--set", f"objectStore.s3.endpoint={config['s3_endpoint']}"])
        args.extend(["--set", f"objectStore.s3.region={config['s3_region']}"])
        if config['s3_access_key']:
            args.extend(["--set", f"objectStore.s3.accessKeyId={config['s3_access_key']}"])
        if config['s3_secret_key']:
            args.extend(["--set", f"objectStore.s3.secretAccessKey={config['s3_secret_key']}"])
        log(f"Object storage: S3 ({config['s3_endpoint']})")
    elif config['object_store_type'] == 'gcs':
        args.extend(["--set", f"objectStore.bucket={config['gcs_bucket']}"])
        log("Object storage: GCS")
    else:
        log(f"Object storage: {config['object_store_type']}")

    # VCS integration
    if config['vcs_enabled']:
        args.extend(["--set", "vcs.enabled=true"])
    if config['vcs_base_url']:
        args.extend(["--set", f"vcs.baseURL={config['vcs_base_url']}"])

    # Gateway API HTTPRoutes
    if config['gateway_enabled'] and config['gateway_domains']:
        args.extend(["--set", "app.gateway.enabled=true"])
        args.extend(["--set", "web.gateway.enabled=true"])

        # Convert comma-separated domains to helm array format
        domains = [d.strip() for d in config['gateway_domains'].split(',')]
        for idx, domain in enumerate(domains):
            args.extend(["--set", f"app.gateway.domains[{idx}]={domain}"])
            args.extend(["--set", f"web.gateway.domains[{idx}]={domain}"])

        if config['gateway_name']:
            args.extend(["--set", f"app.gateway.gatewayName={config['gateway_name']}"])
            args.extend(["--set", f"web.gateway.gatewayName={config['gateway_name']}"])
        if config['gateway_namespace']:
            args.extend(["--set", f"app.gateway.gatewayNamespace={config['gateway_namespace']}"])
            args.extend(["--set", f"web.gateway.gatewayNamespace={config['gateway_namespace']}"])
        if config['gateway_section']:
            args.extend(["--set", f"app.gateway.sectionName={config['gateway_section']}"])
            args.extend(["--set", f"web.gateway.sectionName={config['gateway_section']}"])

        log(f"Gateway: enabled for domains: {config['gateway_domains']}")

    if config['default_org']:
        args.extend(["--set", f"defaults.orgName={config['default_org']}"])

    # User secret name (if non-default)
    if config['api_token_secret_name'] != 'base-reactorcide-api-token':
        args.extend(["--set", f"defaults.apiTokenSecretName={config['api_token_secret_name']}"])

    # Worker: coordinator-mediated. Only Secret name/key
    # references are ever passed here -- the enrollment token value itself
    # is never read, logged, or set by this script.
    if config['worker_coordinator_url']:
        args.extend(["--set", f"worker.coordinatorUrl={config['worker_coordinator_url']}"])
    if config['worker_enrollment_token_secret']:
        args.extend(["--set", f"worker.enrollmentTokenSecret.name={config['worker_enrollment_token_secret']}"])
        if config['worker_enrollment_token_key']:
            args.extend(["--set", f"worker.enrollmentTokenSecret.key={config['worker_enrollment_token_key']}"])
        log(f"Worker enrollment token: operator override Secret {config['worker_enrollment_token_secret']}")
    else:
        log("Worker enrollment token: zero-touch -- the chart auto-generates a "
            "stable enrollment token Secret and the coordinator seeds its "
            "default worker pool from it (no manual pool/token/kubectl step "
            "needed). To use your own admin-minted token instead, set "
            "REACTORCIDE_WORKER_ENROLLMENT_TOKEN_SECRET (see docs/workers.md).")

    return args


def deploy_helm(config: Dict[str, Any], helm_args: list, dry_run: bool = False) -> None:
    """Deploy Reactorcide via Helm."""
    log("Deploying Reactorcide")

    namespace = config['namespace']
    release = config['release']

    # Determine helm chart path - check for /job/src first (container), then current directory
    if Path("/job/src/helm_chart").exists():
        chart_path = "/job/src/helm_chart"
    elif Path("helm_chart").exists():
        chart_path = "helm_chart"
    else:
        raise RuntimeError("Could not find helm_chart directory")

    cmd_parts = [
        "helm", "upgrade", "--install", release, chart_path,
        "--namespace", namespace,
        "--wait", "--timeout", "10m"
    ] + helm_args

    cmd = " ".join(cmd_parts)
    run_cmd(cmd, dry_run=dry_run)


def wait_for_rollout(
    config: Dict[str, Any],
    *,
    include_web: bool,
    dry_run: bool = False,
) -> None:
    """Wait for deployment rollout."""
    log("Waiting for rollout")

    namespace = config['namespace']
    release = config['release']

    # app and worker are always deployed and are the deploy's whole point, so a
    # failed rollout must fail the job (no "|| true" masking) -- otherwise a
    # CrashLooping coordinator/worker reports a green deploy over broken pods.
    run_cmd(f"kubectl rollout status deployment/{release}app -n {namespace} --timeout=5m", dry_run=dry_run)
    run_cmd(f"kubectl rollout status deployment/{release}-worker -n {namespace} --timeout=5m", dry_run=dry_run)
    if include_web:
        run_cmd(
            f"kubectl rollout status deployment/{release}web "
            f"-n {namespace} --timeout=5m",
            dry_run=dry_run,
        )


def run_migrations(config: Dict[str, Any], dry_run: bool = False) -> None:
    """Run database migrations."""
    log("Running migrations")

    namespace = config['namespace']
    release = config['release']
    coordinator_deployment = f"deployment/{release}app"

    if dry_run:
        log("(would run migrations)")
        return

    run_cmd(f"kubectl exec -n {namespace} {coordinator_deployment} -- /reactorcide migrate")


def api_token_exists(config: Dict[str, Any], dry_run: bool = False) -> bool:
    """Return whether the deployment API-token Secret contains a token."""
    if dry_run:
        return False

    namespace = config['namespace']
    secret_name = config['api_token_secret_name']
    try:
        token_data = run_cmd_output(
            f"kubectl get secret {secret_name} -n {namespace} "
            f"-o jsonpath='{{.data.token}}' 2>/dev/null"
        )
    except subprocess.CalledProcessError:
        return False
    return bool(token_data)


def create_api_token(config: Dict[str, Any], dry_run: bool = False) -> bool:
    """Create a global API token and store it in the token secret.

    The Helm chart creates the defaults.apiTokenSecretName Secret.
    This function checks if a token already exists. If it does not exist, the
    function creates one and updates the secret.
    """
    log("Creating API token")

    namespace = config['namespace']
    release = config['release']
    api_token_secret_name = config['api_token_secret_name']

    if dry_run:
        log("(would create API token)")
        return True

    if api_token_exists(config):
        log(f"API token already exists in Secret {api_token_secret_name}. Skip token creation.")
        return True

    coordinator_deployment = f"deployment/{release}app"
    token_name = f"deploy-{datetime.now().strftime('%Y%m%d-%H%M%S')}"

    token_output = run_cmd_output(
        f"kubectl exec -n {namespace} {coordinator_deployment} -- "
        f"/reactorcide token create --name {token_name}"
    )

    api_token = ""
    token_id = ""
    for line in token_output.split('\n'):
        if line.startswith("Token: "):
            api_token = line.split(" ", 1)[1]
        elif line.startswith("Token ID: "):
            token_id = line.split(" ", 2)[2] if len(line.split(" ")) > 2 else ""

    if not api_token:
        raise RuntimeError("The coordinator did not return a new API token")

    register_secret(api_token)

    # Use a protected patch file. Do not put the token or its encoded form in
    # a command argument or a deployment log.
    patch = {
        "data": {
            "token": base64.b64encode(api_token.encode()).decode(),
            "token-id": base64.b64encode((token_id or 'unknown').encode()).decode(),
        }
    }
    patch_path = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w",
            encoding="utf-8",
            prefix="reactorcide-api-token-",
            suffix=".json",
            delete=False,
        ) as patch_file:
            patch_path = Path(patch_file.name)
            json.dump(patch, patch_file)
        log(f"Updating API token Secret: {api_token_secret_name}")
        subprocess.run(
            [
                "kubectl", "patch", "secret", api_token_secret_name,
                "-n", namespace, "--type=merge", "--patch-file", str(patch_path),
            ],
            check=True,
        )
    finally:
        if patch_path is not None:
            patch_path.unlink(missing_ok=True)

    if not api_token_exists(config):
        raise RuntimeError("The API token Secret does not contain the new token")
    log(f"API token stored in Secret: {api_token_secret_name}")
    return True


def show_status(config: Dict[str, Any], dry_run: bool = False) -> None:
    """Show deployment status."""
    log("Deployment status")

    namespace = config['namespace']
    release = config['release']

    run_cmd(f"kubectl get pods -n {namespace}", dry_run=dry_run)
    print("")
    run_cmd(f"kubectl get svc -n {namespace}", dry_run=dry_run)

    if config['gateway_enabled']:
        print("")
        run_cmd(f"kubectl get httproutes -n {namespace}", dry_run=dry_run)


def deploy(config: Dict[str, Any], dry_run: bool = False) -> int:
    """Run the deployment."""
    namespace = config['namespace']
    release = config['release']

    log("=" * 50)
    log("Reactorcide Kubernetes Deployment (Production)")
    log("=" * 50)
    log("")
    log("NOTE: For dev/test, use skaffold instead.")
    log("")
    log(f"Namespace: {namespace}")
    log(f"Release: {release}")
    log("")

    # Validate required config
    if not config['db_uri'] and not config['provision_postgres']:
        raise RuntimeError("REACTORCIDE_DB_URI is required unless REACTORCIDE_PROVISION_POSTGRES=true")

    # Step 1: Verify tools
    log("Step 1: Verifying tools")
    run_cmd("helm version --short", dry_run=dry_run)
    run_cmd("kubectl version --client --short 2>/dev/null || kubectl version --client", dry_run=dry_run)

    # Step 2: Setup kubeconfig
    log("")
    log("Step 2: Configuring kubeconfig")
    setup_kubeconfig(config, dry_run=dry_run)

    # Step 3: Verify cluster connection
    log("")
    log("Step 3: Verifying cluster connection")
    verify_cluster(dry_run=dry_run)

    # Step 4: Create namespace
    log("")
    log("Step 4: Creating namespace")
    create_namespace(namespace, dry_run=dry_run)

    # Step 4b: Apply the registry pull Secret
    if config.get('registry_pull_secret_name'):
        log("")
        log("Step 4b: Applying registry pull Secret")
        apply_registry_pull_secret(config, dry_run=dry_run)

    # Step 5a: Provision PostgreSQL or use provided URI
    db_uri = config['db_uri']

    if config['provision_postgres']:
        log("")
        log("Step 5a: Provisioning PostgreSQL via Zalando operator")
        db_uri = provision_postgres(config, dry_run=dry_run)
    else:
        log("")
        log("Step 5a: Using provided database URI")

    # Step 5b: Deploy Corndogs
    corndogs_url = config['corndogs_url']
    if config['deploy_corndogs']:
        log("")
        log("Step 5b: Deploying Corndogs")
        corndogs_url = deploy_corndogs(config, dry_run=dry_run)
    else:
        log("")
        log("Step 5b: Skipping Corndogs deployment")

    # Step 6: Prepare Helm values
    log("")
    log("Step 6: Preparing Helm values")
    helm_args = build_helm_values(config, db_uri, corndogs_url)

    # Step 7: Deploy
    log("")
    log("Step 7: Deploying Reactorcide")
    token_ready = api_token_exists(config, dry_run=dry_run)
    first_helm_args = helm_args
    if not token_ready:
        log("The API token is not ready. Deploying the control plane before the web application.")
        first_helm_args = [*helm_args, "--set", "web.enabled=false"]
    deploy_helm(config, first_helm_args, dry_run=dry_run)

    # Step 8: Wait for rollout
    log("")
    log("Step 8: Waiting for rollout")
    wait_for_rollout(config, include_web=token_ready, dry_run=dry_run)

    # Step 9: Run migrations
    log("")
    log("Step 9: Running migrations")
    run_migrations(config, dry_run=dry_run)

    # Step 10: Create API token
    log("")
    log("Step 10: Creating API token")
    create_api_token(config, dry_run=dry_run)

    if not token_ready:
        log("")
        log("Step 11: Enabling the web application")
        deploy_helm(config, helm_args, dry_run=dry_run)
        wait_for_rollout(config, include_web=True, dry_run=dry_run)

    # Step 12: Show status
    log("")
    log("Step 12: Deployment status")
    show_status(config, dry_run=dry_run)

    log("")
    log("=" * 50)
    log("Deployment complete!")
    log("=" * 50)
    log("")
    log(f"API token Secret: {config['api_token_secret_name']}")
    log("API endpoint (cluster-internal):")
    log(f"  http://{release}app.{namespace}.svc.cluster.local:6080")
    log("")

    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Deploy Reactorcide to Kubernetes")
    parser.add_argument("--dry-run", action="store_true", help="Print commands without executing")
    args = parser.parse_args()

    try:
        setup_path()
        config = read_config()
        return deploy(config, dry_run=args.dry_run)
    except subprocess.CalledProcessError as e:
        log(f"Command failed with exit code {e.returncode}")
        if e.stderr:
            log(f"stderr: {e.stderr}")
        return e.returncode
    except Exception as e:
        log(f"Error: {e}")
        return 1


if __name__ == "__main__":
    sys.exit(main())
