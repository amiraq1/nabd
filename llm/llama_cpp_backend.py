"""
LlamaCppBackend — llama.cpp HTTP server backend for Nabd AI Assist.

Connects to a local llama.cpp server via its OpenAI-compatible API.
Uses stdlib only (urllib, json, socket) — no new dependencies.

Advisory only — never executes actions.
All outputs are requested as strict JSON and fully validated before use.
Any failure (timeout, unavailable, invalid JSON) returns a safe fallback,
never propagates an exception to the caller.
"""
from __future__ import annotations

import json
import socket
import urllib.error
import urllib.request
from typing import Any

from llm.backend import LLMBackend
from llm.prompts import (
    LLAMA_SYSTEM_PROMPT,
    SUGGEST_COMMAND_JSON_TEMPLATE,
    EXPLAIN_RESULT_JSON_TEMPLATE,
    CLARIFY_REQUEST_JSON_TEMPLATE,
    SUGGEST_INTENT_JSON_TEMPLATE,
)
from llm.schemas import CommandSuggestion, Clarification, IntentSuggestion, ResultExplanation

_HEALTH_TIMEOUT = 3          # seconds for availability probe
_DEFAULT_TIMEOUT = 30        # default request timeout if not configured


class LlamaCppBackend(LLMBackend):
    """
    llama.cpp HTTP server backend.

    Expects a llama.cpp server running with --server mode, accessible at
    server_url (default: http://localhost:8080).

    Uses the OpenAI-compatible /v1/chat/completions endpoint.
    All failures degrade gracefully to a safe fallback response.
    """

    def __init__(
        self,
        server_url: str = "http://localhost:8080",
        timeout_seconds: int = _DEFAULT_TIMEOUT,
        model_name: str | None = None,
    ) -> None:
        self._server_url = server_url.rstrip("/")
        self._timeout = int(timeout_seconds)
        self._model = model_name or "local-model"

    # ── Availability ──────────────────────────────────────────────────────────

    def is_available(self) -> bool:
        """
        Probe the llama.cpp server with a lightweight GET /health request.
        Returns False on any error — never raises.
        """
        try:
            req = urllib.request.Request(
                f"{self._server_url}/health",
                method="GET",
            )
            with urllib.request.urlopen(req, timeout=_HEALTH_TIMEOUT) as resp:
                return resp.status == 200
        except Exception:
            return False

    # ── Internal HTTP call ────────────────────────────────────────────────────

    def _chat(self, system: str, user: str) -> dict[str, Any]:
        """
        POST a chat completion request to /v1/chat/completions.

        Returns the parsed JSON dict from the model's reply content.

        Raises:
          TimeoutError    — server took longer than timeout_seconds
          ConnectionError — server is unreachable
          ValueError      — response is not valid JSON or structure is unexpected
        """
        payload = {
            "model": self._model,
            "messages": [
                {"role": "system", "content": system},
                {"role": "user", "content": user},
            ],
            "temperature": 0.1,
            "response_format": {"type": "json_object"},
        }
        data = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(
            f"{self._server_url}/v1/chat/completions",
            data=data,
            headers={"Content-Type": "application/json"},
        )

        try:
            with urllib.request.urlopen(req, timeout=self._timeout) as resp:
                raw = resp.read().decode("utf-8")
        except socket.timeout:
            raise TimeoutError(
                f"llama.cpp server timed out after {self._timeout}s. "
                "Try increasing timeout_seconds in config/ai_assist.json."
            )
        except urllib.error.URLError as e:
            raise ConnectionError(
                f"llama.cpp server unavailable at {self._server_url}: {e.reason}"
            )

        try:
            outer = json.loads(raw)
            content = outer["choices"][0]["message"]["content"]
            result = json.loads(content)
            if not isinstance(result, dict):
                raise ValueError(f"Expected JSON object, got {type(result).__name__}")
            return result
        except (json.JSONDecodeError, KeyError, IndexError, TypeError) as e:
            raise ValueError(
                f"Invalid response from llama.cpp: {e!r} | "
                f"raw (first 200 chars): {raw[:200]}"
            )

    # ── Advisory methods ──────────────────────────────────────────────────────

    def suggest_command(
        self,
        user_text: str,
        available_intents: list[str],
    ) -> CommandSuggestion:
        """
        Ask the model for the best Nabd command matching user_text.
        Always returns a CommandSuggestion — falls back to 'doctor' on any error.
        """
        intent_list = "\n".join(f"- {i}" for i in available_intents)
        user_prompt = SUGGEST_COMMAND_JSON_TEMPLATE.format(
            user_text=user_text,
            available_intents=intent_list,
        )
        try:
            data = self._chat(LLAMA_SYSTEM_PROMPT, user_prompt)
            cmd = str(data.get("suggested_command", "")).strip()
            rationale = str(data.get("rationale", "")).strip()
            confidence = float(data.get("confidence", 0.0))
            confidence = max(0.0, min(1.0, confidence))
            if not cmd:
                raise ValueError("'suggested_command' field is empty")
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
        """
        Ask the model for a plain-English explanation of the last result.
        Falls back gracefully if the server is unavailable or returns bad output.
        """
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
            summary = str(data.get("summary", "")).strip()
            safety_note = data.get("safety_note") or None
            if safety_note:
                safety_note = str(safety_note).strip() or None
            next_step = data.get("suggested_next_step") or None
            if next_step:
                next_step = str(next_step).strip() or None
            if not summary:
                raise ValueError("'summary' field is empty")
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
        """
        Ask the model for a focused clarification question.
        Falls back gracefully on any error.
        """
        intent_list = "\n".join(f"- {i}" for i in available_intents)
        user_prompt = CLARIFY_REQUEST_JSON_TEMPLATE.format(
            user_text=user_text,
            available_intents=intent_list,
        )
        try:
            data = self._chat(LLAMA_SYSTEM_PROMPT, user_prompt)
            needed = bool(data.get("clarification_needed", True))
            question = str(data.get("clarification_question", "")).strip() or None
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
        Ask the model for the best matching intent from allowed_intents.
        Applies safety gate: any intent not in allowed_intents is discarded.
        Falls back gracefully on any error.
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
                intent = str(intent).strip()
            confidence = float(data.get("confidence", 0.0))
            confidence = max(0.0, min(1.0, confidence))
            explanation = str(data.get("explanation", "")).strip()

            # Safety gate — discard any intent not in the allowed whitelist
            if intent and intent not in allowed_intents:
                return IntentSuggestion(
                    intent=None,
                    confidence=0.0,
                    explanation=(
                        f"Model suggested '{intent}' which is not in the "
                        "Nabd whitelist — discarded for safety."
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


# ── Private fallback helpers ──────────────────────────────────────────────────

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
        suggested_next_step="Check that the llama.cpp server is running.",
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
