"""
tests/test_v4_phone_browser.py — Tests for Nabd v0.4 phone and browser intents.

Covers:
- Parser detection for all 8 new intents
- URL scheme validation (safety layer)
- Unsupported app rejection (safety layer)
- open_file path validation
- Planner output for each new intent
- Executor whitelist enforcement
- Reporter formatting
- Battery / network mock behaviour
- Browser extract / list-links mocking
- Logging integration
"""
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import json
import unittest
from unittest.mock import MagicMock, patch

from agent.models import ExecutionPlan, OperationStatus, ParsedIntent, RiskLevel, ToolAction
from agent.parser import detect_intent, parse_command
from agent.planner import plan
from agent.reporter import report_result
from agent.safety import validate_intent_safety, validate_url_safety, validate_app_safety
from core.exceptions import SafetyError, ValidationError


# ─── helpers ──────────────────────────────────────────────────────────────────

def _parsed(command: str) -> ParsedIntent:
    return parse_command(command)


# ══════════════════════════════════════════════════════════════════════════════
# 1. PARSER — intent detection
# ══════════════════════════════════════════════════════════════════════════════

class TestParserPhoneIntents(unittest.TestCase):

    # phone_status_battery
    def test_battery_keyword(self):
        self.assertEqual(detect_intent("show battery status"), "phone_status_battery")

    def test_battery_level(self):
        self.assertEqual(detect_intent("what is my battery level"), "phone_status_battery")

    def test_battery_charging(self):
        self.assertEqual(detect_intent("charging status"), "phone_status_battery")

    def test_battery_power_level(self):
        self.assertEqual(detect_intent("check power level"), "phone_status_battery")

    # phone_status_network
    def test_network_wifi_status(self):
        self.assertEqual(detect_intent("wifi status"), "phone_status_network")

    def test_network_show_wifi(self):
        self.assertEqual(detect_intent("show wifi"), "phone_status_network")

    def test_network_connection_info(self):
        self.assertEqual(detect_intent("connection info"), "phone_status_network")

    def test_network_internet_status(self):
        self.assertEqual(detect_intent("internet status"), "phone_status_network")

    def test_network_show_network(self):
        self.assertEqual(detect_intent("show network status"), "phone_status_network")

    # open_app
    def test_open_chrome(self):
        self.assertEqual(detect_intent("open chrome"), "open_app")

    def test_open_settings(self):
        self.assertEqual(detect_intent("open settings"), "open_app")

    def test_open_files_app(self):
        self.assertEqual(detect_intent("open files"), "open_app")

    def test_launch_camera(self):
        self.assertEqual(detect_intent("launch camera"), "open_app")

    def test_start_calculator(self):
        self.assertEqual(detect_intent("start calculator"), "open_app")

    # open_file
    def test_open_file_path(self):
        self.assertEqual(detect_intent("open file /sdcard/Download/report.pdf"), "open_file")

    def test_open_slash_path(self):
        self.assertEqual(detect_intent("open /sdcard/Download/video.mp4"), "open_file")

    def test_view_path(self):
        self.assertEqual(detect_intent("view /sdcard/Documents/notes.txt"), "open_file")

    # open_url
    def test_open_https(self):
        self.assertEqual(detect_intent("open https://example.com"), "open_url")

    def test_visit_url(self):
        self.assertEqual(detect_intent("visit https://google.com"), "open_url")

    def test_go_to_url(self):
        self.assertEqual(detect_intent("go to https://github.com"), "open_url")

    def test_browse_to_url(self):
        self.assertEqual(detect_intent("browse to https://example.com"), "open_url")


class TestParserBrowserIntents(unittest.TestCase):

    # browser_search
    def test_search_for(self):
        self.assertEqual(detect_intent("search for local llm tools"), "browser_search")

    def test_search_simple(self):
        self.assertEqual(detect_intent("search android tips"), "browser_search")

    def test_google_query(self):
        self.assertEqual(detect_intent("google python tutorials"), "browser_search")

    def test_look_up(self):
        self.assertEqual(detect_intent("look up termux api commands"), "browser_search")

    def test_web_search(self):
        self.assertEqual(detect_intent("web search for best notes app"), "browser_search")

    # browser_extract_text
    def test_extract_text_from(self):
        self.assertEqual(
            detect_intent("extract text from https://example.com"),
            "browser_extract_text",
        )

    def test_get_text_from(self):
        self.assertEqual(
            detect_intent("get text from https://example.com"),
            "browser_extract_text",
        )

    def test_read_page_from(self):
        self.assertEqual(
            detect_intent("read page from https://example.com"),
            "browser_extract_text",
        )

    def test_fetch_text(self):
        self.assertEqual(
            detect_intent("fetch text from https://example.com"),
            "browser_extract_text",
        )

    # browser_list_links
    def test_list_links_from(self):
        self.assertEqual(
            detect_intent("list links from https://example.com"),
            "browser_list_links",
        )

    def test_find_links_on(self):
        self.assertEqual(
            detect_intent("find links on https://example.com"),
            "browser_list_links",
        )

    def test_get_links_from(self):
        self.assertEqual(
            detect_intent("get links from https://example.com"),
            "browser_list_links",
        )

    def test_show_links_on(self):
        self.assertEqual(
            detect_intent("show links on https://example.com"),
            "browser_list_links",
        )


