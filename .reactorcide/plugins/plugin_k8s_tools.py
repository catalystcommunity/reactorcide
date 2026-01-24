#!/usr/bin/env python3
"""Plugin to install Kubernetes tools after source checkout.

Can be tested independently:
    INSTALL_HELM=true INSTALL_KUBECTL=true python plugin_k8s_tools.py
"""
import subprocess
import os
from pathlib import Path

# Only import runnerlib modules when running as plugin
try:
    from src.plugins import Plugin, PluginPhase, PluginContext
    from src.logging import log_stdout
    HAS_RUNNERLIB = True
except ImportError:
    HAS_RUNNERLIB = False
    def log_stdout(msg):
        print(msg, flush=True)


def install_helm(local_bin: Path) -> None:
    """Install helm to the specified directory."""
    if (local_bin / "helm").exists():
        log_stdout("Helm already installed")
        return

    log_stdout("Installing helm...")
    cmd = (
        "curl -fsSL https://get.helm.sh/helm-v3.17.0-linux-amd64.tar.gz | tar xz -C /tmp && "
        f"mv /tmp/linux-amd64/helm {local_bin}/helm && "
        f"chmod +x {local_bin}/helm && "
        "rm -rf /tmp/linux-amd64"
    )
    subprocess.run(["sh", "-c", cmd], check=True)
    log_stdout("Helm installed successfully")


def install_kubectl(local_bin: Path) -> None:
    """Install kubectl to the specified directory."""
    if (local_bin / "kubectl").exists():
        log_stdout("Kubectl already installed")
        return

    log_stdout("Installing kubectl...")
    cmd = (
        'curl -L "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" '
        f'-o /tmp/kubectl && mv /tmp/kubectl {local_bin}/kubectl && chmod +x {local_bin}/kubectl'
    )
    subprocess.run(["sh", "-c", cmd], check=True)
    log_stdout("Kubectl installed successfully")


def install_tools() -> None:
    """Install helm and kubectl to ~/.local/bin."""
    local_bin = Path.home() / ".local" / "bin"
    local_bin.mkdir(parents=True, exist_ok=True)

    # Update PATH
    os.environ["PATH"] = f"{local_bin}:{os.environ.get('PATH', '')}"

    install_helm(local_bin)
    install_kubectl(local_bin)


if HAS_RUNNERLIB:
    class K8sToolsPlugin(Plugin):
        """Plugin to install Kubernetes tools after source checkout."""

        def __init__(self):
            super().__init__(name="k8s_tools", priority=10)

        def supported_phases(self):
            return [PluginPhase.POST_SOURCE_PREP]

        def execute(self, context: PluginContext):
            if context.phase == PluginPhase.POST_SOURCE_PREP:
                install_tools()


if __name__ == "__main__":
    # Allow standalone testing - uses temp directory by default to avoid clobbering home
    import tempfile
    import shutil

    test_dir = Path(tempfile.mkdtemp(prefix="k8s_tools_test_"))
    print(f"Testing K8s tools installation in {test_dir}...")

    try:
        if os.environ.get("INSTALL_HELM", "").lower() == "true":
            install_helm(test_dir)
            print(f"Helm installed to {test_dir / 'helm'}")

        if os.environ.get("INSTALL_KUBECTL", "").lower() == "true":
            install_kubectl(test_dir)
            print(f"Kubectl installed to {test_dir / 'kubectl'}")

        if os.environ.get("INSTALL_ALL", "").lower() == "true":
            # For INSTALL_ALL, still uses temp dir but simulates the real flow
            os.environ["PATH"] = f"{test_dir}:{os.environ.get('PATH', '')}"
            install_helm(test_dir)
            install_kubectl(test_dir)
            print(f"All tools installed to {test_dir}")

        print("Test complete - tools available in temp dir")
        print(f"To keep: export PATH={test_dir}:$PATH")
        print(f"To clean: rm -rf {test_dir}")
    except Exception as e:
        print(f"Test failed: {e}")
        shutil.rmtree(test_dir, ignore_errors=True)
        raise
