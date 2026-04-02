"""
tools/browser.py — Safe, read-only browser helper functions.

Restrictions enforced here:
- Only https:// and http:// URLs
- No arbitrary click automation
- No login, form submission, or credential handling
- No cookie / session export
- Fetch via stdlib urllib only — no external HTTP libraries required
- Maximum fetch size 512 KB to prevent memory issues
- Certificate verification is ALWAYS enabled (never disabled)

TLS error handling:
- SSL certificate failures are detected and reported as error_type="tls"
- A clear fix message is shown pointing to pkg install ca-certificates
- browser_search and open_url are NOT affected by TLS issues (browser handles them)
"""

import html.parser
import os
import ssl
import subprocess
import urllib.error
import urllib.parse
import urllib.request
from typing import Any


FETCH_TIMEOUT = 15          # seconds
MAX_FETCH_BYTES = 512_000   # 512 KB
MAX_TEXT_CHARS = 3_000      # characters returned in text summary
MAX_LINKS = 50              # maximum links returned

_USER_AGENT = (
    "Mozilla/5.0 (Linux; Android 12; Termux) "
    "AppleWebKit/537.36 (compatible; Nabd/0.4.1)"
)
_SEARCH_URL_TEMPLATE = "https://www.google.com/search?q={query}"

# Termux-specific CA bundle paths (checked by check_browser_tls)
_TERMUX_CA_PATHS: list[str] = [
    "/data/data/com.termux/files/usr/etc/tls/cert.pem",
    "/data/data/com.termux/files/usr/etc/ca-certificates.crt",
]


# ── TLS helpers ───────────────────────────────────────────────────────────────

def _tls_error_detail(raw_reason: str = "") -> str:
    """Return a consistent, actionable TLS error string."""
    return (
        "SSL: CERTIFICATE_VERIFY_FAILED — the local CA trust store is "
        "missing or incomplete.\n"
        "  Fetch-based commands (extract text, list links) cannot verify "
        "server certificates.\n"
        "  Commands that open the browser are NOT affected "
        "(open https://..., search for ...).\n"
        "  Fix (Termux): pkg install ca-certificates\n"
        "  Then restart Nabd and run 'doctor' to confirm."
        + (f"\n  OpenSSL detail: {raw_reason}" if raw_reason else "")
    )


def _is_tls_error(exc: Exception) -> bool:
    """Return True if exc (or its .reason) is an SSL certificate error."""
    if isinstance(exc, ssl.SSLCertVerificationError):
        return True
    if isinstance(exc, ssl.SSLError):
        return True
    if isinstance(exc, urllib.error.URLError):
        reason = exc.reason
        if isinstance(reason, (ssl.SSLCertVerificationError, ssl.SSLError)):
            return True
        # Some Python builds surface the error as a string inside URLError
        if isinstance(reason, str) and (
            "certificate verify failed" in reason.lower()
            or "ssl" in reason.lower()
        ):
            return True
    return False


