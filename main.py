#!/usr/bin/env python3
"""
نبض (Nabd) — Local phone operations agent for Android/Termux.
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
║          نبض  —  Nabd  v0.1.0                   ║
║   Local Phone Operations Agent for Termux        ║
╚══════════════════════════════════════════════════╝
Type a command in Arabic or English.
Type 'help' for examples  |  'exit' or 'خروج' to quit.
"""

HELP_TEXT = """
────────────────────────────────────────────────────
  SUPPORTED COMMANDS
────────────────────────────────────────────────────
  English examples:
    storage report /sdcard/Download
    list large files /sdcard/Download
    organize /sdcard/Download
    find duplicates /sdcard/Download
    back up /sdcard/Documents to /sdcard/Backup
    convert /sdcard/Movies/video.mp4 to mp3
    compress images /sdcard/Pictures
    rename files /sdcard/Download prefix backup_
    move /sdcard/Download/file.txt to /sdcard/Documents

  Arabic examples:
    تقرير التخزين /sdcard/Download
    اعرض أكبر الملفات /sdcard/Download
    رتّب مجلد /sdcard/Download
    ابحث عن الملفات المكررة /sdcard/Download
    انسخ /sdcard/Documents احتياطيًا إلى /sdcard/Backup
    حوّل /sdcard/Movies/video.mp4 إلى mp3
    اضغط صور /sdcard/Pictures

  Other commands:
    history    — show recent command history
    help       — show this message
    exit       — quit
────────────────────────────────────────────────────
"""

EXIT_COMMANDS = {"exit", "quit", "خروج", "q", "bye"}
HISTORY_COMMANDS = {"history", "hist", "سجل", "السجل"}


def prompt_confirmation(plan_summary: str, risk_label: str) -> bool:
    print(f"\n  Preview: {plan_summary}")
    try:
        answer = input(f"\n  [{risk_label} RISK] Apply these changes? [y/n]: ").strip().lower()
        return answer in {"y", "yes", "نعم", "موافق", "ok"}
    except (EOFError, KeyboardInterrupt):
        return False


def show_history() -> None:
    from core.logging_db import get_history
    entries = get_history(limit=10)
    if not entries:
        print("\n  No history yet.")
        return
    print("\n  Recent commands:")
    print(f"  {'TIME':<16}  {'STATUS':<16}  COMMAND")
    print("  " + "─" * 60)
    for entry in entries:
        status = entry.get("status", "?")
        ts = entry.get("timestamp", "?")[:16]
        cmd = entry.get("command", "?")
        if len(cmd) > 50:
            cmd = cmd[:47] + "..."
        print(f"  {ts:<16}  {status:<16}  {cmd}")


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

    except UnknownIntentError as e:
        print(f"\n  [!] Could not understand command.")
        print(f"      {e}")
        error_detail = str(e)
        log_status = "unknown_intent"

    except (PathTraversalError, PathNotAllowedError) as e:
        print(f"\n  [SAFETY] {e}")
        error_detail = str(e)
        log_status = "safety_blocked"

    except ValidationError as e:
        print(f"\n  [!] {e}")
        error_detail = str(e)
        log_status = "validation_error"

    except SafetyError as e:
        print(f"\n  [SAFETY] {e}")
        error_detail = str(e)
        log_status = "safety_error"

    except ConfigError as e:
        print(f"\n  [CONFIG ERROR] {e}")
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
    print(BANNER)

    while True:
        try:
            command = input("\nنبض> ").strip()
        except (EOFError, KeyboardInterrupt):
            print("\n\n  Goodbye! / مع السلامة")
            break

        if not command:
            continue

        if command.lower() in EXIT_COMMANDS:
            print("\n  Goodbye! / مع السلامة")
            break

        if command.lower() in HISTORY_COMMANDS:
            show_history()
            continue

        if command.lower() in {"help", "مساعدة", "?"}:
            print(HELP_TEXT)
            continue

        run_command(command)


if __name__ == "__main__":
    main()
