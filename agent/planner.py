import os
from typing import Any

from agent.models import ExecutionPlan, ParsedIntent, RiskLevel, ToolAction
from agent.parser import ALL_INTENTS
from core.config import get_settings
from core.exceptions import UnknownIntentError, ValidationError


def _default_source(intent_name: str) -> str:
    defaults = {
        "storage_report": "/sdcard/Download",
        "list_large_files": "/sdcard/Download",
        "organize_folder_by_type": "/sdcard/Download",
        "find_duplicates": "/sdcard/Download",
        "compress_images": "/sdcard/Pictures",
        "show_files": "/sdcard/Download",
        "show_folders": "/sdcard/Download",
        "list_media": "/sdcard/Download",
    }
    return defaults.get(intent_name, "/sdcard/Download")


def plan(intent: ParsedIntent) -> ExecutionPlan:
    settings = get_settings()

    planners = {
        # ── Diagnostic ─────────────────────────────────────────────────────────
        "doctor": _plan_doctor,
        # ── Skills system ──────────────────────────────────────────────────────
        "show_skills": _plan_show_skills,
        "skill_info": _plan_skill_info,
        "run_skill": _plan_run_skill,
        # ── AI Assist (advisory only — never executes tool actions) ────────────
        "ai_backend_status": _plan_ai_backend_status,
        "ai_suggest_command": _plan_ai_suggest_command,
        "ai_explain_last_result": _plan_ai_explain_last_result,
        "ai_clarify_request": _plan_ai_clarify_request,
        # ── Phone status ───────────────────────────────────────────────────────
        "phone_status_battery": _plan_phone_battery,
        "phone_status_network": _plan_phone_network,
        # ── Storage ────────────────────────────────────────────────────────────
        "storage_report": _plan_storage_report,
        "list_large_files": _plan_list_large_files,
        # ── Browse ─────────────────────────────────────────────────────────────
        "show_files": _plan_show_files,
        "show_folders": _plan_show_folders,
        "list_media": _plan_list_media,
        # ── File management ────────────────────────────────────────────────────
        "organize_folder_by_type": _plan_organize_folder,
        "find_duplicates": _plan_find_duplicates,
        "backup_folder": _plan_backup_folder,
        "convert_video_to_mp3": _plan_convert_video,
        "compress_images": _plan_compress_images,
        "safe_rename_files": _plan_rename_files,
        "safe_move_files": _plan_move_files,
        # ── Open / launch ──────────────────────────────────────────────────────
        "open_url": _plan_open_url,
        "open_file": _plan_open_file,
        "open_app": _plan_open_app,
        # ── Browser ────────────────────────────────────────────────────────────
        "browser_search": _plan_browser_search,
        "browser_page_title": _plan_browser_page_title,
        "browser_extract_text": _plan_browser_extract_text,
        "browser_list_links": _plan_browser_list_links,
        "history_search": _plan_history_search,
        "history_intent": _plan_history_intent,
        "history_show": _plan_history_show,
        # ── Scheduling ─────────────────────────────────────────────────────────
        "schedule_create": _plan_schedule_create,
        "schedule_list": _plan_schedule_list,
        "schedule_delete": _plan_schedule_delete,
    }

    handler = planners.get(intent.intent)
    if handler is None:
        raise UnknownIntentError(f"No planner for intent: '{intent.intent}'")

    return handler(intent, settings)


# ── Skills system ──────────────────────────────────────────────────────────────

def _plan_show_skills(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(tool_name="skill", function_name="list_skills", arguments={})],
        preview_summary="List all registered Nabd skills",
    )


def _plan_skill_info(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    skill_name = intent.query or ""
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="skill",
            function_name="skill_info",
            arguments={"skill_name": skill_name},
        )],
        preview_summary=f"Show details for skill: '{skill_name}'",
    )


def _plan_run_skill(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    skill_name = intent.query or ""
    if not skill_name:
        raise ValidationError("Please specify the skill name to run.")
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="skill",
            function_name="run_skill",
            arguments={"skill_name": skill_name},
        )],
        preview_summary=f"Run skill '{skill_name}' through the whitelisted skill executor",
    )


# ── AI Assist planners (advisory only — no tool actions executed) ──────────────

def _plan_ai_backend_status(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="skill",
            function_name="backend_status",
            arguments={},
        )],
        preview_summary="Show AI Assist backend status and availability",
    )