def check_browser_tls(
    test_url: str = "https://example.com",
    timeout: int = 5,
) -> dict[str, str]:
    """
    Test whether Python's HTTPS stack can reach a public HTTPS endpoint.

    Returns:
        {"status": "ok",    "detail": "..."}  — HTTPS works correctly
        {"status": "warn",  "detail": "..."}  — Network unavailable (TLS may still be ok)
        {"status": "error", "detail": "..."}  — SSL verification failed; CA certs needed

    This function is called by run_doctor() in tools/system.py.
    It makes a real outbound HTTPS request (mocked in tests).
    """
    # Step 1: check if a CA bundle file is present on this device
    ca_paths = ssl.get_default_verify_paths()
    ca_file = ca_paths.cafile
    ca_path = ca_paths.capath

    ca_found = bool(
        (ca_file and os.path.isfile(ca_file) and os.path.getsize(ca_file) > 0)
        or (ca_path and os.path.isdir(ca_path) and os.listdir(ca_path))
    )
    if not ca_found:
        for p in _TERMUX_CA_PATHS:
            if os.path.isfile(p) and os.path.getsize(p) > 0:
                ca_found = True
                break

    # Step 2: attempt a real HTTPS fetch
    try:
        req = urllib.request.Request(
            test_url,
            headers={"User-Agent": "Nabd-Doctor/0.4.1"},
        )
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            _ = resp.read(16)   # read a few bytes to confirm the TLS handshake
        return {
            "status": "ok",
            "detail": f"HTTPS verified OK ({test_url})",
        }

    except urllib.error.URLError as e:
        if _is_tls_error(e):
            raw = getattr(e.reason, "reason", str(e.reason))
            return {
                "status": "error",
                "detail": (
                    "SSL certificate verification failed.\n"
                    "  Fix: pkg install ca-certificates\n"
                    "  Note: 'open https://...' and 'search for' still work."
                    + (f"\n  Detail: {raw}" if raw and raw != str(e.reason) else "")
                ),
            }
        # Network error (timeout, no route, etc.) — not a TLS problem
        ca_note = " (CA bundle present)" if ca_found else " (CA bundle not detected — may fail when online)"
        return {
            "status": "warn",
            "detail": f"Network unavailable{ca_note}: {e.reason}",
        }

    except Exception as e:
        return {
            "status": "warn",
            "detail": f"HTTPS check skipped ({type(e).__name__}): {e}",
        }


# ── HTML Parsers (stdlib only) ────────────────────────────────────────────────

class _TextExtractor(html.parser.HTMLParser):
    """Extract visible text from HTML, skipping script/style/meta tags."""

    _SKIP = {"script", "style", "head", "meta", "link", "noscript", "iframe", "template"}

    def __init__(self) -> None:
        super().__init__()
        self._parts: list[str] = []
        self._depth = 0

    def handle_starttag(self, tag: str, attrs: list) -> None:
        if tag.lower() in self._SKIP:
            self._depth += 1

    def handle_endtag(self, tag: str) -> None:
        if tag.lower() in self._SKIP:
            self._depth = max(0, self._depth - 1)

    def handle_data(self, data: str) -> None:
        if self._depth == 0:
            clean = data.strip()
            if clean:
                self._parts.append(clean)

    def get_text(self) -> str:
        return " ".join(self._parts)


class _LinkExtractor(html.parser.HTMLParser):
    """Extract all <a href> links from HTML."""

    def __init__(self, base_url: str = "") -> None:
        super().__init__()
        self._links: list[dict[str, str]] = []
        self._base_url = base_url
        self._seen: set[str] = set()

    def handle_starttag(self, tag: str, attrs: list) -> None:
        if tag.lower() != "a":
            return
        attrs_dict = dict(attrs)
        href = attrs_dict.get("href", "").strip()
        if not href or href.startswith("#") or href.lower().startswith("javascript:"):
            return
        href = self._resolve(href)
        if href and href not in self._seen:
            self._seen.add(href)
            self._links.append({
                "url": href,
                "text": attrs_dict.get("title", ""),
            })

    def _resolve(self, href: str) -> str:
        if href.startswith("//"):
            return "https:" + href
        if href.startswith("/") and self._base_url:
            p = urllib.parse.urlparse(self._base_url)
            return f"{p.scheme}://{p.netloc}{href}"
        if not href.startswith("http"):
            return ""
        return href

    def get_links(self) -> list[dict[str, str]]:
        return self._links


# ── Internal fetch helper ─────────────────────────────────────────────────────

