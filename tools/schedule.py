import json
import os
import uuid
from datetime import datetime
from typing import Any

def _get_schedule_file() -> str:
    base = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    return os.path.join(base, "data", "schedules.json")

def _load_schedules() -> dict[str, dict[str, Any]]:
    path = _get_schedule_file()
    if not os.path.exists(path):
        return {}
    try:
        with open(path, "r") as f:
            return json.load(f)
    except Exception:
        return {}

def _save_schedules(schedules: dict[str, dict[str, Any]]) -> None:
    path = _get_schedule_file()
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(schedules, f, indent=2)

def create_schedule(target_command: str, interval: str) -> dict[str, Any]:
    schedules = _load_schedules()
    schedule_id = uuid.uuid4().hex[:8]
    schedule = {
        "id": schedule_id,
        "target_command": target_command,
        "interval": interval,
        "created_at": datetime.now().isoformat(),
    }
    schedules[schedule_id] = schedule
    _save_schedules(schedules)
    return {"success": True, "schedule": schedule}

def list_schedules() -> dict[str, Any]:
    schedules = _load_schedules()
    return {"success": True, "schedules": list(schedules.values())}

def delete_schedule(schedule_id: str) -> dict[str, Any]:
    schedules = _load_schedules()
    if schedule_id in schedules:
        del schedules[schedule_id]
        _save_schedules(schedules)
        return {"success": True, "deleted_id": schedule_id}
    return {"success": False, "error": f"Schedule '{schedule_id}' not found."}
