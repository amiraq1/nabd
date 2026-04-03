import os
import urllib.parse
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

# Intents that only read data and require no path, URL, or confirmation
READ_ONLY_INTENTS = {
    "doctor",
    "phone_status_battery",
    "phone_status_network",
    # Skills system — read-only registry queries
    "show_skills",
    "skill_info",
    # AI Assist — advisory only, never executes tool actions
    "ai_suggest_command",
    "ai_explain_last_result",
    "ai_clarify_request",
    "ai_backend_status",
    "history_search",
    "history_intent",
    "history_show",
}

# Read-only intents that may accept a user-supplied path. If a path is present,
# it must still pass the allowlist.
READ_ONLY_PATH_INTENTS = {
    "storage_report",
    "list_large_files",
    "find_duplicates",
}

# Browser intents that use a URL (not a local path)
URL_REQUIRED_INTENTS = {
    "open_url",
    "browser_extract_text",
    "browser_list_links",
    "browser_page_title",
}

# Intents that require a local path
PATH_REQUIRED_INTENTS = {
    "show_files",
    "show_folders",
    "list_media",
    "organize_folder_by_type",
    "safe_rename_files",
    "compress_images",
}

# Allowed URL schemes (lower-case)
ALLOWED_URL_SCHEMES = {"https", "http"}

# Explicitly banned URL schemes (checked before urllib.parse)
BANNED_URL_PREFIXES = (
    "javascript:",
    "file:",
    "intent:",
    "data:",
    "vbscript:",
    "jar:",
    "blob:",
)


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


def validate_url_safety(url: str) -> str:
    """
    Validate that a URL is safe to open/fetch.
    - Scheme must be https or http.
    - Banned schemes (javascript:, file:, intent:, data:, …) are rejected.
    - URL must have a non-empty hostname.
    """
    if not url or not url.strip():
        raise ValidationError("URL must not be empty.")

    stripped = url.strip()
    lower = stripped.lower()

    for prefix in BANNED_URL_PREFIXES:
        if lower.startswith(prefix):
            raise SafetyError(
                f"URL scheme is not allowed: '{prefix.rstrip(':')}'\n"
                "  Only https:// and http:// URLs are supported."
            )

    try:
        parsed = urllib.parse.urlparse(stripped)
    except Exception:
        raise ValidationError(f"Could not parse URL: '{url}'")

    if parsed.scheme.lower() not in ALLOWED_URL_SCHEMES:
        raise SafetyError(
            f"URL scheme '{parsed.scheme}' is not allowed.\n"
            "  Only https:// and http:// URLs are supported."
        )

    if not parsed.netloc:
        raise ValidationError(
            f"URL has no hostname: '{url}'\n"
            "  Example: https://example.com"
        )

    return stripped


def validate_app_safety(app_name: str) -> str:
    """
    Validate that an app name is in the supported allowlist.
    Import done inside function to avoid circular imports.
    """
    from tools.phone import SUPPORTED_APPS

    key = app_name.lower().strip() if app_name else ""
    if not key:
        raise ValidationError(
            "Please specify an app name.\n"
            f"  Supported apps: {', '.join(sorted(SUPPORTED_APPS))}"
        )
    if key not in SUPPORTED_APPS:
        raise ValidationError(
            f"App '{app_name}' is not supported.\n"
            f"  Supported apps: {', '.join(sorted(SUPPORTED_APPS))}\n"
            "  Type 'help' to see all supported commands."
        )
    return key


def validate_query_safety(query: str) -> str:
    """Validate a search query: must be non-empty, no obvious injection."""
    if not query or not query.strip():
        raise ValidationError(
            "Please specify a search query.\n"
            "  Example: search for local llm tools"
        )
    cleaned = query.strip()
    if len(cleaned) > 500:
        raise ValidationError("Search query is too long (max 500 characters).")
    return cleaned


def _is_same_or_descendant(path: str, base: str) -> bool:
    return path == base or path.startswith(base + os.sep)


def validate_intent_safety(intent: ParsedIntent) -> None:
    """
    Run all safety checks for a parsed intent.
    Raises SafetyError, ValidationError, PathNotAllowedError, or PathTraversalError
    on any violation.
    """
    name = intent.intent

    # ── Phone status / doctor: no paths or URLs needed ────────────────────────
    if name in READ_ONLY_INTENTS:
        return

    # ── Read-only intents with optional paths ─────────────────────────────────
    if name in READ_ONLY_PATH_INTENTS:
        if intent.source_path:
            validate_path_safety(intent.source_path)
        return

    # ── Browser search ────────────────────────────────────────────────────────
    if name == "browser_search":
        validate_query_safety(intent.query or "")
        return

    # ── URL-based intents ─────────────────────────────────────────────────────
    if name in URL_REQUIRED_INTENTS:
        if not intent.url:
            raise ValidationError(
                f"Please specify a URL for '{name}'.\n"
                "  Example: open https://example.com"
            )
        validate_url_safety(intent.url)
        return

    # ── App launch ────────────────────────────────────────────────────────────
    if name == "open_app":
        validate_app_safety(intent.app_name or "")
        return

    # ── File open ─────────────────────────────────────────────────────────────
    if name == "open_file":
        if not intent.source_path:
            raise ValidationError(
                "Please specify the file path to open.\n"
                "  Example: open file /sdcard/Download/report.pdf"
            )
        validate_path_safety(intent.source_path)
        return

    # ── Path-required file intents ────────────────────────────────────────────
    resolved_source: str | None = None
    resolved_target: str | None = None

    if intent.source_path:
        resolved_source = validate_path_safety(intent.source_path)
    elif name in PATH_REQUIRED_INTENTS:
        raise ValidationError(
            f"Please specify a directory path for '{name}'.\n"
            "  Example: organize /sdcard/Download"
        )

    if intent.target_path:
        resolved_target = validate_path_safety(intent.target_path)

    if name == "backup_folder":
        if not intent.target_path:
            raise ValidationError(
                "Please specify the destination for the backup.\n"
                "  Example: back up /sdcard/Documents to /sdcard/Backup"
            )
        if resolved_source and resolved_target and _is_same_or_descendant(resolved_target, resolved_source):
            raise SafetyError(
                "Backup destination must be outside the source folder.\n"
                "  Choose a separate destination root, not the same folder or one of its subfolders."
            )

    if name == "safe_move_files":
        if not intent.target_path:
            raise ValidationError(
                "Please specify the destination directory.\n"
                "  Example: move /sdcard/Download/file.txt to /sdcard/Documents"
            )
        if resolved_source and resolved_target:
            source_parent = os.path.dirname(resolved_source.rstrip(os.sep)) or os.sep
            if resolved_target == resolved_source:
                raise SafetyError(
                    "Move destination cannot be the same as the source path.\n"
                    "  Choose a different destination directory."
                )
            if resolved_target == source_parent:
                raise SafetyError(
                    "Move destination cannot be the source's current parent directory.\n"
                    "  That would be a no-op or an unintended rename."
                )
            if _is_same_or_descendant(resolved_target, resolved_source):
                raise SafetyError(
                    "Cannot move a folder into itself or one of its subfolders.\n"
                    "  Choose a destination outside the source folder."
                )

    if name == "convert_video_to_mp3":
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
            f"High-risk operation '{name}' must require confirmation."
        )
