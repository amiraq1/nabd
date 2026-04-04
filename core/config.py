import copy
import json
import os
from typing import Any

from core.exceptions import ConfigError

_BASE_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_CONFIG_DIR = os.path.join(_BASE_DIR, "config")
_CONFIG_CACHE: dict[str, dict[str, Any]] = {}


def _load_json(filename: str) -> dict[str, Any]:
    path = os.path.join(_CONFIG_DIR, filename)
    if not os.path.isfile(path):
        raise ConfigError(f"Config file not found: {path}")
    try:
        with open(path, "r", encoding="utf-8") as f:
            return json.load(f)
    except json.JSONDecodeError as e:
        raise ConfigError(f"Invalid JSON in {filename}: {e}") from e


def _get_cached_json(filename: str) -> dict[str, Any]:
    if filename not in _CONFIG_CACHE:
        _CONFIG_CACHE[filename] = _load_json(filename)
    return copy.deepcopy(_CONFIG_CACHE[filename])


def clear_config_cache() -> None:
    """Clear cached config values so tests or reload flows can start fresh."""
    _CONFIG_CACHE.clear()


def get_settings() -> dict[str, Any]:
    return _get_cached_json("settings.json")


def get_allowed_roots() -> list[str]:
    data = _get_cached_json("allowed_paths.json")
    roots = data.get("allowed_roots", [])
    if not isinstance(roots, list):
        raise ConfigError("allowed_roots must be a list in allowed_paths.json")
    return [str(r) for r in roots]
