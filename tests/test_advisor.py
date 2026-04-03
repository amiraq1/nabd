"""
Tests for agent/advisor.py — Advisor (Nabd v1.0).

Coverage:
  - suggest() returns advisory strings for known intents (success)
  - suggest() returns empty list for intents with no suggestions defined
  - suggest() returns empty list on failure (non-doctor) except env hints
  - suggest() includes env hint when error message contains known keyword
  - suggest() returns empty list for AI/skill intents
  - suggest() never produces mutating commands in suggestions
  - doctor suggestions: surfaces missing-tool hints from raw_results
  - suggest() never raises — absorbs internal errors
"""

import pytest

from agent.advisor import Advisor
from agent.context import ContextMemory
from agent.models import ExecutionResult, OperationStatus


# ── Helpers ───────────────────────────────────────────────────────────────────

def _ok(message: str = "Done.", raw_results: list | None = None) -> ExecutionResult:
    return ExecutionResult(
        status=OperationStatus.SUCCESS,
        message=message,
        raw_results=raw_results or [],
    )


def _fail(message: str = "Failed.") -> ExecutionResult:
    return ExecutionResult(
        status=OperationStatus.FAILURE,
        message=message,
    )


def _fail_errors(errors: list[str]) -> ExecutionResult:
    return ExecutionResult(
        status=OperationStatus.FAILURE,
        message="Failed.",
        errors=errors,
    )


def _ctx_with_path(path: str = "/sdcard/Download") -> ContextMemory:
    ctx = ContextMemory()
    ctx.update("show_files", "cmd", "ok", source_path=path, success=True)
    return ctx


def _ctx_with_url(url: str = "https://example.com") -> ContextMemory:
    ctx = ContextMemory()
    ctx.update("browser_extract_text", "cmd", "ok", url=url, success=True)
    return ctx


def _empty_ctx() -> ContextMemory:
    return ContextMemory()


advisor = Advisor()


# ── Per-intent suggestions (success + path context) ───────────────────────────