class TestParserFieldExtraction(unittest.TestCase):

    def test_open_url_extracts_url(self):
        p = parse_command("open https://example.com")
        self.assertEqual(p.url, "https://example.com")

    def test_extract_text_extracts_url(self):
        p = parse_command("extract text from https://example.com")
        self.assertEqual(p.url, "https://example.com")

    def test_list_links_extracts_url(self):
        p = parse_command("list links from https://example.com")
        self.assertEqual(p.url, "https://example.com")

    def test_browser_search_extracts_query(self):
        p = parse_command("search for local llm tools")
        self.assertEqual(p.query, "local llm tools")

    def test_google_extracts_query(self):
        p = parse_command("google android tips")
        self.assertIsNotNone(p.query)
        self.assertIn("android", p.query.lower())

    def test_look_up_extracts_query(self):
        p = parse_command("look up termux api")
        self.assertIsNotNone(p.query)

    def test_open_chrome_extracts_app_name(self):
        p = parse_command("open chrome")
        self.assertEqual(p.app_name, "chrome")

    def test_open_settings_extracts_app_name(self):
        p = parse_command("open settings")
        self.assertEqual(p.app_name, "settings")

    def test_open_file_extracts_source_path(self):
        p = parse_command("open file /sdcard/Download/test.pdf")
        self.assertEqual(p.source_path, "/sdcard/Download/test.pdf")


class TestParserRiskAndConfirmation(unittest.TestCase):

    def test_battery_low_risk_no_confirm(self):
        p = parse_command("show battery status")
        self.assertEqual(p.risk_level, RiskLevel.LOW)
        self.assertFalse(p.requires_confirmation)

    def test_network_low_risk_no_confirm(self):
        p = parse_command("show network status")
        self.assertEqual(p.risk_level, RiskLevel.LOW)
        self.assertFalse(p.requires_confirmation)

    def test_browser_search_low_risk_no_confirm(self):
        p = parse_command("search for python tutorials")
        self.assertEqual(p.risk_level, RiskLevel.LOW)
        self.assertFalse(p.requires_confirmation)

    def test_browser_extract_low_risk_no_confirm(self):
        p = parse_command("extract text from https://example.com")
        self.assertEqual(p.risk_level, RiskLevel.LOW)
        self.assertFalse(p.requires_confirmation)

    def test_open_url_medium_risk_confirm(self):
        p = parse_command("open https://example.com")
        self.assertEqual(p.risk_level, RiskLevel.MEDIUM)
        self.assertTrue(p.requires_confirmation)

    def test_open_file_medium_risk_confirm(self):
        p = parse_command("open file /sdcard/Download/test.pdf")
        self.assertEqual(p.risk_level, RiskLevel.MEDIUM)
        self.assertTrue(p.requires_confirmation)

    def test_open_app_medium_risk_no_confirm(self):
        p = parse_command("open chrome")
        self.assertEqual(p.risk_level, RiskLevel.MEDIUM)
        self.assertFalse(p.requires_confirmation)


# ══════════════════════════════════════════════════════════════════════════════
# 2. SAFETY — URL scheme validation
# ══════════════════════════════════════════════════════════════════════════════

