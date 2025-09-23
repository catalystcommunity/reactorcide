# Plugin Development Guide

## Overview

Reactorcide's plugin system allows you to extend the job execution lifecycle with custom logic. Plugins can hook into various phases of job execution to add functionality like notifications, resource management, artifact collection, and more.

## Plugin Architecture

### Plugin Lifecycle Phases

Plugins can hook into the following phases:

1. **PRE_VALIDATION** - Before configuration validation
2. **POST_VALIDATION** - After configuration validation
3. **PRE_SOURCE_PREP** - Before git checkout/directory copy
4. **POST_SOURCE_PREP** - After source is prepared
5. **PRE_CONTAINER** - Before container execution
6. **POST_CONTAINER** - After container execution (includes exit code)
7. **ON_ERROR** - When an error occurs
8. **CLEANUP** - During cleanup phase

### Plugin Loading

Plugins are loaded from:
1. Built-in plugins directory (`src/builtin_plugins/`)
2. Environment variable `REACTORCIDE_PLUGIN_DIR`
3. CLI option `--plugin-dir`

**Important**: Only `.py` files starting with `plugin_` prefix are loaded as plugins.

## Creating a Plugin

### Basic Plugin Structure

```python
from typing import List
from src.plugins import Plugin, PluginPhase, PluginContext
from src.logging import logger


class MyCustomPlugin(Plugin):
    """Description of what your plugin does."""

    def __init__(self):
        super().__init__(
            name="my_custom_plugin",  # Unique plugin name
            priority=100               # Execution priority (lower = earlier)
        )

    def supported_phases(self) -> List[PluginPhase]:
        """Return list of phases this plugin supports."""
        return [PluginPhase.PRE_CONTAINER, PluginPhase.POST_CONTAINER]

    def execute(self, context: PluginContext) -> None:
        """Main plugin logic executed for all supported phases."""
        if context.phase == PluginPhase.PRE_CONTAINER:
            self._before_container(context)
        elif context.phase == PluginPhase.POST_CONTAINER:
            self._after_container(context)

    def _before_container(self, context: PluginContext):
        """Logic to run before container execution."""
        logger.info(f"Plugin {self.name} preparing for container execution")
        # Your custom logic here

    def _after_container(self, context: PluginContext):
        """Logic to run after container execution."""
        exit_code = context.exit_code
        logger.info(f"Plugin {self.name} processing results, exit code: {exit_code}")
        # Your custom logic here
```

### Plugin Context

The `PluginContext` object provides information about the current execution:

```python
@dataclass
class PluginContext:
    config: RunnerConfig           # Job configuration
    phase: PluginPhase             # Current lifecycle phase
    job_path: Optional[Path]       # Path to job directory
    env_vars: Optional[Dict]       # Environment variables
    exit_code: Optional[int]       # Container exit code (POST_CONTAINER only)
    error: Optional[Exception]     # Error that occurred (ON_ERROR only)
    metadata: Dict[str, Any]       # Shared metadata between plugins
```

### Plugin Priority

- Plugins are executed in priority order (lowest number first)
- Default priority is 100
- Recommended ranges:
  - 0-50: Critical system plugins
  - 50-100: Resource management, environment setup
  - 100-150: General purpose plugins
  - 150-200: Notification, reporting plugins
  - 200+: Cleanup, archival plugins

## Example Plugins

### Environment Variable Injection

```python
class EnvironmentInjectorPlugin(Plugin):
    """Inject custom environment variables."""

    def __init__(self):
        super().__init__(name="env_injector", priority=50)

    def supported_phases(self) -> List[PluginPhase]:
        return [PluginPhase.PRE_CONTAINER]

    def execute(self, context: PluginContext) -> None:
        if context.phase == PluginPhase.PRE_CONTAINER and context.env_vars:
            # Add custom environment variables
            context.env_vars["BUILD_ID"] = str(uuid.uuid4())
            context.env_vars["BUILD_TIMESTAMP"] = datetime.now().isoformat()
```

### Job Timer

```python
class JobTimerPlugin(Plugin):
    """Track job execution time."""

    def __init__(self):
        super().__init__(name="job_timer", priority=10)

    def supported_phases(self) -> List[PluginPhase]:
        return [PluginPhase.PRE_VALIDATION, PluginPhase.POST_CONTAINER]

    def execute(self, context: PluginContext) -> None:
        if context.phase == PluginPhase.PRE_VALIDATION:
            context.metadata["start_time"] = time.time()
        elif context.phase == PluginPhase.POST_CONTAINER:
            duration = time.time() - context.metadata.get("start_time", 0)
            logger.info(f"Job completed in {duration:.2f} seconds")
```

### Resource Limiter

```python
class ResourceLimiterPlugin(Plugin):
    """Add resource limits to container execution."""

    def __init__(self):
        super().__init__(name="resource_limiter", priority=75)
        self.memory_limit = os.environ.get("JOB_MEMORY_LIMIT", "2g")
        self.cpu_limit = os.environ.get("JOB_CPU_LIMIT", "2")

    def supported_phases(self) -> List[PluginPhase]:
        return [PluginPhase.PRE_CONTAINER]

    def execute(self, context: PluginContext) -> None:
        if context.phase == PluginPhase.PRE_CONTAINER:
            # Store limits in metadata for container to use
            context.metadata["resource_limits"] = {
                "memory": self.memory_limit,
                "cpus": self.cpu_limit
            }
```

## Plugin Best Practices

### 1. Error Handling

Always handle errors gracefully:

