"""
Backend registry for Nabd AI Assist.

Centralises backend loading, validation, and selection so that
AIAssistSkill no longer hard-codes instantiation logic.

Supported backends (v1.1):
  local     — deterministic keyword matcher, always available (default)
  llama_cpp — local llama.cpp server or CLI, optional

Future-ready (fully implemented in v1.1):
  ollama    — Ollama server via /api/chat, optional

The registry never executes tool actions and never bypasses the
advisory-only safety contract.
"""
from __future__ import annotations

from llm.backend import LLMBackend

# ── Backend name constants ────────────────────────────────────────────────────

KNOWN_BACKENDS: frozenset[str] = frozenset({"local", "llama_cpp", "ollama"})

_ADVISORY_CAPABILITIES: list[str] = [
    "suggest_command",
    "explain_result",
    "clarify_request",
    "suggest_intent",
]


# ── Registry ──────────────────────────────────────────────────────────────────

class BackendRegistry:
    """
    Loads, validates, and returns Nabd LLM backends.

    Usage:
        registry = BackendRegistry(config)
        backend  = registry.get_backend()
        name     = registry.get_active_name()
        names    = registry.list_backends()
    """

    def __init__(self, config: dict) -> None:
        raw_name = config.get("backend", "local")
        self._active_name: str = (raw_name or "local").lower().strip()
        self._config = config

    # ── Introspection ─────────────────────────────────────────────────────────

    def get_active_name(self) -> str:
        """Return the configured backend name (normalised to lower-case)."""
        return self._active_name

    @staticmethod
    def list_backends() -> list[str]:
        """Return all known backend names in alphabetical order."""
        return sorted(KNOWN_BACKENDS)

    @staticmethod
    def is_known(name: str) -> bool:
        """
        Return True if *name* is an exact recognised backend identifier.
        Case-sensitive — "local" is known, "Local" is not.
        Note: BackendRegistry.__init__ normalises names to lower-case,
        so config values like "LOCAL" still work at runtime.
        """
        return (name or "") in KNOWN_BACKENDS

    # ── Backend construction ──────────────────────────────────────────────────

    def get_backend(self) -> LLMBackend:
        """
        Instantiate and return the configured backend.

        Raises:
            ValueError — if the configured backend name is not in KNOWN_BACKENDS.

        Never returns None.  Never falls back silently — callers must handle
        the ValueError explicitly and decide the recovery strategy.
        """
        name = self._active_name

        if name == "local":
            return self._build_local()
        if name == "llama_cpp":
            return self._build_llama_cpp()
        if name == "ollama":
            return self._build_ollama()

        raise ValueError(
            f"Unknown AI backend: '{name}'. "
            f"Valid backends: {sorted(KNOWN_BACKENDS)}. "
            "Check the 'backend' field in config/ai_assist.json."
        )

    # ── Private builders ──────────────────────────────────────────────────────

    def _build_local(self) -> LLMBackend:
        from llm.local_backend import LocalBackend
        return LocalBackend()

    def _build_llama_cpp(self) -> LLMBackend:
        from llm.llama_cpp_backend import LlamaCppBackend
        cfg = self._config.get("llama_cpp", {})
        endpoint = (
            cfg.get("endpoint")
            or cfg.get("server_url")
            or "http://127.0.0.1:8080"
        )
        return LlamaCppBackend(
            endpoint=endpoint,
            transport=cfg.get("transport", "server"),
            timeout_seconds=cfg.get("timeout_seconds", 20),
            max_tokens=cfg.get("max_tokens", 256),
            temperature=cfg.get("temperature", 0.2),
            model_name=cfg.get("model_name"),
            binary_path=cfg.get("binary_path", ""),
            model_path=cfg.get("model_path", ""),
        )

    def _build_ollama(self) -> LLMBackend:
        from llm.ollama_backend import OllamaBackend
        cfg = self._config.get("ollama", {})
        return OllamaBackend(
            endpoint=cfg.get("endpoint", "http://127.0.0.1:11434"),
            model=cfg.get("model", "llama3"),
            timeout_seconds=cfg.get("timeout_seconds", 30),
        )
