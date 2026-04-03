#!/usr/bin/env python3
"""
Nabd v0.8 — Local phone operations agent for Android/Termux.
Interactive CLI entry point.
"""

import sys
import os
from typing import Any

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from agent.models import OperationStatus
from agent.context import (
    apply_session_context_to_intent,
    new_session_context,
    resolve_command_with_context,
    update_session_context,
)
from agent.parser import parse_command
from agent.planner import plan
from agent.safety import validate_intent_safety
from agent.executor import execute
from agent.reporter import report_parsed_intent, report_plan, report_result
from agent.advisor import format_advisory_suggestions, generate_advisory_suggestions
from core.exceptions import (
    NabdError,
    SafetyError,
    PathNotAllowedError,
    PathTraversalError,
    ValidationError,
    UnknownIntentError,
    ConfigError,
)
from core.logging_db import get_history, log_operation
from core.colors import Colors, colorize

BANNER = f"""
{Colors.OKCYAN}╔══════════════════════════════════════════════════╗
║              Nabd  v0.8                          ║
║   Local Phone Operations Agent for Termux        ║
╚══════════════════════════════════════════════════╝{Colors.ENDC}"""

ONBOARDING = """
  Welcome! Here are a few things to try:

    doctor                                — check your setup
    storage report /sdcard/Download       — see disk usage
    show files in /sdcard/Download        — browse a folder
    list media in /sdcard/Download        — list photos/videos/audio
    find duplicates /sdcard/Download      — find duplicate files
    organize /sdcard/Download             — sort files into subfolders
    back up /sdcard/Documents to /sdcard/Backup
    show battery status                   — check battery (needs termux-api)
    show network status                   — check wifi (needs termux-api)
    open chrome                           — launch Chrome browser
    search for local llm tools            — open a web search
    open https://example.com              — open a URL in browser
    extract text from https://example.com — read a page's text
    history                                — view the last 20 commands
    history search storage               — filter history without replaying
    show files in that folder             — keep working in the same folder

  Type 'help' for the full command list.
  Type 'exit' to quit.
"""

RETURNING_HINT = "  Type 'help' for commands  |  'history' for recent runs  |  'exit' to quit."

HELP_TEXT = """
────────────────────────────────────────────────────
  NABD COMMANDS  (v0.8)
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

    show folders in /sdcard/Download
      List only subfolders with item counts (no files shown).

    list media in /sdcard/Download
    list media in /sdcard/Pictures recursively
      List images, videos, and audio files grouped by type.
      Add 'recursively' to scan subfolders too.

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

  PHONE  (requires termux-api: pkg install termux-api)
    show battery status
      Check battery level, status, health, and temperature.

    show network status
      Check wifi connection name, IP, and signal.

    open chrome
    open settings
    open files
    open camera
    open gallery
    open calculator
      Launch a supported Android app.
      Asks for confirmation before opening URL / file.

    open https://example.com
      Open a URL in the default browser.

    open file /sdcard/Download/report.pdf
      Open a local file in the appropriate app.

  BROWSER
    search for local llm tools
    google for android tips
      Open a web search in the default browser.

    show page title from https://example.com
      Fetch only the page title. Fast and minimal.

    extract text from https://example.com
      Fetch a page and return its readable text (no account needed).

    list links from https://example.com
      Fetch a page and list all links found on it.

  SKILLS
    show skills
      List all available Nabd skill modules.

    skill info ai_assist
      Show details about the AI Assist skill.

  AI ASSIST  (optional, off by default)
    suggest command for <text>
      Ask AI to suggest the best Nabd command for your request.
      Example: suggest command for check my phone setup

    explain last result
      Get a plain-English explanation of the last command's output.

    help me with <text>
      Ask AI to clarify what command to use.
      Example: help me with finding duplicate files

    ai backend status
      Show which backend is active (local or llama.cpp) and whether
      it is reachable. Safe to run whether or not AI Assist is enabled.

    AI Assist is advisory only — it suggests and explains, but
    never executes actions on your behalf.

    Backends:
      local     — deterministic keyword matching, always available (default)
      llama_cpp — local llama.cpp HTTP server (set backend in ai_assist.json)

    To enable: edit config/ai_assist.json → "enabled": true

  HISTORY
    history
      Show your 20 most recent Nabd commands.

    history search <term>
    history intent <intent>
    history show <id>
      Search or filter the history log without replaying commands.

  CONTEXT & FOLLOW-UPS
    show files in that folder
    list media in that folder
    list links from it
    explain that result
      Continue work on the last folder, media scan, or browser result when Nabd already described it. Nabd clarifies before acting if the reference is ambiguous.

  OTHER
    help     — show this message
    exit     — quit Nabd

────────────────────────────────────────────────────
  NABD VS TERMUX SHELL
────────────────────────────────────────────────────
  Nabd is not a shell. Common shell commands are not
  supported here. Type 'exit' to return to Termux.

  Shell → Nabd equivalent:
    ls <path>         →  show files in <path>
    ls -d */ <path>   →  show folders in <path>
    find <path>       →  find duplicates <path>
    du <path>         →  storage report <path>
    mv <f> <d>        →  move <f> to <d>
    cp -r <s> <d>     →  back up <s> to <d>
    python script.py  →  (use Termux for scripting)
────────────────────────────────────────────────────
"""

