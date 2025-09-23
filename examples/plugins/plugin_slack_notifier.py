"""Example plugin that sends notifications to Slack."""

import os
import json
from typing import List
from src.plugins import Plugin, PluginPhase, PluginContext
from src.logging import logger


class SlackNotifierPlugin(Plugin):
    """Plugin that sends job status notifications to Slack."""

    def __init__(self):
        super().__init__(name="slack_notifier", priority=200)  # Run late
        self.webhook_url = os.environ.get("SLACK_WEBHOOK_URL")
        self.channel = os.environ.get("SLACK_CHANNEL", "#ci-notifications")
        self.enabled = bool(self.webhook_url)

    def supported_phases(self) -> List[PluginPhase]:
        """This plugin runs after container execution and on errors."""
        return [PluginPhase.POST_CONTAINER, PluginPhase.ON_ERROR]

    def execute(self, context: PluginContext) -> None:
        """Send notification to Slack."""
        if not self.webhook_url:
            return

        message = self._build_message(context)
        self._send_to_slack(message)

    def _build_message(self, context: PluginContext) -> dict:
        """Build Slack message from context."""
        job_name = context.config.job_command.split()[0] if context.config.job_command else "Unknown"
        duration = context.metadata.get("duration", 0)

        if context.phase == PluginPhase.ON_ERROR:
            color = "danger"
            status = "Failed"
            text = f"Job failed with error: {context.error}"
        else:
            if context.exit_code == 0:
                color = "good"
                status = "Success"
                text = f"Job completed successfully"
            else:
                color = "warning"
                status = "Failed"
                text = f"Job failed with exit code {context.exit_code}"

        return {
            "channel": self.channel,
            "username": "Reactorcide CI",
            "attachments": [
                {
                    "color": color,
                    "title": f"Job {status}: {job_name}",
                    "text": text,
                    "fields": [
                        {
                            "title": "Image",
                            "value": context.config.runner_image,
                            "short": True
                        },
                        {
                            "title": "Duration",
                            "value": f"{duration:.1f} seconds",
                            "short": True
                        }
                    ],
                    "footer": "Reactorcide",
                    "ts": int(context.metadata.get("start_time", 0))
                }
            ]
        }

    def _send_to_slack(self, message: dict) -> None:
        """Send message to Slack webhook."""
        try:
            # In a real implementation, you would use requests or urllib
            # For this example, we'll just log what would be sent
            logger.info(
                "Slack notification (simulated)",
                fields={
                    "plugin": self.name,
                    "channel": message["channel"],
                    "status": message["attachments"][0]["title"],
                    "webhook_configured": bool(self.webhook_url)
                }
            )
            # Example of what the real implementation would look like:
            # import requests
            # response = requests.post(self.webhook_url, json=message)
            # response.raise_for_status()
        except Exception as e:
            logger.error(f"Failed to send Slack notification: {e}")

    def post_container(self, context: PluginContext) -> None:
        """Send notification after container execution."""
        if context.exit_code != 0:
            logger.debug(f"Slack notifier: Job failed with exit code {context.exit_code}")

    def on_error(self, context: PluginContext) -> None:
        """Send notification on error."""
        logger.debug(f"Slack notifier: Job error occurred: {context.error}")