"""
Tests for agent/context.py — ContextMemory (Nabd v1.0).

Coverage:
  - update(): path/url stored only for correct intent types and on success
  - update(): AI/skill intents are skipped entirely
  - update(): failed executions do not set path/url context
  - resolve(): explicit path reference ("that folder", "same path")
  - resolve(): explicit URL reference ("that url", "same link")
  - resolve(): "it" resolves to path when only path in context
  - resolve(): "it" resolves to url when only url in context
  - resolve(): "it" raises ValidationError when both path and url present
  - resolve(): "it" raises ValidationError when no context at all
  - resolve(): commands with no reference pass through unchanged
  - resolve(): mutating verbs raise ValidationError for any reference
  - resolve(): "explain that" / "that result" are passed through (AI handles)
  - resolve(): missing context raises ValidationError with instructive message
  - _revalidate_path(): clears stale path and raises ValidationError
"""

import pytest

from agent.context import ContextMemory
from core.exceptions import ValidationError


# ── Helpers ───────────────────────────────────────────────────────────────────

def _make_ctx(
    *,
    intent: str = "show_files",
    command: str = "show files in /sdcard/Download",
    result_msg: str = "Found 10 files.",
    source_path: str | None = "/sdcard/Download",
    url: str | None = None,
    success: bool = True,
) -> ContextMemory:
    ctx = ContextMemory()
    ctx.update(
        intent=intent,
        command=command,
        result_msg=result_msg,
        source_path=source_path,
        url=url,
        success=success,
    )
    return ctx


def _make_url_ctx(url: str = "https://example.com") -> ContextMemory:
    ctx = ContextMemory()
    ctx.update(
        intent="browser_extract_text",
        command=f"extract text from {url}",
        result_msg="Extracted 200 words.",
        url=url,
        success=True,
    )
    return ctx


# ── update() tests ────────────────────────────────────────────────────────────

class TestContextMemoryUpdate:

    def test_path_stored_for_path_intent_on_success(self):
        ctx = _make_ctx(intent="show_files", source_path="/sdcard/Download", success=True)
        assert ctx.last_source_path == "/sdcard/Download"

    def test_path_not_stored_on_failure(self):
        ctx = _make_ctx(intent="show_files", source_path="/sdcard/Download", success=False)
        assert ctx.last_source_path is None

    def test_path_not_stored_for_non_path_intent(self):
        ctx = _make_ctx(intent="phone_status_battery", source_path="/sdcard/Download", success=True)
        assert ctx.last_source_path is None

    def test_url_stored_for_url_intent_on_success(self):
        ctx = _make_url_ctx()
        assert ctx.last_url == "https://example.com"

    def test_url_not_stored_on_failure(self):
        ctx = ContextMemory()
        ctx.update(
            intent="browser_page_title",
            command="show page title from https://x.com",
            result_msg="SSL error.",
            url="https://x.com",
            success=False,
        )
        assert ctx.last_url is None

    def test_url_not_stored_for_non_url_intent(self):
        ctx = ContextMemory()
        ctx.update(
            intent="show_files",
            command="show files in /sdcard/Download",
            result_msg="ok",
            url="https://example.com",
            success=True,
        )
        assert ctx.last_url is None

    def test_last_command_and_result_always_updated(self):
        ctx = _make_ctx(command="doctor", result_msg="All checks passed.")
        assert ctx.last_command == "doctor"
        assert ctx.last_result_msg == "All checks passed."

    def test_ai_intents_do_not_update_context(self):
        ctx = ContextMemory()
        ctx.update(
            intent="ai_suggest_command",
            command="suggest command for backup",
            result_msg="Try: back up /sdcard/Documents to /sdcard/Backup",
            success=True,
        )
        assert ctx.last_intent is None
        assert ctx.last_command == ""
        assert ctx.last_result_msg == ""
        assert ctx.last_source_path is None

    def test_show_skills_does_not_update_context(self):
        ctx = ContextMemory()
        ctx.update(intent="show_skills", command="show skills", result_msg="ok", success=True)
        assert ctx.last_intent is None

    def test_skill_info_does_not_update_context(self):
        ctx = ContextMemory()
        ctx.update(intent="skill_info", command="skill info ai_assist", result_msg="ok", success=True)
        assert ctx.last_intent is None

    def test_multiple_updates_last_one_wins_for_path(self):
        ctx = ContextMemory()
        ctx.update("show_files", "show files in /sdcard/A", "ok", source_path="/sdcard/A", success=True)
        ctx.update("show_files", "show files in /sdcard/B", "ok", source_path="/sdcard/B", success=True)
        assert ctx.last_source_path == "/sdcard/B"

    def test_failed_update_does_not_overwrite_previous_good_context(self):
        ctx = ContextMemory()
        ctx.update("show_files", "show files in /sdcard/A", "ok", source_path="/sdcard/A", success=True)
        # Simulate a failed second command — should not clear existing path context
        ctx.update("show_files", "show files in /sdcard/MISSING", "error", source_path="/sdcard/MISSING", success=False)
        assert ctx.last_source_path == "/sdcard/A"

    def test_path_context_intents_covered(self):
        for intent in ("show_files", "show_folders", "list_media", "find_duplicates",
                       "list_large_files", "storage_report", "compress_images",
                       "organize_folder_by_type", "backup_folder"):
            ctx = ContextMemory()
            ctx.update(intent, "cmd", "ok", source_path="/sdcard/Test", success=True)
            assert ctx.last_source_path == "/sdcard/Test", f"intent {intent} should set path context"

    def test_url_context_intents_covered(self):
        for intent in ("browser_page_title", "browser_extract_text", "browser_list_links",
                       "open_url", "browser_search"):
            ctx = ContextMemory()
            ctx.update(intent, "cmd", "ok", url="https://test.com", success=True)
            assert ctx.last_url == "https://test.com", f"intent {intent} should set url context"


