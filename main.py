#!/usr/bin/env python3
"""
Nabd v0.2 — Local phone operations agent for Android/Termux.
Interactive CLI entry point.
"""

import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from agent.models import OperationStatus
from agent.parser import parse_command
from agent.planner import plan
from agent.safety import validate_intent_safety
from agent.executor import execute
from agent.reporter import report_parsed_intent, report_plan, report_result
from core.exceptions import (
    NabdError,
    SafetyError,
    PathNotAllowedError,
    PathTraversalError,
    ValidationError,
    UnknownIntentError,
    ConfigError,
)
from core.logging_db import log_operation

BANNER = """
╔══════════════════════════════════════════════════╗
║              Nabd  v0.2.0                        ║
║   Local Phone Operations Agent for Termux        ║
╚══════════════════════════════════════════════════╝"""

ONBOARDING = """
  Welcome! Here are a few things to try:

    doctor                                — check your setup
    storage report /sdcard/Download       — see disk usage
    show files in /sdcard/Download        — browse a folder
    list media in /sdcard/Download        — list photos/videos/audio
    find duplicates /sdcard/Download      — find duplicate files
    organize /sdcard/Download             — sort files into subfolders
    back up /sdcard/Documents to /sdcard/Backup

  Type 'help' for the full command list.
  Type 'exit' to quit.
"""

RETURNING_HINT = "  Type 'help' for commands  |  'history' for recent runs  |  'exit' to quit."

HELP_TEXT = """
────────────────────────────────────────────────────
  NABD COMMANDS  (v0.2)
────────────────────────────────────────────────────

  DIAGNOSTICS
    doctor
      Check Python, ffmpeg, Pillow, and storage access.

  STORAGE
    storage report /sdcard/Download
      Show total size, file count, and breakdown by type.

    list large files /sdcard/Download
    list large files /sdcard/Download top 10
      Show the biggest files (sorted by size).

  BROWSE
    show files in /sdcard/Download
    show files in /sdcard/Download sorted by size
      List every file and folder in a directory.

    list media in /sdcard/Download
    list media in /sdcard/Pictures recursively
      List images, videos, and audio files grouped by type.

  FIND
    find duplicates /sdcard/Download
      Find identical files (via SHA-256 hash). No changes made.

  ORGANISE
    organize /sdcard/Download
      Preview moving files into images/, videos/, documents/,
      audio/, archives/, code/, apks/, other/ subfolders.
      Asks for confirmation before making any changes.

  BACKUP
    back up /sdcard/Documents to /sdcard/Backup
      Copy a folder to a timestamped backup directory.

  CONVERT
    convert /sdcard/Movies/film.mp4 to mp3
      Extract audio from a video file. Requires ffmpeg.

  COMPRESS
    compress images /sdcard/Pictures
    compress images /sdcard/Pictures quality 60
      Re-save images at lower quality (overwrites originals).
      Requires Pillow.  Asks for confirmation first.

  RENAME
    rename files /sdcard/Download prefix old_
    rename files /sdcard/Download suffix _bak
      Add a prefix or suffix to every filename in a folder.

  MOVE
    move /sdcard/Download/file.txt to /sdcard/Documents
      Move a file or folder to a new location.

  HISTORY
    history
      Show your 20 most recent Nabd commands.

  OTHER
    help     — show this message
    exit     — quit Nabd
────────────────────────────────────────────────────
"""

EXIT_COMMANDS = {"exit", "quit", "q", "bye"}
HISTORY_COMMANDS = {"history", "hist"}


def prompt_confirmation(plan_summary: str, risk_label: str) -> bool:
    print(f"\n  Preview: {plan_summary}")
    try:
        answer = input(f"\n  [{risk_label} RISK] Apply these changes? [y/n]: ").strip().lower()
        return answer in {"y", "yes", "ok"}
    except (EOFError, KeyboardInterrupt):
        return False


def show_history() -> None:
    from core.logging_db import get_history
    entries = get_history(limit=20)
    if not entries:
        print("\n  No history yet. Run some commands first.")
        return

    print(f"\n  Last {len(entries)} command(s):\n")
    print(f"  {'#':<4}  {'TIME':<19}  {'STATUS':<16}  COMMAND")
    print("  " + "─" * 65)
    for i, entry in enumerate(entries, 1):
        status = entry.get("status", "?")
        ts = entry.get("timestamp", "?")[:19].replace("T", " ")
        cmd = entry.get("command", "?")
        if len(cmd) > 38:
            cmd = cmd[:35] + "..."
        status_icon = {
            "success": "✓ success",
            "cancelled": "- cancel",
            "unknown_intent": "? unknown",
            "safety_blocked": "✗ blocked",
            "failure": "✗ failed",
        }.get(status, status)
        print(f"  {i:<4}  {ts:<19}  {status_icon:<16}  {cmd}")