class TestUrlSafetyValidation(unittest.TestCase):

    def test_https_allowed(self):
        self.assertEqual(validate_url_safety("https://example.com"), "https://example.com")

    def test_http_allowed(self):
        self.assertEqual(validate_url_safety("http://example.com"), "http://example.com")

    def test_javascript_blocked(self):
        with self.assertRaises(SafetyError):
            validate_url_safety("javascript:alert(1)")

    def test_file_scheme_blocked(self):
        with self.assertRaises(SafetyError):
            validate_url_safety("file:///etc/passwd")

    def test_intent_scheme_blocked(self):
        with self.assertRaises(SafetyError):
            validate_url_safety("intent://example.com")

    def test_data_scheme_blocked(self):
        with self.assertRaises(SafetyError):
            validate_url_safety("data:text/html,<h1>hello</h1>")

    def test_vbscript_blocked(self):
        with self.assertRaises(SafetyError):
            validate_url_safety("vbscript:msgbox(1)")

    def test_empty_url_rejected(self):
        with self.assertRaises(ValidationError):
            validate_url_safety("")

    def test_url_without_host_rejected(self):
        with self.assertRaises(ValidationError):
            validate_url_safety("https://")

    def test_unknown_scheme_blocked(self):
        with self.assertRaises(SafetyError):
            validate_url_safety("ftp://example.com/file.txt")


class TestAppSafetyValidation(unittest.TestCase):

    def test_chrome_allowed(self):
        result = validate_app_safety("chrome")
        self.assertEqual(result, "chrome")

    def test_settings_allowed(self):
        result = validate_app_safety("settings")
        self.assertEqual(result, "settings")

    def test_files_allowed(self):
        result = validate_app_safety("files")
        self.assertEqual(result, "files")

    def test_camera_allowed(self):
        result = validate_app_safety("camera")
        self.assertEqual(result, "camera")

    def test_unknown_app_rejected(self):
        with self.assertRaises(ValidationError):
            validate_app_safety("whatsapp")

    def test_arbitrary_package_rejected(self):
        with self.assertRaises(ValidationError):
            validate_app_safety("com.evil.malware")

    def test_empty_app_name_rejected(self):
        with self.assertRaises(ValidationError):
            validate_app_safety("")


class TestOpenFileSafety(unittest.TestCase):
    """open_file must validate path against allowed roots."""

    def test_open_file_outside_allowed_blocked(self):
        intent = parse_command("open file /etc/passwd")
        intent.source_path = "/etc/passwd"
        from core.exceptions import PathNotAllowedError
        with self.assertRaises(PathNotAllowedError):
            validate_intent_safety(intent)

    def test_open_file_traversal_blocked(self):
        intent = parse_command("open file /sdcard/../etc/passwd")
        intent.source_path = "/sdcard/../etc/passwd"
        from core.exceptions import PathTraversalError
        with self.assertRaises(PathTraversalError):
            validate_intent_safety(intent)

    def test_open_file_no_path_raises_validation(self):
        intent = ParsedIntent(intent="open_file", source_path=None, raw_command="open file")
        with self.assertRaises(ValidationError):
            validate_intent_safety(intent)


class TestBrowserIntentSafety(unittest.TestCase):

    def test_browser_search_empty_query_blocked(self):
        intent = ParsedIntent(intent="browser_search", query="", raw_command="search")
        with self.assertRaises(ValidationError):
            validate_intent_safety(intent)

    def test_browser_search_none_query_blocked(self):
        intent = ParsedIntent(intent="browser_search", query=None, raw_command="search")
        with self.assertRaises(ValidationError):
            validate_intent_safety(intent)

    def test_open_url_no_url_blocked(self):
        intent = ParsedIntent(intent="open_url", url=None, raw_command="open url")
        with self.assertRaises(ValidationError):
            validate_intent_safety(intent)

    def test_open_url_javascript_blocked(self):
        intent = ParsedIntent(
            intent="open_url",
            url="javascript:alert(1)",
            raw_command="open javascript:alert(1)",
        )
        with self.assertRaises(SafetyError):
            validate_intent_safety(intent)

    def test_extract_text_no_url_blocked(self):
        intent = ParsedIntent(intent="browser_extract_text", url=None, raw_command="extract text")
        with self.assertRaises(ValidationError):
            validate_intent_safety(intent)


# ══════════════════════════════════════════════════════════════════════════════
# 3. PLANNER — plan shape for new intents
# ══════════════════════════════════════════════════════════════════════════════

