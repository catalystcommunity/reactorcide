"""Plugin system for runnerlib job lifecycle hooks."""

import os
import importlib
import importlib.util
from abc import ABC, abstractmethod
from dataclasses import dataclass
from pathlib import Path
from typing import Dict, List, Optional, Any
from enum import Enum

from src.config import RunnerConfig
from src.logging import logger


class PluginPhase(Enum):
    """Job lifecycle phases where plugins can hook."""
    PRE_VALIDATION = "pre_validation"      # Before configuration validation
    POST_VALIDATION = "post_validation"    # After configuration validation
    PRE_SOURCE_PREP = "pre_source_prep"   # Before git checkout/directory copy
    POST_SOURCE_PREP = "post_source_prep" # After source is prepared
    PRE_CONTAINER = "pre_container"       # Before container execution
    POST_CONTAINER = "post_container"     # After container execution (includes exit code)
    ON_ERROR = "on_error"                 # When an error occurs
    CLEANUP = "cleanup"                   # During cleanup phase


@dataclass
class PluginContext:
    """Context passed to plugin hooks."""
    config: RunnerConfig
    phase: PluginPhase
    job_path: Optional[Path] = None
    env_vars: Optional[Dict[str, str]] = None
    exit_code: Optional[int] = None
    error: Optional[Exception] = None
    metadata: Dict[str, Any] = None

    def __post_init__(self):
        if self.metadata is None:
            self.metadata = {}


class Plugin(ABC):
    """Base class for all runnerlib plugins."""

    def __init__(self, name: str, priority: int = 100):
        """Initialize plugin.

        Args:
            name: Plugin name
            priority: Execution priority (lower = earlier)
        """
        self.name = name
        self.priority = priority
        self.enabled = True

    @abstractmethod
    def supported_phases(self) -> List[PluginPhase]:
        """Return list of phases this plugin supports."""
        pass

    @abstractmethod
    def execute(self, context: PluginContext) -> None:
        """Execute plugin logic for the given context.

        Args:
            context: Plugin execution context

        Raises:
            Exception: Can raise exceptions to abort job execution
        """
        pass

    def pre_validation(self, context: PluginContext) -> None:
        """Hook called before configuration validation."""
        pass

    def post_validation(self, context: PluginContext) -> None:
        """Hook called after configuration validation."""
        pass

    def pre_source_prep(self, context: PluginContext) -> None:
        """Hook called before source preparation."""
        pass

    def post_source_prep(self, context: PluginContext) -> None:
        """Hook called after source preparation."""
        pass

    def pre_container(self, context: PluginContext) -> None:
        """Hook called before container execution."""
        pass

    def post_container(self, context: PluginContext) -> None:
        """Hook called after container execution."""
        pass

    def on_error(self, context: PluginContext) -> None:
        """Hook called when an error occurs."""
        pass

    def cleanup(self, context: PluginContext) -> None:
        """Hook called during cleanup."""
        pass


