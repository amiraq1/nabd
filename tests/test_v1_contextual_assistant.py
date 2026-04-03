"""
Integration tests for Nabd v1.0 contextual assistant features.

Coverage:
  - Version banner reports v1.0
  - Context + Advisor module imports succeed
  - ContextMemory.resolve() integrates correctly in command flows
  - ValidationError from context resolution is handled gracefully in run_command
  - Context updated after successful command, not after failed/blocked
  - Advisor.suggest() never raises in integration scenarios
  - "explain last result" uses context memory, not _session dict
  - Help text mentions context shortcuts
  - Context is not set by AI meta-commands
  - Full context follow-up flow: show_files → list media in that folder
"""

import importlib
import sys
import types
import unittest
from io import StringIO
from unittest.mock import MagicMock, patch

import pytest


# ── Import sanity ─────────────────────────────────────────────────────────────

class TestImports:

    def test_context_memory_importable(self):
        from agent.context import ContextMemory
        assert ContextMemory is not None

    def test_advisor_importable(self):
        from agent.advisor import Advisor
        assert Advisor is not None

    def test_context_memory_instantiable(self):
        from agent.context import ContextMemory
        ctx = ContextMemory()
        assert ctx.last_intent is None
        assert ctx.last_source_path is None
        assert ctx.last_url is None
        assert ctx.last_command == ""
        assert ctx.last_result_msg == ""

    def test_advisor_instantiable(self):
        from agent.advisor import Advisor
        adv = Advisor()
        assert callable(adv.suggest)


# ── Version ───────────────────────────────────────────────────────────────────

class TestVersion:

    def test_banner_shows_v1(self):
        import main as m
        assert "v1.0" in m.BANNER

    def test_help_text_shows_v1(self):
        import main as m
        assert "v1.0" in m.HELP_TEXT

    def test_help_text_has_context_section(self):
        import main as m
        assert "CONTEXT SHORTCUTS" in m.HELP_TEXT or "Context" in m.HELP_TEXT

    def test_onboarding_mentions_context(self):
        import main as m
        text = m.ONBOARDING.lower()
        assert "that folder" in text or "context" in text

    def test_main_exports_ctx(self):
        import main as m
        assert hasattr(m, "_ctx")
        from agent.context import ContextMemory
        assert isinstance(m._ctx, ContextMemory)

    def test_main_exports_advisor(self):
        import main as m
        assert hasattr(m, "_advisor")
        from agent.advisor import Advisor
        assert isinstance(m._advisor, Advisor)

    def test_no_session_dict(self):
        import main as m
        assert not hasattr(m, "_session"), \
            "_session dict should be replaced by ContextMemory in v1.0"


# ── Context resolution integration ───────────────────────────────────────────

