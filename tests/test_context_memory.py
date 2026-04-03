import io
import unittest
from contextlib import redirect_stdout
from unittest.mock import MagicMock, patch

from agent.context import (
    apply_session_context_to_intent,
    new_session_context,
    resolve_command_with_context,
    update_session_context,
)
from agent.models import ExecutionResult, OperationStatus, ParsedIntent, RiskLevel
from core.exceptions import ValidationError


class TestContextResolution(unittest.TestCase):
    def test_resolves_that_folder_from_last_folder_context(self):
        session = new_session_context()
        session["last_folder"] = "/sdcard/Download"

        with patch("agent.context.validate_path_safety", return_value="/sdcard/Download"):
            resolved = resolve_command_with_context("show files in that folder", session)

        self.assertEqual(resolved, "show files in /sdcard/Download")

    def test_resolves_it_for_safe_url_followup(self):
        session = new_session_context()
        session["last_url"] = "https://example.com"

        resolved = resolve_command_with_context("list links from it", session)

        self.assertEqual(resolved, "list links from https://example.com")

    def test_missing_folder_context_asks_for_clarification(self):
        with self.assertRaises(ValidationError):
            resolve_command_with_context("show files in that folder", new_session_context())

    def test_ambiguous_it_for_move_is_rejected(self):
        session = new_session_context()
        session["last_folder"] = "/sdcard/Download"

        with self.assertRaises(ValidationError):
            resolve_command_with_context("move it to /sdcard/Documents", session)

    def test_rejects_unsafe_stored_url_during_resolution(self):
        session = new_session_context()
        session["last_url"] = "javascript:alert(1)"

        with patch("agent.context.validate_url_safety", side_effect=ValidationError("blocked")):
            with self.assertRaises(ValidationError):
                resolve_command_with_context("list links from it", session)


class TestExplainLastResultContext(unittest.TestCase):
    def test_explain_last_result_requires_prior_result(self):
        parsed = ParsedIntent(
            intent="ai_explain_last_result",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="explain that result",
        )

        with self.assertRaises(ValidationError):
            apply_session_context_to_intent(parsed, new_session_context())

    def test_explain_last_result_injects_session_values(self):
        parsed = ParsedIntent(
            intent="ai_explain_last_result",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="explain that result",
        )
        session = new_session_context()
        session["last_command"] = "storage report /sdcard/Download"
        session["last_result"] = "Operation completed successfully."

        parsed = apply_session_context_to_intent(parsed, session)

        self.assertEqual(parsed.options["last_command"], "storage report /sdcard/Download")
        self.assertEqual(parsed.options["last_result"], "Operation completed successfully.")


class TestSessionContextUpdates(unittest.TestCase):
    def test_update_session_context_tracks_safe_folder(self):
        session = new_session_context()
        parsed = ParsedIntent(
            intent="show_files",
            source_path="/sdcard/Download",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="show files in /sdcard/Download",
        )
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{"directory": "/sdcard/Download"}],
        )

        with patch("agent.context.validate_path_safety", return_value="/sdcard/Download"):
            update_session_context(session, parsed.raw_command, parsed, result)

        self.assertEqual(session["last_folder"], "/sdcard/Download")
        self.assertEqual(session["last_command"], parsed.raw_command)
        self.assertTrue(session["recent_context"])

    def test_failed_result_does_not_update_folder_context(self):
        session = new_session_context()
        parsed = ParsedIntent(
            intent="show_files",
            source_path="/sdcard/Download",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="show files in /sdcard/Download",
        )
        result = ExecutionResult(
            status=OperationStatus.FAILURE,
            message="failed",
            raw_results=[],
            errors=["Cannot read directory"],
        )

        with patch("agent.context.validate_path_safety", return_value="/sdcard/Download"):
            update_session_context(session, parsed.raw_command, parsed, result)

        self.assertEqual(session["last_command"], parsed.raw_command)
        self.assertEqual(session["last_result"], "failed")
        self.assertEqual(session["last_folder"], "")
        self.assertEqual(session["recent_context"], [])

    def test_move_command_does_not_create_ambiguous_folder_context(self):
        session = new_session_context()
        parsed = ParsedIntent(
            intent="safe_move_files",
            source_path="/sdcard/Download/file.txt",
            target_path="/sdcard/Documents",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
            raw_command="move /sdcard/Download/file.txt to /sdcard/Documents",
        )
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{"target_directory": "/sdcard/Documents"}],
        )

        with patch("agent.context.validate_path_safety", return_value="/sdcard/Documents"):
            update_session_context(session, parsed.raw_command, parsed, result)

        self.assertEqual(session["last_folder"], "")

    def test_rejects_unsafe_stored_folder_during_resolution(self):
        session = new_session_context()
        session["last_folder"] = "/etc"

        with patch("agent.context.validate_path_safety", side_effect=ValidationError("blocked")):
            with self.assertRaises(ValidationError):
                resolve_command_with_context("show files in that folder", session)


class TestMainContextIntegration(unittest.TestCase):
    @patch("main.log_operation")
    @patch("main.get_history", return_value=[])
    @patch("main.format_advisory_suggestions", return_value="")
    @patch("main.generate_advisory_suggestions", return_value=[])
    @patch("main.report_result", return_value="RESULT")
    @patch("main.execute")
    @patch("main.plan")
    @patch("main.validate_intent_safety")
    @patch("main.report_plan", return_value="PLAN")
    @patch("main.report_parsed_intent", return_value="PARSED")
    @patch("main.parse_command")
    def test_run_command_resolves_context_before_parse(
        self,
        mock_parse_command,
        _mock_report_parsed,
        _mock_report_plan,
        _mock_validate,
        mock_plan,
        mock_execute,
        _mock_report_result,
        _mock_advisory,
        _mock_format_advisory,
        _mock_history,
        _mock_log,
    ):
        import main

        main._session = new_session_context()
        main._session["last_folder"] = "/sdcard/Download"

        parsed = ParsedIntent(
            intent="show_files",
            source_path="/sdcard/Download",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="show files in /sdcard/Download",
        )
        plan_obj = MagicMock(requires_confirmation=False, preview_summary="preview")
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{"directory": "/sdcard/Download", "entries": []}],
        )

        mock_parse_command.return_value = parsed
        mock_plan.return_value = plan_obj
        mock_execute.return_value = result

        with patch("main.resolve_command_with_context", return_value="show files in /sdcard/Download"):
            with redirect_stdout(io.StringIO()):
                main.run_command("show files in that folder")

        mock_parse_command.assert_called_once_with("show files in /sdcard/Download")

    def test_run_command_reports_clarification_for_ambiguous_context(self):
        import main

        main._session = new_session_context()
        main._session["last_folder"] = "/sdcard/Download"

        stream = io.StringIO()
        with redirect_stdout(stream):
            main.run_command("move it to /sdcard/Documents")

        output = stream.getvalue()
        self.assertIn("Context reference 'it' is ambiguous here", output)


if __name__ == "__main__":
    unittest.main()
