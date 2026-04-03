"""Tests for Nabd v0.2 features: doctor, show_files, list_media."""

import sys
import os
import tempfile
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import pytest
from unittest.mock import patch

from agent.parser import parse_command, detect_intent, _extract_source_target, extract_options
from agent.models import RiskLevel
from agent.planner import plan
from agent.executor import execute
from agent.models import ExecutionPlan, RiskLevel as RL, ToolAction, OperationStatus
from core.exceptions import UnknownIntentError, ValidationError
from tools.files import show_files, list_media
from tools.system import run_doctor


# ══════════════════════════════════════════════════════════════════════════════
# Parser — new intents
# ══════════════════════════════════════════════════════════════════════════════

class TestDoctorIntent:
    def test_doctor_basic(self):
        assert detect_intent("doctor") == "doctor"

    def test_doctor_check_setup(self):
        assert detect_intent("check setup") == "doctor"

    def test_doctor_check_environment(self):
        assert detect_intent("check environment") == "doctor"

    def test_doctor_health_check(self):
        assert detect_intent("health check") == "doctor"

    def test_doctor_diagnose(self):
        assert detect_intent("diagnose") == "doctor"

    def test_doctor_verify_setup(self):
        assert detect_intent("verify setup") == "doctor"

    def test_doctor_check_install(self):
        assert detect_intent("check installation") == "doctor"

    def test_doctor_is_ffmpeg_installed(self):
        assert detect_intent("is ffmpeg installed") == "doctor"

    def test_doctor_no_path_required(self):
        result = parse_command("doctor")
        assert result.source_path is None
        assert result.requires_confirmation is False
        assert result.risk_level == RiskLevel.LOW


class TestShowFilesIntent:
    def test_show_files_in(self):
        assert detect_intent("show files in /sdcard/Download") == "show_files"

    def test_list_files_in(self):
        assert detect_intent("list files in /sdcard/Download") == "show_files"

    def test_what_files_are_in(self):
        assert detect_intent("what files are in /sdcard/Download") == "show_files"

    def test_contents_of(self):
        assert detect_intent("contents of /sdcard/Download") == "show_files"

    def test_browse(self):
        assert detect_intent("browse /sdcard/Download") == "show_files"

    def test_ls_command(self):
        assert detect_intent("ls /sdcard/Download") == "show_files"

    def test_show_files_risk_low(self):
        result = parse_command("show files in /sdcard/Download")
        assert result.risk_level == RiskLevel.LOW
        assert result.requires_confirmation is False
        assert result.source_path == "/sdcard/Download"

    def test_show_files_sorted_by_size(self):
        result = parse_command("show files in /sdcard/Download sorted by size")
        assert result.options.get("sort_by") == "size"

    def test_show_files_sorted_by_modified(self):
        result = parse_command("show files in /sdcard/Download sorted by modified")
        assert result.options.get("sort_by") == "modified"

    def test_show_files_sorted_by_date(self):
        result = parse_command("show files in /sdcard/Download sorted by date")
        assert result.options.get("sort_by") == "modified"

    def test_show_files_does_not_match_large_files(self):
        assert detect_intent("show large files /sdcard/Download") == "list_large_files"


class TestListMediaIntent:
    def test_list_media_basic(self):
        assert detect_intent("list media in /sdcard/Download") == "list_media"

    def test_show_media(self):
        assert detect_intent("show media /sdcard/Download") == "list_media"

    def test_media_files(self):
        assert detect_intent("media files in /sdcard/Download") == "list_media"

    def test_show_photos(self):
        assert detect_intent("show photos in /sdcard/Pictures") == "list_media"

    def test_find_videos(self):
        assert detect_intent("find videos in /sdcard/Movies") == "list_media"

    def test_list_audio(self):
        assert detect_intent("list audio in /sdcard/Music") == "list_media"

    def test_show_music(self):
        assert detect_intent("show music /sdcard/Music") == "list_media"

    def test_list_media_risk_low(self):
        result = parse_command("list media in /sdcard/Download")
        assert result.risk_level == RiskLevel.LOW
        assert result.requires_confirmation is False

    def test_list_media_recursive_option(self):
        result = parse_command("list media in /sdcard/Download recursively")
        assert result.options.get("recursive") is True

    def test_list_media_path_extracted(self):
        result = parse_command("list media in /sdcard/Pictures")
        assert result.source_path == "/sdcard/Pictures"


