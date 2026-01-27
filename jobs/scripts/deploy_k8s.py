#!/usr/bin/env python3
"""Deploy Reactorcide to Kubernetes cluster.

This script is invoked by runnerlib after source checkout and plugin execution.
Tools (helm, kubectl) are installed by the plugin_k8s_tools.py lifecycle hook.

Can be tested independently with required env vars:
    KUBECONFIG_CONTENT="..." REACTORCIDE_K8S_NAMESPACE=test python deploy_k8s.py --dry-run
"""
import os
import subprocess
import sys
import argparse
import time
import uuid
from pathlib import Path
from typing import Optional, Dict, Any


def log(msg: str) -> None:
    """Print log message."""
    print(f"[deploy] {msg}", flush=True)


def run_cmd(cmd: str, check: bool = True, capture: bool = False, dry_run: bool = False) -> subprocess.CompletedProcess:
    """Run a shell command."""
    log(f"Running: {cmd}")
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
        'db_uri': os.environ.get('REACTORCIDE_DB_URI', ''),
        'provision_postgres': os.environ.get('REACTORCIDE_PROVISION_POSTGRES', 'false').lower() == 'true',
        'postgres_team': os.environ.get('REACTORCIDE_POSTGRES_TEAM', 'reactorcide'),
        'postgres_version': os.environ.get('REACTORCIDE_POSTGRES_VERSION', '18'),
        'postgres_size': os.environ.get('REACTORCIDE_POSTGRES_SIZE', '5Gi'),
        'postgres_instances': os.environ.get('REACTORCIDE_POSTGRES_INSTANCES', '1'),
        'deploy_corndogs': os.environ.get('REACTORCIDE_DEPLOY_CORNDOGS', 'false').lower() == 'true',
        'corndogs_url': os.environ.get('REACTORCIDE_CORNDOGS_URL', ''),
        'corndogs_db_host': os.environ.get('REACTORCIDE_CORNDOGS_DB_HOST', ''),
        'corndogs_db_name': os.environ.get('REACTORCIDE_CORNDOGS_DB_NAME', 'corndogs'),
        'corndogs_db_user': os.environ.get('REACTORCIDE_CORNDOGS_DB_USER', 'corndogs'),
        'corndogs_db_password': os.environ.get('REACTORCIDE_CORNDOGS_DB_PASSWORD', ''),
        'object_store_type': os.environ.get('REACTORCIDE_OBJECT_STORE_TYPE', 's3'),
        's3_endpoint': os.environ.get('REACTORCIDE_S3_ENDPOINT', 'http://seaweedfs-s3.seaweedfs.svc.cluster.local:8333'),
        's3_bucket': os.environ.get('REACTORCIDE_S3_BUCKET', 'reactorcide'),
        's3_region': os.environ.get('REACTORCIDE_S3_REGION', 'us-east-1'),
        's3_access_key': os.environ.get('REACTORCIDE_S3_ACCESS_KEY', ''),
        's3_secret_key': os.environ.get('REACTORCIDE_S3_SECRET_KEY', ''),
        'gcs_bucket': os.environ.get('REACTORCIDE_GCS_BUCKET', 'reactorcide'),
        'gateway_enabled': os.environ.get('REACTORCIDE_GATEWAY_ENABLED', 'false').lower() == 'true',
        'gateway_domains': os.environ.get('REACTORCIDE_GATEWAY_DOMAINS', ''),
        'gateway_name': os.environ.get('REACTORCIDE_GATEWAY_NAME', ''),
        'gateway_namespace': os.environ.get('REACTORCIDE_GATEWAY_NAMESPACE', ''),
        'gateway_section': os.environ.get('REACTORCIDE_GATEWAY_SECTION', 'https'),
        'default_user_id': os.environ.get('REACTORCIDE_DEFAULT_USER_ID', ''),
        'kubeconfig_content': os.environ.get('KUBECONFIG_CONTENT', ''),
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


