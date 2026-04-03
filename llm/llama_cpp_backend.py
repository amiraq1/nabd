"""
LlamaCppBackend — llama.cpp backend for Nabd AI Assist.

Supports two transport modes (selected via config):
  server (default) — HTTP to a running llama.cpp server (OpenAI-compatible API).
  cli              — subprocess call to llama-cli binary (no shell, fixed arg list).

Advisory only — never executes actions.
All outputs are requested as strict JSON and fully validated before use.
Any failure (timeout, unavailable, invalid JSON, missing binary) returns a safe
fallback, never propagates an exception to the caller.

Stdlib only — urllib, json, socket, subprocess, os, tempfile.
"""
from __future__ import annotations

import json
import os
import socket
import subprocess
import urllib.error
import urllib.request
from typing import Any

from llm.backend import LLMBackend
from llm.prompts import (
    LLAMA_SYSTEM_PROMPT,
    CLARIFY_REQUEST_JSON_TEMPLATE,
    EXPLAIN_RESULT_JSON_TEMPLATE,
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

_HEALTH_TIMEOUT = 3          # seconds for availability probe
_DEFAULT_TIMEOUT = 20        # if not in config
_DEFAULT_MAX_TOKENS = 256
_DEFAULT_TEMPERATURE = 0.2
_DEFAULT_ENDPOINT = "http://127.0.0.1:8080"


class LlamaCppBackend(LLMBackend):
    """
    llama.cpp backend with server and CLI transport modes.

    Transport "server" (default):
      Connects to a running llama.cpp server via its OpenAI-compatible
      /v1/chat/completions endpoint.  Start with:
        ./server -m model.gguf --port 8080 --host 127.0.0.1

    Transport "cli":
      Runs llama-cli directly as a subprocess.  Requires binary_path and
      model_path to be configured.  Uses a fixed arg list — never passes
      user text through a shell.
    """

    def __init__(
        self,
        endpoint: str = _DEFAULT_ENDPOINT,
        transport: str = "server",
        timeout_seconds: int = _DEFAULT_TIMEOUT,
        max_tokens: int = _DEFAULT_MAX_TOKENS,
        temperature: float = _DEFAULT_TEMPERATURE,
        model_name: str | None = None,
        binary_path: str = "",
        model_path: str = "",
        # Legacy alias kept for backward-compatibility with tests that use server_url=
        server_url: str | None = None,
    ) -> None:
        # Support legacy 'server_url' kwarg so existing tests still pass
        if server_url is not None and endpoint == _DEFAULT_ENDPOINT:
            endpoint = server_url
        self._server_url = endpoint.rstrip("/")    # kept for backward-compat attr access
        self._endpoint = self._server_url
        self._transport = transport.lower() if transport else "server"
        self._timeout = int(timeout_seconds)
        self._max_tokens = int(max_tokens)
        self._temperature = float(temperature)
        self._model = model_name or "local-model"
        self._binary_path = binary_path or ""
        self._model_path = model_path or ""

    # ── Availability ──────────────────────────────────────────────────────────

    def is_available(self) -> bool:
        """
        Probe the backend.  Never raises.

        Server mode: GET /health and check status == 200.
        CLI mode:    verify binary_path and model_path exist on disk.
        """
        try:
            if self._transport == "cli":
                return (
                    bool(self._binary_path)
                    and os.path.isfile(self._binary_path)
                    and bool(self._model_path)
                    and os.path.isfile(self._model_path)
                )
            req = urllib.request.Request(
                f"{self._endpoint}/health",
                method="GET",
            )
            with urllib.request.urlopen(req, timeout=_HEALTH_TIMEOUT) as resp:
                return resp.status == 200
        except Exception:
            return False

    def get_status(self) -> BackendStatus:
        """
        Return a structured health report.  Never raises.
        """
        try:
            if self._transport == "cli":
                return self._cli_status()
            return self._server_status()
        except Exception as e:
            return BackendStatus(
                available=False,
                backend_name="llama_cpp",
                transport=self._transport,
                healthy=False,
                detail=f"Error probing backend: {e}",
            )

    def _server_status(self) -> BackendStatus:
        available = self.is_available()
        if available:
            detail = f"Connected to {self._endpoint}"
        else:
            detail = (
                f"Server at {self._endpoint} is not responding. "
                "Start with: ./server -m model.gguf --port 8080 --host 127.0.0.1"
            )
        return BackendStatus(
            available=available,
            backend_name="llama_cpp",
            transport="server",
            healthy=available,
            detail=detail,
        )

    def _cli_status(self) -> BackendStatus:
        issues = []
        if not self._binary_path:
            issues.append("binary_path not set in config/ai_assist.json")
        elif not os.path.isfile(self._binary_path):
            issues.append(f"llama-cli binary not found: {self._binary_path}")
        if not self._model_path:
            issues.append("model_path not set in config/ai_assist.json")
        elif not os.path.isfile(self._model_path):
            issues.append(f"model file not found: {self._model_path}")
        if issues:
            return BackendStatus(
                available=False,
                backend_name="llama_cpp",
                transport="cli",
                healthy=False,
                detail=" | ".join(issues),
            )
        return BackendStatus(
            available=True,
            backend_name="llama_cpp",
            transport="cli",
            healthy=True,
            detail=f"CLI ready: {self._binary_path}  model: {self._model_path}",
        )

    # ── Internal transport dispatch ────────────────────────────────────────────

    def _chat(self, system: str, user: str) -> dict[str, Any]:
        """
        Dispatch to the configured transport and return the parsed JSON dict.

        Raises:
          TimeoutError    — backend took longer than timeout_seconds
          ConnectionError — backend is unreachable or misconfigured
          ValueError      — response is not valid JSON or structure unexpected
        """
        if self._transport == "cli":
            return self._chat_cli(system, user)
        return self._chat_server(system, user)

    def _chat_server(self, system: str, user: str) -> dict[str, Any]:
        """
        POST to /v1/chat/completions (OpenAI-compatible llama.cpp server API).

        Raises TimeoutError, ConnectionError, ValueError.
        """
        payload = {
            "model": self._model,
            "messages": [
                {"role": "system", "content": system},
                {"role": "user", "content": user},
            ],
            "temperature": self._temperature,
            "max_tokens": self._max_tokens,
            "response_format": {"type": "json_object"},
        }
        data = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(
            f"{self._endpoint}/v1/chat/completions",
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
                f"llama.cpp server unavailable at {self._endpoint}: {e.reason}"
            )

        return _parse_chat_response(raw)

    def _chat_cli(self, system: str, user: str) -> dict[str, Any]:
        """
        Run llama-cli as a subprocess with a fixed argument list.

        Safety contract:
          - shell=False: user text is never interpreted by a shell
          - Fixed arg list: no string concatenation for command construction
          - subprocess.run timeout: process is killed if it exceeds timeout_seconds
          - stderr is captured: never leaks to user; inspected only on failure

        Raises TimeoutError, ConnectionError, ValueError.
        """
        if not self._binary_path or not os.path.isfile(self._binary_path):
            raise ConnectionError(
                f"llama-cli binary not found: '{self._binary_path}'. "
                "Set binary_path in config/ai_assist.json."
            )
        if not self._model_path or not os.path.isfile(self._model_path):
            raise ConnectionError(
                f"Model file not found: '{self._model_path}'. "
                "Set model_path in config/ai_assist.json."
            )

        # Build a single prompt string — passed as a list element, never shell-expanded
        prompt = (
            f"<|system|>\n{system}\n<|end|>\n"
            f"<|user|>\n{user}\n<|end|>\n"
            f"<|assistant|>\n"
        )

        cmd = [
            self._binary_path,
            "--model", self._model_path,
            "--prompt", prompt,
            "--n-predict", str(self._max_tokens),
            "--temp", str(self._temperature),
            "--no-display-prompt",
            "--log-disable",
        ]

        try:
            proc = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=self._timeout,
                shell=False,          # NEVER True — security contract
            )
        except subprocess.TimeoutExpired:
            raise TimeoutError(
                f"llama-cli timed out after {self._timeout}s. "
                "Try a smaller model or increase timeout_seconds."
            )
        except FileNotFoundError:
            raise ConnectionError(
                f"llama-cli binary not executable: {self._binary_path}"
            )
        except OSError as e:
            raise ConnectionError(f"Failed to launch llama-cli: {e}")

        if proc.returncode != 0:
            stderr_snippet = proc.stderr[:200] if proc.stderr else "(no stderr)"
            raise ConnectionError(
                f"llama-cli exited with code {proc.returncode}: {stderr_snippet}"
            )

        return _parse_cli_output(proc.stdout)

    # ── Advisory methods ──────────────────────────────────────────────────────

    def suggest_command(
        self,
        user_text: str,
        available_intents: list[str],
    ) -> CommandSuggestion:
        """
        Ask the backend for the best Nabd command matching user_text.
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
        Ask the backend for a plain-English explanation of the last result.
        Falls back gracefully if the backend is unavailable or returns bad output.
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
        Ask the backend for a focused clarification question.
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
        Ask the backend for the best matching intent from allowed_intents.
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

            # Safety gate 1 — discard any intent not in the allowed whitelist
            if intent and intent not in allowed_intents:
                return IntentSuggestion(
                    intent=None,
                    confidence=0.0,
                    explanation=(
                        f"Model suggested '{intent}' which is not in the "
                        "Nabd whitelist — discarded for safety."
                    ),
                )

            # Safety gate 2 — low-confidence gate matching the prompt instruction:
            # "If confidence is below 0.4, set intent to null."
            # Enforced here even if the model disobeys that instruction.
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


# ── Output parsing helpers ────────────────────────────────────────────────────

_LOW_CONFIDENCE_THRESHOLD = 0.4   # matches the threshold stated in SUGGEST_INTENT_JSON_TEMPLATE


def _parse_chat_response(raw: str) -> dict[str, Any]:
    """
    Parse an OpenAI-compatible chat completion response and return the content dict.

    Handles two `content` formats:
      - JSON string (standard): content = '{"key": "val"}'
      - Pre-parsed dict (some servers/proxies): content = {"key": "val"}
    Both are accepted; any other type raises ValueError.
    """
    try:
        outer = json.loads(raw)
        content = outer["choices"][0]["message"]["content"]
        # Accept pre-parsed dict — some llama.cpp builds or proxy layers return the
        # content field already decoded rather than as a JSON string.
        if isinstance(content, dict):
            return content
        result = json.loads(content)
        if not isinstance(result, dict):
            raise ValueError(f"Expected JSON object, got {type(result).__name__}")
        return result
    except (json.JSONDecodeError, KeyError, IndexError, TypeError) as e:
        raise ValueError(
            f"Invalid response from llama.cpp: {e!r} | "
            f"raw (first 200 chars): {raw[:200]}"
        )


def _parse_cli_output(stdout: str) -> dict[str, Any]:
    """
    Extract and parse a JSON object from llama-cli stdout.

    llama-cli may emit extra text before/after the JSON object — we find
    the first '{' and last '}' to extract the JSON substring.
    """
    text = stdout.strip()
    start = text.find("{")
    end = text.rfind("}")
    if start == -1 or end == -1 or end <= start:
        raise ValueError(
            f"No JSON object found in llama-cli output. "
            f"First 200 chars: {text[:200]}"
        )
    json_str = text[start : end + 1]
    try:
        result = json.loads(json_str)
        if not isinstance(result, dict):
            raise ValueError(f"Expected JSON object, got {type(result).__name__}")
        return result
    except json.JSONDecodeError as e:
        raise ValueError(
            f"Invalid JSON from llama-cli: {e!r} | extracted: {json_str[:200]}"
        )


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
        suggested_next_step="Check that the llama.cpp backend is running and reachable.",
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