EXIT_COMMANDS = {"exit", "quit", "q", "bye"}
HISTORY_COMMANDS = {"history", "hist"}

# Session state — short-term current-session memory for explain/context/advisory
_session: dict[str, Any] = new_session_context()

# Shell commands that users might accidentally type inside Nabd
SHELL_COMMANDS: dict[str, str] = {
    "ls":    "  Nabd equivalent: show files in /sdcard/Download",
    "ll":    "  Nabd equivalent: show files in /sdcard/Download sorted by size",
    "dir":   "  Nabd equivalent: show files in /sdcard/Download",
    "cd":    "  Nabd does not track a current directory.\n  Use a full path in each command, e.g. show files in /sdcard/Download",
    "pwd":   "  Nabd does not track a current directory.\n  Use full absolute paths, e.g. /sdcard/Download",
    "mkdir": "  Nabd does not create empty directories.\n  Directories are created automatically during organize and back up.",
    "rm":    "  Nabd does not delete files — your data is safe.",
    "rmdir": "  Nabd does not delete directories — your data is safe.",
    "mv":    "  Nabd equivalent: move /sdcard/Download/file.txt to /sdcard/Documents",
    "cp":    "  Nabd equivalent: back up /sdcard/Documents to /sdcard/Backup",
    "find":  "  Nabd equivalents:\n    find duplicates /sdcard/Download\n    show files in /sdcard/Download",
    "grep":  "  Nabd does not search inside file contents.",
    "cat":   "  Nabd does not display file contents.",
    "less":  "  Nabd does not display file contents.",
    "du":    "  Nabd equivalent: storage report /sdcard/Download",
    "df":    "  Nabd equivalent: storage report /sdcard/Download",
    "chmod": "  Nabd does not change file permissions.",
    "chown": "  Nabd does not change file ownership.",
    "touch": "  Nabd does not create empty files.",
    "stat":  "  Nabd equivalent: show files in /sdcard/Download",
    "tree":   "  Nabd equivalent: show files in /sdcard/Download",
    "python": "  Nabd does not run Python scripts.\n  Use Termux for scripting: python3 script.py",
}


def prompt_confirmation(plan_summary: str, risk_label: str) -> bool:
    print(f"\n  Preview: {plan_summary}")
    try:
        answer = input(f"\n  [{risk_label} RISK] Apply these changes? [y/n]: ").strip().lower()
        return answer in {"y", "yes", "ok"}
    except (EOFError, KeyboardInterrupt):
        return False


def show_history() -> None:
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