def _friendly_error(e: Exception, intent: str = "") -> str:
    """Return a concise, actionable error message."""
    msg = str(e)

    # Append context-specific hints
    hints: dict[str, str] = {
        "backup_folder": "Example: back up /sdcard/Documents to /sdcard/Backup",
        "safe_move_files": "Example: move /sdcard/Download/file.txt to /sdcard/Documents",
        "convert_video_to_mp3": "Example: convert /sdcard/Movies/film.mp4 to mp3",
        "organize_folder_by_type": "Example: organize /sdcard/Download",
        "safe_rename_files": "Example: rename files /sdcard/Download prefix bak_",
    }
    hint = hints.get(intent, "")
    if hint and hint not in msg:
        msg = f"{msg}\n  Hint: {hint}"
    return msg


def run_command(command: str) -> None:
    intent_repr: str | None = None
    plan_repr: str | None = None
    log_status = "failure"
    affected_paths: list[str] = []
    error_detail: str | None = None
    confirmed = False

    try:
        parsed = parse_command(command)
        print(report_parsed_intent(parsed))

        validate_intent_safety(parsed)

        execution_plan = plan(parsed)
        print(report_plan(execution_plan))
        intent_repr = parsed.intent
        plan_repr = execution_plan.preview_summary

        if execution_plan.requires_confirmation:
            risk_label = execution_plan.risk_level.value.upper()
            confirmed = prompt_confirmation(execution_plan.preview_summary, risk_label)
            if not confirmed:
                print("\n  Operation cancelled. No changes were made.")
                log_operation(command, intent_repr, plan_repr, "cancelled")
                return
        else:
            confirmed = False

        result = execute(execution_plan, confirmed=confirmed)
        print(report_result(result, parsed.intent, confirmed))

        affected_paths = result.affected_paths
        log_status = result.status.value
        if result.errors:
            error_detail = "; ".join(result.errors)

    except UnknownIntentError:
        print(
            "\n  [?] Command not recognised."
            "\n      Type 'help' to see all supported commands."
        )
        log_status = "unknown_intent"

    except (PathTraversalError, PathNotAllowedError) as e:
        print(f"\n  [SAFETY] {e}")
        error_detail = str(e)
        log_status = "safety_blocked"

    except ValidationError as e:
        msg = _friendly_error(e, intent_repr or "")
        print(f"\n  [!] {msg}")
        error_detail = str(e)
        log_status = "validation_error"

    except SafetyError as e:
        print(f"\n  [SAFETY] {e}")
        error_detail = str(e)
        log_status = "safety_error"

    except ConfigError as e:
        print(
            f"\n  [CONFIG] {e}"
            "\n  Check that config/allowed_paths.json and config/settings.json exist."
        )
        error_detail = str(e)
        log_status = "config_error"

    except NabdError as e:
        print(f"\n  [ERROR] {e}")
        error_detail = str(e)
        log_status = "error"

    except Exception as e:
        print(f"\n  [UNEXPECTED ERROR] {type(e).__name__}: {e}")
        error_detail = str(e)
        log_status = "unexpected_error"

    finally:
        log_operation(command, intent_repr, plan_repr, log_status, affected_paths, error_detail)


def main() -> None:
    from core.logging_db import is_first_run

    print(BANNER)

    if is_first_run():
        print(ONBOARDING)
    else:
        print(f"\n{RETURNING_HINT}\n")

    while True:
        try:
            command = input("\nnabd> ").strip()
        except (EOFError, KeyboardInterrupt):
            print("\n\n  Goodbye!")
            break

        if not command:
            continue

        if command.lower() in EXIT_COMMANDS:
            print("\n  Goodbye!")
            break

        if command.lower() in HISTORY_COMMANDS:
            show_history()
            continue

        if command.lower() in {"help", "?"}:
            print(HELP_TEXT)
            continue

        run_command(command)


if __name__ == "__main__":
    main()
