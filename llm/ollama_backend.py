"""
Ollama backend for Nabd AI Assist.

Communicates with a running Ollama server via its /api/chat endpoint.
Advisory-only — never executes tool actions, never bypasses safety checks.

To enable:
  1. Install Ollama: https://ollama.ai
  2. Start the server: ollama serve
  3. Pull a model:    ollama pull llama3
  4. Edit config/ai_assist.json:
       "backend": "ollama",
       "ollama": {
           "endpoint": "http://127.0.0.1:11434",
           "model": "llama3",
           "timeout_seconds": 30
       }

Ollama API reference:
  POST /api/chat
    Request:  {"model": "...", "messages": [...], "stream": false}
    Response: {"model": "...", "message": {"role": "assistant", "content": "..."}, "done": true}
"""
from __future__ import annotations

import json
import urllib.error
import urllib.request
from typing import Any

from llm.backend import LLMBackend
from llm.prompts import (
    CLARIFY_REQUEST_JSON_TEMPLATE,
    EXPLAIN_RESULT_JSON_TEMPLATE,
    LLAMA_SYSTEM_PROMPT,
    SUGGEST_COMMAND_JSON_TEMPLATE,
    SUGGEST_INTENT_JSON_TEMPLATE,
)
from llm.schemas import (
    BackendStatus,
    Clarification,
    CommandSuggestion,
    IntentSuggestion,
    ResultExplanation,
)

_DEFAULT_ENDPOINT = "http://127.0.0.1:11434"
_DEFAULT_MODEL = "llama3"
_DEFAULT_TIMEOUT = 30
_HEALTH_TIMEOUT = 3
_LOW_CONFIDENCE_THRESHOLD = 0.4

_ADVISORY_CAPABILITIES: list[str] = [
    "suggest_command",
    "explain_result",
    "clarify_request",
    "suggest_intent",
]