class TestContextResolutionInRunCommand:
    """
    Tests for run_command() context resolution flow.
    Uses monkeypatching to avoid actual filesystem/tool execution.
    """

    def _mock_successful_run(self, intent="show_files", source_path="/sdcard/Download"):
        """Create the mocked parsed intent, plan, and result for a successful command."""
        from agent.models import (
            ExecutionPlan, ExecutionResult, OperationStatus,
            ParsedIntent, RiskLevel,
        )
        parsed = ParsedIntent(
            intent=intent,
            source_path=source_path,
            raw_command="show files in /sdcard/Download",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
        )
        plan_obj = ExecutionPlan(
            intent=intent,
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            dry_run=False,
            preview_summary="List files in /sdcard/Download",
        )
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="Found 5 files.",
        )
        return parsed, plan_obj, result

    def test_context_updated_after_success(self):
        """After a successful show_files command, last_source_path is set."""
        import main as m
        m._ctx = __import__("agent.context", fromlist=["ContextMemory"]).ContextMemory()

        parsed, plan_obj, result = self._mock_successful_run()

        with (
            patch("main.parse_command", return_value=parsed),
            patch("main.validate_intent_safety"),
            patch("main.plan", return_value=plan_obj),
            patch("main.execute", return_value=result),
            patch("main.report_parsed_intent", return_value=""),
            patch("main.report_plan", return_value=""),
            patch("main.report_result", return_value=""),
            patch("main.log_operation"),
            patch("sys.stdout", new_callable=StringIO),
        ):
            m.run_command("show files in /sdcard/Download")

        assert m._ctx.last_source_path == "/sdcard/Download"
        assert m._ctx.last_command == "show files in /sdcard/Download"

    def test_context_not_updated_after_failure(self):
        """After a failed command, last_source_path must not be set."""
        import main as m
        from agent.models import ExecutionResult, OperationStatus
        m._ctx = __import__("agent.context", fromlist=["ContextMemory"]).ContextMemory()

        parsed, plan_obj, _ = self._mock_successful_run()
        fail_result = ExecutionResult(status=OperationStatus.FAILURE, message="Error.")

        with (
            patch("main.parse_command", return_value=parsed),
            patch("main.validate_intent_safety"),
            patch("main.plan", return_value=plan_obj),
            patch("main.execute", return_value=fail_result),
            patch("main.report_parsed_intent", return_value=""),
            patch("main.report_plan", return_value=""),
            patch("main.report_result", return_value=""),
            patch("main.log_operation"),
            patch("sys.stdout", new_callable=StringIO),
        ):
            m.run_command("show files in /sdcard/Download")

        assert m._ctx.last_source_path is None

    def test_context_not_updated_by_ai_intent(self):
        """AI meta-commands must not update context."""
        import main as m
        from agent.models import ExecutionResult, OperationStatus, ParsedIntent, RiskLevel
        m._ctx = __import__("agent.context", fromlist=["ContextMemory"]).ContextMemory()

        ai_parsed = ParsedIntent(
            intent="ai_suggest_command",
            raw_command="suggest command for backup",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
        )
        from agent.models import ExecutionPlan
        ai_plan = ExecutionPlan(
            intent="ai_suggest_command",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            dry_run=False,
        )
        ai_result = ExecutionResult(status=OperationStatus.SUCCESS, message="Try: back up ...")

        with (
            patch("main.parse_command", return_value=ai_parsed),
            patch("main.validate_intent_safety"),
            patch("main.plan", return_value=ai_plan),
            patch("main.execute", return_value=ai_result),
            patch("main.report_parsed_intent", return_value=""),
            patch("main.report_plan", return_value=""),
            patch("main.report_result", return_value=""),
            patch("main.log_operation"),
            patch("sys.stdout", new_callable=StringIO),
        ):
            m.run_command("suggest command for backup")

        assert m._ctx.last_intent is None

    def test_validation_error_from_context_resolution_handled_gracefully(self):
        """
        When context.resolve() raises ValidationError, run_command must
        print the error message and not crash.
        """
        import main as m
        from core.exceptions import ValidationError as VE
        m._ctx = __import__("agent.context", fromlist=["ContextMemory"]).ContextMemory()

        output = StringIO()
        with (
            patch.object(m._ctx, "resolve", side_effect=VE("No folder context available.")),
            patch("main.log_operation"),
            patch("sys.stdout", output),
        ):
            m.run_command("list media in that folder")

        printed = output.getvalue()
        assert "No folder context" in printed or "[!]" in printed

    def test_explain_last_result_uses_context_memory(self):
        """ai_explain_last_result must read from _ctx, not _session."""
        import main as m
        from agent.models import ExecutionResult, OperationStatus, ParsedIntent, RiskLevel, ExecutionPlan
        m._ctx = __import__("agent.context", fromlist=["ContextMemory"]).ContextMemory()
        m._ctx.last_command = "storage report /sdcard/Download"
        m._ctx.last_result_msg = "Total: 4.2 GB"

        explain_parsed = ParsedIntent(
            intent="ai_explain_last_result",
            raw_command="explain last result",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            options={},
        )
        explain_plan = ExecutionPlan(
            intent="ai_explain_last_result",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            dry_run=False,
        )
        explain_result = ExecutionResult(status=OperationStatus.SUCCESS, message="Explanation text.")

        captured_parsed: list = []

        def capture_parse(cmd):
            return explain_parsed

        def capture_validate(p):
            captured_parsed.append(p)

        with (
            patch("main.parse_command", side_effect=capture_parse),
            patch("main.validate_intent_safety", side_effect=capture_validate),
            patch("main.plan", return_value=explain_plan),
            patch("main.execute", return_value=explain_result),
            patch("main.report_parsed_intent", return_value=""),
            patch("main.report_plan", return_value=""),
            patch("main.report_result", return_value=""),
            patch("main.log_operation"),
            patch("sys.stdout", new_callable=StringIO),
        ):
            m.run_command("explain last result")

        # The parsed intent's options should have been injected with context memory values
        assert explain_parsed.options.get("last_command") == "storage report /sdcard/Download"
        assert explain_parsed.options.get("last_result") == "Total: 4.2 GB"


# ── Context follow-up resolution end-to-end ───────────────────────────────────

