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

# Intents that only read data — no path required to proceed
READ_ONLY_INTENTS = {
    "storage_report",
    "list_large_files",
    "find_duplicates",
    "doctor",
}

# Intents that read a path but need source_path to be specified
PATH_REQUIRED_INTENTS = {
    "show_files",
    "list_media",
    "organize_folder_by_type",
    "safe_rename_files",
    "compress_images",
}


def _check_traversal(path: str) -> None:
    for indicator in TRAVERSAL_INDICATORS:
        if indicator in path:
            raise PathTraversalError(
                f"Path contains disallowed sequence '{indicator}': {path}\n"
                "  Only absolute paths inside allowed directories are permitted."
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
            + "\n  Edit config/allowed_paths.json to add more directories."
        )
    return resolved


def validate_intent_safety(intent: ParsedIntent) -> None:
    # doctor needs no path at all
    if intent.intent == "doctor":
        return

    if intent.source_path:
        validate_path_safety(intent.source_path)
    elif intent.intent not in READ_ONLY_INTENTS:
        if intent.intent in PATH_REQUIRED_INTENTS:
            raise ValidationError(
                f"Please specify a directory path for '{intent.intent}'.\n"
                "  Example: organize /sdcard/Download"
            )

    if intent.target_path:
        validate_path_safety(intent.target_path)

    if intent.intent == "backup_folder":
        if not intent.target_path:
            raise ValidationError(
                "Please specify the destination for the backup.\n"
                "  Example: back up /sdcard/Documents to /sdcard/Backup"
            )

    if intent.intent == "safe_move_files":
        if not intent.target_path:
            raise ValidationError(
                "Please specify the destination directory.\n"
                "  Example: move /sdcard/Download/file.txt to /sdcard/Documents"
            )

    if intent.intent == "convert_video_to_mp3":
        if intent.source_path:
            ext = os.path.splitext(intent.source_path)[1].lower()
            valid_exts = {".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm", ".3gp", ".m4v", ""}
            if ext and ext not in valid_exts:
                raise SafetyError(
                    f"Unsupported video format '{ext}'.\n"
                    f"  Supported: {', '.join(sorted(valid_exts - {''}))}"
                )

    if intent.risk_level == RiskLevel.HIGH and not intent.requires_confirmation:
        raise SafetyError(
            f"High-risk operation '{intent.intent}' must require confirmation."
        )