class OllamaBackend(LLMBackend):
    """
    Ollama backend with HTTP server transport.

    Uses Ollama's /api/chat endpoint (POST).  All responses must be JSON
    objects matching the Nabd advisory schema.  Advisory-only — this class
    must never execute tools or bypass safety validation.
    """

    def __init__(
        self,
        endpoint: str = _DEFAULT_ENDPOINT,
        model: str = _DEFAULT_MODEL,
        timeout_seconds: int = _DEFAULT_TIMEOUT,
    ) -> None:
        self._endpoint = endpoint.rstrip("/")
        self._model = model
        self._timeout = int(timeout_seconds)

    # ── Availability ──────────────────────────────────────────────────────────

    def is_available(self) -> bool:
        """
        Probe the Ollama server.  Never raises.
        Checks GET /api/tags — returns 200 when Ollama is running.
        """
        try:
            req = urllib.request.Request(
                f"{self._endpoint}/api/tags",
                method="GET",
            )
            with urllib.request.urlopen(req, timeout=_HEALTH_TIMEOUT) as resp:
                return resp.status == 200
        except Exception:
            return False

    def get_status(self) -> BackendStatus:
        """Return a structured health report.  Never raises."""
        try:
            available = self.is_available()
        except Exception:
            available = False

        if available:
            detail = (
                f"Ollama server running at {self._endpoint}  "
                f"model: {self._model}"
            )
            troubleshooting = None
        else:
            detail = f"Ollama server not responding at {self._endpoint}"
            troubleshooting = (
                f"Start Ollama: ollama serve\n"
                f"  Pull model: ollama pull {self._model}\n"
                f"  Install:    https://ollama.ai"
            )

        return BackendStatus(
            available=available,
            backend_name="ollama",
            transport="server",
            healthy=available,
            detail=detail,
            endpoint=self._endpoint,
            timeout_seconds=self._timeout,
            model_name=self._model,
            capabilities=list(_ADVISORY_CAPABILITIES),
            troubleshooting=troubleshooting,
        )

    # ── Internal HTTP transport ───────────────────────────────────────────────

    def _chat(self, system: str, user: str) -> dict[str, Any]:
        """
        POST to /api/chat and return the parsed JSON content dict.

        Raises:
          TimeoutError    — server took longer than timeout_seconds
          ConnectionError — server is unreachable or responded with an error
          ValueError      — response is not valid JSON or structure unexpected
        """
        payload = json.dumps({
            "model": self._model,
            "messages": [
                {"role": "system", "content": system},
                {"role": "user",   "content": user},
            ],
            "stream": False,
        }).encode()

        req = urllib.request.Request(
            f"{self._endpoint}/api/chat",
            data=payload,
            method="POST",
            headers={"Content-Type": "application/json"},
        )

        try:
            with urllib.request.urlopen(req, timeout=self._timeout) as resp:
                raw = resp.read().decode()
        except urllib.error.URLError as e:
            reason = getattr(e, "reason", e)
            if "timed out" in str(reason).lower():
                raise TimeoutError(
                    f"Ollama at {self._endpoint} did not respond within "
                    f"{self._timeout}s. Is 'ollama serve' running?"
                )
            raise ConnectionError(
                f"Cannot reach Ollama at {self._endpoint}: {reason}. "
                "Run 'ollama serve' to start the server."
            )
        except OSError as e:
            if "timed out" in str(e).lower():
                raise TimeoutError(str(e))
            raise ConnectionError(str(e))

        return _parse_ollama_response(raw)

    # ── Advisory methods ──────────────────────────────────────────────────────

    def suggest_command(
        self,
        user_text: str,
        available_intents: list[str],
    ) -> CommandSuggestion:
        """Suggest the best Nabd command.  Falls back gracefully on any error."""
        intent_list = "\n".join(f"- {i}" for i in available_intents)
        user_prompt = SUGGEST_COMMAND_JSON_TEMPLATE.format(
            user_text=user_text,
            available_intents=intent_list,
        )
        try:
            data = self._chat(LLAMA_SYSTEM_PROMPT, user_prompt)
            cmd = _coerce_str(data.get("suggested_command"))
            rationale = _coerce_str(data.get("rationale"))
            confidence = float(data.get("confidence") or 0.0)
            confidence = max(0.0, min(1.0, confidence))
            if not cmd:
                raise ValueError("'suggested_command' field is empty or null")
            return CommandSuggestion(
                suggested_command=cmd,
                rationale=rationale or "No rationale provided.",
                confidence=round(confidence, 2),
            )
        except TimeoutError as e:
            return _fallback_suggestion(f"Request timed out: {e}")
        except ConnectionError as e:
            return _fallback_suggestion(str(e))
        except (ValueError, KeyError) as e:
            return _fallback_suggestion(f"Response error: {e}")
        except Exception as e:
            return _fallback_suggestion(f"Unexpected error: {e}")

    def explain_result(
        self,
        last_command: str,
        last_result: str,
    ) -> ResultExplanation:
        """Explain the last result in plain English.  Falls back gracefully."""
        if not last_command:
            return ResultExplanation(
                summary="No previous command found in this session.",
                safety_note=None,
                suggested_next_step="Run a command first, then ask 'explain last result'.",
            )
        user_prompt = EXPLAIN_RESULT_JSON_TEMPLATE.format(
            last_command=last_command,
            last_result=last_result or "(no output captured)",
        )
        try:
            data = self._chat(LLAMA_SYSTEM_PROMPT, user_prompt)
            summary = _coerce_str(data.get("summary"))
            safety_note = _coerce_str(data.get("safety_note")) or None
            next_step = _coerce_str(data.get("suggested_next_step")) or None
            if not summary:
                raise ValueError("'summary' field is empty or null")
            return ResultExplanation(
                summary=summary,
                safety_note=safety_note,
                suggested_next_step=next_step,
            )
        except TimeoutError as e:
            return _fallback_explanation(last_command, f"Request timed out: {e}")
        except ConnectionError as e:
            return _fallback_explanation(last_command, str(e))
        except (ValueError, KeyError) as e:
            return _fallback_explanation(last_command, f"Response error: {e}")
        except Exception as e:
            return _fallback_explanation(last_command, f"Unexpected error: {e}")

    def clarify_request(
        self,
        user_text: str,
        available_intents: list[str],
    ) -> Clarification:
        """Ask one clarification question.  Falls back gracefully."""
        intent_list = "\n".join(f"- {i}" for i in available_intents)
        user_prompt = CLARIFY_REQUEST_JSON_TEMPLATE.format(
            user_text=user_text,
            available_intents=intent_list,
        )
        try:
            data = self._chat(LLAMA_SYSTEM_PROMPT, user_prompt)
            needed_raw = data.get("clarification_needed")
            needed = True if needed_raw is None else bool(needed_raw)
            question = _coerce_str(data.get("clarification_question")) or None
            raw_candidates = data.get("candidate_intents", [])
            if not isinstance(raw_candidates, list):
                raw_candidates = []
            candidates = [c for c in raw_candidates if c in available_intents][:3]
            return Clarification(
                clarification_needed=needed,
                clarification_question=question,
                candidate_intents=candidates,
            )
        except TimeoutError as e:
            return _fallback_clarification(f"Request timed out: {e}")
        except ConnectionError as e:
            return _fallback_clarification(str(e))
        except (ValueError, KeyError) as e:
            return _fallback_clarification(f"Response error: {e}")
        except Exception as e:
            return _fallback_clarification(f"Unexpected error: {e}")

    def suggest_intent(
        self,
        user_text: str,
        allowed_intents: list[str],
    ) -> IntentSuggestion:
        """
        Return the best-matching intent from allowed_intents.
        Applies safety gate: any intent not in allowed_intents is discarded.
        """
        intent_list = "\n".join(f"- {i}" for i in allowed_intents)
        user_prompt = SUGGEST_INTENT_JSON_TEMPLATE.format(
            user_text=user_text,
            allowed_intents=intent_list,
        )
        try:
            data = self._chat(LLAMA_SYSTEM_PROMPT, user_prompt)
            intent = data.get("intent") or None
            if intent:
                intent = _coerce_str(intent) or None
            confidence = float(data.get("confidence") or 0.0)
            confidence = max(0.0, min(1.0, confidence))
            explanation = _coerce_str(data.get("explanation"))

            # Safety gate 1 — discard any intent not in the whitelist
            if intent and intent not in allowed_intents:
                return IntentSuggestion(
                    intent=None,
                    confidence=0.0,
                    explanation=(
                        f"Model suggested '{intent}' which is not in the "
                        "Nabd whitelist — discarded for safety."
                    ),
                )

            # Safety gate 2 — low-confidence gate
            if intent and confidence < _LOW_CONFIDENCE_THRESHOLD:
                return IntentSuggestion(
                    intent=None,
                    confidence=round(confidence, 2),
                    explanation=(
                        f"Model returned confidence {confidence:.2f} for '{intent}', "
                        f"below threshold {_LOW_CONFIDENCE_THRESHOLD} — "
                        "treated as no confident match."
                    ),
                )

            return IntentSuggestion(
                intent=intent,
                confidence=round(confidence, 2),
                explanation=explanation or "No explanation provided.",
            )
        except TimeoutError as e:
            return _no_intent(f"Request timed out: {e}")
        except ConnectionError as e:
            return _no_intent(str(e))
        except (ValueError, KeyError) as e:
            return _no_intent(f"Response error: {e}")
        except Exception as e:
            return _no_intent(f"Unexpected error: {e}")


