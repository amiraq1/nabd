"""
AI Assist Skill — Nabd's optional advisory intelligence layer.

What it IS:
  - A command suggester (LocalBackend: deterministic; LlamaCppBackend: LLM)
  - A plain-English result explainer
  - A request clarifier

What it is NOT:
  - An executor — it never calls tool functions
  - An autonomous agent — it never chains actions
  - A shell — it never generates arbitrary commands
  - A bypass for safety validation or confirmation rules

Backends:
  - "local"     — deterministic keyword matching, always available (default)
  - "llama_cpp" — local llama.cpp HTTP server, advisory-only, optional
"""
from __future__ import annotations
import json
import os
from typing import Any

from skills.base import SkillBase, SkillInfo
from llm.schemas import CommandSuggestion, Clarification, IntentSuggestion, ResultExplanation

_CONFIG_PATH = os.path.join(
    os.path.dirname(__file__), "..", "config", "ai_assist.json"
)

# Nabd's deterministic intent whitelist — AI must never suggest outside this list.
AVAILABLE_INTENTS: list[str] = [
    "doctor",
    "storage_report",
    "list_large_files",
    "show_files",
    "show_folders",
    "list_media",
    "organize_folder_by_type",
    "find_duplicates",
    "backup_folder",
    "convert_video_to_mp3",
    "compress_images",
    "safe_rename_files",
    "safe_move_files",
    "open_url",
    "open_file",
    "open_app",
    "phone_status_battery",
    "phone_status_network",
    "browser_search",
    "browser_extract_text",
    "browser_list_links",
    "browser_page_title",
]


def _load_config() -> dict:
    try:
        with open(_CONFIG_PATH) as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return {
            "enabled": False,
            "backend": "local",
            "mode": "assist_only",
            "fallback_intent_suggestion": False,
            "llama_cpp": {
                "transport": "server",
                "endpoint": "http://127.0.0.1:8080",
                "binary_path": "",
                "model_path": "",
                "timeout_seconds": 20,
                "max_tokens": 256,
                "temperature": 0.2,
            },
        }


class AIAssistSkill(SkillBase):
    name = "ai_assist"
    description = (
        "Advisory AI that suggests, explains, and clarifies Nabd commands. "
        "Never executes actions on your behalf."
    )
    version = "0.2.0"

    def __init__(self) -> None:
        config = _load_config()
        self.enabled: bool = config.get("enabled", False)
        self.mode: str = config.get("mode", "assist_only")
        self.backend_name: str = config.get("backend", "local")
        self.fallback_intent_suggestion: bool = config.get(
            "fallback_intent_suggestion", False
        )
        self._llama_cfg: dict = config.get("llama_cpp", {})
        self._backend = None

    def _get_backend(self):
        if self._backend is None:
            if self.backend_name == "llama_cpp":
                from llm.llama_cpp_backend import LlamaCppBackend
                cfg = self._llama_cfg
                # Support both "endpoint" (spec) and "server_url" (legacy) config keys
                endpoint = (
                    cfg.get("endpoint")
                    or cfg.get("server_url")
                    or "http://127.0.0.1:8080"
                )
                self._backend = LlamaCppBackend(
                    endpoint=endpoint,
                    transport=cfg.get("transport", "server"),
                    timeout_seconds=cfg.get("timeout_seconds", 20),
                    max_tokens=cfg.get("max_tokens", 256),
                    temperature=cfg.get("temperature", 0.2),
                    model_name=cfg.get("model_name"),
                    binary_path=cfg.get("binary_path", ""),
                    model_path=cfg.get("model_path", ""),
                )
            else:
                from llm.local_backend import LocalBackend
                self._backend = LocalBackend()
        return self._backend

    def get_info(self) -> SkillInfo:
        return SkillInfo(
            name=self.name,
            description=self.description,
            version=self.version,
            enabled=self.enabled,
            tags=["ai", "advisory", "offline", "deterministic"],
        )

    def get_backend_status(self) -> dict[str, Any]:
        """
        Return a status dict describing the active backend and its availability.
        Safe to call whether or not AI Assist is enabled.
        Delegates to backend.get_status() for structured, transport-aware reporting.
        """
        try:
            backend = self._get_backend()
            status = backend.get_status()
        except Exception as e:
            return {
                "backend": self.backend_name,
                "available": False,
                "enabled": self.enabled,
                "transport": None,
                "detail": f"Error initialising backend: {e}",
            }

        result: dict[str, Any] = {
            "backend": status.backend_name,
            "available": status.available,
            "enabled": self.enabled,
            "transport": status.transport,
            "healthy": status.healthy,
            "detail": status.detail,
        }

        if self.backend_name == "llama_cpp":
            cfg = self._llama_cfg
            endpoint = (
                cfg.get("endpoint")
                or cfg.get("server_url")
                or "http://127.0.0.1:8080"
            )
            result["endpoint"] = endpoint
            result["server_url"] = endpoint       # backward-compat alias
            result["timeout_seconds"] = cfg.get("timeout_seconds", 20)
            result["model_name"] = cfg.get("model_name", "")
            result["max_tokens"] = cfg.get("max_tokens", 256)
            result["temperature"] = cfg.get("temperature", 0.2)
            transport = cfg.get("transport", "server")
            if transport == "cli":
                result["binary_path"] = cfg.get("binary_path", "")
                result["model_path"] = cfg.get("model_path", "")

        return result

    def _check_enabled(self) -> None:
        if not self.enabled:
            raise RuntimeError(
                "AI Assist is disabled.\n"
                "  Edit config/ai_assist.json and set \"enabled\": true to activate it."
            )

    # ── Public advisory methods ───────────────────────────────────────────────

    def suggest_command(self, user_text: str) -> CommandSuggestion:
        self._check_enabled()
        return self._get_backend().suggest_command(user_text, AVAILABLE_INTENTS)

    def explain_result(
        self, last_command: str, last_result: str
    ) -> ResultExplanation:
        self._check_enabled()
        return self._get_backend().explain_result(last_command, last_result)

    def clarify_request(self, user_text: str) -> Clarification:
        self._check_enabled()
        return self._get_backend().clarify_request(user_text, AVAILABLE_INTENTS)

    def suggest_intent(self, user_text: str) -> IntentSuggestion:
        self._check_enabled()
        if not self.fallback_intent_suggestion:
            return IntentSuggestion(
                intent=None,
                confidence=0.0,
                explanation=(
                    "Fallback intent suggestion is disabled. "
                    "Edit config/ai_assist.json and set "
                    "\"fallback_intent_suggestion\": true to enable it."
                ),
            )
        suggestion = self._get_backend().suggest_intent(user_text, AVAILABLE_INTENTS)
        # Safety gate — discard any intent not in the whitelist (defense in depth)
        if suggestion.intent and suggestion.intent not in AVAILABLE_INTENTS:
            suggestion.intent = None
            suggestion.confidence = 0.0
            suggestion.explanation = (
                "Suggested intent was not in the Nabd whitelist — discarded for safety."
            )
        return suggestion
