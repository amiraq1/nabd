"""
tests/test_v4_1_tls.py — Nabd v0.4.1 TLS resilience & diagnostics tests.

Covers:
  - _is_tls_error() detection
  - _fetch_html() returns error_type="tls" for SSL certificate failures
  - browser_extract_text() surfaces error_type="tls" in result dict
  - browser_list_links() surfaces error_type="tls" in result dict
  - check_browser_tls() returns "error" on SSL failure
  - check_browser_tls() returns "ok" on successful fetch
  - check_browser_tls() returns "warn" on non-TLS network error
  - run_doctor() includes the HTTPS / CA certificates check
  - run_doctor() check count is now 6
  - Reporter shows TLS-specific hint for browser_extract_text TLS error
  - Reporter shows TLS-specific hint for browser_list_links TLS error
  - Reporter shows normal error message for non-TLS fetch failures
  - error_type="tls" is absent from successful browser results
  - browser_search result dict contains error_type field
  - _tls_error_detail() contains actionable fix text
  - No certificate verification is disabled (no verify=False patterns)
"""

import ssl
import sys
import types
import unittest
import urllib.error
import urllib.request
from io import BytesIO
from unittest.mock import MagicMock, patch

sys.path.insert(0, ".")


# ── helpers ───────────────────────────────────────────────────────────────────

def _make_ssl_cert_error(msg: str = "certificate verify failed") -> ssl.SSLCertVerificationError:
    err = ssl.SSLCertVerificationError(1, msg)
    return err


def _make_ssl_error(msg: str = "SSL handshake failed") -> ssl.SSLError:
    err = ssl.SSLError(1, msg)
    return err


def _make_url_error_ssl() -> urllib.error.URLError:
    return urllib.error.URLError(reason=_make_ssl_cert_error())


def _make_url_error_ssl_plain() -> urllib.error.URLError:
    return urllib.error.URLError(reason=_make_ssl_error())


def _make_url_error_network() -> urllib.error.URLError:
    return urllib.error.URLError(reason="[Errno 101] Network is unreachable")


def _fake_html_response(html: bytes = b"<html><body>Hello</body></html>") -> MagicMock:
    mock_resp = MagicMock()
    mock_resp.headers.get_content_type.return_value = "text/html"
    mock_resp.headers.get_content_charset.return_value = "utf-8"
    mock_resp.read.return_value = html
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)
    return mock_resp


# ── _is_tls_error ─────────────────────────────────────────────────────────────

class TestIsTlsError(unittest.TestCase):
    def setUp(self):
        from tools.browser import _is_tls_error
        self.fn = _is_tls_error

    def test_ssl_cert_verification_error_direct(self):
        self.assertTrue(self.fn(_make_ssl_cert_error()))

    def test_ssl_error_direct(self):
        self.assertTrue(self.fn(_make_ssl_error()))

    def test_url_error_with_ssl_cert_reason(self):
        self.assertTrue(self.fn(_make_url_error_ssl()))

    def test_url_error_with_ssl_error_reason(self):
        self.assertTrue(self.fn(_make_url_error_ssl_plain()))

    def test_url_error_network_not_tls(self):
        self.assertFalse(self.fn(_make_url_error_network()))

    def test_http_error_not_tls(self):
        exc = urllib.error.HTTPError("https://x.com", 403, "Forbidden", {}, None)
        self.assertFalse(self.fn(exc))

    def test_value_error_not_tls(self):
        self.assertFalse(self.fn(ValueError("something")))


# ── _tls_error_detail ─────────────────────────────────────────────────────────

class TestTlsErrorDetail(unittest.TestCase):
    def setUp(self):
        from tools.browser import _tls_error_detail
        self.fn = _tls_error_detail

    def test_contains_certificate_keyword(self):
        msg = self.fn()
        self.assertIn("CERTIFICATE_VERIFY_FAILED", msg)

    def test_contains_fix_instruction(self):
        msg = self.fn()
        self.assertIn("pkg install ca-certificates", msg)

    def test_mentions_browser_still_works(self):
        msg = self.fn()
        lower = msg.lower()
        self.assertIn("not affected", lower)

    def test_extra_detail_appended(self):
        msg = self.fn("verify return code: 20")
        self.assertIn("verify return code: 20", msg)

    def test_no_extra_detail_when_empty(self):
        msg = self.fn("")
        self.assertNotIn("OpenSSL detail:", msg)


