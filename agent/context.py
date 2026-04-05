"""
Nabd v1.0 — Narrow, safe context memory.

What it tracks (updated only on successful non-AI commands):
  last_intent        — intent name of the last successful run
  last_command       — raw text of the last successful command
  last_result_msg    — result message of the last successful command
  last_source_path   — filesystem path from the last successful read-path command
  last_url           — URL from the last successful browser command

What it does NOT do:
  - carry over failed, blocked, or cancelled commands as context
  - infer parameters from vague references
  - resolve "it" when the referent is ambiguous
  - bypass allowed-roots path validation at resolution time
  - resolve context references for mutating operations

Context resolution rules:
  - only explicit follow-up phrases trigger substitution
  - "explain that / that result / that error" are passed through to the AI intent
  - mutating verbs always require explicit operands — no context substitution
  - "it" resolves only when exactly one type of context (path OR url) is available
  - stored paths are revalidated via allowed-roots before substitution
"""
from __future__ import annotations

import re
from typing import Optional

from core.exceptions import ValidationError


# ── Intent classification ─────────────────────────────────────────────────────

# Intents whose source_path becomes canonical folder context.
# Only read-path and inspection intents are included.
_PATH_CONTEXT_INTENTS: frozenset[str] = frozenset({
    "show_files",
    "show_folders",
    "list_media",
    "find_duplicates",
    "list_large_files",
    "storage_report",
    "compress_images",          # preview / dry-run result is safe as context
    "organize_folder_by_type",  # preview / dry-run result is safe as context
    "backup_folder",            # source path is safe for follow-up inspection
})

# Intents whose URL becomes canonical URL context.
_URL_CONTEXT_INTENTS: frozenset[str] = frozenset({
    "browser_page_title",
    "browser_extract_text",
    "browser_list_links",
    "open_url",
    "browser_search",
})

# Intents that must NOT update context (meta commands + skill queries/runs).
_CONTEXT_SKIP_INTENTS: frozenset[str] = frozenset({
    "ai_suggest_command",
    "ai_explain_last_result",
    "ai_clarify_request",
    "ai_backend_status",
    "show_skills",
    "skill_info",
    "run_skill",
})

# ── Reference patterns ────────────────────────────────────────────────────────

# Explicit path follow-up phrases — "that folder", "same path", etc.
_EXPLICIT_PATH_REF = re.compile(
    r"\b(that\s+(?:folder|directory|dir|path)|same\s+(?:folder|directory|dir|path))\b",
    re.IGNORECASE,
)

# Explicit URL follow-up phrases — "that url", "same link", etc.
_EXPLICIT_URL_REF = re.compile(
    r"\b(that\s+(?:url|link|site|page)|same\s+(?:url|link|site|page))\b",
    re.IGNORECASE,
)

# "it" alone — disambiguated at resolution time.
# Hyphenated terms like "it-projects" are treated as ordinary text, not context.
_IT_REF = re.compile(r"(?<![\w-])it(?![\w-])", re.IGNORECASE)

# "explain that / that result / that error" → handled by ai_explain_last_result;
# do not intercept these.
_AI_EXPLAIN_PASSTHROUGH = re.compile(
    r"\b(explain\s+that|that\s+result|that\s+error|explain\s+last)\b",
    re.IGNORECASE,
)

# Mutating verb patterns — these commands must not use stored context operands.
# Use prefix stems instead of full words to match "organise", "organize", etc.
_MUTATING_VERBS = re.compile(
    r"\b(move|back\s*up|backup|rename|compress|organi[sz]e?|convert)\b",
    re.IGNORECASE,
)


# ── ContextMemory ─────────────────────────────────────────────────────────────