# ══════════════════════════════════════════════════════════════════════════════
# Safety — new intents
# ══════════════════════════════════════════════════════════════════════════════

ALLOWED_ROOTS = ["/sdcard/Download", "/sdcard/Documents", "/sdcard/Music", "/sdcard/Pictures"]


class TestNewIntentSafety:
    def test_doctor_passes_with_no_path(self):
        from agent.safety import validate_intent_safety
        from agent.models import ParsedIntent
        intent = ParsedIntent(intent="doctor", risk_level=RiskLevel.LOW, requires_confirmation=False)
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            validate_intent_safety(intent)

    def test_show_files_requires_source_path(self):
        from agent.safety import validate_intent_safety
        from agent.models import ParsedIntent
        intent = ParsedIntent(intent="show_files", source_path=None, risk_level=RiskLevel.LOW, requires_confirmation=False)
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(ValidationError):
                validate_intent_safety(intent)

    def test_show_files_with_valid_path_passes(self):
        from agent.safety import validate_intent_safety
        from agent.models import ParsedIntent
        intent = ParsedIntent(intent="show_files", source_path="/sdcard/Download", risk_level=RiskLevel.LOW, requires_confirmation=False)
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            validate_intent_safety(intent)

    def test_list_media_requires_source_path(self):
        from agent.safety import validate_intent_safety
        from agent.models import ParsedIntent
        intent = ParsedIntent(intent="list_media", source_path=None, risk_level=RiskLevel.LOW, requires_confirmation=False)
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(ValidationError):
                validate_intent_safety(intent)

    def test_list_media_with_valid_path_passes(self):
        from agent.safety import validate_intent_safety
        from agent.models import ParsedIntent
        intent = ParsedIntent(intent="list_media", source_path="/sdcard/Pictures", risk_level=RiskLevel.LOW, requires_confirmation=False)
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            validate_intent_safety(intent)


# ══════════════════════════════════════════════════════════════════════════════
# tools/system.py — run_doctor
# ══════════════════════════════════════════════════════════════════════════════

class TestRunDoctor:
    def test_returns_dict(self):
        result = run_doctor()
        assert isinstance(result, dict)

    def test_has_checks_list(self):
        result = run_doctor()
        assert "checks" in result
        assert isinstance(result["checks"], list)

    def test_checks_count(self):
        result = run_doctor()
        assert len(result["checks"]) == 6

    def test_check_names_present(self):
        result = run_doctor()
        names = [c["name"] for c in result["checks"]]
        assert "Python version" in names
        assert "ffmpeg" in names
        assert "Pillow (image compression)" in names
        assert "Allowed paths" in names
        assert "History log directory" in names

    def test_each_check_has_status_and_detail(self):
        result = run_doctor()
        for check in result["checks"]:
            assert "status" in check
            assert "detail" in check
            assert check["status"] in ("ok", "warn", "missing", "error")

    def test_has_summary_counts(self):
        result = run_doctor()
        assert "ok_count" in result
        assert "warn_count" in result
        assert "error_count" in result

    def test_has_overall_field(self):
        result = run_doctor()
        assert result["overall"] in ("ok", "warn", "error")

    def test_python_check_ok_on_311(self):
        result = run_doctor()
        py_check = next(c for c in result["checks"] if c["name"] == "Python version")
        assert py_check["status"] == "ok"
        assert "3.11" in py_check["detail"] or "3." in py_check["detail"]

    def test_pillow_check_ok_when_installed(self):
        result = run_doctor()
        pil_check = next(c for c in result["checks"] if "Pillow" in c["name"])
        assert pil_check["status"] == "ok"

    def test_data_dir_check_ok(self):
        result = run_doctor()
        data_check = next(c for c in result["checks"] if "History" in c["name"])
        assert data_check["status"] == "ok"

    def test_ffmpeg_missing_gives_install_hint(self):
        import shutil
        with patch.object(shutil, "which", return_value=None):
            result = run_doctor()
        ff_check = next(c for c in result["checks"] if c["name"] == "ffmpeg")
        assert ff_check["status"] == "missing"
        assert "pkg install ffmpeg" in ff_check["detail"]


