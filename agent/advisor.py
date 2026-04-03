import os
import re
from typing import Any
from urllib.parse import urlparse

from agent.models import ExecutionResult, ParsedIntent
from core.config import get_allowed_roots
from core.paths import is_under_allowed_root, resolve_path
from agent.safety import validate_url_safety


SEPARATOR = "─" * 55
MAX_SUGGESTIONS = 3
TRAVERSAL_INDICATORS = ("..", "//", "\x00", "%2e", "%2f")
PATH_RE = re.compile(r'(?:^|[\s"\'])(/[^\s"\',:;]+)')
QUOTED_RE = re.compile(r'["\']([^"\']+)["\']')
SAFE_HISTORY_RETRY_INTENTS = {
    "doctor",
    "phone_status_battery",
    "phone_status_network",
    "browser_search",
    "browser_extract_text",
    "browser_list_links",
    "storage_report",
    "list_large_files",
    "find_duplicates",
    "show_files",
    "list_media",
}


def generate_advisory_suggestions(
    intent: ParsedIntent,
    result: ExecutionResult | None = None,
    recent_history: list[dict[str, Any]] | None = None,
) -> list[str]:
    suggestions: list[str] = []
    history = recent_history or []

    if result and result.raw_results:
        raw = result.raw_results[0]
        suggestions.extend(_suggest_from_result(intent, raw, result, history))

    suggestions.extend(_suggest_from_history(intent, result, history))
    suggestions = _dedupe_suggestions(suggestions)
    suggestions = _filter_recent_command_repeats(suggestions, history)
    return suggestions[:MAX_SUGGESTIONS]


def format_advisory_suggestions(suggestions: list[str]) -> str:
    if not suggestions:
        return ""

    lines = [
        f"\n{SEPARATOR}",
        "  ADVISORY SUGGESTIONS",
        SEPARATOR,
    ]
    for suggestion in suggestions:
        lines.append(f"  - {suggestion}")
    return "\n".join(lines)