class ContextMemory:
    """
    Short-term session context for Nabd.

    Updated after every successful non-AI command.
    Resolved only when the user uses an explicit follow-up phrase.
    """

    def __init__(self) -> None:
        self.last_intent: Optional[str] = None
        self.last_command: str = ""
        self.last_result_msg: str = ""
        self.last_source_path: Optional[str] = None
        self.last_url: Optional[str] = None

    # ── Update ────────────────────────────────────────────────────────────────

    def update(
        self,
        intent: str,
        command: str,
        result_msg: str,
        source_path: Optional[str] = None,
        url: Optional[str] = None,
        *,
        success: bool = True,
    ) -> None:
        """
        Update context after a command completes.

        Rules:
          - Always updates last_intent, last_command, last_result_msg.
          - Updates last_source_path only on success AND for a path-context intent.
          - Updates last_url only on success AND for a URL-context intent.
          - Skips AI meta-commands and skill-registry queries entirely.
          - Failed or blocked commands do not become canonical path/url context.
        """
        if intent in _CONTEXT_SKIP_INTENTS:
            return

        self.last_intent = intent
        self.last_command = command
        self.last_result_msg = result_msg

        if success and source_path and intent in _PATH_CONTEXT_INTENTS:
            self.last_source_path = source_path

        if success and url and intent in _URL_CONTEXT_INTENTS:
            self.last_url = url

    # ── Resolve ───────────────────────────────────────────────────────────────

    def resolve(self, command: str) -> str:
        """
        Scan command for explicit context reference phrases and substitute them.

        Returns:
          The original command (unchanged) if no reference phrase is found.
          The substituted command if exactly one reference resolves unambiguously.

        Raises:
          ValidationError — when a reference is found but:
            - context is unavailable for that reference type, OR
            - "it" is ambiguous (both path and url in context), OR
            - the command contains a mutating verb (must specify explicitly).

        Passes through without substitution:
          - "explain that / that result / that error" (handled by AI intent).
        """
        # Fast path — no reference phrase present
        has_path_ref = bool(_EXPLICIT_PATH_REF.search(command))
        has_url_ref = bool(_EXPLICIT_URL_REF.search(command))
        has_it_ref = bool(_IT_REF.search(command))

        if not (has_path_ref or has_url_ref or has_it_ref):
            return command

        # AI explain passthrough — do not intercept
        if _AI_EXPLAIN_PASSTHROUGH.search(command):
            return command

        # Mutating operations must specify operands explicitly
        if _MUTATING_VERBS.search(command) and (has_path_ref or has_url_ref or has_it_ref):
            raise ValidationError(
                "Please specify the path explicitly for this operation.\n"
                "  Nabd does not apply stored context to mutating commands.\n"
                "  Example: back up /sdcard/Documents to /sdcard/Backup"
            )

        # ── Explicit path reference ───────────────────────────────────────────
        if has_path_ref:
            if not self.last_source_path:
                raise ValidationError(
                    "No folder context is available yet.\n"
                    "  Run a folder command first, then use 'that folder'.\n"
                    "  Example: show files in /sdcard/Download\n"
                    "           list media in that folder"
                )
            self._revalidate_path()
            resolved = _EXPLICIT_PATH_REF.sub(self.last_source_path, command)
            return resolved

        # ── Explicit URL reference ────────────────────────────────────────────
        if has_url_ref:
            if not self.last_url:
                raise ValidationError(
                    "No URL context is available yet.\n"
                    "  Fetch a URL first, then use 'that url'.\n"
                    "  Example: show page title from https://example.com\n"
                    "           extract text from that url"
                )
            resolved = _EXPLICIT_URL_REF.sub(self.last_url, command)
            return resolved

        # ── "it" — resolve only when unambiguous ─────────────────────────────
        if has_it_ref:
            has_path_ctx = bool(self.last_source_path)
            has_url_ctx = bool(self.last_url)

            if has_path_ctx and has_url_ctx:
                raise ValidationError(
                    "'it' is ambiguous — I have both a folder and a URL in context.\n"
                    "  Please be specific:\n"
                    "    use 'that folder' to refer to the last folder, or\n"
                    "    use 'that url' to refer to the last URL."
                )
            if has_path_ctx:
                self._revalidate_path()
                return _IT_REF.sub(self.last_source_path, command)
            if has_url_ctx:
                return _IT_REF.sub(self.last_url, command)

            raise ValidationError(
                "'it' has no context to refer to.\n"
                "  Run a folder or URL command first."
            )

        return command  # unreachable, but satisfies the linter

    # ── Internal helpers ──────────────────────────────────────────────────────

    def _revalidate_path(self) -> None:
        """
        Verify that the stored path still passes allowed-roots validation.
        Clears last_source_path and raises ValidationError if it no longer passes.
        """
        if not self.last_source_path:
            return
        try:
            from core.paths import validate_path
            validate_path(self.last_source_path)
        except Exception:
            stale = self.last_source_path
            self.last_source_path = None
            raise ValidationError(
                f"The stored folder '{stale}' is no longer accessible or allowed.\n"
                "  Please specify the folder explicitly."
            )

    # ── Representation ────────────────────────────────────────────────────────

    def __repr__(self) -> str:  # pragma: no cover
        return (
            f"ContextMemory("
            f"intent={self.last_intent!r}, "
            f"path={self.last_source_path!r}, "
            f"url={self.last_url!r})"
        )
