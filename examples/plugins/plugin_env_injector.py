"""Example plugin that injects additional environment variables."""

from typing import List
from src.plugins import Plugin, PluginPhase, PluginContext
from src.logging import logger


class EnvironmentInjectorPlugin(Plugin):
    """Plugin that injects custom environment variables before container execution."""

    def __init__(self):
        super().__init__(name="env_injector", priority=50)

    def supported_phases(self) -> List[PluginPhase]:
        """This plugin runs before container execution."""
        return [PluginPhase.PRE_CONTAINER]

    def execute(self, context: PluginContext) -> None:
        """Inject custom environment variables."""
        if context.phase != PluginPhase.PRE_CONTAINER:
            return

        # Example: Add build metadata
        if context.env_vars is not None:
            context.env_vars["PLUGIN_INJECTED"] = "true"
            context.env_vars["BUILD_TIMESTAMP"] = str(context.metadata.get("start_time", ""))

            # Add CI/CD platform detection
            if "GITHUB_ACTIONS" in context.env_vars:
                context.env_vars["CI_PLATFORM"] = "github"
            elif "GITLAB_CI" in context.env_vars:
                context.env_vars["CI_PLATFORM"] = "gitlab"
            elif "JENKINS_HOME" in context.env_vars:
                context.env_vars["CI_PLATFORM"] = "jenkins"
            else:
                context.env_vars["CI_PLATFORM"] = "reactorcide"

            logger.info(
                "Injected environment variables",
                fields={
                    "plugin": self.name,
                    "ci_platform": context.env_vars.get("CI_PLATFORM"),
                    "variables_count": 3
                }
            )

    def pre_container(self, context: PluginContext) -> None:
        """Additional pre-container hook."""
        logger.debug(f"Environment injector plugin preparing for container execution")