"""
Nabd v1.0 — Proactive advisory suggestions.

The Advisor suggests logical next steps after a command completes.
It is strictly advisory — it produces text strings only and never
triggers execution, modifies state, or bypasses safety rules.

Rules:
  - suggestions are per-intent, based on what is useful after that operation
  - suggestions reference the actual path/url from context where available
  - mutating commands are never suggested as retry actions
  - failed, blocked, or cancelled executions receive targeted guidance only
  - advisory text is concise, practical, and grounded in the actual result
"""
from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from agent.context import ContextMemory
    from agent.models import ExecutionResult, OperationStatus


# ── Safe follow-up suggestion maps ───────────────────────────────────────────
# Each entry is (template_string, requires_path, requires_url).
# Templates may use {path} and {url} placeholders — filled from context.
# If a placeholder is needed but missing, the suggestion is skipped.

_SUGGESTIONS: dict[str, list[tuple[str, bool, bool]]] = {
    "show_files": [
        ("list media in {path}", True, False),
        ("find duplicates {path}", True, False),
        ("list large files {path}", True, False),
        ("storage report {path}", True, False),
    ],
    "show_folders": [
        ("show files in {path}", True, False),
        ("storage report {path}", True, False),
    ],
    "list_media": [
        ("find duplicates {path}", True, False),
        ("compress images {path}", True, False),
        ("list large files {path}", True, False),
    ],
    "find_duplicates": [
        ("list large files {path}", True, False),
        ("storage report {path}", True, False),
    ],
    "list_large_files": [
        ("storage report {path}", True, False),
        ("find duplicates {path}", True, False),
    ],
    "storage_report": [
        ("show files in {path}", True, False),
        ("find duplicates {path}", True, False),
        ("list large files {path}", True, False),
    ],
    "backup_folder": [
        ("storage report {path}", True, False),
        ("show files in {path}", True, False),
    ],
    "organize_folder_by_type": [
        ("show files in {path}", True, False),
        ("storage report {path}", True, False),
    ],
    "compress_images": [
        ("storage report {path}", True, False),
        ("list media in {path}", True, False),
    ],
    "browser_page_title": [
        ("extract text from {url}", False, True),
        ("list links from {url}", False, True),
    ],
    "browser_extract_text": [
        ("list links from {url}", False, True),
        ("show page title from {url}", False, True),
    ],
    "browser_list_links": [
        ("extract text from {url}", False, True),
    ],
    "doctor": [],   # doctor suggestions depend on result content, handled separately
    "open_url": [
        ("extract text from {url}", False, True),
        ("show page title from {url}", False, True),
    ],
}

# ── Environment-specific failure guidance ─────────────────────────────────────

_ENV_HINTS: dict[str, str] = {
    "ffmpeg": (
        "  ffmpeg is missing — convert/video commands need it.\n"
        "  Install: pkg install ffmpeg"
    ),
    "pillow": (
        "  Pillow is missing — compress images needs it.\n"
        "  Install: pip install Pillow"
    ),
    "termux-api": (
        "  termux-api is not installed — phone/battery/network commands need it.\n"
        "  Install: pkg install termux-api"
    ),
    "ssl": (
        "  SSL/TLS error — browser commands cannot verify certificates.\n"
        "  Fix: pkg install ca-certificates\n"
        "  Verify: doctor"
    ),
    "ca-certificates": (
        "  SSL/TLS error — browser commands cannot verify certificates.\n"
        "  Fix: pkg install ca-certificates\n"
        "  Verify: doctor"
    ),
}

# ── Advisor ───────────────────────────────────────────────────────────────────


class Advisor:
    """
    Produces short, safe, advisory next-step suggestions.

    Usage:
        advisor = Advisor()
        hints = advisor.suggest(intent="list_media", result=result, ctx=ctx)
        for h in hints: print(h)
    """

    def suggest(
        self,
        intent: str,
        result: "ExecutionResult",
        ctx: "ContextMemory",
    ) -> list[str]:
        """
        Return a list of advisory suggestion strings (may be empty).

        Never raises — all errors are silently absorbed.
        Advisory text only — no side effects.
        """
        try:
            return self._build_suggestions(intent, result, ctx)
        except Exception:
            return []

    def _build_suggestions(
        self,
        intent: str,
        result: "ExecutionResult",
        ctx: "ContextMemory",
    ) -> list[str]:
        from agent.models import OperationStatus

        suggestions: list[str] = []

        # ── Environment failure hints ─────────────────────────────────────────
        all_text = (result.message + " " + " ".join(result.errors)).lower()
        for keyword, hint in _ENV_HINTS.items():
            if keyword in all_text:
                suggestions.append(hint)
                break  # one environment hint is enough

        # ── Doctor — parse result to surface actionable hints -────────────────
        if intent == "doctor":
            if result.status == OperationStatus.SUCCESS:
                raw = result.raw_results[0] if result.raw_results else {}
                overall = raw.get("overall", "ok")
                checks = raw.get("checks", [])
                for check in checks:
                    name = check.get("name", "").lower()
                    status = check.get("status", "")
                    if status in ("missing", "error"):
                        if "ffmpeg" in name:
                            suggestions.append(
                                "  ffmpeg is missing — install it:\n"
                                "    pkg install ffmpeg"
                            )
                        elif "pillow" in name:
                            suggestions.append(
                                "  Pillow is missing — install it:\n"
                                "    pip install Pillow"
                            )
                        elif "termux" in name:
                            suggestions.append(
                                "  termux-api is missing — install it:\n"
                                "    pkg install termux-api"
                            )
                        elif "storage" in name or "sdcard" in name:
                            suggestions.append(
                                "  Storage access issue — check Android permissions for Termux."
                            )
            return suggestions

        # ── Only continue for successful results ──────────────────────────────
        if result.status != OperationStatus.SUCCESS:
            return suggestions

        # ── Per-intent suggestions ────────────────────────────────────────────
        path = ctx.last_source_path
        url = ctx.last_url

        for template, needs_path, needs_url in _SUGGESTIONS.get(intent, []):
            if needs_path and not path:
                continue
            if needs_url and not url:
                continue
            try:
                hint = template.format(path=path or "", url=url or "")
                suggestions.append(f"  {hint}")
            except Exception:
                continue

        return suggestions
