"""Tests for the plugin system."""

import os
import pytest
from pathlib import Path
from unittest.mock import Mock, patch, MagicMock
from typing import List

from src.plugins import (
    Plugin,
    PluginPhase,
    PluginContext,
    PluginManager,
    plugin_manager,
    initialize_plugins,
    get_plugin_manager
)
from src.config import RunnerConfig


class TestPlugin(Plugin):
    """Test plugin for unit tests."""

    def __init__(self, name: str = "test_plugin", priority: int = 100):
        super().__init__(name=name, priority=priority)
        self.executed_phases = []

    def supported_phases(self) -> List[PluginPhase]:
        return [
            PluginPhase.PRE_VALIDATION,
            PluginPhase.POST_VALIDATION,
            PluginPhase.PRE_CONTAINER,
            PluginPhase.POST_CONTAINER
        ]

    def execute(self, context: PluginContext) -> None:
        self.executed_phases.append(context.phase)
        context.metadata[f"plugin_{self.name}_ran"] = True


class ErrorPlugin(Plugin):
    """Plugin that raises errors for testing."""

    def __init__(self):
        super().__init__(name="error_plugin", priority=50)

    def supported_phases(self) -> List[PluginPhase]:
        return [PluginPhase.PRE_CONTAINER]

    def execute(self, context: PluginContext) -> None:
        raise ValueError("Test error from plugin")


class TestPluginCore:
    """Tests for core plugin functionality."""

    def test_plugin_initialization(self):
        """Test plugin initialization."""
        plugin = TestPlugin(name="my_test", priority=75)
        assert plugin.name == "my_test"
        assert plugin.priority == 75
        assert plugin.enabled is True

    def test_supported_phases(self):
        """Test plugin phase support."""
        plugin = TestPlugin()
        phases = plugin.supported_phases()
        assert PluginPhase.PRE_VALIDATION in phases
        assert PluginPhase.POST_CONTAINER in phases
        assert PluginPhase.ON_ERROR not in phases

    def test_plugin_context(self):
        """Test PluginContext initialization."""
        config = Mock(spec=RunnerConfig)
        context = PluginContext(
            config=config,
            phase=PluginPhase.PRE_CONTAINER,
            job_path=Path("/tmp/job"),
            env_vars={"KEY": "value"}
        )

        assert context.config == config
        assert context.phase == PluginPhase.PRE_CONTAINER
        assert context.job_path == Path("/tmp/job")
        assert context.env_vars == {"KEY": "value"}
        assert context.metadata == {}

    def test_plugin_execution(self):
        """Test plugin execution tracking."""
        plugin = TestPlugin()
        config = Mock(spec=RunnerConfig)

        # Execute in different phases
        for phase in [PluginPhase.PRE_VALIDATION, PluginPhase.POST_CONTAINER]:
            context = PluginContext(config=config, phase=phase)
            plugin.execute(context)

        assert PluginPhase.PRE_VALIDATION in plugin.executed_phases
        assert PluginPhase.POST_CONTAINER in plugin.executed_phases
        assert len(plugin.executed_phases) == 2


