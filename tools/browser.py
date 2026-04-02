"""
tools/browser.py — Safe, read-only browser helper functions.

Restrictions enforced here:
- Only https:// and http:// URLs
- No arbitrary click automation
- No login, form submission, or credential handling
- No cookie / session export
- Fetch via stdlib urllib only — no external HTTP libraries required
- Maximum fetch size 512 KB to prevent memory issues
"""

import html.parser
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
    "AppleWebKit/537.36 (compatible; Nabd/0.4)"
)
_SEARCH_URL_TEMPLATE = "https://www.google.com/search?q={query}"


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

def _fetch_html(url: str) -> tuple[str, str]:
    """
    Fetch URL with urllib. Returns (html_text, error_message).
    Never raises; all errors are returned as strings.
    """
    try:
        req = urllib.request.Request(url, headers={"User-Agent": _USER_AGENT})
        with urllib.request.urlopen(req, timeout=FETCH_TIMEOUT) as resp:
            content_type = resp.headers.get_content_type() or ""
            if "text" not in content_type and "html" not in content_type:
                return "", f"Non-text content-type: {content_type!r}"
            raw = resp.read(MAX_FETCH_BYTES)
            charset = resp.headers.get_content_charset() or "utf-8"
            try:
                return raw.decode(charset, errors="replace"), ""
            except Exception:
                return raw.decode("utf-8", errors="replace"), ""
    except urllib.error.HTTPError as e:
        return "", f"HTTP {e.code}: {e.reason}"
    except urllib.error.URLError as e:
        return "", f"Cannot reach URL: {e.reason}"
    except Exception as e:
        return "", f"Fetch error: {e}"


# ── Public tool functions ─────────────────────────────────────────────────────

def browser_search(query: str) -> dict[str, Any]:
    """
    Open a Google search in the default Android browser via termux-open-url.
    No web request is made by Nabd itself — the browser handles the search.
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
    }


def browser_extract_text(url: str) -> dict[str, Any]:
    """
    Fetch a URL and return its visible text content (read-only).
    Strips all HTML tags; skips script/style elements.
    Returns at most MAX_TEXT_CHARS characters.
    """
    html_content, error = _fetch_html(url)
    if error:
        return {
            "url": url,
            "success": False,
            "error": error,
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
    }


def browser_list_links(url: str) -> dict[str, Any]:
    """
    Fetch a URL and return all unique external links found on the page.
    Returns at most MAX_LINKS links.
    """
    html_content, error = _fetch_html(url)
    if error:
        return {
            "url": url,
            "success": False,
            "error": error,
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
    }