class TestContextFollowUpEndToEnd:
    """
    Tests that context.resolve() is called before parse_command() and
    that the resolved command reaches the parser correctly.
    """

    def test_that_folder_reaches_parser_as_resolved_path(self):
        """
        If _ctx has last_source_path, and user types 'list media in that folder',
        parse_command should receive 'list media in /sdcard/Download'.
        """
        import main as m
        from agent.models import ExecutionResult, OperationStatus, ParsedIntent, RiskLevel, ExecutionPlan
        m._ctx = __import__("agent.context", fromlist=["ContextMemory"]).ContextMemory()
        m._ctx.last_source_path = "/sdcard/Download"

        received_command: list = []

        def capture_parse(cmd):
            received_command.append(cmd)
            return ParsedIntent(
                intent="list_media",
                source_path="/sdcard/Download",
                raw_command=cmd,
                risk_level=RiskLevel.LOW,
                requires_confirmation=False,
            )

        plan_obj = ExecutionPlan(
            intent="list_media",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            dry_run=False,
        )
        result = ExecutionResult(status=OperationStatus.SUCCESS, message="Found 3 media files.")

        with (
            patch("main.parse_command", side_effect=capture_parse),
            patch("main.validate_intent_safety"),
            patch("main.plan", return_value=plan_obj),
            patch("main.execute", return_value=result),
            patch("main.report_parsed_intent", return_value=""),
            patch("main.report_plan", return_value=""),
            patch("main.report_result", return_value=""),
            patch("main.log_operation"),
            patch("sys.stdout", new_callable=StringIO),
        ):
            m.run_command("list media in that folder")

        assert len(received_command) == 1
        assert "/sdcard/Download" in received_command[0]
        assert "that folder" not in received_command[0]

    def test_that_url_reaches_parser_as_resolved_url(self):
        """
        If _ctx has last_url, and user types 'extract text from that url',
        parse_command should receive the actual URL.
        """
        import main as m
        from agent.models import ExecutionResult, OperationStatus, ParsedIntent, RiskLevel, ExecutionPlan
        m._ctx = __import__("agent.context", fromlist=["ContextMemory"]).ContextMemory()
        m._ctx.last_url = "https://example.com"

        received_command: list = []

        def capture_parse(cmd):
            received_command.append(cmd)
            return ParsedIntent(
                intent="browser_extract_text",
                url="https://example.com",
                raw_command=cmd,
                risk_level=RiskLevel.LOW,
                requires_confirmation=False,
            )

        plan_obj = ExecutionPlan(
            intent="browser_extract_text",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            dry_run=False,
        )
        result = ExecutionResult(status=OperationStatus.SUCCESS, message="Extracted text.")

        with (
            patch("main.parse_command", side_effect=capture_parse),
            patch("main.validate_intent_safety"),
            patch("main.plan", return_value=plan_obj),
            patch("main.execute", return_value=result),
            patch("main.report_parsed_intent", return_value=""),
            patch("main.report_plan", return_value=""),
            patch("main.report_result", return_value=""),
            patch("main.log_operation"),
            patch("sys.stdout", new_callable=StringIO),
        ):
            m.run_command("extract text from that url")

        assert len(received_command) == 1
        assert "https://example.com" in received_command[0]
        assert "that url" not in received_command[0]


# ── Advisor integration in run_command ───────────────────────────────────────

class TestAdvisorIntegration:

    def test_advisor_suggestions_printed_on_success(self):
        """When advisor returns suggestions, they appear in output."""
        import main as m
        from agent.models import ExecutionResult, OperationStatus, ParsedIntent, RiskLevel, ExecutionPlan
        m._ctx = __import__("agent.context", fromlist=["ContextMemory"]).ContextMemory()
        m._ctx.last_source_path = "/sdcard/Download"

        parsed = ParsedIntent(
            intent="show_files",
            source_path="/sdcard/Download",
            raw_command="show files in /sdcard/Download",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
        )
        plan_obj = ExecutionPlan(
            intent="show_files",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            dry_run=False,
        )
        result = ExecutionResult(status=OperationStatus.SUCCESS, message="Found 5 files.")

        output = StringIO()
        with (
            patch("main.parse_command", return_value=parsed),
            patch("main.validate_intent_safety"),
            patch("main.plan", return_value=plan_obj),
            patch("main.execute", return_value=result),
            patch("main.report_parsed_intent", return_value=""),
            patch("main.report_plan", return_value=""),
            patch("main.report_result", return_value=""),
            patch("main.log_operation"),
            patch("sys.stdout", output),
        ):
            m.run_command("show files in /sdcard/Download")

        printed = output.getvalue()
        # Advisor should have printed at least one suggestion
        assert "Suggestions" in printed or "list media" in printed or "find duplicates" in printed

    def test_advisor_never_crashes_run_command(self):
        """Even if advisor.suggest() is broken, run_command must not crash."""
        import main as m
        from agent.models import ExecutionResult, OperationStatus, ParsedIntent, RiskLevel, ExecutionPlan
        from agent.advisor import Advisor
        m._ctx = __import__("agent.context", fromlist=["ContextMemory"]).ContextMemory()

        parsed = ParsedIntent(
            intent="show_files",
            source_path="/sdcard/Download",
            raw_command="show files in /sdcard/Download",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
        )
        plan_obj = ExecutionPlan(
            intent="show_files",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            dry_run=False,
        )
        result = ExecutionResult(status=OperationStatus.SUCCESS, message="ok")

        broken_advisor = Advisor()
        # Patch internal method to raise
        broken_advisor._build_suggestions = MagicMock(side_effect=RuntimeError("advisor broken"))
        m._advisor = broken_advisor

        with (
            patch("main.parse_command", return_value=parsed),
            patch("main.validate_intent_safety"),
            patch("main.plan", return_value=plan_obj),
            patch("main.execute", return_value=result),
            patch("main.report_parsed_intent", return_value=""),
            patch("main.report_plan", return_value=""),
            patch("main.report_result", return_value=""),
            patch("main.log_operation"),
            patch("sys.stdout", new_callable=StringIO),
        ):
            # Must not raise
            m.run_command("show files in /sdcard/Download")

        # Restore advisor
        from agent.advisor import Advisor
        m._advisor = Advisor()
