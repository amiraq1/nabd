"""Public core API for config, paths, history logging, and exceptions."""

from .config import clear_config_cache, get_allowed_roots, get_settings
from .exceptions import (
    ConfigError,
    ConfirmationRequiredError,
    ExecutionError,
    NabdError,
    PathNotAllowedError,
    PathTraversalError,
    SafetyError,
    ToolError,
    UnknownIntentError,
    ValidationError,
)
from .logging_db import get_history, get_history_entry, is_first_run, log_operation
from .paths import is_under_allowed_root, resolve_path, validate_path

__all__ = [
    "ConfigError",
    "ConfirmationRequiredError",
    "ExecutionError",
    "NabdError",
    "PathNotAllowedError",
    "PathTraversalError",
    "SafetyError",
    "ToolError",
    "UnknownIntentError",
    "ValidationError",
    "clear_config_cache",
    "get_allowed_roots",
    "get_history",
    "get_history_entry",
    "get_settings",
    "is_first_run",
    "is_under_allowed_root",
    "log_operation",
    "resolve_path",
    "validate_path",
]