class PluginManager:
    """Manages plugin lifecycle and execution."""

    def __init__(self):
        """Initialize the plugin manager."""
        self.plugins: Dict[str, Plugin] = {}
        self.phase_plugins: Dict[PluginPhase, List[Plugin]] = {
            phase: [] for phase in PluginPhase
        }

    def register_plugin(self, plugin: Plugin) -> None:
        """Register a plugin with the manager.

        Args:
            plugin: Plugin instance to register
        """
        if plugin.name in self.plugins:
            logger.warning(f"Plugin {plugin.name} already registered, replacing")

        self.plugins[plugin.name] = plugin

        # Register plugin for each phase it supports
        for phase in plugin.supported_phases():
            self.phase_plugins[phase].append(plugin)

        # Sort plugins by priority for each phase
        for phase in PluginPhase:
            self.phase_plugins[phase].sort(key=lambda p: p.priority)

        logger.info(f"Registered plugin: {plugin.name}")

    def unregister_plugin(self, name: str) -> None:
        """Unregister a plugin.

        Args:
            name: Name of plugin to unregister
        """
        if name not in self.plugins:
            return

        del self.plugins[name]

        # Remove from phase mappings
        for phase in PluginPhase:
            self.phase_plugins[phase] = [
                p for p in self.phase_plugins[phase] if p.name != name
            ]

        logger.info(f"Unregistered plugin: {name}")

    def load_plugin_from_file(self, file_path: str) -> None:
        """Load a plugin from a Python file.

        Args:
            file_path: Path to the plugin file
        """
        path = Path(file_path)
        if not path.exists():
            raise FileNotFoundError(f"Plugin file not found: {file_path}")

        # Load the module
        spec = importlib.util.spec_from_file_location(path.stem, path)
        if spec is None or spec.loader is None:
            raise ImportError(f"Could not load plugin from {file_path}")

        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)

        # Find and register Plugin subclasses
        plugin_found = False
        for attr_name in dir(module):
            attr = getattr(module, attr_name)
            if (isinstance(attr, type) and
                issubclass(attr, Plugin) and
                attr != Plugin):
                # Instantiate and register the plugin
                plugin = attr()
                self.register_plugin(plugin)
                plugin_found = True

        if not plugin_found:
            raise ImportError(f"No Plugin subclass found in {file_path}")

    def load_plugins_from_directory(self, directory: str) -> None:
        """Load all plugins from a directory.

        Only loads .py files that start with 'plugin_' prefix.

        Args:
            directory: Directory containing plugin files
        """
        dir_path = Path(directory)
        if not dir_path.exists():
            logger.warning(f"Plugin directory not found: {directory}")
            return

        # Only load files that start with 'plugin_' and end with '.py'
        for file_path in sorted(dir_path.glob("plugin_*.py")):
            try:
                self.load_plugin_from_file(str(file_path))
                logger.info(f"Loaded plugin from {file_path.name}")
            except Exception as e:
                logger.error(f"Failed to load plugin from {file_path}: {e}")

    def load_builtin_plugins(self) -> None:
        """Load built-in plugins from the plugins directory."""
        builtin_dir = Path(__file__).parent / "builtin_plugins"
        if builtin_dir.exists():
            self.load_plugins_from_directory(str(builtin_dir))

    def execute_phase(self, phase: PluginPhase, context: PluginContext) -> None:
        """Execute all plugins for a specific phase.

        Args:
            phase: The lifecycle phase to execute
            context: Plugin execution context
        """
        plugins = self.phase_plugins.get(phase, [])

        for plugin in plugins:
            if not plugin.enabled:
                continue

            try:
                logger.debug(f"Executing plugin {plugin.name} for phase {phase.value}")

                # Call the generic execute method
                plugin.execute(context)

                # Also call the phase-specific method if it exists
                method_name = phase.value
                if hasattr(plugin, method_name):
                    method = getattr(plugin, method_name)
                    method(context)

            except Exception as e:
                logger.error(f"Plugin {plugin.name} failed in phase {phase.value}: {e}")
                if phase != PluginPhase.ON_ERROR:  # Avoid infinite loop
                    # Execute error hooks for other plugins
                    error_context = PluginContext(
                        config=context.config,
                        phase=PluginPhase.ON_ERROR,
                        job_path=context.job_path,
                        env_vars=context.env_vars,
                        error=e,
                        metadata=context.metadata
                    )
                    self.execute_phase(PluginPhase.ON_ERROR, error_context)
                raise

    def get_plugin(self, name: str) -> Optional[Plugin]:
        """Get a plugin by name.

        Args:
            name: Plugin name

        Returns:
            Plugin instance or None if not found
        """
        return self.plugins.get(name)

    def list_plugins(self) -> List[str]:
        """List all registered plugin names.

        Returns:
            List of plugin names
        """
        return list(self.plugins.keys())

    def enable_plugin(self, name: str) -> None:
        """Enable a plugin.

        Args:
            name: Plugin name
        """
        if name in self.plugins:
            self.plugins[name].enabled = True
            logger.info(f"Enabled plugin: {name}")

    def disable_plugin(self, name: str) -> None:
        """Disable a plugin.

        Args:
            name: Plugin name
        """
        if name in self.plugins:
            self.plugins[name].enabled = False
            logger.info(f"Disabled plugin: {name}")


# Global plugin manager instance
plugin_manager = PluginManager()


def initialize_plugins(plugin_dir: Optional[str] = None) -> None:
    """Initialize the plugin system.

    Args:
        plugin_dir: Optional directory to load custom plugins from
    """
    # Load built-in plugins
    plugin_manager.load_builtin_plugins()

    # Load custom plugins from environment variable
    env_plugin_dir = os.environ.get("REACTORCIDE_PLUGIN_DIR")
    if env_plugin_dir:
        plugin_manager.load_plugins_from_directory(env_plugin_dir)

    # Load custom plugins from provided directory
    if plugin_dir:
        plugin_manager.load_plugins_from_directory(plugin_dir)

    logger.info(f"Initialized {len(plugin_manager.plugins)} plugins")


def get_plugin_manager() -> PluginManager:
    """Get the global plugin manager instance.

    Returns:
        The plugin manager instance
    """
    return plugin_manager