def _suggest_from_result(
    intent: ParsedIntent,
    raw: Any,
    result: ExecutionResult,
    recent_history: list[dict[str, Any]],
) -> list[str]:
    name = intent.intent
    suggestions: list[str] = []

    if name == "doctor" and isinstance(raw, dict):
        checks = {c.get("name"): c for c in raw.get("checks", []) if isinstance(c, dict)}

        ffmpeg = checks.get("ffmpeg", {})
        if ffmpeg.get("status") == "missing":
            retry = _recent_command_for_intent(recent_history, {"convert_video_to_mp3"})
            if retry:
                suggestions.append(f"After installing ffmpeg, retry: {retry}")
            else:
                suggestions.append("If you need audio conversion, install ffmpeg in Termux: pkg install ffmpeg")

        pillow = checks.get("Pillow (image compression)", {})
        if pillow.get("status") == "missing":
            retry = _recent_command_for_intent(recent_history, {"compress_images"})
            if retry:
                suggestions.append(f"After installing Pillow, retry: {retry}")
            else:
                suggestions.append("If you need image compression, install Pillow: pip install Pillow")

        allowed = checks.get("Allowed paths", {})
        if allowed.get("status") in {"warn", "error"}:
            suggestions.append("Grant storage access if needed, then rerun: doctor")

        tls = checks.get("HTTPS / CA certificates", {})
        if tls.get("status") == "error":
            retry = _recent_command_for_intent(recent_history, {"browser_extract_text", "browser_list_links"})
            if retry:
                suggestions.append(f"After fixing CA certificates, retry: {retry}")
            else:
                suggestions.append("If page fetches fail, install CA certs in Termux and rerun: doctor")

    elif name == "storage_report" and isinstance(raw, dict):
        directory = raw.get("directory") or intent.source_path
        if directory:
            suggestions.append(f"Review the biggest files next: list large files {directory}")
            if raw.get("file_count", 0):
                suggestions.append(f"Check duplicates in the same folder: find duplicates {directory}")
                if any(cat in raw.get("category_breakdown", {}) for cat in ("images", "videos", "audio")):
                    suggestions.append(f"See media files by type: list media in {directory}")
            else:
                suggestions.append(f"Inspect the folder contents directly: show files in {directory}")

    elif name == "show_files" and isinstance(raw, dict):
        directory = raw.get("directory") or intent.source_path
        if directory:
            if raw.get("sort_by") != "size" and raw.get("file_count", 0):
                suggestions.append(f"Sort the same folder by size next: show files in {directory} sorted by size")
            suggestions.append(f"See the heaviest files there: list large files {directory}")
            if raw.get("file_count", 0):
                suggestions.append(f"Filter that folder to media only: list media in {directory}")

    elif name == "list_large_files" and isinstance(raw, list):
        directory = intent.source_path or "/sdcard/Download"
        if raw:
            suggestions.append(f"Get a category breakdown for the same folder: storage report {directory}")
            suggestions.append(f"Check whether those files have duplicates: find duplicates {directory}")
        else:
            suggestions.append(f"Review the full folder summary instead: storage report {directory}")
            suggestions.append(f"Inspect the folder contents directly: show files in {directory}")

    elif name == "list_media" and isinstance(raw, dict):
        directory = raw.get("directory") or intent.source_path
        if raw.get("total_media_count", 0) == 0 and raw.get("has_subdirs") and not raw.get("recursive"):
            suggestions.append(f"Scan subfolders too: list media in {directory} recursively")
        elif directory:
            suggestions.append(f"See the largest files in that folder: list large files {directory}")
            image_count = raw.get("summary", {}).get("images", {}).get("count", 0)
            if image_count:
                suggestions.append(f"If you need space, preview image compression: compress images {directory}")
            elif raw.get("total_media_count", 0):
                suggestions.append(f"Review the folder summary too: storage report {directory}")

    elif name == "find_duplicates" and isinstance(raw, dict):
        directory = raw.get("directory") or intent.source_path or "/sdcard/Download"
        if raw.get("total_groups", 0) > 0:
            suggestions.append(f"Inspect the folder before cleaning manually: show files in {directory} sorted by size")
            suggestions.append(f"Review total usage for that folder: storage report {directory}")
        else:
            suggestions.append(f"If space is still tight, inspect the largest files: list large files {directory}")

    elif name in {"browser_extract_text", "browser_list_links"} and isinstance(raw, dict):
        if raw.get("error_type") == "tls":
            url = raw.get("url") or intent.url
            if url:
                suggestions.append(f"Open the page in Android instead: open {url}")
                domain = _extract_domain(url)
                if domain:
                    suggestions.append(f"Use browser search as a fallback: search for {domain}")
            suggestions.append("Re-check Nabd environment after fixing certs: doctor")
        elif raw.get("success") and name == "browser_extract_text":
            url = raw.get("url") or intent.url
            if url:
                suggestions.append(f"Inspect the page links too: list links from {url}")
        elif raw.get("success") and name == "browser_list_links":
            url = raw.get("url") or intent.url
            if url:
                suggestions.append(f"Extract readable text from the same page: extract text from {url}")

    elif name in {"phone_status_battery", "phone_status_network"} and isinstance(raw, dict):
        if not raw.get("success", False):
            suggestions.append("Check the environment and Termux integration: doctor")

    elif name == "organize_folder_by_type" and isinstance(raw, dict) and result.status.value in {"success", "partial"}:
        directory = raw.get("directory") or intent.source_path
        if directory:
            suggestions.append(f"Review the folder after organizing: show files in {directory} sorted by size")
            suggestions.append(f"Check the updated storage summary: storage report {directory}")

    elif name == "backup_folder" and isinstance(raw, dict) and result.status.value in {"success", "partial"}:
        destination = raw.get("destination")
        if destination:
            suggestions.append(f"Inspect the backup folder: show files in {destination}")

    elif name == "safe_move_files" and isinstance(raw, dict) and result.status.value in {"success", "partial"}:
        target_directory = raw.get("target_directory") or intent.target_path
        if target_directory:
            suggestions.append(f"Review the destination folder: show files in {target_directory} sorted by modified")

    elif name == "safe_rename_files" and isinstance(raw, dict) and result.status.value in {"success", "partial"}:
        directory = raw.get("directory") or intent.source_path
        if directory:
            suggestions.append(f"Review the renamed files: show files in {directory} sorted by modified")

    elif name == "compress_images" and isinstance(raw, dict) and result.status.value in {"success", "partial"}:
        directory = raw.get("directory") or intent.source_path
        if directory:
            suggestions.append(f"Review the updated images: show files in {directory} sorted by modified")
            suggestions.append(f"Check the folder summary after compression: storage report {directory}")

    elif name == "convert_video_to_mp3" and isinstance(raw, dict) and result.status.value in {"success", "partial"}:
        output_path = raw.get("output_path")
        if output_path:
            out_dir = os.path.dirname(output_path) or "."
            suggestions.append(f"Review the output folder: show files in {out_dir} sorted by modified")

    return suggestions