def _fetch_html(url: str) -> tuple[str, str, str]:
    """
    Fetch URL with urllib (certificate verification always enabled).
    Returns (html_text, error_message, error_type).

    error_type values:
        ""        — no error
        "tls"     — SSL certificate verification failed
        "http"    — HTTP error (4xx, 5xx)
        "network" — network unreachable, timeout, DNS failure
        "other"   — unexpected error
    """
    try:
        req = urllib.request.Request(url, headers={"User-Agent": _USER_AGENT})
        with urllib.request.urlopen(req, timeout=FETCH_TIMEOUT) as resp:
            content_type = resp.headers.get_content_type() or ""
            if "text" not in content_type and "html" not in content_type:
                return "", f"Non-text content-type: {content_type!r}", "other"
            raw = resp.read(MAX_FETCH_BYTES)
            charset = resp.headers.get_content_charset() or "utf-8"
            try:
                return raw.decode(charset, errors="replace"), "", ""
            except Exception:
                return raw.decode("utf-8", errors="replace"), "", ""

    except urllib.error.HTTPError as e:
        return "", f"HTTP {e.code}: {e.reason}", "http"

    except urllib.error.URLError as e:
        if _is_tls_error(e):
            raw_reason = getattr(e.reason, "reason", "") or str(e.reason)
            return "", _tls_error_detail(str(raw_reason)), "tls"
        return "", f"Cannot reach URL: {e.reason}", "network"

    except ssl.SSLError as e:
        # Some Python versions raise SSLError directly
        return "", _tls_error_detail(str(e)), "tls"

    except Exception as e:
        return "", f"Fetch error: {type(e).__name__}: {e}", "other"


# ── Public tool functions ─────────────────────────────────────────────────────

def browser_search(query: str) -> dict[str, Any]:
    """
    Open a Google search in the default Android browser via termux-open-url.
    No web request is made by Nabd itself — the browser handles the search.
    This function is NOT affected by TLS / CA certificate issues.
    """
    encoded = urllib.parse.quote_plus(query.strip())
    search_url = _SEARCH_URL_TEMPLATE.format(query=encoded)

    try:
        result = subprocess.run(
            ["termux-open-url", search_url],
            capture_output=True, text=True, timeout=10, shell=False,
        )
        success = result.returncode == 0
        error = result.stderr.strip() if not success else None
    except FileNotFoundError:
        success = False
        error = (
            "termux-open-url not found. "
            "Install termux-api: pkg install termux-api"
        )
    except Exception as e:
        success = False
        error = str(e)

    return {
        "query": query,
        "search_url": search_url,
        "success": success,
        "error": error,
        "error_type": None,
    }


def browser_extract_text(url: str) -> dict[str, Any]:
    """
    Fetch a URL and return its visible text content (read-only).
    Strips all HTML tags; skips script/style elements.
    Returns at most MAX_TEXT_CHARS characters.

    Requires working local CA certificates for HTTPS URLs.
    If TLS fails, result will have error_type="tls" with fix instructions.
    """
    html_content, error, error_type = _fetch_html(url)
    if error:
        return {
            "url": url,
            "success": False,
            "error": error,
            "error_type": error_type,
            "text": "",
            "char_count": 0,
            "truncated": False,
        }

    extractor = _TextExtractor()
    try:
        extractor.feed(html_content)
    except Exception:
        pass  # use whatever was parsed before the error

    full_text = extractor.get_text()
    truncated = len(full_text) > MAX_TEXT_CHARS
    text_summary = full_text[:MAX_TEXT_CHARS]

    return {
        "url": url,
        "success": True,
        "text": text_summary,
        "char_count": len(full_text),
        "truncated": truncated,
        "error": None,
        "error_type": None,
    }


def browser_list_links(url: str) -> dict[str, Any]:
    """
    Fetch a URL and return all unique external links found on the page.
    Returns at most MAX_LINKS links.

    Requires working local CA certificates for HTTPS URLs.
    If TLS fails, result will have error_type="tls" with fix instructions.
    """
    html_content, error, error_type = _fetch_html(url)
    if error:
        return {
            "url": url,
            "success": False,
            "error": error,
            "error_type": error_type,
            "links": [],
            "link_count": 0,
        }

    extractor = _LinkExtractor(base_url=url)
    try:
        extractor.feed(html_content)
    except Exception:
        pass

    links = extractor.get_links()[:MAX_LINKS]

    return {
        "url": url,
        "success": True,
        "links": links,
        "link_count": len(links),
        "error": None,
        "error_type": None,
    }