# ── _fetch_html TLS error_type ────────────────────────────────────────────────

class TestFetchHtmlTlsErrorType(unittest.TestCase):
    def setUp(self):
        from tools.browser import _fetch_html
        self.fn = _fetch_html

    @patch("urllib.request.urlopen", side_effect=_make_url_error_ssl())
    def test_ssl_cert_error_returns_tls_type(self, _):
        html, error, error_type = self.fn("https://example.com")
        self.assertEqual(html, "")
        self.assertEqual(error_type, "tls")
        self.assertIn("CERTIFICATE_VERIFY_FAILED", error)

    @patch("urllib.request.urlopen", side_effect=_make_url_error_ssl_plain())
    def test_ssl_error_returns_tls_type(self, _):
        html, error, error_type = self.fn("https://example.com")
        self.assertEqual(error_type, "tls")

    @patch("urllib.request.urlopen", side_effect=_make_url_error_network())
    def test_network_error_returns_network_type(self, _):
        html, error, error_type = self.fn("https://example.com")
        self.assertEqual(error_type, "network")
        self.assertNotEqual(error_type, "tls")

    @patch("urllib.request.urlopen",
           side_effect=urllib.error.HTTPError("https://x.com", 404, "Not Found", {}, None))
    def test_http_error_returns_http_type(self, _):
        html, error, error_type = self.fn("https://example.com")
        self.assertEqual(error_type, "http")

    @patch("urllib.request.urlopen", return_value=_fake_html_response())
    def test_success_returns_empty_error_type(self, _):
        html, error, error_type = self.fn("https://example.com")
        self.assertEqual(error_type, "")
        self.assertEqual(error, "")
        self.assertIn("Hello", html)

    @patch("urllib.request.urlopen", side_effect=ValueError("unexpected"))
    def test_unexpected_error_returns_other_type(self, _):
        html, error, error_type = self.fn("https://example.com")
        self.assertEqual(error_type, "other")


# ── browser_extract_text TLS ──────────────────────────────────────────────────

class TestBrowserExtractTextTls(unittest.TestCase):
    @patch("urllib.request.urlopen", side_effect=_make_url_error_ssl())
    def test_tls_error_surfaced_in_result(self, _):
        from tools.browser import browser_extract_text
        result = browser_extract_text("https://example.com")
        self.assertFalse(result["success"])
        self.assertEqual(result["error_type"], "tls")
        self.assertIn("CERTIFICATE_VERIFY_FAILED", result["error"])

    @patch("urllib.request.urlopen", side_effect=_make_url_error_ssl())
    def test_tls_error_has_empty_text(self, _):
        from tools.browser import browser_extract_text
        result = browser_extract_text("https://example.com")
        self.assertEqual(result["text"], "")
        self.assertEqual(result["char_count"], 0)

    @patch("urllib.request.urlopen", side_effect=_make_url_error_network())
    def test_network_error_type_is_network(self, _):
        from tools.browser import browser_extract_text
        result = browser_extract_text("https://example.com")
        self.assertFalse(result["success"])
        self.assertEqual(result["error_type"], "network")

    @patch("urllib.request.urlopen", return_value=_fake_html_response())
    def test_success_has_no_error_type(self, _):
        from tools.browser import browser_extract_text
        result = browser_extract_text("https://example.com")
        self.assertTrue(result["success"])
        self.assertIsNone(result["error_type"])

    @patch("urllib.request.urlopen", return_value=_fake_html_response())
    def test_success_contains_text(self, _):
        from tools.browser import browser_extract_text
        result = browser_extract_text("https://example.com")
        self.assertIn("Hello", result["text"])


# ── browser_list_links TLS ────────────────────────────────────────────────────

