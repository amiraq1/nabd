"""
Tests for Nabd v0.4.2
  - show_folders intent (parser, planner, executor, reporter)
  - browser_page_title intent (parser, planner, executor, reporter)
  - Shell command hint: python
  - Help text version bump
"""
import sys
import os
import unittest
from unittest.mock import patch, MagicMock, call
import tempfile

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from agent.parser import parse_command
from agent.planner import plan
from agent.reporter import report_result
from agent.models import (
    ExecutionResult,
    OperationStatus,
    RiskLevel,
)
from tools.browser import _TitleExtractor, browser_page_title


# ── _TitleExtractor unit tests ────────────────────────────────────────────────

class TestTitleExtractor(unittest.TestCase):

    def _extract(self, html: str) -> str:
        e = _TitleExtractor()
        e.feed(html)
        return e.get_title()

    def test_simple_title(self):
        self.assertEqual(self._extract("<html><head><title>Hello World</title></head></html>"), "Hello World")

    def test_title_with_whitespace(self):
        self.assertEqual(self._extract("<title>  Spaces   Here  </title>"), "Spaces Here")

    def test_title_with_newlines(self):
        self.assertEqual(self._extract("<title>\n  Multi\n  Line\n</title>"), "Multi Line")

    def test_only_first_title(self):
        self.assertEqual(self._extract("<title>First</title><title>Second</title>"), "First")

    def test_no_title(self):
        self.assertEqual(self._extract("<html><head></head></html>"), "")

    def test_empty_title(self):
        self.assertEqual(self._extract("<title></title>"), "")

    def test_title_with_entities(self):
        # html.parser resolves &amp; automatically
        result = self._extract("<title>Rock &amp; Roll</title>")
        self.assertEqual(result, "Rock & Roll")

    def test_uppercase_tag(self):
        self.assertEqual(self._extract("<TITLE>Uppercase</TITLE>"), "Uppercase")

    def test_mixed_case_tag(self):
        self.assertEqual(self._extract("<Title>Mixed</Title>"), "Mixed")

    def test_real_world_title(self):
        html = (
            "<!DOCTYPE html><html><head>"
            "<meta charset='utf-8'>"
            "<title>Python.org</title>"
            "</head><body></body></html>"
        )
        self.assertEqual(self._extract(html), "Python.org")

    def test_no_head_tag(self):
        self.assertEqual(self._extract("<title>No head</title>"), "No head")

    def test_title_with_long_content(self):
        long_title = "A" * 200
        self.assertEqual(self._extract(f"<title>{long_title}</title>"), long_title)


# ── browser_page_title tool tests ─────────────────────────────────────────────

class TestBrowserPageTitle(unittest.TestCase):

    def _mock_fetch(self, html="", error="", error_type=""):
        return patch(
            "tools.browser._fetch_html",
            return_value=(html, error, error_type),
        )

    def test_success_with_title(self):
        with self._mock_fetch(html="<html><head><title>Test Page</title></head></html>"):
            result = browser_page_title("https://example.com")
        self.assertTrue(result["success"])
        self.assertEqual(result["title"], "Test Page")
        self.assertEqual(result["url"], "https://example.com")
        self.assertIsNone(result["error"])

    def test_success_empty_title(self):
        with self._mock_fetch(html="<html><head></head></html>"):
            result = browser_page_title("https://example.com")
        self.assertTrue(result["success"])
        self.assertEqual(result["title"], "")

    def test_fetch_error_generic(self):
        with self._mock_fetch(error="Connection refused", error_type="network"):
            result = browser_page_title("https://example.com")
        self.assertFalse(result["success"])
        self.assertEqual(result["error"], "Connection refused")
        self.assertEqual(result["error_type"], "network")
        self.assertEqual(result["title"], "")

    def test_fetch_error_tls(self):
        with self._mock_fetch(error="SSL: CERTIFICATE_VERIFY_FAILED", error_type="tls"):
            result = browser_page_title("https://self-signed.example.com")
        self.assertFalse(result["success"])
        self.assertEqual(result["error_type"], "tls")

    def test_malformed_html_does_not_crash(self):
        with self._mock_fetch(html="<title>Broken<body>no closing"):
            result = browser_page_title("https://example.com")
        # Should not raise; title extraction is best-effort
        self.assertIn("success", result)

    def test_result_keys_present_on_success(self):
        with self._mock_fetch(html="<title>Hi</title>"):
            result = browser_page_title("https://example.com")
        for key in ("url", "success", "title", "error", "error_type"):
            self.assertIn(key, result)

    def test_result_keys_present_on_failure(self):
        with self._mock_fetch(error="err", error_type="http"):
            result = browser_page_title("https://example.com")
        for key in ("url", "success", "title", "error", "error_type"):
            self.assertIn(key, result)


