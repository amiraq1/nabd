import json
import os
from typing import Any

from core.exceptions import ConfigError

_BASE_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_CONFIG_DIR = os.path.join(_BASE_DIR, "config")


def _load_json(filename: str) -> dict[str, Any]:
    path = os.path.join(_CONFIG_DIR, filename)
    if not os.path.isfile(path):
        raise ConfigError(f"Config file not found: {path}")
    try:
        with open(path, "r", encoding="utf-8") as f:
            return json.load(f)
    except json.JSONDecodeError as e:
        raise ConfigError(f"Invalid JSON in {filename}: {e}") from e


def get_settings() -> dict[str, Any]:
    return _load_json("settings.json")


def get_allowed_roots() -> list[str]:
    data = _load_json("allowed_paths.json")
    roots = data.get("allowed_roots", [])
    if not isinstance(roots, list):
        raise ConfigError("allowed_roots must be a list in allowed_paths.json")
    return [str(r) for r in roots]