def _plan_ai_suggest_command(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    user_text = intent.query or ""
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="ai_skill",
            function_name="suggest_command",
            arguments={"user_text": user_text},
        )],
        preview_summary=f"AI suggests a Nabd command for: '{user_text}'",
    )


def _plan_ai_explain_last_result(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    last_command = intent.options.get("last_command", "")
    last_result = intent.options.get("last_result", "")
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="ai_skill",
            function_name="explain_result",
            arguments={"last_command": last_command, "last_result": last_result},
        )],
        preview_summary="AI explains the last result in plain English",
    )


def _plan_ai_clarify_request(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    user_text = intent.query or ""
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="ai_skill",
            function_name="clarify_request",
            arguments={"user_text": user_text},
        )],
        preview_summary=f"AI clarifies request: '{user_text}'",
    )


def _plan_history_search(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    term = (intent.options.get("term") or "").strip()
    if not term:
        raise ValidationError("Please specify a term to search in history.")
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="history",
            function_name="search_history",
            arguments={"term": term},
        )],
        preview_summary=f"Search history for: {term}",
    )


def _plan_history_intent(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    target_intent = (intent.options.get("target_intent") or "").strip()
    if not target_intent:
        raise ValidationError("Please specify an intent name to filter history.")
    if target_intent not in ALL_INTENTS:
        raise ValidationError(f"Unknown intent name: '{target_intent}'")
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="history",
            function_name="history_by_intent",
            arguments={"intent_name": target_intent},
        )],
        preview_summary=f"Show history entries for intent: {target_intent}",
    )


def _plan_history_show(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    entry_id = intent.options.get("entry_id")
    if entry_id is None:
        raise ValidationError("Please provide a history entry id to show.")
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="history",
            function_name="show_history_entry",
            arguments={"entry_id": entry_id},
        )],
        preview_summary=f"Show history entry #{entry_id}",
    )


# ── Diagnostic ─────────────────────────────────────────────────────────────────

def _plan_doctor(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(tool_name="system", function_name="run_doctor", arguments={})],
        preview_summary="Check Nabd environment (Python, ffmpeg, Pillow, allowed paths, history log)",
    )


# ── Phone status ───────────────────────────────────────────────────────────────

def _plan_phone_battery(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(tool_name="phone", function_name="get_battery_status", arguments={})],
        preview_summary="Read battery status from termux-api",
    )


def _plan_phone_network(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(tool_name="phone", function_name="get_network_status", arguments={})],
        preview_summary="Read wifi / network status from termux-api",
    )


# ── Storage ────────────────────────────────────────────────────────────────────

def _plan_storage_report(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    directory = intent.source_path or _default_source("storage_report")
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="storage",
            function_name="get_storage_report",
            arguments={"directory": directory},
        )],
        preview_summary=f"Generate storage report for: {directory}",
    )


def _plan_list_large_files(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    directory = intent.source_path or _default_source("list_large_files")
    top_n = intent.options.get("top_n", settings.get("max_large_files", 20))
    threshold_mb = settings.get("large_file_threshold_mb", 0.0)
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="storage",
            function_name="list_large_files",
            arguments={"directory": directory, "top_n": top_n, "threshold_mb": threshold_mb},
        )],
        preview_summary=f"List top {top_n} largest files in: {directory}",
    )


# ── Browse ─────────────────────────────────────────────────────────────────────

def _plan_show_files(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    if not intent.source_path:
        raise ValidationError(
            "Please specify the directory to list.\n"
            "  Example: show files in /sdcard/Download"
        )
    directory = intent.source_path
    sort_by = intent.options.get("sort_by", "name")
    limit = intent.options.get("limit", 100)
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="files",
            function_name="show_files",
            arguments={"directory": directory, "sort_by": sort_by, "limit": limit},
        )],
        preview_summary=f"List files in: {directory} (sorted by {sort_by})",
    )


def _plan_show_folders(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    directory = intent.source_path or _default_source("show_folders")
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="files",
            function_name="show_folders",
            arguments={"directory": directory},
        )],
        preview_summary=f"List subfolders in: {directory}",
    )