class TestPluginManager:
    """Tests for PluginManager functionality."""

    def setup_method(self):
        """Clear plugin manager before each test."""
        self.manager = PluginManager()

    def test_register_plugin(self):
        """Test plugin registration."""
        plugin = TestPlugin()
        self.manager.register_plugin(plugin)

        assert "test_plugin" in self.manager.plugins
        assert self.manager.get_plugin("test_plugin") == plugin

    def test_register_duplicate_plugin(self):
        """Test registering duplicate plugins."""
        plugin1 = TestPlugin(name="duplicate")
        plugin2 = TestPlugin(name="duplicate")

        self.manager.register_plugin(plugin1)
        self.manager.register_plugin(plugin2)

        # Should replace the first plugin
        assert self.manager.get_plugin("duplicate") == plugin2

    def test_unregister_plugin(self):
        """Test plugin unregistration."""
        plugin = TestPlugin()
        self.manager.register_plugin(plugin)

        self.manager.unregister_plugin("test_plugin")
        assert "test_plugin" not in self.manager.plugins
        assert self.manager.get_plugin("test_plugin") is None

    def test_plugin_priority_ordering(self):
        """Test plugins are executed in priority order."""
        plugin1 = TestPlugin(name="high_priority", priority=10)
        plugin2 = TestPlugin(name="low_priority", priority=100)
        plugin3 = TestPlugin(name="medium_priority", priority=50)

        self.manager.register_plugin(plugin2)
        self.manager.register_plugin(plugin1)
        self.manager.register_plugin(plugin3)

        # Check ordering for PRE_CONTAINER phase
        phase_plugins = self.manager.phase_plugins[PluginPhase.PRE_CONTAINER]
        assert phase_plugins[0].name == "high_priority"
        assert phase_plugins[1].name == "medium_priority"
        assert phase_plugins[2].name == "low_priority"

    def test_execute_phase(self):
        """Test executing plugins for a phase."""
        plugin1 = TestPlugin(name="plugin1")
        plugin2 = TestPlugin(name="plugin2")

        self.manager.register_plugin(plugin1)
        self.manager.register_plugin(plugin2)

        config = Mock(spec=RunnerConfig)
        context = PluginContext(
            config=config,
            phase=PluginPhase.PRE_CONTAINER,
            metadata={}
        )

        self.manager.execute_phase(PluginPhase.PRE_CONTAINER, context)

        assert context.metadata.get("plugin_plugin1_ran") is True
        assert context.metadata.get("plugin_plugin2_ran") is True

    def test_disabled_plugin_not_executed(self):
        """Test disabled plugins are not executed."""
        plugin = TestPlugin()
        self.manager.register_plugin(plugin)
        self.manager.disable_plugin("test_plugin")

        config = Mock(spec=RunnerConfig)
        context = PluginContext(
            config=config,
            phase=PluginPhase.PRE_CONTAINER,
            metadata={}
        )

        self.manager.execute_phase(PluginPhase.PRE_CONTAINER, context)

        # Plugin should not have run
        assert "plugin_test_plugin_ran" not in context.metadata

    def test_enable_disable_plugin(self):
        """Test enabling and disabling plugins."""
        plugin = TestPlugin()
        self.manager.register_plugin(plugin)

        assert plugin.enabled is True

        self.manager.disable_plugin("test_plugin")
        assert plugin.enabled is False

        self.manager.enable_plugin("test_plugin")
        assert plugin.enabled is True

    def test_list_plugins(self):
        """Test listing registered plugins."""
        plugin1 = TestPlugin(name="plugin1")
        plugin2 = TestPlugin(name="plugin2")

        self.manager.register_plugin(plugin1)
        self.manager.register_plugin(plugin2)

        plugins = self.manager.list_plugins()
        assert "plugin1" in plugins
        assert "plugin2" in plugins
        assert len(plugins) == 2

    def test_error_handling_in_plugin(self):
        """Test error handling when plugin fails."""
        error_plugin = ErrorPlugin()
        self.manager.register_plugin(error_plugin)

        config = Mock(spec=RunnerConfig)
        context = PluginContext(
            config=config,
            phase=PluginPhase.PRE_CONTAINER,
            metadata={}
        )

        with pytest.raises(ValueError, match="Test error from plugin"):
            self.manager.execute_phase(PluginPhase.PRE_CONTAINER, context)

    def test_on_error_phase_execution(self):
        """Test ON_ERROR phase is triggered on plugin failure."""
        error_plugin = ErrorPlugin()
        error_handler = TestPlugin(name="error_handler")

        # Modify error_handler to support ON_ERROR phase
        error_handler.supported_phases = lambda: [PluginPhase.ON_ERROR]

        self.manager.register_plugin(error_plugin)
        self.manager.register_plugin(error_handler)

        config = Mock(spec=RunnerConfig)
        context = PluginContext(
            config=config,
            phase=PluginPhase.PRE_CONTAINER,
            metadata={}
        )

        with pytest.raises(ValueError):
            self.manager.execute_phase(PluginPhase.PRE_CONTAINER, context)

        # Error handler should have been called
        assert PluginPhase.ON_ERROR in error_handler.executed_phases


