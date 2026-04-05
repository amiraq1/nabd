import json
import os
import uuid
from datetime import datetime
from typing import Any

from core.exceptions import ToolError

_SCHEDULE_INTENTS = {"schedule_create", "schedule_delete", "schedule_list"}


def _get_schedule_file() -> str:
    base = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    return os.path.join(base, "data", "schedules.json")


def _load_schedules() -> dict[str, dict[str, Any]]:
    path = _get_schedule_file()
    if not os.path.exists(path):
        return {}
    try:
        with open(path, "r", encoding="utf-8") as f:
            return json.load(f)
    except Exception:
        return {}


def _save_schedules(schedules: dict[str, dict[str, Any]]) -> None:
    path = _get_schedule_file()
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(schedules, f, indent=2)


def inspect_schedule_target(target_command: str) -> dict[str, Any]:
    """
    Parse and safety-validate a stored schedule command.

    Revalidation is intentionally callable at read time so a future scheduler
    never trusts stale or tampered command text.
    """
    normalized = (target_command or "").strip()
    validated_at = datetime.now().isoformat()

    if not normalized:
        return {
            "ok": False,
            "target_command": normalized,
            "error": "Schedule target command must not be empty.",
            "validated_at": validated_at,
        }

    try:
        from agent.parser import parse_command
        from agent.safety import validate_intent_safety

        parsed = parse_command(normalized)
        if parsed.intent in _SCHEDULE_INTENTS:
            raise ToolError("Cannot schedule a scheduling command.")
        validate_intent_safety(parsed)
        return {
            "ok": True,
            "target_command": normalized,
            "intent": parsed.intent,
            "risk_level": parsed.risk_level.value,
            "requires_confirmation": parsed.requires_confirmation,
            "validated_at": validated_at,
        }
    except Exception as e:
        return {
            "ok": False,
            "target_command": normalized,
            "error": str(e),
            "validated_at": validated_at,
        }


def create_schedule(target_command: str, interval: str) -> dict[str, Any]:
    validation = inspect_schedule_target(target_command)
    if not validation.get("ok"):
        raise ToolError(f"Invalid scheduled command: {validation.get('error', 'unknown error')}")

    schedules = _load_schedules()
    schedule_id = uuid.uuid4().hex[:8]
    schedule = {
        "id": schedule_id,
        "target_command": validation["target_command"],
        "interval": interval,
        "created_at": datetime.now().isoformat(),
        "creation_validation": validation,
    }
    schedules[schedule_id] = schedule
    _save_schedules(schedules)
    return {"success": True, "schedule": schedule}


def list_schedules() -> dict[str, Any]:
    schedules = _load_schedules()
    enriched: list[dict[str, Any]] = []
    invalid_count = 0

    for schedule in schedules.values():
        entry = dict(schedule)
        runtime_validation = inspect_schedule_target(entry.get("target_command", ""))
        entry["runtime_validation"] = runtime_validation
        if not runtime_validation.get("ok"):
            invalid_count += 1
        enriched.append(entry)

    enriched.sort(key=lambda item: item.get("created_at", ""))
    return {
        "success": True,
        "schedules": enriched,
        "invalid_count": invalid_count,
    }


def prepare_schedule_for_execution(schedule_id: str) -> dict[str, Any]:
    schedules = _load_schedules()
    schedule = schedules.get(schedule_id)
    if schedule is None:
        return {"success": False, "error": f"Schedule '{schedule_id}' not found."}

    runtime_validation = inspect_schedule_target(schedule.get("target_command", ""))
    if not runtime_validation.get("ok"):
        return {
            "success": False,
            "schedule": schedule,
            "runtime_validation": runtime_validation,
            "error": runtime_validation.get("error", "Scheduled command is invalid."),
        }

    return {
        "success": True,
        "schedule": schedule,
        "runtime_validation": runtime_validation,
    }


def delete_schedule(schedule_id: str) -> dict[str, Any]:
    schedules = _load_schedules()
    if schedule_id in schedules:
        del schedules[schedule_id]
        _save_schedules(schedules)
        return {"success": True, "deleted_id": schedule_id}
    return {"success": False, "error": f"Schedule '{schedule_id}' not found."}
