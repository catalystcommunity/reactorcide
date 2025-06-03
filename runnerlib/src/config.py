"""Configuration management for runnerlib with environment variable hierarchy."""

import os
from pathlib import Path
from typing import Dict, Optional, Any
from dataclasses import dataclass


@dataclass
class RunnerConfig:
    """Configuration for the job runner."""
    code_dir: str
    job_dir: str
    job_command: str
    runner_image: str
    job_env: Optional[str] = None


class ConfigManager:
    """Manages configuration with hierarchy: defaults < env vars < CLI args."""
    
    # Default values
    DEFAULTS = {
        'code_dir': '/job/src',
        'runner_image': 'quay.io/catalystcommunity/reactorcide_runner'
    }
    
    # Environment variable mappings
    ENV_VARS = {
        'code_dir': 'REACTORCIDE_CODE_DIR',
        'job_dir': 'REACTORCIDE_JOB_DIR', 
        'job_command': 'REACTORCIDE_JOB_COMMAND',
        'runner_image': 'REACTORCIDE_RUNNER_IMAGE',
        'job_env': 'REACTORCIDE_JOB_ENV'
    }
    
    def __init__(self):
        self._config_cache = {}
    
    def get_config(self, **cli_overrides) -> RunnerConfig:
        """Get the resolved configuration using hierarchy: defaults < env vars < CLI args.
        
        Args:
            **cli_overrides: CLI argument overrides
            
        Returns:
            RunnerConfig with resolved values
            
        Raises:
            ValueError: If required configuration is missing
        """
        config = {}
        
        # Start with defaults
        for key, default_value in self.DEFAULTS.items():
            config[key] = default_value
        
        # Override with environment variables
        for key, env_var in self.ENV_VARS.items():
            env_value = os.getenv(env_var)
            if env_value is not None:
                config[key] = env_value
        
        # Override with CLI arguments
        for key, value in cli_overrides.items():
            if value is not None:
                config[key] = value
        
        # Handle job_dir default derivation from code_dir if not set
        if 'job_dir' not in config or config['job_dir'] is None:
            config['job_dir'] = config['code_dir']
        
        # Validate required fields
        required_fields = ['job_command']
        missing_fields = [field for field in required_fields if field not in config or config[field] is None]
        if missing_fields:
            raise ValueError(f"Missing required configuration: {', '.join(missing_fields)}")
        
        return RunnerConfig(
            code_dir=config['code_dir'],
            job_dir=config['job_dir'],
            job_command=config['job_command'],
            runner_image=config['runner_image'],
            job_env=config.get('job_env')
        )
    
    def _validate_job_env_path(self, path: str) -> None:
        """Validate that job_env file path is secure and within job directory.
        
        Args:
            path: File path to validate
            
        Raises:
            ValueError: If path is invalid or insecure
        """
        # Check for path traversal attempts
        if '..' in path:
            raise ValueError(f"Path traversal not allowed in job_env path: {path}")
        
        # Must start with ./job/
        if not path.startswith('./job/'):
            raise ValueError(f"job_env file path must start with './job/': {path}")
    
    def parse_job_environment(self, job_env: Optional[str]) -> Dict[str, str]:
        """Parse job environment variables from string or file.
        
        Args:
            job_env: Environment variables as key=value pairs or file path
            
        Returns:
            Dictionary of environment variables
            
        Raises:
            FileNotFoundError: If file path doesn't exist
            ValueError: If environment variable format is invalid or path is insecure
        """
        if not job_env:
            return {}
        
        env_vars = {}
        
        # Check if it's a file path (starts with ./job/)
        if job_env.startswith('./job/'):
            self._validate_job_env_path(job_env)
            try:
                with open(job_env, 'r') as f:
                    content = f.read()
            except FileNotFoundError:
                raise FileNotFoundError(f"Environment file not found: {job_env}")
        else:
            # Treat as inline key=value pairs
            content = job_env
        
        # Parse key=value pairs
        for line in content.strip().split('\n'):
            line = line.strip()
            if not line or line.startswith('#'):
                continue
            
            if '=' not in line:
                raise ValueError(f"Invalid environment variable format: {line}")
            
            key, value = line.split('=', 1)
            key = key.strip()
            value = value.strip()
            
            if not key:
                raise ValueError(f"Empty environment variable key in: {line}")
            
            env_vars[key] = value
        
        return env_vars
    
    def get_all_environment_vars(self, config: RunnerConfig) -> Dict[str, str]:
        """Get all environment variables to pass to container.
        
        Args:
            config: Runner configuration
            
        Returns:
            Dictionary of all environment variables including REACTORCIDE_* vars
        """
        env_vars = {}
        
        # Add REACTORCIDE_* configuration variables
        env_vars['REACTORCIDE_CODE_DIR'] = config.code_dir
        env_vars['REACTORCIDE_JOB_DIR'] = config.job_dir
        env_vars['REACTORCIDE_JOB_COMMAND'] = config.job_command
        env_vars['REACTORCIDE_RUNNER_IMAGE'] = config.runner_image
        if config.job_env:
            env_vars['REACTORCIDE_JOB_ENV'] = config.job_env
        
        # Add job-specific environment variables
        if config.job_env:
            job_env_vars = self.parse_job_environment(config.job_env)
            env_vars.update(job_env_vars)
        
        return env_vars


# Global config manager instance
config_manager = ConfigManager()


def get_config(**cli_overrides) -> RunnerConfig:
    """Convenience function to get configuration."""
    return config_manager.get_config(**cli_overrides)


def get_environment_vars(config: RunnerConfig) -> Dict[str, str]:
    """Convenience function to get all environment variables."""
    return config_manager.get_all_environment_vars(config)