class TestAdvisorPathIntentSuggestions:

    def test_show_files_suggestions(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("show_files", _ok(), ctx)
        assert isinstance(hints, list)
        assert any("list media" in h for h in hints)
        assert any("find duplicates" in h for h in hints)
        assert any("list large files" in h for h in hints)
        assert any("storage report" in h for h in hints)

    def test_show_folders_suggestions(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("show_folders", _ok(), ctx)
        assert any("show files" in h for h in hints)
        assert any("storage report" in h for h in hints)

    def test_list_media_suggestions(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("list_media", _ok(), ctx)
        assert any("find duplicates" in h for h in hints)
        assert any("compress images" in h for h in hints)
        assert any("list large files" in h for h in hints)

    def test_find_duplicates_suggestions(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("find_duplicates", _ok(), ctx)
        assert any("list large files" in h for h in hints)
        assert any("storage report" in h for h in hints)

    def test_list_large_files_suggestions(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("list_large_files", _ok(), ctx)
        assert any("storage report" in h for h in hints)
        assert any("find duplicates" in h for h in hints)

    def test_storage_report_suggestions(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("storage_report", _ok(), ctx)
        assert any("show files" in h for h in hints)
        assert any("find duplicates" in h for h in hints)
        assert any("list large files" in h for h in hints)

    def test_backup_folder_suggestions(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("backup_folder", _ok(), ctx)
        assert any("storage report" in h for h in hints)
        assert any("show files" in h for h in hints)

    def test_organize_folder_suggestions(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("organize_folder_by_type", _ok(), ctx)
        assert any("show files" in h for h in hints)
        assert any("storage report" in h for h in hints)

    def test_compress_images_suggestions(self):
        ctx = _ctx_with_path("/sdcard/Pictures")
        hints = advisor.suggest("compress_images", _ok(), ctx)
        assert any("storage report" in h for h in hints)
        assert any("list media" in h for h in hints)


# ── Per-intent suggestions (success + URL context) ────────────────────────────

class TestAdvisorUrlIntentSuggestions:

    def test_browser_page_title_suggestions(self):
        ctx = _ctx_with_url("https://example.com")
        hints = advisor.suggest("browser_page_title", _ok(), ctx)
        assert any("extract text" in h for h in hints)
        assert any("list links" in h for h in hints)

    def test_browser_extract_text_suggestions(self):
        ctx = _ctx_with_url("https://example.com")
        hints = advisor.suggest("browser_extract_text", _ok(), ctx)
        assert any("list links" in h for h in hints)

    def test_browser_list_links_suggestions(self):
        ctx = _ctx_with_url("https://example.com")
        hints = advisor.suggest("browser_list_links", _ok(), ctx)
        assert any("extract text" in h for h in hints)

    def test_open_url_suggestions(self):
        ctx = _ctx_with_url("https://example.com")
        hints = advisor.suggest("open_url", _ok(), ctx)
        assert any("extract text" in h for h in hints)


# ── Suggestions include the actual path/url ───────────────────────────────────

class TestAdvisorContextSubstitution:

    def test_path_appears_in_suggestion(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("show_files", _ok(), ctx)
        assert any("/sdcard/Download" in h for h in hints)

    def test_url_appears_in_suggestion(self):
        ctx = _ctx_with_url("https://example.com")
        hints = advisor.suggest("browser_page_title", _ok(), ctx)
        assert any("https://example.com" in h for h in hints)

    def test_no_suggestions_when_path_missing_for_path_intent(self):
        ctx = _empty_ctx()
        hints = advisor.suggest("show_files", _ok(), ctx)
        # All path-dependent suggestions should be skipped
        assert not any("/sdcard" in h for h in hints)

    def test_no_suggestions_when_url_missing_for_url_intent(self):
        ctx = _empty_ctx()
        hints = advisor.suggest("browser_page_title", _ok(), ctx)
        assert not any("https://" in h for h in hints)


# ── Unknown/read-only intents with no suggestion map ─────────────────────────

class TestAdvisorNoSuggestions:

    def test_doctor_no_suggestions_on_clean_result(self):
        raw = {"overall": "ok", "checks": [
            {"name": "Python", "status": "ok"},
            {"name": "ffmpeg", "status": "ok"},
        ]}
        hints = advisor.suggest("doctor", _ok(raw_results=[raw]), _empty_ctx())
        assert hints == []

    def test_phone_status_battery_no_suggestions(self):
        ctx = _empty_ctx()
        hints = advisor.suggest("phone_status_battery", _ok(), ctx)
        assert hints == []

    def test_phone_status_network_no_suggestions(self):
        ctx = _empty_ctx()
        hints = advisor.suggest("phone_status_network", _ok(), ctx)
        assert hints == []

    def test_ai_suggest_command_no_suggestions(self):
        ctx = _empty_ctx()
        hints = advisor.suggest("ai_suggest_command", _ok(), ctx)
        assert hints == []

    def test_safe_rename_files_no_suggestions(self):
        ctx = _empty_ctx()
        hints = advisor.suggest("safe_rename_files", _ok(), ctx)
        assert hints == []

    def test_convert_video_no_suggestions(self):
        ctx = _empty_ctx()
        hints = advisor.suggest("convert_video_to_mp3", _ok(), ctx)
        assert hints == []


# ── Failure path — only env hints should appear ───────────────────────────────

class TestAdvisorFailure:

    def test_no_hints_on_plain_failure(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("list_media", _fail("Could not open directory."), ctx)
        assert hints == []

    def test_ffmpeg_env_hint_on_failure(self):
        result = _fail_errors(["ffmpeg not found"])
        ctx = _empty_ctx()
        hints = advisor.suggest("convert_video_to_mp3", result, ctx)
        assert any("ffmpeg" in h for h in hints)
        assert any("pkg install ffmpeg" in h for h in hints)

    def test_pillow_env_hint_on_failure(self):
        result = ExecutionResult(
            status=OperationStatus.FAILURE,
            message="pillow not installed",
            errors=[],
        )
        ctx = _empty_ctx()
        hints = advisor.suggest("compress_images", result, ctx)
        assert any("Pillow" in h for h in hints)
        assert any("pip install Pillow" in h for h in hints)

    def test_ssl_env_hint_on_failure(self):
        result = _fail_errors(["SSL certificate verify failed"])
        ctx = _empty_ctx()
        hints = advisor.suggest("browser_extract_text", result, ctx)
        assert any("SSL" in h or "TLS" in h for h in hints)

    def test_termux_api_env_hint_on_failure(self):
        result = _fail_errors(["termux-api not installed"])
        ctx = _empty_ctx()
        hints = advisor.suggest("phone_status_battery", result, ctx)
        assert any("termux-api" in h for h in hints)


# ── Doctor intent — surfaces actionable hints from raw_results ────────────────

class TestAdvisorDoctor:

    def test_doctor_surfaces_missing_ffmpeg(self):
        raw = {"overall": "issues", "checks": [
            {"name": "ffmpeg", "status": "missing"},
            {"name": "Python", "status": "ok"},
        ]}
        hints = advisor.suggest("doctor", _ok(raw_results=[raw]), _empty_ctx())
        assert any("ffmpeg" in h for h in hints)
        assert any("pkg install ffmpeg" in h for h in hints)

    def test_doctor_surfaces_missing_pillow(self):
        raw = {"overall": "issues", "checks": [
            {"name": "Pillow", "status": "missing"},
        ]}
        hints = advisor.suggest("doctor", _ok(raw_results=[raw]), _empty_ctx())
        assert any("Pillow" in h for h in hints)
        assert any("pip install Pillow" in h for h in hints)

    def test_doctor_surfaces_missing_termux_api(self):
        raw = {"overall": "issues", "checks": [
            {"name": "termux-api", "status": "missing"},
        ]}
        hints = advisor.suggest("doctor", _ok(raw_results=[raw]), _empty_ctx())
        assert any("termux" in h.lower() for h in hints)

    def test_doctor_surfaces_storage_issue(self):
        raw = {"overall": "issues", "checks": [
            {"name": "sdcard storage", "status": "error"},
        ]}
        hints = advisor.suggest("doctor", _ok(raw_results=[raw]), _empty_ctx())
        assert any("storage" in h.lower() or "permission" in h.lower() for h in hints)

    def test_doctor_empty_raw_results_no_crash(self):
        hints = advisor.suggest("doctor", _ok(raw_results=[]), _empty_ctx())
        assert isinstance(hints, list)

    def test_doctor_missing_raw_results_no_crash(self):
        result = ExecutionResult(status=OperationStatus.SUCCESS, message="ok")
        hints = advisor.suggest("doctor", result, _empty_ctx())
        assert isinstance(hints, list)


# ── Suggestions are advisory text only — no unsafe shell commands ─────────────
#
# The advisor may suggest any valid Nabd command (including confirmation-required
# ones like 'compress images' and 'organize') because:
#   - suggestions are text only — the user must type them
#   - every Nabd command passes through safety validation when run
#   - confirmation-required commands still prompt before making changes
# What is prohibited: shell commands, direct file deletion, and executor bypasses.

class TestAdvisorSafetyCheck:

    SHELL_VERBS = {"rm ", "rmdir ", "sudo ", "chmod ", "chown ", "dd ", "mkfs "}

    def _assert_no_shell_commands(self, hints: list[str]) -> None:
        for hint in hints:
            lower = hint.lower()
            for verb in self.SHELL_VERBS:
                assert verb not in lower, f"Shell command found in suggestion: {hint!r}"

    def test_show_files_no_shell_suggestions(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("show_files", _ok(), ctx)
        self._assert_no_shell_commands(hints)

    def test_list_media_no_shell_suggestions(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("list_media", _ok(), ctx)
        self._assert_no_shell_commands(hints)

    def test_find_duplicates_no_shell_suggestions(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("find_duplicates", _ok(), ctx)
        self._assert_no_shell_commands(hints)

    def test_storage_report_no_shell_suggestions(self):
        ctx = _ctx_with_path("/sdcard/Download")
        hints = advisor.suggest("storage_report", _ok(), ctx)
        self._assert_no_shell_commands(hints)

    def test_all_suggestions_are_strings(self):
        """Every suggestion must be a string — no objects or None."""
        ctx = _ctx_with_path("/sdcard/Download")
        for intent in ("show_files", "list_media", "find_duplicates",
                       "list_large_files", "storage_report", "backup_folder"):
            hints = advisor.suggest(intent, _ok(), ctx)
            for h in hints:
                assert isinstance(h, str), f"Non-string suggestion for {intent}: {h!r}"


# ── Error absorption — suggest() never raises ─────────────────────────────────

class TestAdvisorErrorAbsorption:

    def test_broken_result_no_crash(self):
        ctx = _empty_ctx()
        hints = advisor.suggest("show_files", None, ctx)  # type: ignore[arg-type]
        assert isinstance(hints, list)

    def test_broken_ctx_no_crash(self):
        hints = advisor.suggest("show_files", _ok(), None)  # type: ignore[arg-type]
        assert isinstance(hints, list)

    def test_unknown_intent_no_crash(self):
        ctx = _empty_ctx()
        hints = advisor.suggest("completely_unknown_intent", _ok(), ctx)
        assert isinstance(hints, list)
