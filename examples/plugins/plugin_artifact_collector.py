"""Example plugin that collects and archives job artifacts."""

import os
import shutil
from pathlib import Path
from typing import List
from src.plugins import Plugin, PluginPhase, PluginContext
from src.logging import logger


class ArtifactCollectorPlugin(Plugin):
    """Plugin that collects specified artifacts after job execution."""

    def __init__(self):
        super().__init__(name="artifact_collector", priority=150)
        self.artifact_dir = os.environ.get("ARTIFACT_DIR", "/tmp/artifacts")
        self.artifact_patterns = os.environ.get("ARTIFACT_PATTERNS", "*.log,*.tar.gz,build/**").split(",")

    def supported_phases(self) -> List[PluginPhase]:
        """This plugin runs after source preparation and container execution."""
        return [PluginPhase.POST_SOURCE_PREP, PluginPhase.POST_CONTAINER]

    def execute(self, context: PluginContext) -> None:
        """Collect artifacts based on phase."""
        if context.phase == PluginPhase.POST_SOURCE_PREP:
            # Prepare artifact directory
            self._prepare_artifact_directory(context)

        elif context.phase == PluginPhase.POST_CONTAINER:
            # Collect artifacts after job execution
            self._collect_artifacts(context)

    def _prepare_artifact_directory(self, context: PluginContext) -> None:
        """Prepare the artifact collection directory."""
        if not context.job_path:
            return

        # Create job-specific artifact directory
        job_id = context.metadata.get("job_id", "unknown")
        artifact_path = Path(self.artifact_dir) / job_id
        artifact_path.mkdir(parents=True, exist_ok=True)

        context.metadata["artifact_path"] = str(artifact_path)
        logger.info(
            "Artifact directory prepared",
            fields={
                "plugin": self.name,
                "path": str(artifact_path)
            }
        )

    def _collect_artifacts(self, context: PluginContext) -> None:
        """Collect artifacts from job directory."""
        if not context.job_path:
            return

        artifact_path = context.metadata.get("artifact_path")
        if not artifact_path:
            logger.warning("Artifact path not set, skipping collection")
            return

        collected = []
        job_path = Path(context.job_path)

        for pattern in self.artifact_patterns:
            pattern = pattern.strip()

            # Handle glob patterns
            if "*" in pattern or "**" in pattern:
                for file_path in job_path.glob(pattern):
                    if file_path.is_file():
                        self._copy_artifact(file_path, Path(artifact_path), job_path)
                        collected.append(str(file_path.relative_to(job_path)))
            else:
                # Handle exact file paths
                file_path = job_path / pattern
                if file_path.exists() and file_path.is_file():
                    self._copy_artifact(file_path, Path(artifact_path), job_path)
                    collected.append(pattern)

        if collected:
            logger.info(
                "Artifacts collected",
                fields={
                    "plugin": self.name,
                    "count": len(collected),
                    "files": collected[:5],  # Log first 5 files
                    "destination": artifact_path
                }
            )
        else:
            logger.info(
                "No artifacts found to collect",
                fields={
                    "plugin": self.name,
                    "patterns": self.artifact_patterns
                }
            )

    def _copy_artifact(self, source: Path, dest_dir: Path, job_path: Path) -> None:
        """Copy a single artifact preserving directory structure."""
        try:
            # Preserve relative path structure
            rel_path = source.relative_to(job_path)
            dest_file = dest_dir / rel_path

            # Create parent directories
            dest_file.parent.mkdir(parents=True, exist_ok=True)

            # Copy the file
            shutil.copy2(source, dest_file)
            logger.debug(f"Copied artifact: {rel_path}")
        except Exception as e:
            logger.error(f"Failed to copy artifact {source}: {e}")