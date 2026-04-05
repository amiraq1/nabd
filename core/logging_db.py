import json
import os
import sqlite3
from datetime import datetime
from typing import Any, Optional


def _get_db_path() -> str:
    try:
        from core.config import get_settings
        settings = get_settings()
        log_db = settings.get("log_db_path", "data/nabd_history.db")
    except Exception:
        log_db = "data/nabd_history.db"

    base = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    return os.path.join(base, log_db)


def _ensure_db(db_path: str) -> sqlite3.Connection:
    os.makedirs(os.path.dirname(db_path), exist_ok=True)
    conn = sqlite3.connect(db_path)
    conn.execute(
        """
        CREATE TABLE IF NOT EXISTS history (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            timestamp TEXT NOT NULL,
            command TEXT NOT NULL,
            intent TEXT,
            plan TEXT,
            status TEXT,
            affected_paths TEXT,
            error_details TEXT
        )
        """
    )
    conn.commit()
    return conn


def log_operation(
    command: str,
    intent: Optional[str],
    plan: Optional[str],
    status: str,
    affected_paths: Optional[list[str]] = None,
    error_details: Optional[str] = None,
) -> None:
    conn: sqlite3.Connection | None = None
    try:
        db_path = _get_db_path()
        conn = _ensure_db(db_path)
        conn.execute(
            """
            INSERT INTO history
              (timestamp, command, intent, plan, status, affected_paths, error_details)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (
                datetime.now().isoformat(),
                command,
                intent,
                plan,
                status,
                json.dumps(affected_paths or []),
                error_details,
            ),
        )
        conn.commit()
    except Exception:
        pass
    finally:
        if conn is not None:
            try:
                conn.close()
            except Exception:
                pass


def get_history(limit: int = 20) -> list[dict[str, Any]]:
    try:
        db_path = _get_db_path()
        if not os.path.isfile(db_path):
            return []
        with sqlite3.connect(db_path) as conn:
            conn.row_factory = sqlite3.Row
            rows = conn.execute(
                "SELECT * FROM history ORDER BY id DESC LIMIT ?", (limit,)
            ).fetchall()
            return [dict(r) for r in rows]
    except Exception:
        return []


def get_history_entry(entry_id: int) -> Optional[dict[str, Any]]:
    try:
        db_path = _get_db_path()
        if not os.path.isfile(db_path):
            return None
        with sqlite3.connect(db_path) as conn:
            conn.row_factory = sqlite3.Row
            row = conn.execute(
                "SELECT * FROM history WHERE id = ?",
                (entry_id,),
            ).fetchone()
            return dict(row) if row is not None else None
    except Exception:
        return None


def is_first_run() -> bool:
    """Return True if no operations have been logged yet (first launch)."""
    try:
        db_path = _get_db_path()
        if not os.path.isfile(db_path):
            return True
        with sqlite3.connect(db_path) as conn:
            count = conn.execute("SELECT COUNT(*) FROM history").fetchone()[0]
            return count == 0
    except Exception:
        return True
