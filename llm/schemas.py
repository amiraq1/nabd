"""
Typed structured output schemas for all LLM backend responses.
Using dataclasses for explicit, inspectable types.
"""
from __future__ import annotations
from dataclasses import dataclass, field


@dataclass
class CommandSuggestion:
    """Result of suggest_command — advisory only, never auto-executed."""
    suggested_command: str
    rationale: str
    confidence: float   # 0.0–1.0; rule-based match score, not neural probability


@dataclass
class Clarification:
    """Result of clarify_request — a single focused question."""
    clarification_needed: bool
    clarification_question: str | None
    candidate_intents: list[str] = field(default_factory=list)


@dataclass
class IntentSuggestion:
    """
    Result of suggest_intent — must only name intents from the allowed whitelist.
    intent=None means no confident match was found.
    """
    intent: str | None
    confidence: float   # 0.0–1.0
    explanation: str


@dataclass
class ResultExplanation:
    """Result of explain_result — plain-English summary of the last operation."""
    summary: str
    safety_note: str | None
    suggested_next_step: str | None


@dataclass
class BackendStatus:
    """
    Structured health report for an LLM backend.

    Core fields (v1.0):
      available    — can the backend currently handle requests?
      backend_name — "local", "llama_cpp", "ollama", etc.
      transport    — "server", "cli", or None (local has no transport)
      healthy      — available AND no config errors
      detail       — human-readable one-line status message

    Extended fields (v1.1):
      endpoint         — HTTP endpoint for server-based backends (None for local/cli)
      timeout_seconds  — configured timeout in seconds (None if not applicable)
      model_name       — active model name or identifier (None if not applicable)
      capabilities     — list of supported method names (advisory methods only)
      troubleshooting  — plain-text troubleshooting hint when not available; None if healthy
    """
    available: bool
    backend_name: str
    transport: str | None
    healthy: bool
    detail: str
    # v1.1 additions — all optional, default-safe for backward compatibility
    endpoint: str | None = None
    timeout_seconds: int | None = None
    model_name: str | None = None
    capabilities: list[str] = field(default_factory=list)
    troubleshooting: str | None = None