class TestPluginLoading:
    """Tests for plugin loading functionality."""

    def setup_method(self):
        """Create a fresh plugin manager for each test."""
        self.manager = PluginManager()

    def test_load_plugin_from_file(self, tmp_path):
        """Test loading a plugin from a file."""
        # Create a test plugin file
        plugin_file = tmp_path / "plugin_test.py"
        plugin_code = '''
from src.plugins import Plugin, PluginPhase, PluginContext
from typing import List

class CustomTestPlugin(Plugin):
    def __init__(self):
        super().__init__(name="custom_test", priority=50)

    def supported_phases(self) -> List[PluginPhase]:
        return [PluginPhase.PRE_CONTAINER]

    def execute(self, context: PluginContext) -> None:
        context.metadata["custom_plugin_loaded"] = True
'''
        plugin_file.write_text(plugin_code)

        # Load the plugin
        self.manager.load_plugin_from_file(str(plugin_file))

        # Verify it was loaded
        assert "custom_test" in self.manager.plugins
        plugin = self.manager.get_plugin("custom_test")
        assert plugin is not None
        assert plugin.priority == 50

    def test_load_plugin_file_not_found(self):
        """Test loading non-existent plugin file."""
        with pytest.raises(FileNotFoundError):
            self.manager.load_plugin_from_file("/nonexistent/plugin.py")

    def test_load_plugin_no_plugin_class(self, tmp_path):
        """Test loading file without Plugin subclass."""
        plugin_file = tmp_path / "plugin_invalid.py"
        plugin_file.write_text("# No plugin class here\nprint('hello')")

        with pytest.raises(ImportError, match="No Plugin subclass found"):
            self.manager.load_plugin_from_file(str(plugin_file))

    def test_load_plugins_from_directory(self, tmp_path):
        """Test loading all plugins from a directory."""
        # Create multiple plugin files
        for i in range(3):
            plugin_file = tmp_path / f"plugin_test{i}.py"
            plugin_code = f'''
from src.plugins import Plugin, PluginPhase, PluginContext
from typing import List

class TestPlugin{i}(Plugin):
    def __init__(self):
        super().__init__(name="test{i}", priority={i * 10})

    def supported_phases(self) -> List[PluginPhase]:
        return [PluginPhase.PRE_CONTAINER]

    def execute(self, context: PluginContext) -> None:
        pass
'''
            plugin_file.write_text(plugin_code)

        # Also create a non-plugin file that should be ignored
        non_plugin = tmp_path / "helper.py"
        non_plugin.write_text("# Helper file, not a plugin")

        # Load plugins from directory
        self.manager.load_plugins_from_directory(str(tmp_path))

        # Verify correct plugins were loaded
        assert "test0" in self.manager.plugins
        assert "test1" in self.manager.plugins
        assert "test2" in self.manager.plugins
        assert len(self.manager.plugins) == 3

    def test_load_plugins_only_plugin_prefix(self, tmp_path):
        """Test that only files with plugin_ prefix are loaded."""
        # Create a plugin file with correct prefix
        correct_plugin = tmp_path / "plugin_correct.py"
        correct_plugin.write_text('''
from src.plugins import Plugin, PluginPhase, PluginContext
from typing import List

class CorrectPlugin(Plugin):
    def __init__(self):
        super().__init__(name="correct", priority=1)

    def supported_phases(self) -> List[PluginPhase]:
        return [PluginPhase.PRE_CONTAINER]

    def execute(self, context: PluginContext) -> None:
        pass
''')

        # Create a plugin file without correct prefix
        wrong_plugin = tmp_path / "wrong_plugin.py"
        wrong_plugin.write_text('''
from src.plugins import Plugin, PluginPhase, PluginContext
from typing import List

class WrongPlugin(Plugin):
    def __init__(self):
        super().__init__(name="wrong", priority=1)

    def supported_phases(self) -> List[PluginPhase]:
        return [PluginPhase.PRE_CONTAINER]

    def execute(self, context: PluginContext) -> None:
        pass
''')

        # Load plugins from directory
        self.manager.load_plugins_from_directory(str(tmp_path))

        # Only correct plugin should be loaded
        assert "correct" in self.manager.plugins
        assert "wrong" not in self.manager.plugins

    def test_load_plugins_nonexistent_directory(self):
        """Test loading from non-existent directory."""
        # Should not raise, just log warning
        self.manager.load_plugins_from_directory("/nonexistent/directory")
        assert len(self.manager.plugins) == 0