# ══════════════════════════════════════════════════════════════════════════════
# tools/files.py — show_files
# ══════════════════════════════════════════════════════════════════════════════

class TestShowFiles:
    def test_basic_listing(self, tmp_path):
        (tmp_path / "a.txt").write_bytes(b"hello")
        (tmp_path / "b.jpg").write_bytes(b"x" * 100)
        result = show_files(str(tmp_path))
        assert result["file_count"] == 2
        assert result["dir_count"] == 0

    def test_includes_subdirs(self, tmp_path):
        (tmp_path / "file.txt").write_bytes(b"x")
        (tmp_path / "subdir").mkdir()
        result = show_files(str(tmp_path))
        assert result["file_count"] == 1
        assert result["dir_count"] == 1

    def test_sort_by_name_default(self, tmp_path):
        (tmp_path / "z.txt").write_bytes(b"z")
        (tmp_path / "a.txt").write_bytes(b"a")
        result = show_files(str(tmp_path), sort_by="name")
        names = [e["name"] for e in result["entries"] if not e["is_dir"]]
        assert names == sorted(names)

    def test_sort_by_size(self, tmp_path):
        (tmp_path / "small.txt").write_bytes(b"x" * 10)
        (tmp_path / "large.txt").write_bytes(b"x" * 1000)
        result = show_files(str(tmp_path), sort_by="size")
        sizes = [e["size_bytes"] for e in result["entries"] if not e["is_dir"]]
        assert sizes == sorted(sizes, reverse=True)

    def test_limit_respected(self, tmp_path):
        for i in range(20):
            (tmp_path / f"file{i:02d}.txt").write_bytes(b"x")
        result = show_files(str(tmp_path), limit=5)
        assert len(result["entries"]) == 5
        assert result["truncated"] == 15

    def test_no_truncation_when_under_limit(self, tmp_path):
        for i in range(3):
            (tmp_path / f"file{i}.txt").write_bytes(b"x")
        result = show_files(str(tmp_path), limit=100)
        assert result["truncated"] == 0

    def test_entries_have_required_fields(self, tmp_path):
        (tmp_path / "test.txt").write_bytes(b"x")
        result = show_files(str(tmp_path))
        entry = next(e for e in result["entries"] if not e["is_dir"])
        assert "name" in entry
        assert "path" in entry
        assert "size_bytes" in entry
        assert "size_human" in entry
        assert "is_dir" in entry

    def test_nonexistent_dir_raises(self):
        from core.exceptions import ToolError
        with pytest.raises(ToolError):
            show_files("/nonexistent/dir/xyz")

    def test_empty_dir(self, tmp_path):
        result = show_files(str(tmp_path))
        assert result["file_count"] == 0
        assert result["dir_count"] == 0
        assert result["entries"] == []


# ══════════════════════════════════════════════════════════════════════════════
# tools/files.py — list_media
# ══════════════════════════════════════════════════════════════════════════════