class TestPlannerPhoneIntents(unittest.TestCase):

    def test_plan_battery(self):
        intent = ParsedIntent(intent="phone_status_battery", raw_command="show battery")
        p = plan(intent)
        self.assertEqual(p.intent, "phone_status_battery")
        self.assertFalse(p.requires_confirmation)
        self.assertEqual(len(p.actions), 1)
        self.assertEqual(p.actions[0].tool_name, "phone")
        self.assertEqual(p.actions[0].function_name, "get_battery_status")

    def test_plan_network(self):
        intent = ParsedIntent(intent="phone_status_network", raw_command="show network")
        p = plan(intent)
        self.assertEqual(p.intent, "phone_status_network")
        self.assertFalse(p.requires_confirmation)
        self.assertEqual(len(p.actions), 1)
        self.assertEqual(p.actions[0].tool_name, "phone")
        self.assertEqual(p.actions[0].function_name, "get_network_status")

    def test_plan_open_url(self):
        intent = ParsedIntent(
            intent="open_url",
            url="https://example.com",
            raw_command="open https://example.com",
        )
        p = plan(intent)
        self.assertEqual(p.intent, "open_url")
        self.assertTrue(p.requires_confirmation)
        self.assertEqual(p.actions[0].tool_name, "phone")
        self.assertEqual(p.actions[0].function_name, "open_url")
        self.assertEqual(p.actions[0].arguments["url"], "https://example.com")

    def test_plan_open_url_no_url_raises(self):
        from core.exceptions import ValidationError
        intent = ParsedIntent(intent="open_url", url=None, raw_command="open url")
        with self.assertRaises(ValidationError):
            plan(intent)

    def test_plan_open_file(self):
        intent = ParsedIntent(
            intent="open_file",
            source_path="/sdcard/Download/test.pdf",
            raw_command="open file /sdcard/Download/test.pdf",
        )
        p = plan(intent)
        self.assertEqual(p.intent, "open_file")
        self.assertTrue(p.requires_confirmation)
        self.assertEqual(p.actions[0].tool_name, "phone")
        self.assertEqual(p.actions[0].function_name, "open_file")

    def test_plan_open_app(self):
        intent = ParsedIntent(
            intent="open_app",
            app_name="chrome",
            raw_command="open chrome",
        )
        p = plan(intent)
        self.assertEqual(p.intent, "open_app")
        self.assertFalse(p.requires_confirmation)
        self.assertEqual(p.actions[0].tool_name, "phone")
        self.assertEqual(p.actions[0].function_name, "open_app")
        self.assertEqual(p.actions[0].arguments["app_name"], "chrome")

    def test_plan_open_app_no_name_raises(self):
        from core.exceptions import ValidationError
        intent = ParsedIntent(intent="open_app", app_name=None, raw_command="open app")
        with self.assertRaises(ValidationError):
            plan(intent)


class TestPlannerBrowserIntents(unittest.TestCase):

    def test_plan_browser_search(self):
        intent = ParsedIntent(
            intent="browser_search",
            query="local llm tools",
            raw_command="search for local llm tools",
        )
        p = plan(intent)
        self.assertEqual(p.intent, "browser_search")
        self.assertFalse(p.requires_confirmation)
        self.assertEqual(p.actions[0].tool_name, "browser")
        self.assertEqual(p.actions[0].function_name, "browser_search")
        self.assertEqual(p.actions[0].arguments["query"], "local llm tools")

    def test_plan_browser_search_no_query_raises(self):
        from core.exceptions import ValidationError
        intent = ParsedIntent(intent="browser_search", query=None, raw_command="search")
        with self.assertRaises(ValidationError):
            plan(intent)

    def test_plan_browser_extract_text(self):
        intent = ParsedIntent(
            intent="browser_extract_text",
            url="https://example.com",
            raw_command="extract text from https://example.com",
        )
        p = plan(intent)
        self.assertEqual(p.intent, "browser_extract_text")
        self.assertFalse(p.requires_confirmation)
        self.assertEqual(p.actions[0].tool_name, "browser")
        self.assertEqual(p.actions[0].function_name, "browser_extract_text")

    def test_plan_browser_list_links(self):
        intent = ParsedIntent(
            intent="browser_list_links",
            url="https://example.com",
            raw_command="list links from https://example.com",
        )
        p = plan(intent)
        self.assertEqual(p.intent, "browser_list_links")
        self.assertFalse(p.requires_confirmation)
        self.assertEqual(p.actions[0].tool_name, "browser")
        self.assertEqual(p.actions[0].function_name, "browser_list_links")


# ══════════════════════════════════════════════════════════════════════════════
# 4. EXECUTOR — whitelist enforcement
# ══════════════════════════════════════════════════════════════════════════════