# ── Output parsing ────────────────────────────────────────────────────────────

def _parse_ollama_response(raw: str) -> dict[str, Any]:
    """
    Parse an Ollama /api/chat response and return the content dict.

    Ollama response shape:
      {"model": "...", "message": {"role": "assistant", "content": "..."}, "done": true}

    The content field must be a JSON object matching the Nabd advisory schema.
    Handles both JSON-string content and pre-parsed dict content.
    """
    try:
        outer = json.loads(raw)
        content = outer["message"]["content"]
        if isinstance(content, dict):
            return content
        result = json.loads(content)
        if not isinstance(result, dict):
            raise ValueError(f"Expected JSON object, got {type(result).__name__}")
        return result
    except (json.JSONDecodeError, KeyError, IndexError, TypeError) as e:
        raise ValueError(
            f"Invalid response from Ollama: {e!r} | "
            f"raw (first 200 chars): {raw[:200]}"
        )


def _coerce_str(value: Any, default: str = "") -> str:
    if value is None:
        return default
    return str(value).strip()


# ── Fallback helpers ──────────────────────────────────────────────────────────

def _fallback_suggestion(reason: str) -> CommandSuggestion:
    return CommandSuggestion(
        suggested_command="doctor",
        rationale=(
            f"AI backend unavailable ({reason}). "
            "'doctor' checks your environment and is always a safe starting point."
        ),
        confidence=0.0,
    )


def _fallback_explanation(last_command: str, reason: str) -> ResultExplanation:
    return ResultExplanation(
        summary=f"You ran: '{last_command}'. (AI explanation unavailable: {reason})",
        safety_note=None,
        suggested_next_step="Check that the Ollama server is running: ollama serve",
    )


def _fallback_clarification(reason: str) -> Clarification:
    return Clarification(
        clarification_needed=True,
        clarification_question=(
            f"AI backend unavailable ({reason}). "
            "Type 'help' to see all supported Nabd commands."
        ),
        candidate_intents=[],
    )


def _no_intent(reason: str) -> IntentSuggestion:
    return IntentSuggestion(
        intent=None,
        confidence=0.0,
        explanation=f"AI backend unavailable: {reason}",
    )