class TestBrowserListLinksTls(unittest.TestCase):
    @patch("urllib.request.urlopen", side_effect=_make_url_error_ssl())
    def test_tls_error_surfaced_in_result(self, _):
        from tools.browser import browser_list_links
        result = browser_list_links("https://example.com")
        self.assertFalse(result["success"])
        self.assertEqual(result["error_type"], "tls")
        self.assertIn("CERTIFICATE_VERIFY_FAILED", result["error"])

    @patch("urllib.request.urlopen", side_effect=_make_url_error_ssl())
    def test_tls_error_has_empty_links(self, _):
        from tools.browser import browser_list_links
        result = browser_list_links("https://example.com")
        self.assertEqual(result["links"], [])
        self.assertEqual(result["link_count"], 0)

    @patch("urllib.request.urlopen", return_value=_fake_html_response())
    def test_success_has_no_error_type(self, _):
        from tools.browser import browser_list_links
        result = browser_list_links("https://example.com")
        self.assertTrue(result["success"])
        self.assertIsNone(result["error_type"])


# ── browser_search has error_type field ───────────────────────────────────────

class TestBrowserSearchErrorTypeField(unittest.TestCase):
    @patch("subprocess.run", return_value=MagicMock(returncode=0, stderr=""))
    def test_success_result_has_error_type_none(self, _):
        from tools.browser import browser_search
        result = browser_search("test query")
        self.assertIn("error_type", result)
        self.assertIsNone(result["error_type"])

    @patch("subprocess.run", return_value=MagicMock(returncode=1, stderr="error msg"))
    def test_failure_result_has_error_type_field(self, _):
        from tools.browser import browser_search
        result = browser_search("test query")
        self.assertIn("error_type", result)


# ── check_browser_tls ─────────────────────────────────────────────────────────

class TestCheckBrowserTls(unittest.TestCase):
    def setUp(self):
        from tools.browser import check_browser_tls
        self.fn = check_browser_tls

    @patch("urllib.request.urlopen", side_effect=_make_url_error_ssl())
    def test_ssl_cert_failure_returns_error_status(self, _):
        result = self.fn(test_url="https://example.com", timeout=3)
        self.assertEqual(result["status"], "error")
        self.assertIn("certificate", result["detail"].lower())
        self.assertIn("pkg install ca-certificates", result["detail"])

    @patch("urllib.request.urlopen", side_effect=_make_url_error_ssl_plain())
    def test_ssl_error_returns_error_status(self, _):
        result = self.fn(test_url="https://example.com", timeout=3)
        self.assertEqual(result["status"], "error")

    @patch("urllib.request.urlopen", side_effect=_make_url_error_network())
    def test_network_error_returns_warn_status(self, _):
        result = self.fn(test_url="https://example.com", timeout=3)
        self.assertEqual(result["status"], "warn")
        self.assertNotEqual(result["status"], "error")

    def test_successful_fetch_returns_ok_status(self):
        mock_resp = MagicMock()
        mock_resp.read.return_value = b"<!DOCTYPE html>"
        mock_resp.__enter__ = lambda s: s
        mock_resp.__exit__ = MagicMock(return_value=False)
        with patch("urllib.request.urlopen", return_value=mock_resp):
            result = self.fn(test_url="https://example.com", timeout=3)
        self.assertEqual(result["status"], "ok")
        self.assertIn("OK", result["detail"])

    @patch("urllib.request.urlopen", side_effect=TimeoutError("timed out"))
    def test_timeout_returns_warn_not_error(self, _):
        result = self.fn(test_url="https://example.com", timeout=1)
        self.assertIn(result["status"], ("warn", "ok"))
        self.assertNotEqual(result["status"], "error")

    def test_error_result_has_detail_key(self):
        with patch("urllib.request.urlopen", side_effect=_make_url_error_ssl()):
            result = self.fn(test_url="https://example.com", timeout=1)
        self.assertIn("detail", result)
        self.assertIn("status", result)

    def test_ok_result_mentions_test_url(self):
        mock_resp = MagicMock()
        mock_resp.read.return_value = b"data"
        mock_resp.__enter__ = lambda s: s
        mock_resp.__exit__ = MagicMock(return_value=False)
        with patch("urllib.request.urlopen", return_value=mock_resp):
            result = self.fn(test_url="https://example.com", timeout=3)
        self.assertIn("example.com", result["detail"])

    def test_warn_mentions_network_unavailable(self):
        with patch("urllib.request.urlopen", side_effect=_make_url_error_network()):
            result = self.fn(test_url="https://example.com", timeout=1)
        self.assertIn("Network unavailable", result["detail"])

    def test_error_mentions_still_works_note(self):
        with patch("urllib.request.urlopen", side_effect=_make_url_error_ssl()):
            result = self.fn(test_url="https://example.com", timeout=1)
        self.assertIn("still work", result["detail"])