class TestExecutorWhitelist(unittest.TestCase):

    def test_phone_functions_whitelisted(self):
        from agent.executor import WHITELISTED_FUNCTIONS
        self.assertIn("phone", WHITELISTED_FUNCTIONS)
        expected = {"open_app", "open_file", "open_url",
                    "get_battery_status", "get_network_status"}
        self.assertEqual(WHITELISTED_FUNCTIONS["phone"], expected)

    def test_browser_functions_whitelisted(self):
        from agent.executor import WHITELISTED_FUNCTIONS
        self.assertIn("browser", WHITELISTED_FUNCTIONS)
        expected = {"browser_search", "browser_extract_text", "browser_list_links",
                    "browser_page_title"}
        self.assertEqual(WHITELISTED_FUNCTIONS["browser"], expected)

    def test_unlisted_phone_function_blocked(self):
        from agent.executor import _execute_action
        action = ToolAction(
            tool_name="phone",
            function_name="arbitrary_shell_exec",
            arguments={},
        )
        from core.exceptions import ExecutionError
        with self.assertRaises(ExecutionError):
            _execute_action(action, confirmed=True)

    def test_unlisted_browser_function_blocked(self):
        from agent.executor import _execute_action
        action = ToolAction(
            tool_name="browser",
            function_name="click_button",
            arguments={},
        )
        from core.exceptions import ExecutionError
        with self.assertRaises(ExecutionError):
            _execute_action(action, confirmed=True)

    def test_unlisted_tool_blocked(self):
        from agent.executor import _execute_action
        action = ToolAction(
            tool_name="shell",
            function_name="exec",
            arguments={},
        )
        from core.exceptions import ExecutionError
        with self.assertRaises(ExecutionError):
            _execute_action(action, confirmed=True)


# ══════════════════════════════════════════════════════════════════════════════
# 5. TOOLS — battery / network mock behaviour
# ══════════════════════════════════════════════════════════════════════════════

class TestBatteryToolMock(unittest.TestCase):

    @patch("tools.phone.subprocess.run")
    def test_battery_success(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=json.dumps({
                "percentage": 87,
                "status": "CHARGING",
                "health": "GOOD",
                "temperature": 28.5,
                "plugged": "AC",
            }),
            stderr="",
        )
        from tools.phone import get_battery_status
        result = get_battery_status()
        self.assertTrue(result["success"])
        self.assertEqual(result["percentage"], 87)
        self.assertEqual(result["status"], "CHARGING")

    @patch("tools.phone.subprocess.run")
    def test_battery_command_not_found(self, mock_run):
        mock_run.side_effect = FileNotFoundError("termux-battery-status")
        from tools.phone import get_battery_status
        result = get_battery_status()
        self.assertFalse(result["success"])
        self.assertIn("error", result)

    @patch("tools.phone.subprocess.run")
    def test_battery_bad_json(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="not json", stderr="")
        from tools.phone import get_battery_status
        result = get_battery_status()
        self.assertFalse(result["success"])
        self.assertIn("error", result)


class TestNetworkToolMock(unittest.TestCase):

    @patch("tools.phone.subprocess.run")
    def test_network_success(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=json.dumps({
                "ssid": "MyNetwork",
                "ip": "192.168.1.42",
                "link_speed_mbps": 72,
                "rssi": -55,
            }),
            stderr="",
        )
        from tools.phone import get_network_status
        result = get_network_status()
        self.assertTrue(result["success"])
        self.assertEqual(result["ssid"], "MyNetwork")
        self.assertEqual(result["ip"], "192.168.1.42")

    @patch("tools.phone.subprocess.run")
    def test_network_failure(self, mock_run):
        mock_run.return_value = MagicMock(returncode=1, stdout="", stderr="not found")
        from tools.phone import get_network_status
        result = get_network_status()
        self.assertFalse(result["success"])

    @patch("tools.phone.subprocess.run")
    def test_network_bad_json(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="??", stderr="")
        from tools.phone import get_network_status
        result = get_network_status()
        self.assertFalse(result["success"])