# ── show_folders tool tests ───────────────────────────────────────────────────

class TestShowFoldersTool(unittest.TestCase):

    def setUp(self):
        self.tmp = tempfile.mkdtemp()

    def tearDown(self):
        import shutil
        shutil.rmtree(self.tmp, ignore_errors=True)

    def _make_dir(self, name: str, files: list[str] | None = None) -> str:
        path = os.path.join(self.tmp, name)
        os.makedirs(path, exist_ok=True)
        for f in (files or []):
            open(os.path.join(path, f), "w").close()
        return path

    def test_lists_subfolders_sorted(self):
        self._make_dir("bravo")
        self._make_dir("alpha")
        self._make_dir("charlie")

        from tools.files import show_folders
        result = show_folders(self.tmp)
        names = [f["name"] for f in result["folders"]]
        self.assertEqual(names, ["alpha", "bravo", "charlie"])

    def test_does_not_list_files(self):
        self._make_dir("subfolder")
        open(os.path.join(self.tmp, "file.txt"), "w").close()

        from tools.files import show_folders
        result = show_folders(self.tmp)
        names = [f["name"] for f in result["folders"]]
        self.assertIn("subfolder", names)
        self.assertNotIn("file.txt", names)

    def test_item_count_correct(self):
        sub = self._make_dir("mydir", files=["a.txt", "b.txt", "c.txt"])
        from tools.files import show_folders
        result = show_folders(self.tmp)
        self.assertEqual(result["folder_count"], 1)
        self.assertEqual(result["folders"][0]["item_count"], 3)

    def test_empty_directory_returns_zero_folders(self):
        from tools.files import show_folders
        result = show_folders(self.tmp)
        self.assertEqual(result["folder_count"], 0)
        self.assertEqual(result["folders"], [])

    def test_subfolder_itself_has_zero_items(self):
        self._make_dir("empty_sub")
        from tools.files import show_folders
        result = show_folders(self.tmp)
        self.assertEqual(result["folders"][0]["item_count"], 0)

    def test_nonexistent_directory_raises(self):
        from tools.files import show_folders
        from core.exceptions import ToolError
        with self.assertRaises(ToolError):
            show_folders("/nonexistent/path/xyz123")

    def test_result_keys_present(self):
        from tools.files import show_folders
        result = show_folders(self.tmp)
        for key in ("directory", "folder_count", "folders", "errors"):
            self.assertIn(key, result)

    def test_path_set_correctly(self):
        self._make_dir("sub")
        from tools.files import show_folders
        result = show_folders(self.tmp)
        self.assertEqual(result["directory"], self.tmp)

    def test_nested_structure_not_recursive(self):
        sub = self._make_dir("outer")
        inner = os.path.join(sub, "inner")
        os.makedirs(inner)
        from tools.files import show_folders
        result = show_folders(self.tmp)
        # outer/ has 1 item (inner/), but inner/ is NOT listed at top level
        self.assertEqual(result["folder_count"], 1)
        self.assertEqual(result["folders"][0]["name"], "outer")
        self.assertEqual(result["folders"][0]["item_count"], 1)


# ── Parser tests: show_folders ────────────────────────────────────────────────