```python
def execute(self, context: PluginContext) -> None:
    try:
        self._do_work(context)
    except SpecificError as e:
        logger.error(f"Plugin {self.name} failed: {e}")
        # Decide whether to re-raise (abort job) or continue
        if self._is_critical_error(e):
            raise  # Aborts job execution
        # Otherwise, log and continue
```

### 2. Configuration

Use environment variables for plugin configuration:

```python
def __init__(self):
    super().__init__(name="my_plugin", priority=100)
    self.enabled = os.environ.get("MY_PLUGIN_ENABLED", "true").lower() == "true"
    self.config_value = os.environ.get("MY_PLUGIN_CONFIG", "default")
```

### 3. Metadata Sharing

Use `context.metadata` to share data between phases:

```python
def execute(self, context: PluginContext) -> None:
    if context.phase == PluginPhase.PRE_CONTAINER:
        # Store data for later phases
        context.metadata["my_plugin_data"] = {"key": "value"}
    elif context.phase == PluginPhase.POST_CONTAINER:
        # Retrieve data from earlier phases
        data = context.metadata.get("my_plugin_data", {})
```

### 4. Logging

Use structured logging for better observability:

```python
logger.info(
    "Plugin action completed",
    fields={
        "plugin": self.name,
        "phase": context.phase.value,
        "duration": duration,
        "result": "success"
    }
)
```

### 5. Idempotency

Make plugins idempotent where possible:

```python
def _prepare_directory(self, path: Path):
    """Create directory if it doesn't exist."""
    if not path.exists():
        path.mkdir(parents=True, exist_ok=True)
        logger.info(f"Created directory: {path}")
    else:
        logger.debug(f"Directory already exists: {path}")
```

## Testing Plugins

### Unit Testing

```python
import pytest
from unittest.mock import Mock, patch
from src.plugins import PluginContext, PluginPhase
from my_plugin import MyCustomPlugin


def test_plugin_pre_container():
    """Test plugin pre-container behavior."""
    plugin = MyCustomPlugin()

    context = PluginContext(
        config=Mock(),
        phase=PluginPhase.PRE_CONTAINER,
        env_vars={"KEY": "value"},
        metadata={}
    )

    plugin.execute(context)

    # Assert expected behavior
    assert "my_key" in context.env_vars
    assert context.metadata.get("plugin_ran") is True
```

### Integration Testing

```python
def test_plugin_with_job_execution():
    """Test plugin in actual job execution."""
    # Load plugin
    plugin_manager.register_plugin(MyCustomPlugin())

    # Run job
    config = get_config(job_command="echo test")
    exit_code = run_container(config)

    # Verify plugin effects
    assert exit_code == 0
    # Check for expected side effects
```

## Debugging Plugins

### Enable Debug Logging

Set log level to debug:

```bash
export REACTORCIDE_LOG_LEVEL=DEBUG
```

### Test in Dry-Run Mode

Test plugins without executing containers:

```bash
runnerlib run --dry-run --plugin-dir ./my_plugins
```

### Interactive Development

Test plugins interactively:

```python
from src.plugins import plugin_manager, PluginContext, PluginPhase
from src.config import get_config

# Load your plugin
plugin_manager.load_plugin_from_file("./plugin_example.py")

# Create test context
config = get_config()
context = PluginContext(
    config=config,
    phase=PluginPhase.PRE_CONTAINER,
    metadata={}
)

# Execute plugin
plugin_manager.execute_phase(PluginPhase.PRE_CONTAINER, context)
```

## Plugin Distribution

### Package Structure

```
my_plugin_package/
├── plugin_my_feature.py      # Plugin implementation
├── requirements.txt           # Dependencies
├── README.md                  # Documentation
└── tests/
    └── test_my_feature.py     # Tests
```

### Installation

Users can install plugins by:

1. Copying to plugin directory:
```bash
cp plugin_*.py /path/to/plugins/
runnerlib run --plugin-dir /path/to/plugins
```

2. Using environment variable:
```bash
export REACTORCIDE_PLUGIN_DIR=/path/to/plugins
runnerlib run
```

3. Adding to built-in plugins (for system-wide deployment)

## Security Considerations

1. **Trusted Sources**: Only load plugins from trusted sources
2. **Sandboxing**: Plugins run in the same process as runnerlib
3. **Resource Limits**: Implement timeouts for long-running plugin operations
4. **Input Validation**: Validate all plugin inputs and configurations
5. **Secrets**: Never log sensitive information from context

## Common Use Cases

### 1. Notifications
- Slack/Teams notifications on job completion
- Email alerts for failures
- Webhook triggers

### 2. Resource Management
- CPU/Memory limits
- Disk quota enforcement
- Network restrictions

### 3. Artifact Management
- Collect build artifacts
- Upload to S3/GCS
- Archive logs

### 4. Metrics & Monitoring
- Job duration tracking
- Success/failure rates
- Resource usage metrics

### 5. Environment Setup
- Inject CI/CD variables
- Set up credentials
- Configure proxies

### 6. Compliance & Auditing
- Log job metadata
- Enforce security policies
- Generate audit trails

## Troubleshooting

### Plugin Not Loading

Check:
- File starts with `plugin_` prefix
- File has `.py` extension
- No syntax errors in plugin
- Plugin class inherits from `Plugin`
- Plugin directory is correctly specified

### Plugin Not Executing

Verify:
- Plugin is registered: `plugin_manager.list_plugins()`
- Plugin supports the phase: `plugin.supported_phases()`
- Plugin is enabled: `plugin.enabled == True`
- No exceptions in earlier plugins

### Performance Issues

- Use appropriate priority levels
- Avoid blocking operations in plugins
- Cache expensive computations in metadata
- Use async operations where possible

## API Reference

See the [Plugin API Documentation](plugin_api.md) for complete API reference.