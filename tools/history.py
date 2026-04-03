from typing import Any

from core.logging_db import get_history


def search_history(term: str) -> dict[str, Any]:
    results = get_history(limit=100)
    normalized = term.lower().strip()
    matches = [
        entry for entry in results
        if normalized in (entry.get("command") or "").lower()
        or normalized in (entry.get("intent") or "").lower()
        or normalized in (entry.get("status") or "").lower()
    ]
    return {
        "term": term,
        "count": len(matches),
        "entries": matches,
    }


def history_by_intent(intent_name: str) -> dict[str, Any]:
    results = get_history(limit=200)
    matches = [
        entry for entry in results
        if (entry.get("intent") or "") == intent_name
    ]
    return {
        "intent": intent_name,
        "count": len(matches),
        "entries": matches,
    }


def show_history_entry(entry_id: int) -> dict[str, Any]:
    results = get_history(limit=500)
    for entry in results:
        if entry.get("id") == entry_id:
            return {"entry": entry}
    return {"entry": None, "message": f"No history entry found with id {entry_id}"}
