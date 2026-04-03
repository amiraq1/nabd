import io
import unittest
from contextlib import redirect_stdout
from unittest.mock import MagicMock, call, patch

from agent.models import ExecutionResult, OperationStatus, ParsedIntent, RiskLevel


class TestMainDryRunPreview(unittest.TestCase):
    @patch("main.log_operation")
    @patch("main.get_history", return_value=[])
    @patch("main.format_advisory_suggestions", return_value="")
    @patch("main.generate_advisory_suggestions", return_value=[])
    @patch("main.prompt_confirmation", return_value=True)
    @patch("main.report_result")
    @patch("main.execute")
    @patch("main.plan")
    @patch("main.validate_intent_safety")
    @patch("main.report_plan", return_value="PLAN")
    @patch("main.report_parsed_intent", return_value="PARSED")
    @patch("main.parse_command")
    def test_modifying_command_runs_real_preview_before_confirmation(
        self,
        mock_parse_command,
        _mock_report_parsed,
        _mock_report_plan,
        _mock_validate,
        mock_plan,
        mock_execute,
        mock_report_result,
        mock_prompt,
        _mock_advisory,
        _mock_format_advisory,
        _mock_history,
        _mock_log,
    ):
        from main import run_command

        parsed = ParsedIntent(
            intent="organize_folder_by_type",
            source_path="/sdcard/Download",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
            raw_command="organize /sdcard/Download",
        )
        plan_obj = MagicMock(
            requires_confirmation=True,
            dry_run=True,
            preview_summary="Organize '/sdcard/Download' into category subfolders",
            risk_level=RiskLevel.MEDIUM,
        )
        preview_result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="[DRY RUN] preview",
            raw_results=[{"planned_moves": [{"source": "/sdcard/Download/a.jpg", "destination": "/sdcard/Download/images/a.jpg"}]}],
        )
        final_result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="done",
            raw_results=[{"moved": ["/sdcard/Download/images/a.jpg"]}],
        )

        mock_parse_command.return_value = parsed
        mock_plan.return_value = plan_obj
        mock_execute.side_effect = [preview_result, final_result]
        mock_report_result.side_effect = ["PREVIEW RESULT", "FINAL RESULT"]

        stream = io.StringIO()
        with redirect_stdout(stream):
            run_command("organize /sdcard/Download")

        output = stream.getvalue()
        self.assertIn("PREVIEW RESULT", output)
        self.assertIn("FINAL RESULT", output)
        self.assertLess(output.index("PREVIEW RESULT"), output.index("FINAL RESULT"))
        mock_execute.assert_has_calls([
            call(plan_obj, confirmed=False),
            call(plan_obj, confirmed=True),
        ])
        mock_report_result.assert_has_calls([
            call(preview_result, parsed.intent, confirmed=False),
            call(final_result, parsed.intent, True),
        ])
        mock_prompt.assert_called_once_with(plan_obj.preview_summary, "MEDIUM")

    @patch("main.log_operation")
    @patch("main.get_history", return_value=[])
    @patch("main.format_advisory_suggestions", return_value="")
    @patch("main.generate_advisory_suggestions", return_value=[])
    @patch("main.prompt_confirmation", return_value=False)
    @patch("main.report_result")
    @patch("main.execute")
    @patch("main.plan")
    @patch("main.validate_intent_safety")
    @patch("main.report_plan", return_value="PLAN")
    @patch("main.report_parsed_intent", return_value="PARSED")
    @patch("main.parse_command")
    def test_cancel_after_preview_does_not_execute_real_action(
        self,
        mock_parse_command,
        _mock_report_parsed,
        _mock_report_plan,
        _mock_validate,
        mock_plan,
        mock_execute,
        mock_report_result,
        _mock_prompt,
        _mock_advisory,
        _mock_format_advisory,
        _mock_history,
        mock_log,
    ):
        from main import run_command

        parsed = ParsedIntent(
            intent="safe_move_files",
            source_path="/sdcard/Download/file.txt",
            target_path="/sdcard/Documents",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
            raw_command="move /sdcard/Download/file.txt to /sdcard/Documents",
        )
        plan_obj = MagicMock(
            requires_confirmation=True,
            dry_run=True,
            preview_summary="Move '/sdcard/Download/file.txt' → '/sdcard/Documents'",
            risk_level=RiskLevel.MEDIUM,
        )
        preview_result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="[DRY RUN] preview",
            raw_results=[{"planned": {"source": "/sdcard/Download/file.txt", "destination": "/sdcard/Documents/file.txt"}}],
        )

        mock_parse_command.return_value = parsed
        mock_plan.return_value = plan_obj
        mock_execute.return_value = preview_result
        mock_report_result.return_value = "PREVIEW RESULT"

        stream = io.StringIO()
        with redirect_stdout(stream):
            run_command("move /sdcard/Download/file.txt to /sdcard/Documents")

        output = stream.getvalue()
        self.assertIn("PREVIEW RESULT", output)
        self.assertIn("Operation cancelled. No changes were made.", output)
        mock_execute.assert_called_once_with(plan_obj, confirmed=False)
        mock_log.assert_any_call(
            "move /sdcard/Download/file.txt to /sdcard/Documents",
            parsed.intent,
            plan_obj.preview_summary,
            "cancelled",
        )

    @patch("main.log_operation")
    @patch("main.get_history", return_value=[])
    @patch("main.format_advisory_suggestions", return_value="")
    @patch("main.generate_advisory_suggestions", return_value=[])
    @patch("main.prompt_confirmation", return_value=True)
    @patch("main.report_result", return_value="FINAL RESULT")
    @patch("main.execute")
    @patch("main.plan")
    @patch("main.validate_intent_safety")
    @patch("main.report_plan", return_value="PLAN")
    @patch("main.report_parsed_intent", return_value="PARSED")
    @patch("main.parse_command")
    def test_non_dry_run_confirmation_flow_still_executes_once(
        self,
        mock_parse_command,
        _mock_report_parsed,
        _mock_report_plan,
        _mock_validate,
        mock_plan,
        mock_execute,
        _mock_report_result,
        mock_prompt,
        _mock_advisory,
        _mock_format_advisory,
        _mock_history,
        _mock_log,
    ):
        from main import run_command

        parsed = ParsedIntent(
            intent="open_url",
            url="https://example.com",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
            raw_command="open https://example.com",
        )
        plan_obj = MagicMock(
            requires_confirmation=True,
            dry_run=False,
            preview_summary="Open URL in default browser: https://example.com",
            risk_level=RiskLevel.MEDIUM,
        )
        final_result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="done",
            raw_results=[{"url": "https://example.com", "success": True}],
        )

        mock_parse_command.return_value = parsed
        mock_plan.return_value = plan_obj
        mock_execute.return_value = final_result

        with redirect_stdout(io.StringIO()):
            run_command("open https://example.com")

        mock_execute.assert_called_once_with(plan_obj, confirmed=True)
        mock_prompt.assert_called_once_with(plan_obj.preview_summary, "MEDIUM")

    @patch("main.log_operation")
    @patch("main.get_history", return_value=[])
    @patch("main.format_advisory_suggestions", return_value="")
    @patch("main.generate_advisory_suggestions", return_value=[])
    @patch("main.prompt_confirmation", return_value=True)
    @patch("main.report_result")
    @patch("main.execute")
    @patch("main.plan")
    @patch("main.validate_intent_safety")
    @patch("main.report_plan", return_value="PLAN")
    @patch("main.report_parsed_intent", return_value="PARSED")
    @patch("main.parse_command")
    def test_failed_dry_run_preview_stops_before_confirmation(
        self,
        mock_parse_command,
        _mock_report_parsed,
        _mock_report_plan,
        _mock_validate,
        mock_plan,
        mock_execute,
        mock_report_result,
        mock_prompt,
        mock_advisory,
        mock_format_advisory,
        _mock_history,
        mock_log,
    ):
        from main import run_command

        parsed = ParsedIntent(
            intent="safe_move_files",
            source_path="/sdcard/Download/file.txt",
            target_path="/sdcard/Documents",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
            raw_command="move /sdcard/Download/file.txt to /sdcard/Documents",
        )
        plan_obj = MagicMock(
            requires_confirmation=True,
            dry_run=True,
            preview_summary="Move '/sdcard/Download/file.txt' → '/sdcard/Documents'",
            risk_level=RiskLevel.MEDIUM,
        )
        preview_result = ExecutionResult(
            status=OperationStatus.FAILURE,
            message="[DRY RUN] preview failed",
            raw_results=[],
            errors=["Source path does not exist"],
        )

        mock_parse_command.return_value = parsed
        mock_plan.return_value = plan_obj
        mock_execute.return_value = preview_result
        mock_report_result.return_value = "PREVIEW FAILURE"

        stream = io.StringIO()
        with redirect_stdout(stream):
            run_command("move /sdcard/Download/file.txt to /sdcard/Documents")

        output = stream.getvalue()
        self.assertIn("PREVIEW FAILURE", output)
        mock_execute.assert_called_once_with(plan_obj, confirmed=False)
        mock_prompt.assert_not_called()
        mock_advisory.assert_not_called()
        mock_format_advisory.assert_not_called()
        mock_log.assert_any_call(
            "move /sdcard/Download/file.txt to /sdcard/Documents",
            parsed.intent,
            plan_obj.preview_summary,
            "failure",
            [],
            "Source path does not exist",
        )


if __name__ == "__main__":
    unittest.main()