class TestParserShowFolders(unittest.TestCase):

    def _parse(self, cmd: str):
        return parse_command(cmd)

    def test_show_folders_basic(self):
        i = self._parse("show folders in /sdcard/Download")
        self.assertEqual(i.intent, "show_folders")
        self.assertEqual(i.source_path, "/sdcard/Download")

    def test_list_folders(self):
        i = self._parse("list folders in /sdcard")
        self.assertEqual(i.intent, "show_folders")
        self.assertEqual(i.source_path, "/sdcard")

    def test_list_subfolders(self):
        i = self._parse("list subfolders in /sdcard/Pictures")
        self.assertEqual(i.intent, "show_folders")

    def test_show_subdirectories(self):
        i = self._parse("show subdirectories in /sdcard/Download")
        self.assertEqual(i.intent, "show_folders")

    def test_list_directory(self):
        i = self._parse("list directory in /sdcard/Download")
        self.assertEqual(i.intent, "show_folders")

    def test_what_folders(self):
        i = self._parse("what folders are in /sdcard/Music")
        self.assertEqual(i.intent, "show_folders")
        self.assertEqual(i.source_path, "/sdcard/Music")

    def test_show_folder_singular(self):
        i = self._parse("show folder in /sdcard/Download")
        self.assertEqual(i.intent, "show_folders")

    def test_list_directories(self):
        i = self._parse("list directories in /sdcard/Download")
        self.assertEqual(i.intent, "show_folders")

    def test_show_directory(self):
        i = self._parse("show directory in /sdcard")
        self.assertEqual(i.intent, "show_folders")

    def test_what_folder_is_in(self):
        # "is" (singular) must also be recognised — fixed regression
        i = self._parse("what folder is in /sdcard/Music")
        self.assertEqual(i.intent, "show_folders")
        self.assertEqual(i.source_path, "/sdcard/Music")

    def test_what_directory_is_in(self):
        i = self._parse("what directory is in /sdcard")
        self.assertEqual(i.intent, "show_folders")

    def test_what_subdirectory_is_in(self):
        i = self._parse("what subdirectory is in /sdcard/Download")
        self.assertEqual(i.intent, "show_folders")

    def test_what_folders_no_verb(self):
        # "what folders in" without is/are should also match
        i = self._parse("what folders in /sdcard/Download")
        self.assertEqual(i.intent, "show_folders")

    def test_does_not_match_show_files(self):
        i = self._parse("show files in /sdcard/Download")
        self.assertEqual(i.intent, "show_files")

    def test_does_not_match_list_media(self):
        i = self._parse("list media in /sdcard/Pictures")
        self.assertEqual(i.intent, "list_media")


# ── Parser tests: browser_page_title ─────────────────────────────────────────

class TestParserBrowserPageTitle(unittest.TestCase):

    def _parse(self, cmd: str):
        return parse_command(cmd)

    def test_show_page_title(self):
        i = self._parse("show page title from https://example.com")
        self.assertEqual(i.intent, "browser_page_title")
        self.assertEqual(i.url, "https://example.com")

    def test_get_page_title(self):
        i = self._parse("get page title from https://python.org")
        self.assertEqual(i.intent, "browser_page_title")
        self.assertEqual(i.url, "https://python.org")

    def test_fetch_title(self):
        i = self._parse("fetch title from https://github.com")
        self.assertEqual(i.intent, "browser_page_title")
        self.assertEqual(i.url, "https://github.com")

    def test_title_of_url(self):
        i = self._parse("title of https://example.com")
        self.assertEqual(i.intent, "browser_page_title")
        self.assertEqual(i.url, "https://example.com")

    def test_what_is_the_page_title(self):
        i = self._parse("what is the page title of https://realpython.com")
        self.assertEqual(i.intent, "browser_page_title")

    def test_get_title_of(self):
        i = self._parse("get title of https://example.com")
        self.assertEqual(i.intent, "browser_page_title")

    def test_page_title_keyword(self):
        i = self._parse("page title https://example.com")
        self.assertEqual(i.intent, "browser_page_title")

    def test_url_extracted_with_path(self):
        i = self._parse("show page title from https://example.com/path/to/page")
        self.assertEqual(i.intent, "browser_page_title")
        self.assertEqual(i.url, "https://example.com/path/to/page")

    def test_http_url(self):
        i = self._parse("get page title from http://example.com")
        self.assertEqual(i.intent, "browser_page_title")
        self.assertEqual(i.url, "http://example.com")

    def test_does_not_match_extract_text(self):
        i = self._parse("extract text from https://example.com")
        self.assertEqual(i.intent, "browser_extract_text")

    def test_does_not_match_list_links(self):
        i = self._parse("list links from https://example.com")
        self.assertEqual(i.intent, "browser_list_links")


# ── Planner tests ─────────────────────────────────────────────────────────────

