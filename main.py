#!/usr/bin/env python3
"""
Nabd v1.0 — Local phone operations agent for Android/Termux.
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
from agent.context import ContextMemory
from agent.advisor import Advisor
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
║              Nabd  v1.0                          ║
║   Local Phone Operations Agent for Termux        ║
╚══════════════════════════════════════════════════╝"""

ONBOARDING = """
  Welcome! Here are a few things to try:

    doctor                                — check your setup
    show skills                           — list built-in and filesystem skills
    skill info duplicate_helper           — inspect one skill safely
    storage report /sdcard/Download       — see disk usage
    show files in /sdcard/Download        — browse a folder
    list media in /sdcard/Download        — list photos/videos/audio
    find duplicates /sdcard/Download      — find duplicate files
    run skill duplicate_helper            — run a narrow Python-backed skill
    organize /sdcard/Download             — sort files into subfolders
    back up /sdcard/Documents to /sdcard/Backup
    show battery status                   — check battery (needs termux-api)
    show network status                   — check wifi (needs termux-api)
    open chrome                           — launch Chrome browser
    search for local llm tools            — open a web search
    open https://example.com              — open a URL in browser
    extract text from https://example.com — read a page's text

  Context shortcuts (after running a command):
    list media in that folder             — reuse the last folder
    extract text from that url            — reuse the last URL
    explain last result                   — explain what just happened

  Type 'help' for the full command list.
  Type 'exit' to quit.
"""

RETURNING_HINT = "  Type 'help' for commands  |  'history' for recent runs  |  'exit' to quit."

HELP_TEXT = """
────────────────────────────────────────────────────
  NABD COMMANDS  (v1.0)
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

  CONTEXT SHORTCUTS  (v1.0 — after running a folder or URL command)
    list media in that folder             — reuse the last folder
    find duplicates that folder           — reuse the last folder
    extract text from that url            — reuse the last URL
    list links from that url              — reuse the last URL
    show page title from it               — reuse the last URL (unambiguous)
    explain last result                   — explain what just happened

    Notes:
      • Nabd resolves 'that folder', 'that path', 'same folder',
        'that url', 'that link', 'that page', and 'it' (unambiguous only).
      • Mutating commands (move, back up, rename, compress, convert)
        always require an explicit path — context is never substituted.
      • Context is only stored after a successful command.

  SKILLS
    show skills
      List built-in and filesystem skills discovered under skills/<name>/SKILL.md.

    skill info duplicate_helper
    skill info ai_assist
      Show metadata, safety notes, and usage for one skill.

    run skill duplicate_helper
      Execute a Python-backed skill through the normal safety and executor path.
      Only explicit whitelisted entrypoints are allowed.
      Free-form skill arguments are not supported in this phase.

  AI ASSIST  (optional, off by default)
    suggest command for <text>
      Ask AI to suggest the best Nabd command for your request.
      Example: suggest command for check my phone setup

    explain last result
      Get a plain-English explanation of the last command's output.

    help me with <text>
      Ask AI to clarify what command to use.
      Example: help me with duplicate files

    ai backend status
      Show the active backend, reachability, capabilities, and
      troubleshooting hints. Safe to run at any time.

    AI Assist is advisory only — it suggests and explains, but
    never executes actions on your behalf.

    Backends (set "backend" in config/ai_assist.json):
      local     — deterministic keyword matching, always available (default)
      llama_cpp — local llama.cpp server or CLI, optional
      ollama    — Ollama server (/api/chat), optional

    To enable:  edit config/ai_assist.json → "enabled": true
    To switch:  edit config/ai_assist.json → "backend": "ollama"

  HISTORY
    history
      Show your 20 most recent Nabd commands.

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

# Session-level context memory and advisor (module-level singletons)
_ctx = ContextMemory()
_advisor = Advisor()

# Intents that should not overwrite session context
_CONTEXT_SKIP_INTENTS: frozenset[str] = frozenset({
    "ai_suggest_command",
    "ai_explain_last_result",
    "ai_clarify_request",
    "ai_backend_status",
    "show_skills",
    "skill_info",
    "run_skill",
})


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
        # ── Context resolution (v1.0) ─────────────────────────────────────────
        # Resolve explicit follow-up phrases ("that folder", "that url", "it")
        # before parsing. ValidationError is raised here if ambiguous or missing.
        resolved_command = _ctx.resolve(command)

        parsed = parse_command(resolved_command)
        # Keep original command in the intent for logging/explain purposes
        parsed.raw_command = command

        # Inject session context for 'explain last result'
        if parsed.intent == "ai_explain_last_result":
            parsed.options["last_command"] = _ctx.last_command
            parsed.options["last_result"] = _ctx.last_result_msg

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

        # ── Update context memory (v1.0) ──────────────────────────────────────
        # Only update context for operational intents; only store path/url on success.
        if parsed.intent not in _CONTEXT_SKIP_INTENTS:
            _ctx.update(
                intent=parsed.intent,
                command=command,
                result_msg=result.message,
                source_path=parsed.source_path,
                url=parsed.url,
                success=(result.status == OperationStatus.SUCCESS),
            )

        # ── Proactive advisor (v1.0) ──────────────────────────────────────────
        # Produce advisory next-step suggestions; display if non-empty.
        suggestions = _advisor.suggest(parsed.intent, result, _ctx)
        if suggestions:
            print("\n  Suggestions:")
            for s in suggestions:
                print(s)

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

        # Friendly message for accidental shell commands
        first_word = command.split()[0].lower() if command.split() else ""
        if first_word in SHELL_COMMANDS:
            hint = SHELL_COMMANDS[first_word]
            print(
                f"\n  [i] '{first_word}' is a shell command — Nabd is not a shell."
                f"\n\n{hint}"
                f"\n\n  Type 'exit' to return to Termux for shell use."
                f"\n  Type 'help' to see all Nabd commands."
            )
            log_operation(command, None, None, "shell_command_hint")
            continue

        run_command(command)


if __name__ == "__main__":
    main()