def _plan_list_media(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    if not intent.source_path:
        raise ValidationError(
            "Please specify the directory to scan for media.\n"
            "  Example: list media in /sdcard/Download"
        )
    directory = intent.source_path
    recursive = intent.options.get("recursive", False)
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="files",
            function_name="list_media",
            arguments={"directory": directory, "recursive": recursive},
        )],
        preview_summary=f"List media files in: {directory}",
    )


# ── File management ────────────────────────────────────────────────────────────

def _plan_organize_folder(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    if not intent.source_path:
        raise ValidationError(
            "Please specify a directory to organize.\n"
            "  Example: organize /sdcard/Download"
        )
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.MEDIUM,
        requires_confirmation=True,
        dry_run=True,
        actions=[ToolAction(
            tool_name="files",
            function_name="organize_folder_by_type",
            arguments={"directory": intent.source_path, "dry_run": True},
        )],
        preview_summary=f"Organize '{intent.source_path}' into category subfolders",
    )


def _plan_find_duplicates(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    directory = intent.source_path or _default_source("find_duplicates")
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="duplicates",
            function_name="find_duplicates",
            arguments={"directory": directory, "recursive": True},
        )],
        preview_summary=f"Scan for duplicate files in: {directory}",
    )


def _plan_backup_folder(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    source = intent.source_path
    target = intent.target_path
    if not source:
        raise ValidationError(
            "Please specify the folder to back up.\n"
            "  Example: back up /sdcard/Documents to /sdcard/Backup"
        )
    if not target:
        raise ValidationError(
            "Please specify the destination directory for the backup.\n"
            "  Example: back up /sdcard/Documents to /sdcard/Backup"
        )
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.MEDIUM,
        requires_confirmation=True,
        dry_run=True,
        actions=[ToolAction(
            tool_name="backup",
            function_name="backup_folder",
            arguments={"source": source, "destination_root": target, "dry_run": True},
        )],
        preview_summary=f"Back up '{source}' → '{target}'",
    )


def _plan_convert_video(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    source = intent.source_path
    if not source:
        raise ValidationError(
            "Please specify the video file to convert.\n"
            "  Example: convert /sdcard/Movies/film.mp4 to mp3"
        )
    base = os.path.splitext(source)[0]
    output_path = intent.target_path or f"{base}.mp3"
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.MEDIUM,
        requires_confirmation=True,
        dry_run=True,
        actions=[ToolAction(
            tool_name="media",
            function_name="convert_video_to_mp3",
            arguments={"video_path": source, "output_path": output_path, "dry_run": True},
        )],
        preview_summary=f"Convert '{source}' → '{output_path}'",
    )


def _plan_compress_images(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    directory = intent.source_path or _default_source("compress_images")
    quality = intent.options.get("quality", settings.get("image_compress_quality", 75))
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.HIGH,
        requires_confirmation=True,
        dry_run=True,
        actions=[ToolAction(
            tool_name="media",
            function_name="compress_images",
            arguments={"directory": directory, "quality": quality, "dry_run": True},
        )],
        preview_summary=f"Compress images in '{directory}' at quality {quality}% (overwrites originals)",
    )


def _plan_rename_files(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    directory = intent.source_path
    if not directory:
        raise ValidationError(
            "Please specify the directory whose files should be renamed.\n"
            "  Example: rename files /sdcard/Download prefix new_"
        )
    prefix = intent.options.get("prefix", "")
    suffix = intent.options.get("suffix", "")
    if not prefix and not suffix:
        raise ValidationError(
            "Please specify a prefix or suffix.\n"
            "  Example: rename files /sdcard/Download prefix backup_"
        )
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.HIGH,
        requires_confirmation=True,
        dry_run=True,
        actions=[ToolAction(
            tool_name="files",
            function_name="safe_rename_files",
            arguments={"directory": directory, "prefix": prefix, "suffix": suffix, "dry_run": True},
        )],
        preview_summary=f"Rename files in '{directory}' with prefix='{prefix}' suffix='{suffix}'",
    )


def _plan_move_files(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    source = intent.source_path
    target = intent.target_path
    if not source:
        raise ValidationError(
            "Please specify the file or folder to move.\n"
            "  Example: move /sdcard/Download/file.txt to /sdcard/Documents"
        )
    if not target:
        raise ValidationError(
            "Please specify the destination directory.\n"
            "  Example: move /sdcard/Download/file.txt to /sdcard/Documents"
        )
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.MEDIUM,
        requires_confirmation=True,
        dry_run=True,
        actions=[ToolAction(
            tool_name="files",
            function_name="safe_move_files",
            arguments={"source_path": source, "target_directory": target, "dry_run": True},
        )],
        preview_summary=f"Move '{source}' → '{target}'",
    )


