"""Configuration management for runnerlib with environment variable hierarchy."""

import os
from typing import Dict, Optional, List
from dataclasses import dataclass


@dataclass
class RunnerConfig:
    """Configuration for the job runner."""
    code_dir: str
    job_dir: str
    job_command: str
    runner_image: str
    job_env: Optional[str] = None
    secrets_list: Optional[str] = None  # Comma-separated list of secret values to mask
    secrets_file: Optional[str] = None  # Path to secrets file to mount into container

    # Source code configuration (optional - for untrusted code from PRs, etc.)
    source_type: Optional[str] = None  # git, copy, tarball, hg, svn, none
    source_url: Optional[str] = None  # URL or path to source code
    source_ref: Optional[str] = None  # Branch, tag, commit, or version ref

    # CI code configuration (optional - for trusted CI/CD scripts)
    ci_source_type: Optional[str] = None  # git, copy, tarball, hg, svn, none
    ci_source_url: Optional[str] = None  # URL or path to CI code
    ci_source_ref: Optional[str] = None  # Branch, tag, commit, or version ref


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
        'job_env': 'REACTORCIDE_JOB_ENV',
        'secrets_list': 'REACTORCIDE_SECRETS_LIST',
        'secrets_file': 'REACTORCIDE_SECRETS_FILE',
        'source_type': 'REACTORCIDE_SOURCE_TYPE',
        'source_url': 'REACTORCIDE_SOURCE_URL',
        'source_ref': 'REACTORCIDE_SOURCE_REF',
        'ci_source_type': 'REACTORCIDE_CI_SOURCE_TYPE',
        'ci_source_url': 'REACTORCIDE_CI_SOURCE_URL',
        'ci_source_ref': 'REACTORCIDE_CI_SOURCE_REF'
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
            job_env=config.get('job_env'),
            secrets_list=config.get('secrets_list'),
            secrets_file=config.get('secrets_file'),
            source_type=config.get('source_type'),
            source_url=config.get('source_url'),
            source_ref=config.get('source_ref'),
            ci_source_type=config.get('ci_source_type'),
            ci_source_url=config.get('ci_source_url'),
            ci_source_ref=config.get('ci_source_ref')
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
        # NOTE: Do NOT pass REACTORCIDE_JOB_ENV to the container
        # The individual parsed variables are passed instead (see below)

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


def get_secrets_to_mask(config: RunnerConfig, env_vars: Dict[str, str]) -> List[str]:
    """Get the list of secret values that should be masked in logs.

    Args:
        config: Runner configuration
        env_vars: All environment variables

    Returns:
        List of secret values to mask
    """
    secrets = []

    # If explicit secrets list is provided, use ONLY that list
    if config.secrets_list is not None:
        # Could be a file path or comma-separated values
        if config.secrets_list and config.secrets_list.startswith('./job/') and os.path.exists(config.secrets_list):
            # Read secrets from file
            try:
                with open(config.secrets_list, 'r') as f:
                    for line in f:
                        line = line.strip()
                        if line and not line.startswith('#'):
                            secrets.append(line)
            except Exception:
                pass  # Ignore file read errors
        else:
            # Treat as comma-separated values
            secrets.extend([s.strip() for s in config.secrets_list.split(',') if s.strip()])
    else:
        # Only when NO explicit list is provided, use default behavior:
        # Register all non-REACTORCIDE environment variable VALUES as potential secrets
        # (REACTORCIDE vars are system configuration like paths, not secrets)
        for key, value in env_vars.items():
            if not key.startswith('REACTORCIDE_') and value and value not in secrets:
                secrets.append(value)

    return secrets