def provision_postgres(config: Dict[str, Any], dry_run: bool = False) -> tuple:
    """Provision PostgreSQL via Zalando operator.

    Returns:
        Tuple of (db_uri, corndogs_db_config) where corndogs_db_config is a dict if corndogs is enabled.
    """
    log("Provisioning PostgreSQL via Zalando operator")

    namespace = config['namespace']
    release = config['release']

    # Build PostgreSQL manifest
    users_spec = """    reactorcide:
      - superuser
      - createdb"""
    databases_spec = """    reactorcide: reactorcide"""

    if config['deploy_corndogs']:
        users_spec += """
    corndogs:
      - superuser
      - createdb"""
        databases_spec += """
    corndogs: corndogs"""

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
    pg_secret = f"{release}-postgres.reactorcide.credentials.postgresql.acid.zalan.do"
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

        # Register password for masking if socket is available
        secrets_socket = os.environ.get('REACTORCIDE_SECRETS_SOCKET')
        if secrets_socket:
            try:
                subprocess.run(["python", "-m", "src.register_secret", pg_pass], check=False, capture_output=True)
            except Exception:
                pass
    else:
        db_uri = f"postgresql://reactorcide:***@{pg_host}:5432/reactorcide?sslmode=require"

    log("Database URI configured from Zalando PostgreSQL")

    # Get corndogs credentials if deploying corndogs
    corndogs_db_config = None
    if config['deploy_corndogs']:
        corndogs_pg_secret = f"{release}-postgres.corndogs.credentials.postgresql.acid.zalan.do"
        log(f"Retrieving corndogs credentials from secret: {corndogs_pg_secret}")

        if not dry_run:
            for i in range(1, 31):
                try:
                    run_cmd(f"kubectl get secret {corndogs_pg_secret} -n {namespace}", check=True, capture=True)
                    break
                except subprocess.CalledProcessError:
                    log(f"Waiting for corndogs credentials secret... ({i}/30)")
                    time.sleep(2)

            corndogs_db_user = run_cmd_output(
                f"kubectl get secret {corndogs_pg_secret} -n {namespace} -o jsonpath='{{.data.username}}' | base64 -d"
            )
            corndogs_db_pass = run_cmd_output(
                f"kubectl get secret {corndogs_pg_secret} -n {namespace} -o jsonpath='{{.data.password}}' | base64 -d"
            )

            corndogs_db_config = {
                'host': pg_host,
                'name': 'corndogs',
                'user': corndogs_db_user,
                'password': corndogs_db_pass,
            }

            # Register for masking
            secrets_socket = os.environ.get('REACTORCIDE_SECRETS_SOCKET')
            if secrets_socket:
                try:
                    subprocess.run(["python", "-m", "src.register_secret", corndogs_db_pass], check=False, capture_output=True)
                except Exception:
                    pass
        else:
            corndogs_db_config = {
                'host': pg_host,
                'name': 'corndogs',
                'user': 'corndogs',
                'password': '***',
            }

    return db_uri, corndogs_db_config


def deploy_corndogs(config: Dict[str, Any], corndogs_db_config: Optional[Dict], dry_run: bool = False) -> str:
    """Deploy Corndogs to the cluster.

    Returns:
        The Corndogs URL.
    """
    log("Deploying Corndogs")

    namespace = config['namespace']

    # Determine corndogs DB config
    if corndogs_db_config:
        db_host = corndogs_db_config['host']
        db_name = corndogs_db_config['name']
        db_user = corndogs_db_config['user']
        db_pass = corndogs_db_config['password']
    else:
        if not config['corndogs_db_host']:
            raise RuntimeError("REACTORCIDE_CORNDOGS_DB_HOST required when deploying Corndogs without PROVISION_POSTGRES")
        db_host = config['corndogs_db_host']
        db_name = config['corndogs_db_name']
        db_user = config['corndogs_db_user']
        db_pass = config['corndogs_db_password']

    # Add helm repo
    run_cmd("helm repo add catalystcommunity https://raw.githubusercontent.com/catalystcommunity/charts/main || true", dry_run=dry_run)
    run_cmd("helm repo update", dry_run=dry_run)

    # Install corndogs
    cmd = (
        f"helm upgrade --install corndogs catalystcommunity/corndogs "
        f"--namespace {namespace} "
        f"--set postgresql.enabled=false "
        f"--set database.host={db_host} "
        f"--set database.dbname={db_name} "
        f"--set database.user={db_user} "
        f"--set database.password={db_pass} "
        f"--wait --timeout 5m"
    )
    run_cmd(cmd, dry_run=dry_run)

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

    # Database
    args.extend(["--set", f"postgres.uri={db_uri}"])

    # Subcharts always disabled in production
    args.extend(["--set", "postgresql.enabled=false"])
    args.extend(["--set", "corndogs.enabled=false"])

    # Corndogs URL
    if corndogs_url:
        args.extend(["--set", f"corndogs.baseUrl={corndogs_url}"])

    # Image tag
    if config['image_tag']:
        args.extend(["--set", f"app.image.tag={config['image_tag']}"])
        args.extend(["--set", f"worker.image.tag={config['image_tag']}"])

    # Object storage
    args.extend(["--set", f"objectStore.type={config['object_store_type']}"])

    if config['object_store_type'] == 's3':
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

    # Default user
    if config['default_user_id']:
        args.extend(["--set", f"defaults.userId={config['default_user_id']}"])

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


def wait_for_rollout(config: Dict[str, Any], dry_run: bool = False) -> None:
    """Wait for deployment rollout."""
    log("Waiting for rollout")

    namespace = config['namespace']
    release = config['release']

    run_cmd(f"kubectl rollout status deployment/{release}app -n {namespace} --timeout=5m || true", dry_run=dry_run)
    run_cmd(f"kubectl rollout status deployment/{release}-worker -n {namespace} --timeout=5m || true", dry_run=dry_run)


