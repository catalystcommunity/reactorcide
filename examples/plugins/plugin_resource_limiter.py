"""Example plugin that enforces resource limits on containers."""

import os
from typing import List
from src.plugins import Plugin, PluginPhase, PluginContext
from src.logging import logger


class ResourceLimiterPlugin(Plugin):
    """Plugin that adds resource limits to container execution."""

    def __init__(self):
        super().__init__(name="resource_limiter", priority=75)

        # Read limits from environment with defaults
        self.memory_limit = os.environ.get("JOB_MEMORY_LIMIT", "2g")
        self.cpu_limit = os.environ.get("JOB_CPU_LIMIT", "2")
        self.enable_limits = os.environ.get("ENABLE_RESOURCE_LIMITS", "true").lower() == "true"

    def supported_phases(self) -> List[PluginPhase]:
        """This plugin runs before container execution."""
        return [PluginPhase.PRE_CONTAINER]

    def execute(self, context: PluginContext) -> None:
        """Add resource limit flags to container command."""
        if context.phase != PluginPhase.PRE_CONTAINER or not self.enable_limits:
            return

        # Store resource limits in metadata for container to use
        context.metadata["resource_limits"] = {
            "memory": self.memory_limit,
            "cpus": self.cpu_limit
        }

        # Note: In the actual implementation, these would be added to the docker command
        # For now, we just log what would be added
        logger.info(
            "Resource limits configured",
            fields={
                "plugin": self.name,
                "memory_limit": self.memory_limit,
                "cpu_limit": self.cpu_limit,
                "enabled": self.enable_limits
            }
        )

    def pre_container(self, context: PluginContext) -> None:
        """Validate resource limits before container execution."""
        if not self.enable_limits:
            logger.debug(f"Resource limits disabled")
            return

        # Validate memory limit format
        if not self._validate_memory_limit(self.memory_limit):
            logger.warning(f"Invalid memory limit format: {self.memory_limit}, using default")
            self.memory_limit = "2g"

        # Validate CPU limit
        try:
            cpu_float = float(self.cpu_limit)
            if cpu_float <= 0 or cpu_float > 16:
                logger.warning(f"CPU limit out of range: {self.cpu_limit}, using default")
                self.cpu_limit = "2"
        except ValueError:
            logger.warning(f"Invalid CPU limit: {self.cpu_limit}, using default")
            self.cpu_limit = "2"

    def _validate_memory_limit(self, limit: str) -> bool:
        """Validate memory limit format (e.g., 512m, 2g)."""
        if not limit:
            return False

        # Check if it ends with valid unit
        valid_units = ['b', 'k', 'm', 'g']
        if limit[-1].lower() not in valid_units:
            return False

        # Check if the number part is valid
        try:
            value = float(limit[:-1])
            return value > 0
        except ValueError:
            return False