# ── run_doctor integration ────────────────────────────────────────────────────

class TestDoctorTlsIntegration(unittest.TestCase):
    def _run_doctor_with_tls_mock(self, tls_status: str, tls_detail: str) -> dict:
        """Run doctor with check_browser_tls mocked to a fixed result."""
        with patch(
            "tools.browser.check_browser_tls",
            return_value={"status": tls_status, "detail": tls_detail},
        ):
            from tools.system import run_doctor
            return run_doctor()

    def test_doctor_has_six_checks(self):
        result = self._run_doctor_with_tls_mock("ok", "HTTPS verified OK")
        self.assertEqual(len(result["checks"]), 6)

    def test_doctor_https_check_present(self):
        result = self._run_doctor_with_tls_mock("ok", "HTTPS verified OK")
        names = [c["name"] for c in result["checks"]]
        self.assertIn("HTTPS / CA certificates", names)

    def test_doctor_https_check_ok(self):
        result = self._run_doctor_with_tls_mock("ok", "HTTPS verified OK")
        https_check = next(c for c in result["checks"] if "HTTPS" in c["name"])
        self.assertEqual(https_check["status"], "ok")

    def test_doctor_https_check_error_propagates(self):
        result = self._run_doctor_with_tls_mock(
            "error", "SSL certificate verification failed."
        )
        https_check = next(c for c in result["checks"] if "HTTPS" in c["name"])
        self.assertEqual(https_check["status"], "error")

    def test_doctor_https_check_warn_propagates(self):
        result = self._run_doctor_with_tls_mock(
            "warn", "Network unavailable: [Errno 101]"
        )
        https_check = next(c for c in result["checks"] if "HTTPS" in c["name"])
        self.assertEqual(https_check["status"], "warn")

    def test_doctor_overall_error_when_https_fails(self):
        result = self._run_doctor_with_tls_mock(
            "error", "SSL certificate verification failed."
        )
        self.assertEqual(result["overall"], "error")

    def test_doctor_error_count_increases_on_tls_failure(self):
        ok_result = self._run_doctor_with_tls_mock("ok", "HTTPS verified OK")
        err_result = self._run_doctor_with_tls_mock("error", "SSL failed")
        self.assertGreater(err_result["error_count"], ok_result["error_count"])

    def test_doctor_detail_matches_tls_check(self):
        detail_text = "HTTPS verified OK (https://example.com)"
        result = self._run_doctor_with_tls_mock("ok", detail_text)
        https_check = next(c for c in result["checks"] if "HTTPS" in c["name"])
        self.assertEqual(https_check["detail"], detail_text)


# ── _tls_fallback_lines unit tests ────────────────────────────────────────────

class TestTlsFallbackLines(unittest.TestCase):
    def setUp(self):
        from agent.reporter import _tls_fallback_lines
        self.fn = _tls_fallback_lines

    def test_returns_list(self):
        result = self.fn("https://example.com")
        self.assertIsInstance(result, list)
        self.assertGreater(len(result), 0)

    def test_contains_ssl_certificate_error(self):
        lines = self.fn("https://example.com")
        combined = "\n".join(lines)
        self.assertIn("SSL certificate error", combined)

    def test_contains_exact_url_in_open_suggestion(self):
        url = "https://example.com/path/to/page"
        lines = self.fn(url)
        combined = "\n".join(lines)
        self.assertIn(f"open {url}", combined)

    def test_contains_domain_in_search_suggestion(self):
        lines = self.fn("https://example.com/some/path?q=1")
        combined = "\n".join(lines)
        self.assertIn("search for example.com", combined)

    def test_http_url_domain_extracted_correctly(self):
        lines = self.fn("http://docs.python.org/3/library/ssl.html")
        combined = "\n".join(lines)
        self.assertIn("search for docs.python.org", combined)

    def test_no_scheme_prefix_in_search_suggestion(self):
        lines = self.fn("https://example.com")
        combined = "\n".join(lines)
        self.assertNotIn("search for https://", combined)
        self.assertNotIn("search for http://", combined)

    def test_contains_fix_instruction(self):
        lines = self.fn("https://example.com")
        combined = "\n".join(lines)
        self.assertIn("pkg install ca-certificates", combined)

    def test_contains_doctor_reference(self):
        lines = self.fn("https://example.com")
        combined = "\n".join(lines)
        self.assertIn("doctor", combined)

    def test_contains_no_local_tls_needed(self):
        lines = self.fn("https://example.com")
        combined = "\n".join(lines)
        self.assertIn("no local TLS needed", combined)

    def test_contains_environment_issue_note(self):
        lines = self.fn("https://example.com")
        combined = "\n".join(lines)
        self.assertIn("environment issue", combined)

    def test_url_in_error_header_line(self):
        url = "https://example.com"
        lines = self.fn(url)
        self.assertIn(url, lines[0])

    def test_different_urls_produce_different_open_suggestions(self):
        lines_a = self.fn("https://aaa.com")
        lines_b = self.fn("https://bbb.com")
        combined_a = "\n".join(lines_a)
        combined_b = "\n".join(lines_b)
        self.assertIn("open https://aaa.com", combined_a)
        self.assertIn("open https://bbb.com", combined_b)
        self.assertNotIn("open https://bbb.com", combined_a)
        self.assertNotIn("open https://aaa.com", combined_b)