class TestPlannerShowFolders(unittest.TestCase):

    def test_plan_with_path(self):
        i = parse_command("show folders in /sdcard/Download")
        p = plan(i)
        self.assertEqual(p.intent, "show_folders")
        self.assertEqual(p.risk_level, RiskLevel.LOW)
        self.assertFalse(p.requires_confirmation)
        self.assertEqual(len(p.actions), 1)
        self.assertEqual(p.actions[0].function_name, "show_folders")
        self.assertEqual(p.actions[0].arguments["directory"], "/sdcard/Download")

    def test_plan_default_path(self):
        # "show folders" without a path parses as unknown_intent (no 'in \b' match)
        from core.exceptions import UnknownIntentError
        with self.assertRaises(UnknownIntentError):
            parse_command("show folders")


class TestPlannerBrowserPageTitle(unittest.TestCase):

    def test_plan_with_url(self):
        i = parse_command("show page title from https://example.com")
        p = plan(i)
        self.assertEqual(p.intent, "browser_page_title")
        self.assertEqual(p.risk_level, RiskLevel.LOW)
        self.assertFalse(p.requires_confirmation)
        self.assertEqual(p.actions[0].function_name, "browser_page_title")
        self.assertEqual(p.actions[0].arguments["url"], "https://example.com")

    def test_plan_raises_without_url(self):
        from core.exceptions import ValidationError
        i = parse_command("show page title from https://example.com")
        i.url = None
        with self.assertRaises(ValidationError):
            plan(i)


# ── Reporter tests: show_folders ──────────────────────────────────────────────

class TestReporterShowFolders(unittest.TestCase):

    def _result(self, raw: dict) -> ExecutionResult:
        return ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="OK",
            affected_paths=[],
            errors=[],
            raw_results=[raw],
        )

    def test_shows_directory(self):
        raw = {"directory": "/sdcard/Download", "folder_count": 2,
               "folders": [{"name": "alpha", "path": "/sdcard/Download/alpha", "item_count": 3},
                            {"name": "beta",  "path": "/sdcard/Download/beta",  "item_count": 0}],
               "errors": []}
        out = report_result(self._result(raw), "show_folders", confirmed=False)
        self.assertIn("/sdcard/Download", out)
        self.assertIn("alpha/", out)
        self.assertIn("beta/", out)
        self.assertIn("3 items", out)
        self.assertIn("0 items", out)

    def test_shows_subfolder_count(self):
        raw = {"directory": "/sdcard/Download", "folder_count": 1,
               "folders": [{"name": "docs", "path": "/sdcard/Download/docs", "item_count": 5}],
               "errors": []}
        out = report_result(self._result(raw), "show_folders", confirmed=False)
        self.assertIn("Subfolders : 1", out)

    def test_no_folders_message(self):
        raw = {"directory": "/sdcard/Download", "folder_count": 0,
               "folders": [], "errors": []}
        out = report_result(self._result(raw), "show_folders", confirmed=False)
        self.assertIn("No subfolders found", out)

    def test_unreadable_item_count(self):
        raw = {"directory": "/sdcard", "folder_count": 1,
               "folders": [{"name": "restricted", "path": "/sdcard/restricted", "item_count": None}],
               "errors": []}
        out = report_result(self._result(raw), "show_folders", confirmed=False)
        self.assertIn("unreadable", out)

    def test_singular_item(self):
        raw = {"directory": "/sdcard", "folder_count": 1,
               "folders": [{"name": "solo", "path": "/sdcard/solo", "item_count": 1}],
               "errors": []}
        out = report_result(self._result(raw), "show_folders", confirmed=False)
        self.assertIn("1 item)", out)

    def test_errors_shown(self):
        raw = {"directory": "/sdcard", "folder_count": 0,
               "folders": [], "errors": ["Permission denied: /sdcard/secret"]}
        out = report_result(self._result(raw), "show_folders", confirmed=False)
        self.assertIn("Permission denied", out)


# ── Reporter tests: browser_page_title ────────────────────────────────────────