def run_migrations(config: Dict[str, Any], dry_run: bool = False) -> None:
    """Run database migrations."""
    log("Running migrations")

    namespace = config['namespace']
    release = config['release']

    if dry_run:
        log("(would run migrations)")
        return

    # Find coordinator pod
    coordinator_pod = ""
    try:
        coordinator_pod = run_cmd_output(
            f"kubectl get pods -n {namespace} "
            f"-l 'app.kubernetes.io/name=reactorcide,app.kubernetes.io/component=app' "
            f"-o jsonpath='{{.items[0].metadata.name}}'"
        )
    except subprocess.CalledProcessError:
        pass

    if not coordinator_pod:
        try:
            coordinator_pod = run_cmd_output(
                f"kubectl get pods -n {namespace} "
                f"-l 'app.kubernetes.io/instance={release}' "
                f"-o jsonpath='{{.items[0].metadata.name}}'"
            )
        except subprocess.CalledProcessError:
            pass

    if not coordinator_pod:
        log("ERROR: Could not find coordinator pod")
        run_cmd(f"kubectl get pods -n {namespace}")
        raise RuntimeError("Could not find coordinator pod")

    run_cmd(f"kubectl exec -n {namespace} {coordinator_pod} -- /reactorcide migrate")


def create_api_token(config: Dict[str, Any], dry_run: bool = False) -> None:
    """Create API token and store in secret."""
    log("Creating API token")

    namespace = config['namespace']
    release = config['release']
    default_user_id = config['default_user_id'] or str(uuid.uuid4())

    if dry_run:
        log("(would create API token)")
        return

    # Find coordinator pod
    coordinator_pod = run_cmd_output(
        f"kubectl get pods -n {namespace} "
        f"-l 'app.kubernetes.io/name=reactorcide,app.kubernetes.io/component=app' "
        f"-o jsonpath='{{.items[0].metadata.name}}'"
    )

    if not coordinator_pod:
        coordinator_pod = run_cmd_output(
            f"kubectl get pods -n {namespace} "
            f"-l 'app.kubernetes.io/instance={release}' "
            f"-o jsonpath='{{.items[0].metadata.name}}'"
        )

    from datetime import datetime
    token_name = f"deploy-{datetime.now().strftime('%Y%m%d-%H%M%S')}"

    try:
        token_output = run_cmd_output(
            f"kubectl exec -n {namespace} {coordinator_pod} -- "
            f"/reactorcide token create --name {token_name} --user-id {default_user_id}"
        )

        # Parse token output
        api_token = ""
        token_id = ""
        for line in token_output.split('\n'):
            if line.startswith("Token: "):
                api_token = line.split(" ", 1)[1]
            elif line.startswith("Token ID: "):
                token_id = line.split(" ", 2)[2] if len(line.split(" ")) > 2 else ""

        if api_token:
            # Register for masking
            secrets_socket = os.environ.get('REACTORCIDE_SECRETS_SOCKET')
            if secrets_socket:
                try:
                    subprocess.run(["python", "-m", "src.register_secret", api_token], check=False, capture_output=True)
                except Exception:
                    pass

            # Store in k8s secret
            run_cmd(
                f"kubectl create secret generic {release}-api-token "
                f"--namespace {namespace} "
                f"--from-literal=token={api_token} "
                f"--from-literal=token-id={token_id or 'unknown'} "
                f"--from-literal=user-id={default_user_id} "
                f"--dry-run=client -o yaml | kubectl apply -f -"
            )
            log(f"API token stored in secret: {release}-api-token")
        else:
            log("WARNING: Failed to create API token")

    except subprocess.CalledProcessError as e:
        log(f"WARNING: Failed to create API token: {e}")


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

    # Step 5a: Provision PostgreSQL or use provided URI
    db_uri = config['db_uri']
    corndogs_db_config = None

    if config['provision_postgres']:
        log("")
        log("Step 5a: Provisioning PostgreSQL via Zalando operator")
        db_uri, corndogs_db_config = provision_postgres(config, dry_run=dry_run)
    else:
        log("")
        log("Step 5a: Using provided database URI")

    # Step 5b: Deploy Corndogs
    corndogs_url = config['corndogs_url']
    if config['deploy_corndogs']:
        log("")
        log("Step 5b: Deploying Corndogs")
        corndogs_url = deploy_corndogs(config, corndogs_db_config, dry_run=dry_run)
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
    deploy_helm(config, helm_args, dry_run=dry_run)

    # Step 8: Wait for rollout
    log("")
    log("Step 8: Waiting for rollout")
    wait_for_rollout(config, dry_run=dry_run)

    # Step 9: Run migrations
    log("")
    log("Step 9: Running migrations")
    run_migrations(config, dry_run=dry_run)

    # Step 10: Create API token
    log("")
    log("Step 10: Creating API token")
    create_api_token(config, dry_run=dry_run)

    # Step 11: Show status
    log("")
    log("Step 11: Deployment status")
    show_status(config, dry_run=dry_run)

    log("")
    log("=" * 50)
    log("Deployment complete!")
    log("=" * 50)
    log("")
    log("Retrieve API token:")
    log(f"  kubectl get secret {release}-api-token -n {namespace} -o jsonpath='{{.data.token}}' | base64 -d")
    log("")
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