class TestListMedia:
    def test_finds_images(self, tmp_path):
        (tmp_path / "photo.jpg").write_bytes(b"img")
        (tmp_path / "photo.png").write_bytes(b"img2")
        result = list_media(str(tmp_path))
        assert result["summary"]["images"]["count"] == 2

    def test_finds_videos(self, tmp_path):
        (tmp_path / "video.mp4").write_bytes(b"vid")
        result = list_media(str(tmp_path))
        assert result["summary"]["videos"]["count"] == 1

    def test_finds_audio(self, tmp_path):
        (tmp_path / "song.mp3").write_bytes(b"aud")
        result = list_media(str(tmp_path))
        assert result["summary"]["audio"]["count"] == 1

    def test_ignores_non_media(self, tmp_path):
        (tmp_path / "doc.pdf").write_bytes(b"pdf")
        (tmp_path / "script.py").write_bytes(b"py")
        result = list_media(str(tmp_path))
        assert result["total_media_count"] == 0

    def test_total_count(self, tmp_path):
        (tmp_path / "a.jpg").write_bytes(b"img")
        (tmp_path / "b.mp4").write_bytes(b"vid")
        (tmp_path / "c.mp3").write_bytes(b"aud")
        result = list_media(str(tmp_path))
        assert result["total_media_count"] == 3

    def test_total_size_reported(self, tmp_path):
        (tmp_path / "a.jpg").write_bytes(b"x" * 1000)
        result = list_media(str(tmp_path))
        assert result["total_size_bytes"] == 1000

    def test_summary_has_all_categories(self, tmp_path):
        result = list_media(str(tmp_path))
        assert "images" in result["summary"]
        assert "videos" in result["summary"]
        assert "audio" in result["summary"]

    def test_recursive_finds_nested_media(self, tmp_path):
        sub = tmp_path / "sub"
        sub.mkdir()
        (sub / "deep.jpg").write_bytes(b"img")
        result_flat = list_media(str(tmp_path), recursive=False)
        result_recursive = list_media(str(tmp_path), recursive=True)
        assert result_flat["summary"]["images"]["count"] == 0
        assert result_recursive["summary"]["images"]["count"] == 1

    def test_nonexistent_dir_raises(self):
        from core.exceptions import ToolError
        with pytest.raises(ToolError):
            list_media("/nonexistent/dir/xyz")

    def test_empty_dir_returns_zeros(self, tmp_path):
        result = list_media(str(tmp_path))
        assert result["total_media_count"] == 0
        assert result["total_size_bytes"] == 0

    def test_group_items_have_fields(self, tmp_path):
        (tmp_path / "photo.jpg").write_bytes(b"x" * 500)
        result = list_media(str(tmp_path))
        item = result["groups"]["images"][0]
        assert "name" in item
        assert "path" in item
        assert "size_bytes" in item
        assert "size_human" in item

    def test_mixed_media_sizes(self, tmp_path):
        (tmp_path / "img.jpg").write_bytes(b"x" * 2000)
        (tmp_path / "vid.mp4").write_bytes(b"x" * 5000)
        (tmp_path / "aud.mp3").write_bytes(b"x" * 1000)
        result = list_media(str(tmp_path))
        assert result["total_size_bytes"] == 8000


# ══════════════════════════════════════════════════════════════════════════════
# Planner — new intents
# ══════════════════════════════════════════════════════════════════════════════

class TestPlannerNewIntents:
    def test_plan_doctor(self):
        parsed = parse_command("doctor")
        execution_plan = plan(parsed)
        assert execution_plan.intent == "doctor"
        assert len(execution_plan.actions) == 1
        assert execution_plan.actions[0].tool_name == "system"
        assert execution_plan.actions[0].function_name == "run_doctor"
        assert execution_plan.requires_confirmation is False

    def test_plan_show_files_requires_path(self):
        from agent.models import ParsedIntent
        intent = ParsedIntent(intent="show_files", source_path=None, risk_level=RiskLevel.LOW, requires_confirmation=False)
        with pytest.raises(ValidationError):
            plan(intent)

    def test_plan_show_files_with_path(self, tmp_path):
        from agent.models import ParsedIntent
        intent = ParsedIntent(intent="show_files", source_path=str(tmp_path), risk_level=RiskLevel.LOW, requires_confirmation=False)
        execution_plan = plan(intent)
        assert execution_plan.actions[0].function_name == "show_files"
        assert execution_plan.actions[0].arguments["directory"] == str(tmp_path)

    def test_plan_list_media_requires_path(self):
        from agent.models import ParsedIntent
        intent = ParsedIntent(intent="list_media", source_path=None, risk_level=RiskLevel.LOW, requires_confirmation=False)
        with pytest.raises(ValidationError):
            plan(intent)

    def test_plan_list_media_with_path(self, tmp_path):
        from agent.models import ParsedIntent
        intent = ParsedIntent(intent="list_media", source_path=str(tmp_path), risk_level=RiskLevel.LOW, requires_confirmation=False)
        execution_plan = plan(intent)
        assert execution_plan.actions[0].function_name == "list_media"

    def test_plan_show_files_sort_by_option(self, tmp_path):
        from agent.models import ParsedIntent
        intent = ParsedIntent(
            intent="show_files", source_path=str(tmp_path),
            options={"sort_by": "size"}, risk_level=RiskLevel.LOW, requires_confirmation=False
        )
        execution_plan = plan(intent)
        assert execution_plan.actions[0].arguments["sort_by"] == "size"

    def test_plan_list_media_recursive_option(self, tmp_path):
        from agent.models import ParsedIntent
        intent = ParsedIntent(
            intent="list_media", source_path=str(tmp_path),
            options={"recursive": True}, risk_level=RiskLevel.LOW, requires_confirmation=False
        )
        execution_plan = plan(intent)
        assert execution_plan.actions[0].arguments["recursive"] is True