# ── Reporter TLS hint ─────────────────────────────────────────────────────────

class TestReporterTlsHint(unittest.TestCase):
    _TLS_URL = "https://example.com"
    _TLS_RAW_EXTRACT = {
        "success": False,
        "url": _TLS_URL,
        "error": "SSL: CERTIFICATE_VERIFY_FAILED — ...",
        "error_type": "tls",
        "text": "",
        "char_count": 0,
        "truncated": False,
    }
    _TLS_RAW_LINKS = {
        "success": False,
        "url": _TLS_URL,
        "error": "SSL: CERTIFICATE_VERIFY_FAILED — ...",
        "error_type": "tls",
        "links": [],
        "link_count": 0,
    }

    def _make_result(self, intent: str, raw: dict) -> str:
        from agent.models import ExecutionResult, OperationStatus
        from agent.reporter import report_result
        status = OperationStatus.SUCCESS if raw.get("success") else OperationStatus.FAILURE
        result = ExecutionResult(
            status=status,
            message="ok",
            raw_results=[raw],
        )
        return report_result(result, intent, confirmed=True)

    # ── browser_extract_text: TLS error ──────────────────────────────────────

    def test_extract_text_tls_shows_ssl_certificate_error(self):
        output = self._make_result("browser_extract_text", self._TLS_RAW_EXTRACT)
        self.assertIn("SSL certificate error", output)

    def test_extract_text_tls_shows_exact_url_in_open_suggestion(self):
        output = self._make_result("browser_extract_text", self._TLS_RAW_EXTRACT)
        self.assertIn(f"open {self._TLS_URL}", output)

    def test_extract_text_tls_shows_domain_in_search_suggestion(self):
        output = self._make_result("browser_extract_text", self._TLS_RAW_EXTRACT)
        self.assertIn("search for example.com", output)

    def test_extract_text_tls_search_suggestion_has_no_scheme(self):
        output = self._make_result("browser_extract_text", self._TLS_RAW_EXTRACT)
        self.assertNotIn("search for https://", output)

    def test_extract_text_tls_mentions_fix(self):
        output = self._make_result("browser_extract_text", self._TLS_RAW_EXTRACT)
        self.assertIn("pkg install ca-certificates", output)

    def test_extract_text_tls_mentions_doctor(self):
        output = self._make_result("browser_extract_text", self._TLS_RAW_EXTRACT)
        self.assertIn("doctor", output.lower())

    def test_extract_text_tls_mentions_no_local_tls(self):
        output = self._make_result("browser_extract_text", self._TLS_RAW_EXTRACT)
        self.assertIn("no local TLS needed", output)

    def test_extract_text_tls_mentions_environment_issue(self):
        output = self._make_result("browser_extract_text", self._TLS_RAW_EXTRACT)
        self.assertIn("environment issue", output)

    def test_extract_text_non_tls_error_shows_no_ssl_block(self):
        raw = {
            "success": False,
            "url": "https://example.com",
            "error": "HTTP 404: Not Found",
            "error_type": "http",
            "text": "",
            "char_count": 0,
            "truncated": False,
        }
        output = self._make_result("browser_extract_text", raw)
        self.assertNotIn("SSL certificate error", output)
        self.assertIn("HTTP 404", output)

    def test_extract_text_non_tls_error_shows_no_open_suggestion(self):
        raw = {
            "success": False,
            "url": "https://example.com",
            "error": "HTTP 404: Not Found",
            "error_type": "http",
            "text": "",
            "char_count": 0,
            "truncated": False,
        }
        output = self._make_result("browser_extract_text", raw)
        self.assertNotIn("no local TLS needed", output)

    def test_extract_text_success_has_no_ssl_noise(self):
        raw = {
            "success": True,
            "url": "https://example.com",
            "text": "Hello World",
            "char_count": 11,
            "truncated": False,
            "error": None,
            "error_type": None,
        }
        output = self._make_result("browser_extract_text", raw)
        self.assertNotIn("SSL", output)
        self.assertIn("Hello World", output)

    # ── browser_list_links: TLS error ─────────────────────────────────────────

    def test_list_links_tls_shows_ssl_certificate_error(self):
        output = self._make_result("browser_list_links", self._TLS_RAW_LINKS)
        self.assertIn("SSL certificate error", output)

    def test_list_links_tls_shows_exact_url_in_open_suggestion(self):
        output = self._make_result("browser_list_links", self._TLS_RAW_LINKS)
        self.assertIn(f"open {self._TLS_URL}", output)

    def test_list_links_tls_shows_domain_in_search_suggestion(self):
        output = self._make_result("browser_list_links", self._TLS_RAW_LINKS)
        self.assertIn("search for example.com", output)

    def test_list_links_tls_mentions_fix(self):
        output = self._make_result("browser_list_links", self._TLS_RAW_LINKS)
        self.assertIn("pkg install ca-certificates", output)

    def test_list_links_tls_mentions_doctor(self):
        output = self._make_result("browser_list_links", self._TLS_RAW_LINKS)
        self.assertIn("doctor", output.lower())

    def test_list_links_non_tls_error_shows_no_ssl_block(self):
        raw = {
            "success": False,
            "url": "https://example.com",
            "error": "Cannot reach URL: [Errno 101] Network is unreachable",
            "error_type": "network",
            "links": [],
            "link_count": 0,
        }
        output = self._make_result("browser_list_links", raw)
        self.assertNotIn("SSL certificate error", output)
        self.assertIn("Cannot reach URL", output)

    # ── open suggestion uses full URL, search suggestion uses domain ──────────

    def test_extract_text_tls_url_with_path_open_uses_full_url(self):
        raw = {
            **self._TLS_RAW_EXTRACT,
            "url": "https://docs.python.org/3/library/ssl.html",
        }
        output = self._make_result("browser_extract_text", raw)
        self.assertIn("open https://docs.python.org/3/library/ssl.html", output)

    def test_extract_text_tls_url_with_path_search_uses_domain_only(self):
        raw = {
            **self._TLS_RAW_EXTRACT,
            "url": "https://docs.python.org/3/library/ssl.html",
        }
        output = self._make_result("browser_extract_text", raw)
        self.assertIn("search for docs.python.org", output)
        self.assertNotIn("search for https://", output)


# ── No verify=False in codebase ───────────────────────────────────────────────

class TestNoCertVerificationDisabled(unittest.TestCase):
    """Ensure certificate verification is never disabled in tools/browser.py."""

    def test_browser_py_has_no_cert_none(self):
        with open("tools/browser.py") as fh:
            source = fh.read()
        self.assertNotIn("CERT_NONE", source)
        self.assertNotIn("check_hostname = False", source)
        self.assertNotIn("verify_mode = ssl.CERT_NONE", source)

    def test_browser_py_no_ssl_context_with_disabled_verify(self):
        with open("tools/browser.py") as fh:
            source = fh.read()
        # Ensure no SSLContext created with CERT_NONE or no-verify options
        self.assertNotIn("ssl.CERT_NONE", source)
        self.assertNotIn("ssl._create_unverified_context", source)
        self.assertNotIn("ssl.create_default_context(cafile=None, capath=None, cadata=None)", source)


if __name__ == "__main__":
    unittest.main()