def _suggest_from_history(
    intent: ParsedIntent,
    result: ExecutionResult | None,
    recent_history: list[dict[str, Any]],
) -> list[str]:
    suggestions: list[str] = []
    if not recent_history:
        return suggestions

    if intent.intent == "doctor":
        retryable = _recent_retryable_command(recent_history)
        if retryable:
            suggestions.append(f"After fixing the environment, retry your recent command: {retryable}")

    if intent.intent in {"storage_report", "show_files", "list_large_files", "list_media", "find_duplicates"}:
        previous = _recent_command_for_intent(recent_history, {"organize_folder_by_type", "backup_folder"})
        if previous:
            suggestions.append(f"If you were preparing a change, review the earlier command carefully before retrying: {previous}")

    return suggestions


def _recent_retryable_command(recent_history: list[dict[str, Any]]) -> str | None:
    retry_statuses = {
        "failure",
        "partial",
        "error",
        "unexpected_error",
        "validation_error",
        "safety_error",
        "config_error",
        "safety_blocked",
    }
    for entry in recent_history:
        command = (entry.get("command") or "").strip()
        if not command:
            continue
        if (entry.get("status") or "") in retry_statuses and _history_entry_is_safe_to_repeat(entry):
            return command
    return None


def _recent_command_for_intent(
    recent_history: list[dict[str, Any]],
    intents: set[str],
) -> str | None:
    for entry in recent_history:
        if entry.get("intent") in intents:
            command = (entry.get("command") or "").strip()
            if command and _history_entry_is_safe_to_repeat(entry):
                return command
    return None


def _history_entry_is_safe_to_repeat(entry: dict[str, Any]) -> bool:
    command = (entry.get("command") or "").strip()
    intent = (entry.get("intent") or "").strip()
    if not command:
        return False

    if intent not in SAFE_HISTORY_RETRY_INTENTS:
        return False

    if intent in {"doctor", "phone_status_battery", "phone_status_network", "browser_search"}:
        return True

    if intent in {"open_url", "browser_extract_text", "browser_list_links"}:
        return _url_history_command_is_safe(command)

    paths = _extract_paths(command)
    if not paths:
        return False

    try:
        allowed_roots = get_allowed_roots()
    except Exception:
        return False

    for raw_path in paths:
        if any(indicator in raw_path for indicator in TRAVERSAL_INDICATORS):
            return False
        resolved = resolve_path(raw_path)
        if not is_under_allowed_root(resolved, allowed_roots):
            return False
    return True


def _url_history_command_is_safe(command: str) -> bool:
    tokens = command.split()
    url_tokens = [token for token in tokens if token.startswith(("https://", "http://"))]
    if len(url_tokens) != 1:
        return False

    url = url_tokens[0]
    try:
        validate_url_safety(url)
    except Exception:
        return False

    # For advisory reuse, reject commands that mix an otherwise valid URL with
    # extra absolute-path or traversal-looking tokens. That keeps history reuse
    # aligned with Nabd's safety model without re-parsing the full command.
    remainder = command.replace(url, " ", 1)
    if _extract_paths(remainder):
        return False
    lowered = remainder.lower()
    if any(indicator in lowered for indicator in ("..", "%2e", "%2f", "\x00", "//")):
        return False

    return True


def _extract_paths(command: str) -> list[str]:
    paths: list[str] = []
    seen: set[str] = set()

    for match in QUOTED_RE.finditer(command):
        candidate = match.group(1).strip()
        if candidate.startswith("/") and candidate not in seen:
            seen.add(candidate)
            paths.append(candidate)

    for match in PATH_RE.finditer(command):
        candidate = match.group(1).strip().rstrip(".,;)")
        if candidate not in seen:
            seen.add(candidate)
            paths.append(candidate)

    return paths


def _extract_domain(url: str) -> str:
    try:
        return urlparse(url).netloc
    except Exception:
        return ""


def _dedupe_suggestions(suggestions: list[str]) -> list[str]:
    deduped: list[str] = []
    seen: set[str] = set()
    for suggestion in suggestions:
        clean = suggestion.strip()
        if not clean or clean in seen:
            continue
        seen.add(clean)
        deduped.append(clean)
    return deduped


def _filter_recent_command_repeats(
    suggestions: list[str],
    recent_history: list[dict[str, Any]],
) -> list[str]:
    recent_commands = {
        (entry.get("command") or "").strip()
        for entry in recent_history
        if (entry.get("command") or "").strip()
    }
    filtered: list[str] = []
    for suggestion in suggestions:
        if suggestion.lower().startswith("after "):
            filtered.append(suggestion)
            continue
        command = _extract_embedded_command(suggestion)
        if command and command in recent_commands:
            continue
        filtered.append(suggestion)
    return filtered


def _extract_embedded_command(suggestion: str) -> str:
    _prefix, sep, rest = suggestion.partition(": ")
    if not sep:
        return ""
    return rest.strip()
