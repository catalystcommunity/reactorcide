"""Example plugin that tracks job execution time."""

import time
from typing import List
from src.plugins import Plugin, PluginPhase, PluginContext
from src.logging import logger


class JobTimerPlugin(Plugin):
    """Plugin that tracks and reports job execution time."""

    def __init__(self):
        super().__init__(name="job_timer", priority=10)  # Run early

    def supported_phases(self) -> List[PluginPhase]:
        """This plugin runs at start and end of job."""
        return [
            PluginPhase.PRE_VALIDATION,
            PluginPhase.POST_CONTAINER,
            PluginPhase.ON_ERROR
        ]

    def execute(self, context: PluginContext) -> None:
        """Track job timing."""
        if context.phase == PluginPhase.PRE_VALIDATION:
            # Start timing
            context.metadata["start_time"] = time.time()
            logger.info(f"Job timer started", fields={"plugin": self.name})

        elif context.phase in [PluginPhase.POST_CONTAINER, PluginPhase.ON_ERROR]:
            # Calculate duration
            start_time = context.metadata.get("start_time")
            if start_time:
                duration = time.time() - start_time
                context.metadata["duration"] = duration

                # Log execution time
                logger.info(
                    "Job execution time",
                    fields={
                        "plugin": self.name,
                        "duration_seconds": round(duration, 2),
                        "exit_code": context.exit_code,
                        "success": context.exit_code == 0 if context.exit_code is not None else False
                    }
                )

                # Warn if job took too long
                if duration > 300:  # 5 minutes
                    logger.warning(
                        "Job took longer than expected",
                        fields={
                            "plugin": self.name,
                            "duration_seconds": round(duration, 2),
                            "threshold_seconds": 300
                        }
                    )