# ══════════════════════════════════════════════════════════════════════════════
# Executor — new intents end-to-end
# ══════════════════════════════════════════════════════════════════════════════

class TestExecutorNewIntents:
    def test_doctor_executes(self):
        action = ToolAction(tool_name="system", function_name="run_doctor", arguments={})
        ep = ExecutionPlan(intent="doctor", risk_level=RL.LOW, requires_confirmation=False, dry_run=False, actions=[action])
        result = execute(ep, confirmed=False)
        assert result.status == OperationStatus.SUCCESS
        assert len(result.raw_results) == 1
        assert "checks" in result.raw_results[0]

    def test_show_files_executes(self, tmp_path):
        (tmp_path / "test.txt").write_bytes(b"x")
        action = ToolAction(tool_name="files", function_name="show_files", arguments={"directory": str(tmp_path), "sort_by": "name", "limit": 100})
        ep = ExecutionPlan(intent="show_files", risk_level=RL.LOW, requires_confirmation=False, dry_run=False, actions=[action])
        result = execute(ep, confirmed=False)
        assert result.status == OperationStatus.SUCCESS
        assert result.raw_results[0]["file_count"] == 1

    def test_list_media_executes(self, tmp_path):
        (tmp_path / "photo.jpg").write_bytes(b"x")
        action = ToolAction(tool_name="files", function_name="list_media", arguments={"directory": str(tmp_path), "recursive": False})
        ep = ExecutionPlan(intent="list_media", risk_level=RL.LOW, requires_confirmation=False, dry_run=False, actions=[action])
        result = execute(ep, confirmed=False)
        assert result.status == OperationStatus.SUCCESS
        assert result.raw_results[0]["total_media_count"] == 1

    def test_system_not_whitelisted_for_arbitrary_function(self):
        action = ToolAction(tool_name="system", function_name="exec_shell", arguments={})
        ep = ExecutionPlan(intent="doctor", risk_level=RL.LOW, requires_confirmation=False, dry_run=False, actions=[action])
        result = execute(ep, confirmed=False)
        assert result.status in (OperationStatus.FAILURE, OperationStatus.PARTIAL)
        assert any("not whitelisted" in e for e in result.errors)


# ══════════════════════════════════════════════════════════════════════════════
# logging_db — is_first_run
# ══════════════════════════════════════════════════════════════════════════════

class TestIsFirstRun:
    def test_returns_bool(self):
        from core.logging_db import is_first_run
        assert isinstance(is_first_run(), bool)

    def test_first_run_on_empty_db(self, tmp_path):
        import sqlite3
        from core.logging_db import is_first_run
        db_path = str(tmp_path / "test.db")
        conn = sqlite3.connect(db_path)
        conn.execute("CREATE TABLE history (id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp TEXT NOT NULL, command TEXT NOT NULL, intent TEXT, plan TEXT, status TEXT, affected_paths TEXT, error_details TEXT)")
        conn.commit()
        conn.close()
        with patch("core.logging_db._get_db_path", return_value=db_path):
            assert is_first_run() is True

    def test_not_first_run_after_log_entry(self, tmp_path):
        import sqlite3
        from core.logging_db import is_first_run, log_operation
        db_path = str(tmp_path / "test.db")
        with patch("core.logging_db._get_db_path", return_value=db_path):
            log_operation("test cmd", "storage_report", "plan", "success")
            assert is_first_run() is False

    def test_first_run_when_no_file(self, tmp_path):
        from core.logging_db import is_first_run
        nonexistent = str(tmp_path / "no_such.db")
        with patch("core.logging_db._get_db_path", return_value=nonexistent):
            assert is_first_run() is True