# ── resolve() — pass-through cases ───────────────────────────────────────────

class TestResolvePassthrough:

    def test_no_reference_phrase_returns_original(self):
        ctx = _make_ctx()
        assert ctx.resolve("show files in /sdcard/Download") == "show files in /sdcard/Download"

    def test_explain_that_passes_through(self):
        ctx = _make_ctx()
        result = ctx.resolve("explain that")
        assert result == "explain that"

    def test_explain_last_result_passes_through(self):
        ctx = _make_ctx()
        result = ctx.resolve("explain last result")
        assert result == "explain last result"

    def test_that_result_passes_through(self):
        ctx = _make_ctx()
        result = ctx.resolve("what does that result mean")
        assert result == "what does that result mean"

    def test_that_error_passes_through(self):
        ctx = _make_ctx()
        result = ctx.resolve("explain that error")
        assert result == "explain that error"

    def test_unrelated_command_unmodified(self):
        ctx = _make_ctx()
        assert ctx.resolve("doctor") == "doctor"

    def test_history_command_unmodified(self):
        ctx = _make_ctx()
        assert ctx.resolve("history") == "history"


# ── resolve() — explicit path reference ──────────────────────────────────────

class TestResolveExplicitPath:

    def test_that_folder_resolved(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        result = ctx.resolve("list media in that folder")
        assert result == "list media in /sdcard/Download"

    def test_that_directory_resolved(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        result = ctx.resolve("find duplicates that directory")
        assert result == "find duplicates /sdcard/Download"

    def test_that_path_resolved(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        result = ctx.resolve("storage report that path")
        assert result == "storage report /sdcard/Download"

    def test_that_dir_resolved(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        result = ctx.resolve("list large files that dir")
        assert result == "list large files /sdcard/Download"

    def test_same_folder_resolved(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        result = ctx.resolve("show files in same folder")
        assert result == "show files in /sdcard/Download"

    def test_same_path_resolved(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        result = ctx.resolve("list media in same path")
        assert result == "list media in /sdcard/Download"

    def test_case_insensitive_that_folder(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        result = ctx.resolve("list media in That Folder")
        assert "/sdcard/Download" in result

    def test_path_ref_no_context_raises(self):
        ctx = ContextMemory()
        with pytest.raises(ValidationError, match="No folder context"):
            ctx.resolve("list media in that folder")

    def test_path_ref_after_failed_command_raises(self):
        ctx = ContextMemory()
        ctx.update("show_files", "show files in /sdcard/Missing", "error",
                   source_path="/sdcard/Missing", success=False)
        with pytest.raises(ValidationError, match="No folder context"):
            ctx.resolve("list media in that folder")


# ── resolve() — explicit URL reference ───────────────────────────────────────

class TestResolveExplicitUrl:

    def test_that_url_resolved(self):
        ctx = _make_url_ctx("https://example.com")
        result = ctx.resolve("extract text from that url")
        assert result == "extract text from https://example.com"

    def test_that_link_resolved(self):
        ctx = _make_url_ctx("https://example.com")
        result = ctx.resolve("list links from that link")
        assert result == "list links from https://example.com"

    def test_that_site_resolved(self):
        ctx = _make_url_ctx("https://example.com")
        result = ctx.resolve("show page title from that site")
        assert result == "show page title from https://example.com"

    def test_that_page_resolved(self):
        ctx = _make_url_ctx("https://example.com")
        result = ctx.resolve("extract text from that page")
        assert result == "extract text from https://example.com"

    def test_same_url_resolved(self):
        ctx = _make_url_ctx("https://example.com")
        result = ctx.resolve("list links from same url")
        assert result == "list links from https://example.com"

    def test_url_ref_no_context_raises(self):
        ctx = ContextMemory()
        with pytest.raises(ValidationError, match="No URL context"):
            ctx.resolve("extract text from that url")


# ── resolve() — "it" reference ───────────────────────────────────────────────

class TestResolveItRef:

    def test_it_resolves_to_path_when_only_path(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        result = ctx.resolve("list media in it")
        assert result == "list media in /sdcard/Download"

    def test_it_resolves_to_url_when_only_url(self):
        ctx = _make_url_ctx("https://example.com")
        result = ctx.resolve("extract text from it")
        assert result == "extract text from https://example.com"

    def test_it_raises_when_both_path_and_url(self):
        ctx = ContextMemory()
        ctx.update("show_files", "cmd", "ok", source_path="/sdcard/Download", success=True)
        ctx.update("browser_extract_text", "cmd2", "ok", url="https://example.com", success=True)
        with pytest.raises(ValidationError, match="ambiguous"):
            ctx.resolve("show page title from it")

    def test_it_raises_when_no_context(self):
        ctx = ContextMemory()
        with pytest.raises(ValidationError, match="no context"):
            ctx.resolve("list media in it")

    def test_it_not_triggered_inside_words(self):
        """'it' inside a word like 'submit' or 'edit' should not trigger resolution."""
        ctx = _make_ctx(source_path="/sdcard/Download")
        # "edit" contains "it" — regex uses word boundary so should not match
        result = ctx.resolve("edit the file")
        assert result == "edit the file"


# ── resolve() — mutating verb guard ──────────────────────────────────────────

class TestResolveMutatingGuard:

    def test_move_it_raises(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        with pytest.raises(ValidationError, match="explicitly"):
            ctx.resolve("move it to /sdcard/Documents")

    def test_backup_that_folder_raises(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        with pytest.raises(ValidationError, match="explicitly"):
            ctx.resolve("back up that folder to /sdcard/Backup")

    def test_rename_that_path_raises(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        with pytest.raises(ValidationError, match="explicitly"):
            ctx.resolve("rename files in that path prefix bak_")

    def test_compress_it_raises(self):
        ctx = _make_ctx(source_path="/sdcard/Pictures")
        with pytest.raises(ValidationError, match="explicitly"):
            ctx.resolve("compress it")

    def test_organize_that_folder_raises(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        with pytest.raises(ValidationError, match="explicitly"):
            ctx.resolve("organize that folder")

    def test_convert_it_raises(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        with pytest.raises(ValidationError, match="explicitly"):
            ctx.resolve("convert it to mp3")

    def test_non_mutating_read_allowed(self):
        ctx = _make_ctx(source_path="/sdcard/Download")
        result = ctx.resolve("list media in that folder")
        assert "/sdcard/Download" in result


# ── _revalidate_path() ────────────────────────────────────────────────────────

class TestRevalidatePath:

    def test_valid_path_does_not_raise(self, tmp_path, monkeypatch):
        test_dir = tmp_path / "subdir"
        test_dir.mkdir()

        # Patch get_allowed_roots at the location where paths.py imported it
        monkeypatch.setattr("core.paths.get_allowed_roots", lambda: [str(tmp_path)])

        ctx = ContextMemory()
        ctx.update("show_files", "cmd", "ok", source_path=str(test_dir), success=True)
        # Should not raise
        resolved = ctx.resolve("list media in that folder")
        assert str(test_dir) in resolved

    def test_disallowed_path_clears_context_and_raises(self, monkeypatch):
        # Patch at the location where paths.py imported it
        monkeypatch.setattr("core.paths.get_allowed_roots", lambda: ["/sdcard"])
        ctx = ContextMemory()
        # Manually set a path that won't pass allowed-roots validation
        ctx.last_source_path = "/etc/passwd"
        with pytest.raises(ValidationError, match="no longer accessible"):
            ctx.resolve("list media in that folder")
        # Context must be cleared after failed revalidation
        assert ctx.last_source_path is None