class TestOpenAppToolMock(unittest.TestCase):

    @patch("tools.phone.subprocess.run")
    def test_open_chrome_success(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
        from tools.phone import open_app
        result = open_app("chrome")
        self.assertTrue(result["success"])
        self.assertEqual(result["app_name"], "chrome")

    def test_open_unsupported_app(self):
        from tools.phone import open_app
        result = open_app("whatsapp")
        self.assertFalse(result["success"])
        self.assertIn("supported_apps", result)
        self.assertIn("chrome", result["supported_apps"])

    def test_open_arbitrary_package_rejected(self):
        from tools.phone import open_app
        result = open_app("com.evil.app")
        self.assertFalse(result["success"])

    @patch("tools.phone.subprocess.run")
    def test_open_url_success(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
        from tools.phone import open_url
        result = open_url("https://example.com")
        self.assertTrue(result["success"])
        self.assertEqual(result["url"], "https://example.com")

    @patch("tools.phone.subprocess.run")
    def test_open_file_success(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
        from tools.phone import open_file
        result = open_file("/sdcard/Download/test.pdf")
        self.assertTrue(result["success"])
        self.assertEqual(result["path"], "/sdcard/Download/test.pdf")


# ══════════════════════════════════════════════════════════════════════════════
# 6. TOOLS — browser extraction helpers
# ══════════════════════════════════════════════════════════════════════════════

_SIMPLE_HTML = b"""
<html>
<head><title>Test Page</title></head>
<body>
  <p>Hello world. This is a test page.</p>
  <a href="https://example.com/page1">Page 1</a>
  <a href="https://example.com/page2">Page 2</a>
  <a href="/relative/path">Relative</a>
  <script>var x = 1;</script>
</body>
</html>
"""


class FakeResponse:
    def __init__(self, content: bytes, content_type: str = "text/html") -> None:
        self._content = content
        self._ct = content_type

    def read(self, limit: int = -1) -> bytes:
        return self._content[:limit] if limit >= 0 else self._content

    def headers(self):
        return {}

    def get_content_type(self):
        return self._ct

    def get_content_charset(self):
        return "utf-8"

    def __enter__(self):
        return self

    def __exit__(self, *args):
        pass


class TestBrowserExtractText(unittest.TestCase):

    @patch("tools.browser.urllib.request.urlopen")
    def test_extract_text_success(self, mock_open):
        resp = FakeResponse(_SIMPLE_HTML)
        resp.headers = MagicMock()
        resp.headers.get_content_type.return_value = "text/html"
        resp.headers.get_content_charset.return_value = "utf-8"
        mock_open.return_value = resp

        from tools.browser import browser_extract_text
        result = browser_extract_text("https://example.com")
        self.assertTrue(result["success"])
        self.assertIn("Hello world", result["text"])
        self.assertGreater(result["char_count"], 0)

    @patch("tools.browser.urllib.request.urlopen")
    def test_extract_text_http_error(self, mock_open):
        import urllib.error
        mock_open.side_effect = urllib.error.HTTPError(
            "https://example.com", 404, "Not Found", {}, None
        )
        from tools.browser import browser_extract_text
        result = browser_extract_text("https://example.com")
        self.assertFalse(result["success"])
        self.assertIn("error", result)
        self.assertEqual(result["text"], "")

    @patch("tools.browser.urllib.request.urlopen")
    def test_extract_text_strips_script(self, mock_open):
        resp = FakeResponse(_SIMPLE_HTML)
        resp.headers = MagicMock()
        resp.headers.get_content_type.return_value = "text/html"
        resp.headers.get_content_charset.return_value = "utf-8"
        mock_open.return_value = resp

        from tools.browser import browser_extract_text
        result = browser_extract_text("https://example.com")
        self.assertNotIn("var x = 1", result["text"])


class TestBrowserListLinks(unittest.TestCase):

    @patch("tools.browser.urllib.request.urlopen")
    def test_list_links_success(self, mock_open):
        resp = FakeResponse(_SIMPLE_HTML)
        resp.headers = MagicMock()
        resp.headers.get_content_type.return_value = "text/html"
        resp.headers.get_content_charset.return_value = "utf-8"
        mock_open.return_value = resp

        from tools.browser import browser_list_links
        result = browser_list_links("https://example.com")
        self.assertTrue(result["success"])
        self.assertGreater(result["link_count"], 0)
        urls = [lnk["url"] for lnk in result["links"]]
        self.assertIn("https://example.com/page1", urls)
        self.assertIn("https://example.com/page2", urls)

    @patch("tools.browser.urllib.request.urlopen")
    def test_list_links_resolves_relative(self, mock_open):
        resp = FakeResponse(_SIMPLE_HTML)
        resp.headers = MagicMock()
        resp.headers.get_content_type.return_value = "text/html"
        resp.headers.get_content_charset.return_value = "utf-8"
        mock_open.return_value = resp

        from tools.browser import browser_list_links
        result = browser_list_links("https://example.com")
        urls = [lnk["url"] for lnk in result["links"]]
        self.assertIn("https://example.com/relative/path", urls)

    @patch("tools.browser.urllib.request.urlopen")
    def test_list_links_http_error(self, mock_open):
        import urllib.error
        mock_open.side_effect = urllib.error.HTTPError(
            "https://example.com", 403, "Forbidden", {}, None
        )
        from tools.browser import browser_list_links
        result = browser_list_links("https://example.com")
        self.assertFalse(result["success"])
        self.assertEqual(result["links"], [])

    @patch("tools.browser.subprocess.run")
    def test_browser_search_constructs_url(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
        from tools.browser import browser_search
        result = browser_search("local llm tools")
        self.assertEqual(result["query"], "local llm tools")
        self.assertIn("google.com/search", result["search_url"])
        self.assertIn("local+llm+tools", result["search_url"])
        self.assertTrue(result["success"])


# ══════════════════════════════════════════════════════════════════════════════
# 7. REPORTER — formatting smoke tests
# ══════════════════════════════════════════════════════════════════════════════

class TestReporterPhoneIntents(unittest.TestCase):

    def _make_result(self, raw: dict, intent: str):
        from agent.models import ExecutionResult
        return ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[raw],
        )

    def test_battery_success_formatted(self):
        raw = {
            "success": True,
            "percentage": 92,
            "status": "DISCHARGING",
            "health": "GOOD",
            "temperature": 30.0,
            "plugged": "UNPLUGGED",
        }
        result = self._make_result(raw, "phone_status_battery")
        output = report_result(result, "phone_status_battery", confirmed=True)
        self.assertIn("92%", output)
        self.assertIn("DISCHARGING", output)

    def test_battery_failure_shows_hint(self):
        raw = {"success": False, "error": "Command not found: termux-battery-status"}
        result = self._make_result(raw, "phone_status_battery")
        output = report_result(result, "phone_status_battery", confirmed=True)
        self.assertIn("termux-api", output)

    def test_network_success_formatted(self):
        raw = {
            "success": True,
            "ssid": "HomeWifi",
            "ip": "192.168.1.5",
            "link_speed_mbps": 54,
            "rssi": -60,
        }
        result = self._make_result(raw, "phone_status_network")
        output = report_result(result, "phone_status_network", confirmed=True)
        self.assertIn("HomeWifi", output)
        self.assertIn("192.168.1.5", output)

    def test_open_app_success_formatted(self):
        raw = {"success": True, "app_name": "chrome", "description": "Google Chrome browser"}
        result = self._make_result(raw, "open_app")
        output = report_result(result, "open_app", confirmed=True)
        self.assertIn("chrome", output)
        self.assertIn("✓", output)

    def test_open_app_failure_shows_supported(self):
        raw = {
            "success": False,
            "app_name": "whatsapp",
            "error": "App 'whatsapp' is not supported",
            "supported_apps": ["chrome", "files", "settings"],
        }
        result = self._make_result(raw, "open_app")
        output = report_result(result, "open_app", confirmed=True)
        self.assertIn("chrome", output)

    def test_open_url_success_formatted(self):
        raw = {"success": True, "url": "https://example.com"}
        result = self._make_result(raw, "open_url")
        output = report_result(result, "open_url", confirmed=True)
        self.assertIn("https://example.com", output)
        self.assertIn("✓", output)

    def test_open_file_failure_hints_termux(self):
        raw = {"success": False, "path": "/sdcard/test.pdf", "error": "not found"}
        result = self._make_result(raw, "open_file")
        output = report_result(result, "open_file", confirmed=True)
        self.assertIn("termux-api", output)


class TestReporterBrowserIntents(unittest.TestCase):

    def _make_result(self, raw: dict, intent: str):
        from agent.models import ExecutionResult
        return ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="ok",
            raw_results=[raw],
        )

    def test_browser_search_success(self):
        raw = {
            "success": True,
            "query": "local llm tools",
            "search_url": "https://www.google.com/search?q=local+llm+tools",
        }
        result = self._make_result(raw, "browser_search")
        output = report_result(result, "browser_search", confirmed=True)
        self.assertIn("local llm tools", output)
        self.assertIn("✓", output)

    def test_browser_search_failure_shows_url(self):
        raw = {
            "success": False,
            "query": "some query",
            "search_url": "https://www.google.com/search?q=some+query",
            "error": "termux-open-url not found",
        }
        result = self._make_result(raw, "browser_search")
        output = report_result(result, "browser_search", confirmed=True)
        self.assertIn("google.com", output)

    def test_extract_text_formatted(self):
        raw = {
            "success": True,
            "url": "https://example.com",
            "text": "Hello world. This is example content.",
            "char_count": 38,
            "truncated": False,
        }
        result = self._make_result(raw, "browser_extract_text")
        output = report_result(result, "browser_extract_text", confirmed=True)
        self.assertIn("Hello world", output)
        self.assertIn("https://example.com", output)

    def test_extract_text_failure(self):
        raw = {
            "success": False,
            "url": "https://example.com",
            "error": "HTTP 404: Not Found",
            "text": "",
            "char_count": 0,
        }
        result = self._make_result(raw, "browser_extract_text")
        output = report_result(result, "browser_extract_text", confirmed=True)
        self.assertIn("404", output)

    def test_list_links_formatted(self):
        raw = {
            "success": True,
            "url": "https://example.com",
            "links": [
                {"url": "https://example.com/a"},
                {"url": "https://example.com/b"},
            ],
            "link_count": 2,
        }
        result = self._make_result(raw, "browser_list_links")
        output = report_result(result, "browser_list_links", confirmed=True)
        self.assertIn("https://example.com/a", output)
        self.assertIn("2", output)

    def test_list_links_empty_page(self):
        raw = {
            "success": True,
            "url": "https://example.com",
            "links": [],
            "link_count": 0,
        }
        result = self._make_result(raw, "browser_list_links")
        output = report_result(result, "browser_list_links", confirmed=True)
        self.assertIn("no links", output.lower())


# ══════════════════════════════════════════════════════════════════════════════
# 8. LOGGING integration — new intents are logged without error
# ══════════════════════════════════════════════════════════════════════════════

class TestLoggingIntegration(unittest.TestCase):

    def test_log_battery_intent(self):
        from core.logging_db import log_operation
        try:
            log_operation(
                "show battery status",
                "phone_status_battery",
                "Read battery status",
                "success",
            )
        except Exception as e:
            self.fail(f"log_operation raised unexpectedly: {e}")

    def test_log_browser_search_intent(self):
        from core.logging_db import log_operation
        try:
            log_operation(
                "search for local llm tools",
                "browser_search",
                "Open web search: 'local llm tools'",
                "success",
            )
        except Exception as e:
            self.fail(f"log_operation raised unexpectedly: {e}")

    def test_log_open_url_intent(self):
        from core.logging_db import log_operation
        try:
            log_operation(
                "open https://example.com",
                "open_url",
                "Open URL: https://example.com",
                "cancelled",
            )
        except Exception as e:
            self.fail(f"log_operation raised unexpectedly: {e}")

    def test_log_safety_blocked(self):
        from core.logging_db import log_operation
        try:
            log_operation(
                "open javascript:alert(1)",
                "open_url",
                None,
                "safety_blocked",
                error_details="Scheme not allowed",
            )
        except Exception as e:
            self.fail(f"log_operation raised unexpectedly: {e}")


# ══════════════════════════════════════════════════════════════════════════════
# 9. No-arbitrary-shell safety
# ══════════════════════════════════════════════════════════════════════════════

class TestNoArbitraryShell(unittest.TestCase):

    def test_phone_open_app_does_not_accept_arbitrary_package(self):
        from tools.phone import open_app
        result = open_app("com.arbitrary.package")
        self.assertFalse(result["success"])

    def test_phone_tool_uses_fixed_commands_only(self):
        """Verify SUPPORTED_APPS only contains known safe entries."""
        from tools.phone import SUPPORTED_APPS
        for key, entry in SUPPORTED_APPS.items():
            cmd = entry["command"]
            self.assertIsInstance(cmd, list, "command must be a list (no shell=True)")
            # First element must be a known, safe binary
            self.assertIn(cmd[0], {"am", "termux-open", "termux-open-url", "monkey"},
                          f"Unexpected command binary: {cmd[0]}")

    def test_browser_no_form_submission_function(self):
        import tools.browser as browser_mod
        self.assertFalse(hasattr(browser_mod, "submit_form"))
        self.assertFalse(hasattr(browser_mod, "login"))
        self.assertFalse(hasattr(browser_mod, "click"))

    def test_browser_no_cookie_export_function(self):
        import tools.browser as browser_mod
        self.assertFalse(hasattr(browser_mod, "get_cookies"))
        self.assertFalse(hasattr(browser_mod, "export_session"))


if __name__ == "__main__":
    unittest.main(verbosity=2)