# ── Open / launch ──────────────────────────────────────────────────────────────

def _plan_open_url(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    url = intent.url
    if not url:
        raise ValidationError(
            "Please specify a URL to open.\n"
            "  Example: open https://example.com"
        )
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.MEDIUM,
        requires_confirmation=True,
        dry_run=False,
        actions=[ToolAction(
            tool_name="phone",
            function_name="open_url",
            arguments={"url": url},
        )],
        preview_summary=f"Open URL in default browser: {url}",
    )


def _plan_open_file(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    path = intent.source_path
    if not path:
        raise ValidationError(
            "Please specify the file path to open.\n"
            "  Example: open file /sdcard/Download/report.pdf"
        )
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.MEDIUM,
        requires_confirmation=True,
        dry_run=False,
        actions=[ToolAction(
            tool_name="phone",
            function_name="open_file",
            arguments={"path": path},
        )],
        preview_summary=f"Open file in appropriate app: {path}",
    )


def _plan_open_app(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    app_name = intent.app_name
    if not app_name:
        raise ValidationError(
            "Please specify an app name.\n"
            "  Supported: chrome, files, settings, camera, gallery, calculator\n"
            "  Example: open chrome"
        )
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.MEDIUM,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="phone",
            function_name="open_app",
            arguments={"app_name": app_name},
        )],
        preview_summary=f"Launch app: {app_name}",
    )


# ── Browser ────────────────────────────────────────────────────────────────────

def _plan_browser_search(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    query = intent.query
    if not query:
        raise ValidationError(
            "Please specify a search query.\n"
            "  Example: search for local llm tools"
        )
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="browser",
            function_name="browser_search",
            arguments={"query": query},
        )],
        preview_summary=f"Open web search: '{query}'",
    )


def _plan_browser_page_title(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    url = intent.url
    if not url:
        raise ValidationError(
            "Please specify the URL to fetch the title from.\n"
            "  Example: show page title from https://example.com"
        )
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="browser",
            function_name="browser_page_title",
            arguments={"url": url},
        )],
        preview_summary=f"Fetch page title from: {url}",
    )


def _plan_browser_extract_text(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    url = intent.url
    if not url:
        raise ValidationError(
            "Please specify the URL to extract text from.\n"
            "  Example: extract text from https://example.com"
        )
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="browser",
            function_name="browser_extract_text",
            arguments={"url": url},
        )],
        preview_summary=f"Fetch and extract readable text from: {url}",
    )


def _plan_browser_list_links(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    url = intent.url
    if not url:
        raise ValidationError(
            "Please specify the URL to list links from.\n"
            "  Example: list links from https://example.com"
        )
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="browser",
            function_name="browser_list_links",
            arguments={"url": url},
        )],
        preview_summary=f"Fetch and list links from: {url}",
    )


# ── Scheduling ─────────────────────────────────────────────────────────────────

def _plan_schedule_create(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    target_command = intent.options.get("target_command", "")
    interval = intent.options.get("interval", "")
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.MEDIUM,
        requires_confirmation=True,
        dry_run=False,
        actions=[ToolAction(
            tool_name="schedule",
            function_name="create_schedule",
            arguments={"target_command": target_command, "interval": interval},
        )],
        preview_summary=f"Schedule command to run every {interval}: '{target_command}'",
    )


def _plan_schedule_list(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.LOW,
        requires_confirmation=False,
        dry_run=False,
        actions=[ToolAction(
            tool_name="schedule",
            function_name="list_schedules",
            arguments={},
        )],
        preview_summary="List all scheduled commands",
    )


def _plan_schedule_delete(intent: ParsedIntent, settings: dict) -> ExecutionPlan:
    schedule_id = intent.options.get("schedule_id", "")
    return ExecutionPlan(
        intent=intent.intent,
        risk_level=RiskLevel.MEDIUM,
        requires_confirmation=True,
        dry_run=False,
        actions=[ToolAction(
            tool_name="schedule",
            function_name="delete_schedule",
            arguments={"schedule_id": schedule_id},
        )],
        preview_summary=f"Delete schedule ID: {schedule_id}",
    )
