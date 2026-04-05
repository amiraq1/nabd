"""Composable raw-detail reporters used by agent.reporter."""

from .registry import (
    RawDetailRenderer,
    _RAW_DETAIL_RENDERERS,
    get_raw_detail_renderer,
    register_raw_detail,
)
from .shared import append_ai_result, tls_fallback_lines

# Import registration modules for side effects so the registry is populated.
from . import ai_skills, device_web, filesystem, operations  # noqa: F401

__all__ = [
    "RawDetailRenderer",
    "_RAW_DETAIL_RENDERERS",
    "append_ai_result",
    "get_raw_detail_renderer",
    "register_raw_detail",
    "tls_fallback_lines",
]
