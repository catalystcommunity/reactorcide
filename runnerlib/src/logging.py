"""Logging utilities for runnerlib."""

import json
import os
import sys
from datetime import datetime, UTC
from typing import Literal


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