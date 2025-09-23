"""Logging utilities for runnerlib with structured logging support."""

import json
import os
import sys
from datetime import datetime, UTC
from typing import Literal, Dict, Any, Optional
from enum import Enum


class LogLevel(Enum):
    """Log levels for structured logging."""
    DEBUG = "debug"
    INFO = "info"
    WARNING = "warning"
    ERROR = "error"
    FATAL = "fatal"


class StructuredLogger:
    """Structured logger for runnerlib."""

    def __init__(self, component: str = "runnerlib"):
        """Initialize logger with component name."""
        self.component = component
        self.log_format = os.getenv("LOG_FORMAT", "text")
        self.log_level = os.getenv("LOG_LEVEL", "INFO").upper()
        self._level_priority = {
            "DEBUG": 0,
            "INFO": 1,
            "WARNING": 2,
            "ERROR": 3,
            "FATAL": 4
        }

    def _should_log(self, level: LogLevel) -> bool:
        """Check if we should log at this level."""
        level_priority = self._level_priority.get(level.name, 1)
        current_priority = self._level_priority.get(self.log_level, 1)
        return level_priority >= current_priority

    def _format_message(
        self,
        level: LogLevel,
        message: str,
        fields: Optional[Dict[str, Any]] = None,
        error: Optional[Exception] = None
    ) -> str:
        """Format log message based on LOG_FORMAT."""
        timestamp = datetime.now(UTC).isoformat()

        if self.log_format == "json":
            log_entry = {
                "timestamp": timestamp,
                "level": level.value,
                "component": self.component,
                "message": message
            }

            # Add custom fields
            if fields:
                log_entry["fields"] = fields

            # Add error information
            if error:
                log_entry["error"] = {
                    "type": type(error).__name__,
                    "message": str(error)
                }

            return json.dumps(log_entry, default=str)
        else:
            # Text format
            level_str = f"[{level.name}]"
            component_str = f"[{self.component}]"

            # Build fields string
            fields_str = ""
            if fields:
                field_pairs = [f"{k}={v}" for k, v in fields.items()]
                fields_str = f" {' '.join(field_pairs)}"

            # Add error string
            error_str = ""
            if error:
                error_str = f" error={type(error).__name__}: {str(error)}"

            return f"{timestamp} {level_str} {component_str} {message}{fields_str}{error_str}"

    def _log(
        self,
        level: LogLevel,
        message: str,
        fields: Optional[Dict[str, Any]] = None,
        error: Optional[Exception] = None,
        stream: Literal["stdout", "stderr"] = "stderr"
    ):
        """Internal log method."""
        if not self._should_log(level):
            return

        output = self._format_message(level, message, fields, error)

        if stream == "stderr":
            print(output, file=sys.stderr)
        else:
            print(output)

    def debug(self, message: str, fields: Optional[Dict[str, Any]] = None):
        """Log debug message."""
        self._log(LogLevel.DEBUG, message, fields)

    def info(self, message: str, fields: Optional[Dict[str, Any]] = None):
        """Log info message."""
        self._log(LogLevel.INFO, message, fields)

    def warning(self, message: str, fields: Optional[Dict[str, Any]] = None):
        """Log warning message."""
        self._log(LogLevel.WARNING, message, fields)

    def error(self, message: str, error: Optional[Exception] = None, fields: Optional[Dict[str, Any]] = None):
        """Log error message."""
        self._log(LogLevel.ERROR, message, fields, error)

    def fatal(self, message: str, error: Optional[Exception] = None, fields: Optional[Dict[str, Any]] = None):
        """Log fatal message and exit."""
        self._log(LogLevel.FATAL, message, fields, error)
        sys.exit(1)

    def with_component(self, component: str) -> "StructuredLogger":
        """Create a new logger with a different component name."""
        return StructuredLogger(component)


# Create default logger instance
logger = StructuredLogger()


# Keep backward compatibility functions
def log_line(message: str, stream: Literal["stdout", "stderr"] = "stdout"):
    """Log a line with appropriate formatting based on LOG_FORMAT environment variable."""
    log_format = os.getenv("LOG_FORMAT", "text")
    timestamp = datetime.now(UTC).isoformat()

    if log_format == "json":
        log_entry = {
            "timestamp": timestamp,
            "stream": stream,
            "message": message
        }
        output = json.dumps(log_entry)
    else:
        output = f"{timestamp} {message}"

    if stream == "stderr":
        print(output, file=sys.stderr)
    else:
        print(output)


def log_stdout(message: str):
    """Log a message to stdout."""
    log_line(message, "stdout")


def log_stderr(message: str):
    """Log a message to stderr."""
    log_line(message, "stderr")