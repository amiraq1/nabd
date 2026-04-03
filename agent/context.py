import re
from typing import Any

from agent.models import ExecutionResult, OperationStatus, ParsedIntent
from agent.safety import validate_path_safety, validate_url_safety
from core.exceptions import ValidationError


AI_META_INTENTS = {
    "ai_suggest_command",
    "ai_explain_last_result",
    "ai_clarify_request",
    "ai_backend_status",
    "show_skills",
    "skill_info",
}

FOLDER_CONTEXT_INTENTS = {
    "storage_report",
    "list_large_files",
    "find_duplicates",
    "show_files",
    "show_folders",
    "list_media",
    "organize_folder_by_type",
    "compress_images",
    "safe_rename_files",
}
URL_CONTEXT_INTENTS = {
    "open_url",
    "browser_extract_text",
    "browser_list_links",
    "browser_page_title",
}

FOLDER_REFERENCE_RE = re.compile(r"\b(that\s+folder|it)\b", re.IGNORECASE)
URL_REFERENCE_RE = re.compile(r"\bit\b", re.IGNORECASE)


def new_session_context() -> dict[str, Any]:
    return {
        "last_command": "",
        "last_result": "",
        "last_intent": "",
        "last_folder": "",
        "last_url": "",
        "recent_context": [],
    }


def resolve_command_with_context(command: str, session: dict[str, Any]) -> str:
    lowered = command.lower().strip()

    resolved_folder = _resolve_folder_followup(command, lowered, session)
    if resolved_folder is not None:
        return resolved_folder

    resolved_url = _resolve_url_followup(command, lowered, session)
    if resolved_url is not None:
        return resolved_url

    if _looks_like_ambiguous_path_reference(lowered):
        raise ValidationError(
            "Context reference 'it' is ambiguous here.\n"
            "  Please name the exact file or folder path instead."
        )

    return command


def apply_session_context_to_intent(parsed: ParsedIntent, session: dict[str, Any]) -> ParsedIntent:
    if parsed.intent == "ai_explain_last_result":
        last_command = (session.get("last_command") or "").strip()
        last_result = (session.get("last_result") or "").strip()
        if not last_command or not last_result:
            raise ValidationError(
                "There is no recent result to explain yet.\n"
                "  Run a command first, then try: explain last result"
            )
        parsed.options["last_command"] = last_command
        parsed.options["last_result"] = last_result
    return parsed


def update_session_context(
    session: dict[str, Any],
    command: str,
    parsed: ParsedIntent,
    result: ExecutionResult,
) -> None:
    if parsed.intent in AI_META_INTENTS:
        return

    session["last_command"] = command
    session["last_result"] = result.message
    session["last_intent"] = parsed.intent
    session["last_folder"] = _safe_folder_context(parsed, result) or ""
    session["last_url"] = _safe_url_context(parsed, result) or ""

    entry: dict[str, str] = {
        "intent": parsed.intent,
        "command": command,
        "result": result.message,
    }
    if session["last_folder"]:
        entry["kind"] = "folder"
        entry["value"] = session["last_folder"]
    elif session["last_url"]:
        entry["kind"] = "url"
        entry["value"] = session["last_url"]

    recent_context = list(session.get("recent_context") or [])
    if entry.get("kind") and entry.get("value"):
        recent_context = [
            item for item in recent_context
            if not (
                item.get("kind") == entry["kind"]
                and item.get("value") == entry["value"]
            )
        ]
        recent_context.insert(0, entry)
        recent_context = recent_context[:5]
    session["recent_context"] = recent_context


def _resolve_folder_followup(command: str, lowered: str, session: dict[str, Any]) -> str | None:
    if not _looks_like_folder_followup(lowered):
        return None

    folder = (session.get("last_folder") or "").strip()
    if not folder:
        raise ValidationError(
            "I don't have a recent folder in context for that follow-up.\n"
            "  Please name the folder path explicitly."
        )
    try:
        folder = validate_path_safety(folder)
    except Exception:
        raise ValidationError(
            "The recent folder context is no longer safe to reuse.\n"
            "  Please name the folder path explicitly."
        )

    return FOLDER_REFERENCE_RE.sub(folder, command, count=1)


def _resolve_url_followup(command: str, lowered: str, session: dict[str, Any]) -> str | None:
    if not _looks_like_url_followup(lowered):
        return None

    url = (session.get("last_url") or "").strip()
    if not url:
        raise ValidationError(
            "I don't have a recent URL in context for that follow-up.\n"
            "  Please include the full URL explicitly."
        )
    try:
        url = validate_url_safety(url)
    except Exception:
        raise ValidationError(
            "The recent URL context is no longer safe to reuse.\n"
            "  Please include the full URL explicitly."
        )

    return URL_REFERENCE_RE.sub(url, command, count=1)


def _looks_like_folder_followup(lowered: str) -> bool:
    return bool(
        re.search(r"\b(show|list|find|storage|organize|compress|rename)\b", lowered)
        and (
            "that folder" in lowered
            or re.search(r"\bin\s+it\b", lowered)
            or re.search(r"\borganize\s+it\b", lowered)
            or re.search(r"\bcompress\s+images?\s+it\b", lowered)
            or re.search(r"\brename\s+files?\s+it\b", lowered)
        )
    )


def _looks_like_url_followup(lowered: str) -> bool:
    return bool(
        re.search(
            r"\b(extract\s+text|list\s+links?|show\s+(?:the\s+)?(?:page\s+)?title)\s+(?:from|on|at|of)\s+it\b",
            lowered,
        )
    )


def _looks_like_ambiguous_path_reference(lowered: str) -> bool:
    return bool(
        re.search(r"\b(move|back\s*up|backup|open\s+file)\b", lowered)
        and re.search(r"\bit\b", lowered)
    )


def _safe_folder_context(parsed: ParsedIntent, result: ExecutionResult) -> str:
    if result.status not in {OperationStatus.SUCCESS, OperationStatus.PARTIAL}:
        return ""
    candidate = _extract_folder_candidate(parsed, result)
    if not candidate:
        return ""
    try:
        return validate_path_safety(candidate)
    except Exception:
        return ""


def _safe_url_context(parsed: ParsedIntent, result: ExecutionResult) -> str:
    if parsed.intent not in URL_CONTEXT_INTENTS:
        return ""
    candidate = ""
    if parsed.url:
        candidate = parsed.url
    elif result.raw_results and isinstance(result.raw_results[0], dict):
        candidate = result.raw_results[0].get("url") or ""
    if not candidate:
        return ""
    try:
        return validate_url_safety(candidate)
    except Exception:
        return ""


def _extract_folder_candidate(parsed: ParsedIntent, result: ExecutionResult) -> str:
    if parsed.intent in FOLDER_CONTEXT_INTENTS and parsed.source_path:
        return parsed.source_path

    if result.raw_results and isinstance(result.raw_results[0], dict):
        raw = result.raw_results[0]
        if raw.get("directory"):
            return raw["directory"]

    return ""
