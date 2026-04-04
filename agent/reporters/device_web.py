from typing import Any

from tools.utils import truncate_list

from .registry import register_raw_detail
from .shared import tls_fallback_lines


@register_raw_detail("open_app")
def render_open_app(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    success = raw.get("success", False)
    app_name = raw.get("app_name", "?")
    description = raw.get("description", "")
    if success:
        lines.append(f"\n  ✓  Launched: {app_name}")
        if description:
            lines.append(f"     {description}")
        return

    error = raw.get("error", "Unknown error")
    supported = raw.get("supported_apps", [])
    lines.append(f"\n  ✗  Could not launch: {app_name}")
    lines.append(f"     {error}")
    if supported:
        lines.append(f"\n  Supported apps: {', '.join(supported)}")


@register_raw_detail("open_file")
def render_open_file(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    success = raw.get("success", False)
    path = raw.get("path", "?")
    if success:
        lines.append(f"\n  ✓  Opened: {path}")
        return

    lines.append(f"\n  ✗  Could not open: {path}")
    error = raw.get("error", "")
    if error:
        lines.append(f"     {error}")
    lines.append("  Hint: Install termux-api (pkg install termux-api) if termux-open is missing.")


@register_raw_detail("open_url")
def render_open_url(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    success = raw.get("success", False)
    url = raw.get("url", "?")
    if success:
        lines.append(f"\n  ✓  Opened in browser: {url}")
        return

    lines.append(f"\n  ✗  Could not open URL: {url}")
    error = raw.get("error", "")
    if error:
        lines.append(f"     {error}")
    lines.append("  Hint: Install termux-api (pkg install termux-api) if termux-open-url is missing.")


@register_raw_detail("phone_status_battery")
def render_phone_status_battery(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    success = raw.get("success", False)
    if not success:
        lines.append("\n  ✗  Battery status unavailable.")
        error = raw.get("error", "")
        if error:
            lines.append(f"     {error}")
        lines.append("  Hint: Install termux-api: pkg install termux-api")
        return
    percentage = raw.get("percentage", "?")
    status = raw.get("status", "?")
    health = raw.get("health", "?")
    temperature = raw.get("temperature", "?")
    plugged = raw.get("plugged", "?")
    lines.append(f"\n  Battery Level : {percentage}%")
    lines.append(f"  Status        : {status}")
    if health and health != "?":
        lines.append(f"  Health        : {health}")
    if temperature and temperature != "?":
        lines.append(f"  Temperature   : {temperature} °C")
    if plugged and plugged != "?":
        lines.append(f"  Plugged       : {plugged}")


@register_raw_detail("phone_status_network")
def render_phone_status_network(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    success = raw.get("success", False)
    if not success:
        lines.append("\n  ✗  Network status unavailable.")
        error = raw.get("error", "")
        if error:
            lines.append(f"     {error}")
        lines.append("  Hint: Install termux-api: pkg install termux-api")
        return
    ssid = raw.get("ssid", "?")
    ip = raw.get("ip", "?")
    link_speed = raw.get("link_speed_mbps", raw.get("link_speed", "?"))
    signal = raw.get("rssi", "?")
    freq = raw.get("frequency_mhz", raw.get("frequency", "?"))
    lines.append(f"\n  SSID          : {ssid}")
    lines.append(f"  IP Address    : {ip}")
    if link_speed and link_speed != "?":
        lines.append(f"  Link Speed    : {link_speed} Mbps")
    if signal and signal != "?":
        lines.append(f"  Signal (RSSI) : {signal} dBm")
    if freq and freq != "?":
        lines.append(f"  Frequency     : {freq} MHz")


@register_raw_detail("browser_search")
def render_browser_search(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    success = raw.get("success", False)
    query = raw.get("query", "?")
    search_url = raw.get("search_url", "")
    if success:
        lines.append("\n  ✓  Search opened in browser")
        lines.append(f"     Query : {query}")
        return

    lines.append(f"\n  ✗  Could not open search for: {query}")
    error = raw.get("error", "")
    if error:
        lines.append(f"     {error}")
    if search_url:
        lines.append("\n  You can copy this URL and open it manually:")
        lines.append(f"     {search_url}")
    lines.append("  Hint: Install termux-api: pkg install termux-api")


@register_raw_detail("browser_page_title")
def render_browser_page_title(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    success = raw.get("success", False)
    url = raw.get("url", "?")
    if not success:
        error_type = raw.get("error_type", "")
        if error_type == "tls":
            lines.extend(tls_fallback_lines(url))
        else:
            lines.append(f"\n  ✗  Could not fetch: {url}")
            error = raw.get("error", "")
            if error:
                lines.append(f"     {error}")
        return
    title = raw.get("title", "")
    lines.append(f"\n  URL   : {url}")
    lines.append(f"  Title : {title if title else '(no title found)'}")


@register_raw_detail("browser_extract_text")
def render_browser_extract_text(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    success = raw.get("success", False)
    url = raw.get("url", "?")
    if not success:
        error_type = raw.get("error_type", "")
        if error_type == "tls":
            lines.extend(tls_fallback_lines(url))
        else:
            lines.append(f"\n  ✗  Could not fetch: {url}")
            error = raw.get("error", "")
            if error:
                lines.append(f"     {error}")
        return
    text = raw.get("text", "")
    char_count = raw.get("char_count", 0)
    truncated = raw.get("truncated", False)
    lines.append(f"\n  URL      : {url}")
    lines.append(f"  Size     : {char_count} character(s)")
    if truncated:
        lines.append("  (showing first 3,000 characters)")
    lines.append("")
    if text:
        for para in text.split("  "):
            stripped = para.strip()
            if stripped:
                lines.append(f"  {stripped[:120]}")
        return
    lines.append("  (no readable text found)")


@register_raw_detail("browser_list_links")
def render_browser_list_links(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    success = raw.get("success", False)
    url = raw.get("url", "?")
    if not success:
        error_type = raw.get("error_type", "")
        if error_type == "tls":
            lines.extend(tls_fallback_lines(url))
        else:
            lines.append(f"\n  ✗  Could not fetch: {url}")
            error = raw.get("error", "")
            if error:
                lines.append(f"     {error}")
        return
    links = raw.get("links", [])
    link_count = raw.get("link_count", 0)
    lines.append(f"\n  URL        : {url}")
    lines.append(f"  Links found: {link_count}")
    if not links:
        lines.append("  (no links found on this page)")
        return
    lines.append("")
    shown, extra = truncate_list(links, 20)
    for i, link in enumerate(shown, 1):
        href = link.get("url", "?")
        lines.append(f"  {i:>3}.  {href}")
    if extra:
        lines.append(f"\n  ... and {extra} more link(s) not shown.")
