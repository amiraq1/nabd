from unittest.mock import Mock, patch

from core.logging_db import get_history_entry, log_operation


def test_log_operation_closes_connection_on_insert_error():
    conn = Mock()
    conn.execute.side_effect = RuntimeError("boom")

    with patch("core.logging_db._get_db_path", return_value="/tmp/nabd-test.db"), patch(
        "core.logging_db._ensure_db",
        return_value=conn,
    ):
        log_operation("doctor", "doctor", "Run diagnostics", "failure")

    conn.close.assert_called_once()


def test_get_history_entry_returns_row_from_db(tmp_path):
    import sqlite3

    db_path = tmp_path / "history.db"
    with sqlite3.connect(db_path) as conn:
        conn.execute(
            """
            CREATE TABLE history (
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
        conn.execute(
            """
            INSERT INTO history
              (timestamp, command, intent, plan, status, affected_paths, error_details)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            ("2026-04-04T00:00:00", "doctor", "doctor", "Run diagnostics", "success", "[]", None),
        )
        conn.commit()

    with patch("core.logging_db._get_db_path", return_value=str(db_path)):
        entry = get_history_entry(1)

    assert entry is not None
    assert entry["command"] == "doctor"
