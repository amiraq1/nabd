import io
import unittest
from contextlib import redirect_stdout
from unittest.mock import ANY, MagicMock, patch

from agent.advisor import format_advisory_suggestions, generate_advisory_suggestions
from agent.models import ExecutionResult, OperationStatus, ParsedIntent, RiskLevel


class TestAdvisorSuggestions(unittest.TestCase):
    def test_browser_tls_failure_suggests_safe_fallbacks(self):
        intent = ParsedIntent(
            intent="browser_extract_text",
            url="https://example.com/page",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="extract text from https://example.com/page",
        )
        result = ExecutionResult(
            status=OperationStatus.FAILURE,
            message="failed",
            raw_results=[{
                "url": "https://example.com/page",
                "success": False,
                "error_type": "tls",
                "error": "SSL error",
            }],
            errors=["SSL error"],
        )

        suggestions = generate_advisory_suggestions(intent, result, recent_history=[])

        self.assertTrue(any("open https://example.com/page" in s for s in suggestions))
        self.assertTrue(any("search for example.com" in s for s in suggestions))
        self.assertTrue(any("doctor" in s for s in suggestions))

    def test_list_media_empty_with_subdirs_suggests_recursive_scan(self):
        intent = ParsedIntent(
            intent="list_media",
            source_path="/sdcard/Download",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="list media in /sdcard/Download",
        )
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{
                "directory": "/sdcard/Download",
                "total_media_count": 0,
                "has_subdirs": True,
                "recursive": False,
                "summary": {"images": {"count": 0}},
            }],
        )

        suggestions = generate_advisory_suggestions(intent, result, recent_history=[])

        self.assertIn("Scan subfolders too: list media in /sdcard/Download recursively", suggestions)

    def test_doctor_does_not_retry_failed_mutating_history_command(self):
        intent = ParsedIntent(
            intent="doctor",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="doctor",
        )
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{
                "checks": [
                    {"name": "ffmpeg", "status": "missing", "detail": "missing"},
                    {"name": "Allowed paths", "status": "ok", "detail": "ok"},
                    {"name": "HTTPS / CA certificates", "status": "ok", "detail": "ok"},
                ],
            }],
        )
        history = [
            {
                "command": "convert /sdcard/Movies/film.mp4 to mp3",
                "intent": "convert_video_to_mp3",
                "status": "failure",
            }
        ]

        with patch("agent.advisor.get_allowed_roots", return_value=["/sdcard/Movies"]):
            suggestions = generate_advisory_suggestions(intent, result, recent_history=history)

        self.assertFalse(any("retry: convert /sdcard/Movies/film.mp4 to mp3" in s for s in suggestions))
        self.assertTrue(any("pkg install ffmpeg" in s for s in suggestions))

    def test_doctor_does_not_retry_failed_backup_history_command(self):
        intent = ParsedIntent(
            intent="doctor",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="doctor",
        )
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{
                "checks": [
                    {"name": "Allowed paths", "status": "error", "detail": "bad"},
                ],
            }],
        )
        history = [
            {
                "command": "back up /sdcard/Documents to /sdcard/Backup",
                "intent": "backup_folder",
                "status": "failure",
            }
        ]

        with patch(
            "agent.advisor.get_allowed_roots",
            return_value=["/sdcard/Documents", "/sdcard/Backup"],
        ):
            suggestions = generate_advisory_suggestions(intent, result, recent_history=history)

        self.assertFalse(any("back up /sdcard/Documents to /sdcard/Backup" in s for s in suggestions))
        self.assertTrue(any("rerun: doctor" in s for s in suggestions))

    def test_doctor_does_not_repeat_unsafe_history_command(self):
        intent = ParsedIntent(
            intent="doctor",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="doctor",
        )
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{
                "checks": [
                    {"name": "ffmpeg", "status": "missing", "detail": "missing"},
                ],
            }],
        )
        history = [
            {
                "command": "convert /etc/shadow to mp3",
                "intent": "convert_video_to_mp3",
                "status": "failure",
            }
        ]

        with patch("agent.advisor.get_allowed_roots", return_value=["/sdcard/Movies"]):
            suggestions = generate_advisory_suggestions(intent, result, recent_history=history)

        self.assertFalse(any("/etc/shadow" in s for s in suggestions))

    def test_doctor_does_not_repeat_malformed_url_history_command(self):
        intent = ParsedIntent(
            intent="doctor",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="doctor",
        )
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{
                "checks": [
                    {"name": "HTTPS / CA certificates", "status": "error", "detail": "missing"},
                ],
            }],
        )
        history = [
            {
                "command": "extract text from https://example.com ../../etc",
                "intent": "browser_extract_text",
                "status": "failure",
            }
        ]

        suggestions = generate_advisory_suggestions(intent, result, recent_history=history)

        self.assertFalse(any("retry: extract text from https://example.com ../../etc" in s for s in suggestions))
        self.assertFalse(any("../../etc" in s for s in suggestions))

    def test_suggestions_are_advisory_strings_only(self):
        intent = ParsedIntent(
            intent="storage_report",
            source_path="/sdcard/Download",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="storage report /sdcard/Download",
        )
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{
                "directory": "/sdcard/Download",
                "file_count": 4,
            }],
        )

        suggestions = generate_advisory_suggestions(intent, result, recent_history=[])

        self.assertTrue(suggestions)
        self.assertTrue(all(isinstance(item, str) for item in suggestions))
        self.assertFalse(any("ToolAction" in item for item in suggestions))

    def test_browser_extract_text_success_suggests_list_links(self):
        intent = ParsedIntent(
            intent="browser_extract_text",
            url="https://example.com",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="extract text from https://example.com",
        )
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{
                "url": "https://example.com",
                "success": True,
                "text": "hello",
                "char_count": 5,
            }],
        )

        suggestions = generate_advisory_suggestions(intent, result, recent_history=[])

        self.assertIn("Inspect the page links too: list links from https://example.com", suggestions)

    def test_backup_success_suggests_inspecting_backup_folder(self):
        intent = ParsedIntent(
            intent="backup_folder",
            source_path="/sdcard/Documents",
            target_path="/sdcard/Backup",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
            raw_command="back up /sdcard/Documents to /sdcard/Backup",
        )
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{
                "source": "/sdcard/Documents",
                "destination": "/sdcard/Backup/Documents_backup_20260403_010203",
                "success": True,
            }],
        )

        suggestions = generate_advisory_suggestions(intent, result, recent_history=[])

        self.assertIn(
            "Inspect the backup folder: show files in /sdcard/Backup/Documents_backup_20260403_010203",
            suggestions,
        )

    def test_recent_command_repeat_is_filtered_out(self):
        intent = ParsedIntent(
            intent="storage_report",
            source_path="/sdcard/Download",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="storage report /sdcard/Download",
        )
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{
                "directory": "/sdcard/Download",
                "file_count": 4,
                "category_breakdown": {},
            }],
        )
        history = [
            {
                "command": "list large files /sdcard/Download",
                "intent": "list_large_files",
                "status": "success",
            }
        ]

        with patch("agent.advisor.get_allowed_roots", return_value=["/sdcard/Download"]):
            suggestions = generate_advisory_suggestions(intent, result, recent_history=history)

        self.assertFalse(any("list large files /sdcard/Download" in s for s in suggestions))
        self.assertTrue(any("find duplicates /sdcard/Download" in s for s in suggestions))

    def test_session_context_can_suggest_explaining_previous_modifying_result(self):
        intent = ParsedIntent(
            intent="storage_report",
            source_path="/sdcard/Download",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="storage report /sdcard/Download",
        )
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{
                "directory": "/sdcard/Download",
                "file_count": 4,
            }],
        )
        session_context = {
            "last_intent": "organize_folder_by_type",
            "last_command": "organize /sdcard/Download",
            "last_result": "Operation completed successfully.",
            "last_folder": "/sdcard/Download",
        }

        suggestions = generate_advisory_suggestions(
            intent,
            result,
            recent_history=[],
            session_context=session_context,
        )

        self.assertIn(
            "If you want a plain-English recap of the previous step: explain last result",
            suggestions,
        )

    def test_format_advisory_suggestions_has_header(self):
        output = format_advisory_suggestions(["Review the biggest files next: list large files /sdcard/Download"])
        self.assertIn("ADVISORY SUGGESTIONS", output)
        self.assertIn("list large files /sdcard/Download", output)