class TestPluginInitialization:
    """Tests for plugin initialization functions."""

    @patch('src.plugins.plugin_manager')
    def test_initialize_plugins(self, mock_manager, tmp_path):
        """Test initialize_plugins function."""
        # Create test plugin directory
        plugin_dir = tmp_path / "plugins"
        plugin_dir.mkdir()

        # Mock the manager methods
        mock_manager.load_builtin_plugins = MagicMock()
        mock_manager.load_plugins_from_directory = MagicMock()
        mock_manager.plugins = {"test": Mock()}

        with patch.dict(os.environ, {"REACTORCIDE_PLUGIN_DIR": "/env/plugins"}):
            initialize_plugins(str(plugin_dir))

        # Verify calls were made
        mock_manager.load_builtin_plugins.assert_called_once()
        mock_manager.load_plugins_from_directory.assert_any_call("/env/plugins")
        mock_manager.load_plugins_from_directory.assert_any_call(str(plugin_dir))

    def test_get_plugin_manager(self):
        """Test getting global plugin manager."""
        manager = get_plugin_manager()
        assert isinstance(manager, PluginManager)
        # Should return the same instance
        assert manager is get_plugin_manager()


class TestPluginIntegration:
    """Integration tests for plugins with container execution."""

    @patch('src.container.subprocess.Popen')
    @patch('src.container.shutil.which')
    @patch('src.container.prepare_job_directory')
    def test_plugin_execution_in_container_run(self, mock_prep, mock_which, mock_popen):
        """Test plugins are executed during container run."""
        from src.container import run_container

        # Setup mocks
        mock_which.return_value = "/usr/bin/docker"
        mock_prep.return_value = Path("/tmp/job")

        process_mock = Mock()
        process_mock.stdout.readline.side_effect = ["output\n", None]
        process_mock.stderr.readline.side_effect = [None, None]  # Need two None values
        process_mock.poll.side_effect = [None, 0]
        process_mock.communicate.return_value = ("", "")
        process_mock.returncode = 0
        mock_popen.return_value = process_mock

        # Register a test plugin
        test_plugin = TestPlugin()
        plugin_manager.register_plugin(test_plugin)

        # Create config
        config = Mock(spec=RunnerConfig)
        config.job_command = "echo test"
        config.runner_image = "test:latest"
        config.code_dir = "/job/src"
        config.job_dir = "/job"
        config.job_env = None
        config.secrets_list = None
        config.secrets_file = None

        # Run container
        with patch('src.container.SecretRegistrationServer'):
            run_container(config)

        # Verify plugin was executed for supported phases
        # Note: TestPlugin only supports PRE_VALIDATION, POST_VALIDATION, PRE_CONTAINER, POST_CONTAINER
        assert PluginPhase.PRE_CONTAINER in test_plugin.executed_phases
        assert PluginPhase.POST_CONTAINER in test_plugin.executed_phases
        # These phases were executed but our plugin doesn't support them:
        assert PluginPhase.PRE_SOURCE_PREP not in test_plugin.executed_phases
        assert PluginPhase.POST_SOURCE_PREP not in test_plugin.executed_phases

        # Cleanup
        plugin_manager.unregister_plugin("test_plugin")


if __name__ == "__main__":
    pytest.main([__file__, "-v"])