class TestReporterBrowserPageTitle(unittest.TestCase):

    def _result(self, raw: dict) -> ExecutionResult:
        return ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="OK",
            affected_paths=[],
            errors=[],
            raw_results=[raw],
        )

    def test_success_shows_title(self):
        raw = {"success": True, "url": "https://example.com",
               "title": "Example Domain", "error": None, "error_type": None}
        out = report_result(self._result(raw), "browser_page_title", confirmed=False)
        self.assertIn("https://example.com", out)
        self.assertIn("Example Domain", out)

    def test_empty_title_shows_fallback(self):
        raw = {"success": True, "url": "https://example.com",
               "title": "", "error": None, "error_type": None}
        out = report_result(self._result(raw), "browser_page_title", confirmed=False)
        self.assertIn("no title found", out)

    def test_tls_error_shows_fallback(self):
        raw = {"success": False, "url": "https://self-signed.example.com",
               "title": "", "error": "SSL error", "error_type": "tls"}
        out = report_result(self._result(raw), "browser_page_title", confirmed=False)
        self.assertIn("TLS", out)

    def test_network_error_shows_url_and_error(self):
        raw = {"success": False, "url": "https://example.com",
               "title": "", "error": "Connection refused", "error_type": "network"}
        out = report_result(self._result(raw), "browser_page_title", confirmed=False)
        self.assertIn("Could not fetch", out)
        self.assertIn("Connection refused", out)

    def test_url_label_present_on_success(self):
        raw = {"success": True, "url": "https://python.org",
               "title": "Python", "error": None, "error_type": None}
        out = report_result(self._result(raw), "browser_page_title", confirmed=False)
        self.assertIn("URL", out)
        self.assertIn("Title", out)


# ── Shell command hint: python ────────────────────────────────────────────────

class TestShellCommandPython(unittest.TestCase):

    def test_python_in_shell_commands(self):
        from main import SHELL_COMMANDS
        self.assertIn("python", SHELL_COMMANDS)

    def test_python_hint_mentions_termux(self):
        from main import SHELL_COMMANDS
        hint = SHELL_COMMANDS["python"]
        self.assertIn("Termux", hint)

    def test_python3_hint_not_present(self):
        # 'python3' not yet added — only 'python' keyword
        from main import SHELL_COMMANDS
        self.assertNotIn("python3", SHELL_COMMANDS)


# ── Help text ─────────────────────────────────────────────────────────────────

class TestHelpText(unittest.TestCase):

    def test_version_is_v10(self):
        from main import HELP_TEXT
        self.assertIn("v1.0", HELP_TEXT)

    def test_show_folders_in_help(self):
        from main import HELP_TEXT
        self.assertIn("show folders in", HELP_TEXT)

    def test_browser_page_title_in_help(self):
        from main import HELP_TEXT
        self.assertIn("show page title from", HELP_TEXT)

    def test_python_in_termux_section(self):
        from main import HELP_TEXT
        self.assertIn("python", HELP_TEXT.lower())

    def test_recursively_hint_in_help(self):
        from main import HELP_TEXT
        self.assertIn("recursively", HELP_TEXT)


# ── Safety: new intents in correct sets ───────────────────────────────────────

class TestSafetyNewIntents(unittest.TestCase):

    def test_show_folders_in_path_required(self):
        from agent.safety import PATH_REQUIRED_INTENTS
        self.assertIn("show_folders", PATH_REQUIRED_INTENTS)

    def test_browser_page_title_in_url_required(self):
        from agent.safety import URL_REQUIRED_INTENTS
        self.assertIn("browser_page_title", URL_REQUIRED_INTENTS)

    def test_show_folders_not_in_url_required(self):
        from agent.safety import URL_REQUIRED_INTENTS
        self.assertNotIn("show_folders", URL_REQUIRED_INTENTS)

    def test_browser_page_title_not_in_path_required(self):
        from agent.safety import PATH_REQUIRED_INTENTS
        self.assertNotIn("browser_page_title", PATH_REQUIRED_INTENTS)


# ── Executor whitelist ────────────────────────────────────────────────────────

class TestExecutorWhitelist(unittest.TestCase):

    def test_show_folders_whitelisted(self):
        from agent.executor import WHITELISTED_FUNCTIONS
        self.assertIn("show_folders", WHITELISTED_FUNCTIONS["files"])

    def test_browser_page_title_whitelisted(self):
        from agent.executor import WHITELISTED_FUNCTIONS
        self.assertIn("browser_page_title", WHITELISTED_FUNCTIONS["browser"])


if __name__ == "__main__":
    unittest.main()