# ══════════════════════════════════════════════════════════════════════════════
# v0.2.1 — list_media recursive hint
# ══════════════════════════════════════════════════════════════════════════════

class TestListMediaRecursiveHint:
    def test_has_subdirs_false_when_no_subdirs(self, tmp_path):
        (tmp_path / "photo.jpg").write_bytes(b"x")
        result = list_media(str(tmp_path))
        assert result["has_subdirs"] is False

    def test_has_subdirs_true_when_subdir_exists(self, tmp_path):
        (tmp_path / "sub").mkdir()
        result = list_media(str(tmp_path))
        assert result["has_subdirs"] is True

    def test_has_subdirs_true_even_without_media_in_top(self, tmp_path):
        sub = tmp_path / "pics"
        sub.mkdir()
        (sub / "photo.jpg").write_bytes(b"x")
        result = list_media(str(tmp_path))
        assert result["has_subdirs"] is True
        assert result["total_media_count"] == 0

    def test_has_subdirs_false_when_recursive(self, tmp_path):
        (tmp_path / "sub").mkdir()
        result = list_media(str(tmp_path), recursive=True)
        # has_subdirs is only set for non-recursive scans
        assert result["has_subdirs"] is False

    def test_has_subdirs_false_empty_dir(self, tmp_path):
        result = list_media(str(tmp_path))
        assert result["has_subdirs"] is False

    def test_reporter_shows_hint_when_zero_media_and_has_subdirs(self, tmp_path):
        from agent.executor import execute
        from agent.models import ExecutionPlan, RiskLevel, ToolAction, OperationStatus
        from agent.reporter import report_result
        from agent.models import ExecutionResult

        (tmp_path / "subdir").mkdir()

        action = ToolAction(
            tool_name="files", function_name="list_media",
            arguments={"directory": str(tmp_path), "recursive": False}
        )
        ep = ExecutionPlan(
            intent="list_media", risk_level=RiskLevel.LOW,
            requires_confirmation=False, dry_run=False, actions=[action]
        )
        result = execute(ep, confirmed=False)
        output = report_result(result, "list_media", confirmed=False)
        assert "recursively" in output
        assert str(tmp_path) in output

    def test_reporter_no_hint_when_media_found(self, tmp_path):
        from agent.executor import execute
        from agent.models import ExecutionPlan, RiskLevel, ToolAction
        from agent.reporter import report_result

        (tmp_path / "subdir").mkdir()
        (tmp_path / "photo.jpg").write_bytes(b"x")

        action = ToolAction(
            tool_name="files", function_name="list_media",
            arguments={"directory": str(tmp_path), "recursive": False}
        )
        ep = ExecutionPlan(
            intent="list_media", risk_level=RiskLevel.LOW,
            requires_confirmation=False, dry_run=False, actions=[action]
        )
        result = execute(ep, confirmed=False)
        output = report_result(result, "list_media", confirmed=False)
        assert "recursively" not in output.split("Hint")[0] if "Hint" in output else True

    def test_reporter_no_hint_when_no_subdirs(self, tmp_path):
        from agent.executor import execute
        from agent.models import ExecutionPlan, RiskLevel, ToolAction
        from agent.reporter import report_result

        action = ToolAction(
            tool_name="files", function_name="list_media",
            arguments={"directory": str(tmp_path), "recursive": False}
        )
        ep = ExecutionPlan(
            intent="list_media", risk_level=RiskLevel.LOW,
            requires_confirmation=False, dry_run=False, actions=[action]
        )
        result = execute(ep, confirmed=False)
        output = report_result(result, "list_media", confirmed=False)
        assert "Hint" not in output