def run_command(command: str) -> str | None:
    intent_repr: str | None = None
    plan_repr: str | None = None
    log_status = "failure"
    affected_paths: list[str] = []
    error_detail: str | None = None
    confirmed = False
    next_command: str | None = None

    try:
        resolved_command = resolve_command_with_context(command, _session)
        parsed = parse_command(resolved_command)
        parsed = apply_session_context_to_intent(parsed, _session)

        print(report_parsed_intent(parsed))

        validate_intent_safety(parsed)

        execution_plan = plan(parsed)
        print(report_plan(execution_plan))
        intent_repr = parsed.intent
        plan_repr = execution_plan.preview_summary

        # Modifying plans support a real dry-run preview. Show the actual
        # computed changes before asking for confirmation.
        if execution_plan.requires_confirmation and execution_plan.dry_run:
            preview_result = execute(execution_plan, confirmed=False)
            print(report_result(preview_result, parsed.intent, confirmed=False))
            if preview_result.status == OperationStatus.FAILURE:
                update_session_context(_session, command, parsed, preview_result)
                affected_paths = preview_result.affected_paths
                log_status = preview_result.status.value
                if preview_result.errors:
                    error_detail = "; ".join(preview_result.errors)
                return None

        if execution_plan.requires_confirmation:
            risk_label = execution_plan.risk_level.value.upper()
            confirmed = prompt_confirmation(execution_plan.preview_summary, risk_label)
            if not confirmed:
                print(f"\n  {Colors.WARNING}Operation cancelled. No changes were made.{Colors.ENDC}")
                log_operation(command, intent_repr, plan_repr, "cancelled")
                return None
        else:
            confirmed = False

        result = execute(execution_plan, confirmed=confirmed)
        print(report_result(result, parsed.intent, confirmed))

        suggestions = generate_advisory_suggestions(
            parsed,
            result,
            recent_history=get_history(limit=5),
            session_context=_session,
        )
        advisory_text = format_advisory_suggestions(suggestions)
        if advisory_text:
            print(advisory_text)
            try:
                choice = input(f"\n  {Colors.OKCYAN}Run suggestion [1-{len(suggestions)}] or press Enter to skip:{Colors.ENDC} ").strip()
                if choice.isdigit() and 1 <= int(choice) <= len(suggestions):
                    selected = suggestions[int(choice) - 1]
                    next_command = selected.split(": ")[-1].strip() if ": " in selected else selected
            except (EOFError, KeyboardInterrupt):
                pass

        affected_paths = result.affected_paths
        log_status = result.status.value
        if result.errors:
            error_detail = "; ".join(result.errors)

        update_session_context(_session, command, parsed, result)

    except UnknownIntentError:
        print(
            f"\n  {Colors.FAIL}[?]{Colors.ENDC} Command not recognised."
            f"\n      Type 'help' to see all supported commands."
        )
        log_status = "unknown_intent"

    except (PathTraversalError, PathNotAllowedError) as e:
        print(f"\n  {Colors.FAIL}[SAFETY]{Colors.ENDC} {e}")
        error_detail = str(e)
        log_status = "safety_blocked"

    except ValidationError as e:
        msg = _friendly_error(e, intent_repr or "")
        print(f"\n  {Colors.WARNING}[!]{Colors.ENDC} {msg}")
        error_detail = str(e)
        log_status = "validation_error"

    except SafetyError as e:
        print(f"\n  {Colors.FAIL}[SAFETY]{Colors.ENDC} {e}")
        error_detail = str(e)
        log_status = "safety_error"

    except ConfigError as e:
        print(
            f"\n  {Colors.FAIL}[CONFIG]{Colors.ENDC} {e}"
            "\n  Check that config/allowed_paths.json and config/settings.json exist."
        )
        error_detail = str(e)
        log_status = "config_error"

    except NabdError as e:
        print(f"\n  {Colors.FAIL}[ERROR]{Colors.ENDC} {e}")
        error_detail = str(e)
        log_status = "error"

    except Exception as e:
        print(f"\n  {Colors.FAIL}[UNEXPECTED ERROR]{Colors.ENDC} {type(e).__name__}: {e}")
        error_detail = str(e)
        log_status = "unexpected_error"

    finally:
        log_operation(command, intent_repr, plan_repr, log_status, affected_paths, error_detail)

    return next_command


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

        # Friendly message for accidental shell commands
        first_word = command.split()[0].lower() if command.split() else ""
        if first_word in SHELL_COMMANDS:
            hint = SHELL_COMMANDS[first_word]
            print(
                f"\n  {Colors.OKCYAN}[i]{Colors.ENDC} '{first_word}' is a shell command — Nabd is not a shell."
                f"\n\n{hint}"
                f"\n\n  Type 'exit' to return to Termux for shell use."
                f"\n  Type 'help' to see all Nabd commands."
            )
            log_operation(command, None, None, "shell_command_hint")
            continue

        next_cmd = run_command(command)
        while next_cmd:
            print(f"\nnabd> {Colors.OKGREEN}{next_cmd}{Colors.ENDC}")
            next_cmd = run_command(next_cmd)


if __name__ == "__main__":
    main()