class TestAdvisorMainIntegration(unittest.TestCase):
    @patch("main.log_operation")
    @patch("main.get_history", return_value=[])
    @patch("main.format_advisory_suggestions", return_value="\nADVICE")
    @patch("main.generate_advisory_suggestions", return_value=["suggestion"])
    @patch("main.report_result", return_value="RESULT")
    @patch("main.execute")
    @patch("main.plan")
    @patch("main.validate_intent_safety")
    @patch("main.report_plan", return_value="PLAN")
    @patch("main.report_parsed_intent", return_value="PARSED")
    @patch("main.parse_command")
    def test_run_command_prints_advice_after_execution(
        self,
        mock_parse_command,
        _mock_report_parsed,
        _mock_report_plan,
        _mock_validate,
        mock_plan,
        mock_execute,
        _mock_report_result,
        mock_generate,
        mock_format,
        _mock_history,
        _mock_log,
    ):
        from main import run_command

        parsed = ParsedIntent(
            intent="storage_report",
            source_path="/sdcard/Download",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            raw_command="storage report /sdcard/Download",
        )
        plan_obj = MagicMock(requires_confirmation=False, preview_summary="preview")
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[{"directory": "/sdcard/Download", "file_count": 1}],
        )
        mock_parse_command.return_value = parsed
        mock_plan.return_value = plan_obj
        mock_execute.return_value = result

        stream = io.StringIO()
        with redirect_stdout(stream):
            run_command("storage report /sdcard/Download")

        output = stream.getvalue()
        self.assertIn("RESULT", output)
        self.assertIn("ADVICE", output)
        mock_execute.assert_called_once()
        mock_generate.assert_called_once_with(parsed, result, recent_history=[], session_context=ANY)
        mock_format.assert_called_once_with(["suggestion"])


if __name__ == "__main__":
    unittest.main()