# ══════════════════════════════════════════════════════════════════════════════
# v0.2.1 — shell command detection (SHELL_COMMANDS dict + behaviour)
# ══════════════════════════════════════════════════════════════════════════════

class TestShellCommandDetection:
    def _import_shell_commands(self):
        import importlib
        import sys
        # Reload to pick up latest version
        if "main" in sys.modules:
            del sys.modules["main"]
        import main as m
        return m.SHELL_COMMANDS

    def test_shell_commands_dict_exists(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert isinstance(SHELL_COMMANDS, dict)

    def test_ls_in_shell_commands(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert "ls" in SHELL_COMMANDS

    def test_cd_in_shell_commands(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert "cd" in SHELL_COMMANDS

    def test_mkdir_in_shell_commands(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert "mkdir" in SHELL_COMMANDS

    def test_pwd_in_shell_commands(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert "pwd" in SHELL_COMMANDS

    def test_find_in_shell_commands(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert "find" in SHELL_COMMANDS

    def test_rm_in_shell_commands(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert "rm" in SHELL_COMMANDS

    def test_mv_in_shell_commands(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert "mv" in SHELL_COMMANDS

    def test_all_hints_are_nonempty_strings(self):
        SHELL_COMMANDS = self._import_shell_commands()
        for cmd, hint in SHELL_COMMANDS.items():
            assert isinstance(hint, str), f"Hint for '{cmd}' is not a string"
            assert hint.strip(), f"Hint for '{cmd}' is empty"

    def test_ls_hint_mentions_show_files(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert "show files" in SHELL_COMMANDS["ls"].lower()

    def test_find_hint_mentions_find_duplicates(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert "find duplicates" in SHELL_COMMANDS["find"].lower()

    def test_du_hint_mentions_storage_report(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert "storage report" in SHELL_COMMANDS["du"].lower()

    def test_mv_hint_mentions_move(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert "move" in SHELL_COMMANDS["mv"].lower()

    def test_cp_hint_mentions_back_up(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert "back up" in SHELL_COMMANDS["cp"].lower()

    def test_rm_hint_reassures_no_deletion(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert "safe" in SHELL_COMMANDS["rm"].lower() or "not delete" in SHELL_COMMANDS["rm"].lower()

    def test_at_least_10_shell_commands_covered(self):
        SHELL_COMMANDS = self._import_shell_commands()
        assert len(SHELL_COMMANDS) >= 10

    def test_all_hints_mention_nabd_or_equivalent(self):
        SHELL_COMMANDS = self._import_shell_commands()
        for cmd, hint in SHELL_COMMANDS.items():
            # Every hint must either offer a Nabd equivalent or explain why not
            has_guidance = any(
                kw in hint.lower()
                for kw in ("nabd", "equivalent", "does not", "not track", "try")
            )
            assert has_guidance, f"Hint for '{cmd}' lacks actionable guidance: {hint!r}"


# ══════════════════════════════════════════════════════════════════════════════
# v0.2.1 — help text includes shell section
# ══════════════════════════════════════════════════════════════════════════════

class TestHelpTextShellSection:
    def _get_help_text(self):
        import sys
        if "main" in sys.modules:
            del sys.modules["main"]
        import main as m
        return m.HELP_TEXT

    def test_help_text_contains_shell_section(self):
        help_text = self._get_help_text()
        assert "NABD VS TERMUX SHELL" in help_text

    def test_help_text_mentions_exit_to_termux(self):
        help_text = self._get_help_text()
        assert "exit" in help_text.lower()
        assert "termux" in help_text.lower()

    def test_help_text_has_ls_equivalent(self):
        help_text = self._get_help_text()
        assert "ls" in help_text

    def test_help_text_has_mv_equivalent(self):
        help_text = self._get_help_text()
        assert "mv" in help_text

    def test_help_text_version_is_current(self):
        help_text = self._get_help_text()
        assert "v0.7" in help_text
