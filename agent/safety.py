import os
from typing import Optional

from agent.models import ParsedIntent, RiskLevel
from core.config import get_allowed_roots
from core.exceptions import (
    PathNotAllowedError,
    PathTraversalError,
    SafetyError,
    ValidationError,
)
from core.paths import is_under_allowed_root, resolve_path

TRAVERSAL_INDICATORS = ["..", "//", "\x00", "%2e", "%2f"]


def _check_traversal(path: str) -> None:
    for indicator in TRAVERSAL_INDICATORS:
        if indicator in path:
            raise PathTraversalError(
                f"Path contains disallowed sequence '{indicator}': {path}"
            )


def validate_path_safety(raw_path: str) -> str:
    if not raw_path or not raw_path.strip():
        raise ValidationError("Path must not be empty.")
    _check_traversal(raw_path)
    resolved = resolve_path(raw_path)
    allowed_roots = get_allowed_roots()
    if not is_under_allowed_root(resolved, allowed_roots):
        raise PathNotAllowedError(
            f"Path '{resolved}' is outside all allowed directories.\n"
            f"Allowed roots:\n"
            + "\n".join(f"  - {r}" for r in allowed_roots)
        )
    return resolved


def validate_intent_safety(intent: ParsedIntent) -> None:
    READ_ONLY_INTENTS = {"storage_report", "list_large_files", "find_duplicates"}

    if intent.source_path:
        validate_path_safety(intent.source_path)
    elif intent.intent not in READ_ONLY_INTENTS:
        if intent.intent in {"organize_folder_by_type", "safe_rename_files"}:
            raise ValidationError(
                f"Intent '{intent.intent}' requires a source path. "
                "Please specify the directory, e.g. /sdcard/Download"
            )

    if intent.target_path:
        validate_path_safety(intent.target_path)

    if intent.intent == "backup_folder":
        if not intent.target_path:
            raise ValidationError(
                "Backup requires a target (destination) directory. "
                "Please specify where to back up to."
            )

    if intent.intent == "safe_move_files":
        if not intent.target_path:
            raise ValidationError(
                "Move files requires a target directory. "
                "Please specify the destination."
            )

    if intent.intent == "convert_video_to_mp3":
        if intent.source_path:
            ext = os.path.splitext(intent.source_path)[1].lower()
            if ext not in {".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm", ".3gp", ".m4v", ""}:
                raise SafetyError(f"Unsupported video extension: {ext}")

    if intent.risk_level == RiskLevel.HIGH and not intent.requires_confirmation:
        raise SafetyError(
            f"High-risk intent '{intent.intent}' must require confirmation."
        )
