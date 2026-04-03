"""
Tests for Nabd v0.7 — llama.cpp Backend for AI Assist Skill

Covers (per spec section 9):
  - backend unavailable (connection refused, timeout)
  - timeout errors
  - invalid JSON responses
  - unsupported intent rejection (safety gate)
  - suggest_command (success and all failure modes)
  - explain_last_result (success and all failure modes)
  - clarify_request (success and all failure modes)
  - fallback intent suggestion (safety gate)
  - no auto-execution
  - existing command stability
  - ai_backend_status intent (parser → executor → reporter)
  - config loading for llama_cpp backend
  - AIAssistSkill backend switching (local vs llama_cpp)
"""

import sys
import os
import json
import socket
import unittest
import urllib.error
from io import BytesIO
from unittest.mock import MagicMock, patch

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))


# ── Helpers ───────────────────────────────────────────────────────────────────

def _make_http_response(body: dict | str, status: int = 200):
    """Build a mock urllib response wrapping a JSON dict or raw string."""
    if isinstance(body, dict):
        raw = json.dumps(body).encode("utf-8")
    else:
        raw = body.encode("utf-8") if isinstance(body, str) else body

    resp = MagicMock()
    resp.status = status
    resp.read.return_value = raw
    resp.__enter__ = lambda s: s
    resp.__exit__ = MagicMock(return_value=False)
    return resp


def _chat_response(content_dict: dict) -> dict:
    """Wrap a content dict as an OpenAI-style chat completion response."""
    return {
        "choices": [{"message": {"content": json.dumps(content_dict)}}]
    }


# ── LlamaCppBackend.is_available ──────────────────────────────────────────────

class TestLlamaCppAvailability(unittest.TestCase):

    def _make_backend(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        return LlamaCppBackend(server_url="http://localhost:8080", timeout_seconds=5)

    def test_available_when_health_returns_200(self):
        backend = self._make_backend()
        resp = _make_http_response({"status": "ok"})
        with patch("urllib.request.urlopen", return_value=resp):
            self.assertTrue(backend.is_available())

    def test_unavailable_on_connection_refused(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("Connection refused")):
            self.assertFalse(backend.is_available())

    def test_unavailable_on_timeout(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen", side_effect=socket.timeout()):
            self.assertFalse(backend.is_available())

    def test_unavailable_on_any_exception(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen", side_effect=OSError("port closed")):
            self.assertFalse(backend.is_available())

    def test_unavailable_when_status_not_200(self):
        backend = self._make_backend()
        resp = _make_http_response({"status": "loading"}, status=503)
        with patch("urllib.request.urlopen", return_value=resp):
            self.assertFalse(backend.is_available())


# ── LlamaCppBackend — _chat internal method ───────────────────────────────────

class TestLlamaCppChat(unittest.TestCase):

    def _make_backend(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        return LlamaCppBackend(server_url="http://localhost:8080", timeout_seconds=10)

    def test_chat_parses_valid_response(self):
        backend = self._make_backend()
        payload = {"suggested_command": "doctor", "rationale": "checks env", "confidence": 0.9}
        resp = _make_http_response(_chat_response(payload))
        with patch("urllib.request.urlopen", return_value=resp):
            result = backend._chat("sys", "user")
        self.assertEqual(result["suggested_command"], "doctor")

    def test_chat_raises_timeout_error_bare_socket_timeout(self):
        """Bare socket.timeout — raised directly in some edge cases / test mocks."""
        backend = self._make_backend()
        with patch("urllib.request.urlopen", side_effect=socket.timeout()):
            with self.assertRaises(TimeoutError) as ctx:
                backend._chat("sys", "user")
        self.assertIn("timed out", str(ctx.exception).lower())
        self.assertIn("timeout_seconds", str(ctx.exception))

    def test_chat_raises_timeout_error_via_urlerror_production_path(self):
        """
        Production-path timeout: urllib wraps socket.timeout as
        URLError(reason=socket.timeout(...)).  This is how it fires in real usage.
        The previous test covers the mock path; this test covers the real path.
        """
        backend = self._make_backend()
        wrapped = urllib.error.URLError(socket.timeout("timed out"))
        with patch("urllib.request.urlopen", side_effect=wrapped):
            with self.assertRaises(TimeoutError) as ctx:
                backend._chat("sys", "user")
        self.assertIn("timed out", str(ctx.exception).lower())
        self.assertIn("timeout_seconds", str(ctx.exception))

    def test_chat_raises_connection_error(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            with self.assertRaises(ConnectionError) as ctx:
                backend._chat("sys", "user")
        self.assertIn("unavailable", str(ctx.exception).lower())

    def test_chat_urlerror_non_timeout_reason_is_connection_error(self):
        """A URLError whose reason is NOT socket.timeout → ConnectionError, not TimeoutError."""
        backend = self._make_backend()
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("Connection refused")):
            with self.assertRaises(ConnectionError):
                backend._chat("sys", "user")

    def test_chat_raises_value_error_on_invalid_json(self):
        backend = self._make_backend()
        resp = _make_http_response("this is not json at all")
        with patch("urllib.request.urlopen", return_value=resp):
            with self.assertRaises(ValueError):
                backend._chat("sys", "user")

    def test_chat_raises_value_error_on_missing_choices(self):
        backend = self._make_backend()
        resp = _make_http_response({"unexpected": "keys"})
        with patch("urllib.request.urlopen", return_value=resp):
            with self.assertRaises(ValueError):
                backend._chat("sys", "user")

    def test_chat_raises_value_error_on_non_dict_content(self):
        backend = self._make_backend()
        outer = {"choices": [{"message": {"content": json.dumps([1, 2, 3])}}]}
        resp = _make_http_response(outer)
        with patch("urllib.request.urlopen", return_value=resp):
            with self.assertRaises(ValueError) as ctx:
                backend._chat("sys", "user")
        self.assertIn("Expected JSON object", str(ctx.exception))

    def test_chat_accepts_preparsed_dict_content(self):
        """Some llama.cpp builds return content as a dict, not a JSON string."""
        backend = self._make_backend()
        content_dict = {"suggested_command": "doctor", "rationale": "ok", "confidence": 0.9}
        # content field is a dict, not a JSON string
        outer = {"choices": [{"message": {"content": content_dict}}]}
        # We can't use _make_http_response directly since it json.dumps the whole body,
        # which will serialize content_dict as a nested dict — exactly the format to test.
        raw = json.dumps(outer).encode("utf-8")
        resp = MagicMock()
        resp.read.return_value = raw
        resp.__enter__ = lambda s: s
        resp.__exit__ = MagicMock(return_value=False)
        with patch("urllib.request.urlopen", return_value=resp):
            result = backend._chat("sys", "user")
        self.assertEqual(result["suggested_command"], "doctor")

    def test_chat_preparsed_dict_used_in_suggest_command(self):
        """End-to-end: pre-parsed dict content → valid CommandSuggestion, not fallback."""
        backend = self._make_backend()
        content_dict = {"suggested_command": "backup_folder", "rationale": "safest", "confidence": 0.88}
        outer = {"choices": [{"message": {"content": content_dict}}]}
        raw = json.dumps(outer).encode("utf-8")
        resp = MagicMock()
        resp.read.return_value = raw
        resp.__enter__ = lambda s: s
        resp.__exit__ = MagicMock(return_value=False)
        with patch("urllib.request.urlopen", return_value=resp):
            result = backend.suggest_command("back up my files", ["backup_folder", "doctor"])
        self.assertEqual(result.suggested_command, "backup_folder")
        self.assertGreater(result.confidence, 0.0)

    def test_chat_sends_json_content_type(self):
        backend = self._make_backend()
        payload = {"suggested_command": "doctor", "rationale": "ok", "confidence": 0.5}
        resp = _make_http_response(_chat_response(payload))
        captured = {}
        def fake_urlopen(req, timeout):
            captured["headers"] = dict(req.headers)
            return resp
        with patch("urllib.request.urlopen", side_effect=fake_urlopen):
            backend._chat("sys", "user")
        self.assertIn("Content-type", captured["headers"])
        self.assertIn("json", captured["headers"]["Content-type"])


# ── LlamaCppBackend.suggest_command ──────────────────────────────────────────

class TestLlamaCppSuggestCommand(unittest.TestCase):

    def _make_backend(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        return LlamaCppBackend(server_url="http://localhost:8080", timeout_seconds=10)

    def _mock_chat(self, backend, payload):
        resp = _make_http_response(_chat_response(payload))
        return patch("urllib.request.urlopen", return_value=resp)

    def test_returns_valid_suggestion(self):
        backend = self._make_backend()
        payload = {"suggested_command": "doctor", "rationale": "checks env", "confidence": 0.85}
        with self._mock_chat(backend, payload):
            result = backend.suggest_command("check my setup", ["doctor", "backup_folder"])
        self.assertEqual(result.suggested_command, "doctor")
        self.assertAlmostEqual(result.confidence, 0.85, places=2)

    def test_confidence_clamped_above_1(self):
        backend = self._make_backend()
        payload = {"suggested_command": "doctor", "rationale": "ok", "confidence": 2.5}
        with self._mock_chat(backend, payload):
            result = backend.suggest_command("check", ["doctor"])
        self.assertLessEqual(result.confidence, 1.0)

    def test_confidence_clamped_below_0(self):
        backend = self._make_backend()
        payload = {"suggested_command": "doctor", "rationale": "ok", "confidence": -0.5}
        with self._mock_chat(backend, payload):
            result = backend.suggest_command("check", ["doctor"])
        self.assertGreaterEqual(result.confidence, 0.0)

    def test_falls_back_on_timeout_bare(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen", side_effect=socket.timeout()):
            result = backend.suggest_command("check my setup", ["doctor"])
        self.assertEqual(result.suggested_command, "doctor")
        self.assertEqual(result.confidence, 0.0)
        self.assertIn("timed out", result.rationale.lower())

    def test_falls_back_on_timeout_production_path(self):
        """URLError(reason=socket.timeout) — the path that fires in real urllib usage."""
        backend = self._make_backend()
        wrapped = urllib.error.URLError(socket.timeout("timed out"))
        with patch("urllib.request.urlopen", side_effect=wrapped):
            result = backend.suggest_command("check my setup", ["doctor"])
        self.assertEqual(result.suggested_command, "doctor")
        self.assertEqual(result.confidence, 0.0)
        self.assertIn("timed out", result.rationale.lower())
        self.assertIn("unavailable", result.rationale.lower())

    def test_falls_back_on_connection_refused(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            result = backend.suggest_command("check my setup", ["doctor"])
        self.assertEqual(result.suggested_command, "doctor")
        self.assertEqual(result.confidence, 0.0)

    def test_falls_back_on_invalid_json(self):
        backend = self._make_backend()
        resp = _make_http_response("not json at all")
        with patch("urllib.request.urlopen", return_value=resp):
            result = backend.suggest_command("check", ["doctor"])
        self.assertEqual(result.suggested_command, "doctor")
        self.assertEqual(result.confidence, 0.0)

    def test_falls_back_on_empty_suggested_command(self):
        backend = self._make_backend()
        payload = {"suggested_command": "", "rationale": "ok", "confidence": 0.9}
        with self._mock_chat(backend, payload):
            result = backend.suggest_command("check", ["doctor"])
        self.assertEqual(result.suggested_command, "doctor")
        self.assertEqual(result.confidence, 0.0)

    def test_falls_back_on_missing_fields(self):
        backend = self._make_backend()
        payload = {"random_key": "random_value"}
        with self._mock_chat(backend, payload):
            result = backend.suggest_command("check", ["doctor"])
        self.assertEqual(result.suggested_command, "doctor")
        self.assertEqual(result.confidence, 0.0)


# ── LlamaCppBackend.explain_result ────────────────────────────────────────────

class TestLlamaCppExplainResult(unittest.TestCase):

    def _make_backend(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        return LlamaCppBackend(server_url="http://localhost:8080", timeout_seconds=10)

    def _mock_chat(self, payload):
        resp = _make_http_response(_chat_response(payload))
        return patch("urllib.request.urlopen", return_value=resp)

    def test_no_previous_command(self):
        backend = self._make_backend()
        result = backend.explain_result("", "")
        self.assertIn("No previous command", result.summary)

    def test_returns_valid_explanation(self):
        backend = self._make_backend()
        payload = {
            "summary": "The doctor command checked your environment.",
            "safety_note": None,
            "suggested_next_step": "Run backup if all checks passed.",
        }
        with self._mock_chat(payload):
            result = backend.explain_result("doctor", "ok")
        self.assertIn("doctor command", result.summary)
        self.assertIsNone(result.safety_note)
        self.assertIsNotNone(result.suggested_next_step)

    def test_safety_note_preserved(self):
        backend = self._make_backend()
        payload = {
            "summary": "Compressed images.",
            "safety_note": "Originals were overwritten.",
            "suggested_next_step": None,
        }
        with self._mock_chat(payload):
            result = backend.explain_result("compress images /sdcard/Pictures", "ok")
        self.assertEqual(result.safety_note, "Originals were overwritten.")

    def test_null_safety_note_becomes_none(self):
        backend = self._make_backend()
        payload = {"summary": "Done.", "safety_note": None, "suggested_next_step": None}
        with self._mock_chat(payload):
            result = backend.explain_result("doctor", "ok")
        self.assertIsNone(result.safety_note)

    def test_falls_back_on_timeout(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen", side_effect=socket.timeout()):
            result = backend.explain_result("doctor", "ok")
        self.assertIn("doctor", result.summary)
        self.assertIn("unavailable", result.summary.lower())

    def test_falls_back_on_invalid_json(self):
        backend = self._make_backend()
        resp = _make_http_response("bad json")
        with patch("urllib.request.urlopen", return_value=resp):
            result = backend.explain_result("doctor", "ok")
        self.assertIn("unavailable", result.summary.lower())

    def test_falls_back_on_empty_summary(self):
        backend = self._make_backend()
        payload = {"summary": "", "safety_note": None, "suggested_next_step": None}
        with self._mock_chat(payload):
            result = backend.explain_result("doctor", "ok")
        self.assertIn("unavailable", result.summary.lower())


# ── LlamaCppBackend.clarify_request ──────────────────────────────────────────

class TestLlamaCppClarifyRequest(unittest.TestCase):

    def _make_backend(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        return LlamaCppBackend(server_url="http://localhost:8080", timeout_seconds=10)

    def _mock_chat(self, payload):
        resp = _make_http_response(_chat_response(payload))
        return patch("urllib.request.urlopen", return_value=resp)

    def test_returns_valid_clarification(self):
        backend = self._make_backend()
        payload = {
            "clarification_needed": True,
            "clarification_question": "Which folder do you want to organize?",
            "candidate_intents": ["organize_folder_by_type", "show_files"],
        }
        with self._mock_chat(payload):
            result = backend.clarify_request("organize stuff", ["organize_folder_by_type", "show_files"])
        self.assertTrue(result.clarification_needed)
        self.assertIn("folder", result.clarification_question.lower())

    def test_candidate_intents_filtered_to_allowed(self):
        backend = self._make_backend()
        payload = {
            "clarification_needed": True,
            "clarification_question": "What do you want?",
            "candidate_intents": ["doctor", "rm_all_files", "backup_folder"],
        }
        allowed = ["doctor", "backup_folder"]
        with self._mock_chat(payload):
            result = backend.clarify_request("something", allowed)
        for candidate in result.candidate_intents:
            self.assertIn(candidate, allowed)
        self.assertNotIn("rm_all_files", result.candidate_intents)

    def test_candidate_intents_max_three(self):
        backend = self._make_backend()
        payload = {
            "clarification_needed": True,
            "clarification_question": "What?",
            "candidate_intents": ["doctor", "backup_folder", "show_files", "find_duplicates"],
        }
        allowed = ["doctor", "backup_folder", "show_files", "find_duplicates"]
        with self._mock_chat(payload):
            result = backend.clarify_request("many things", allowed)
        self.assertLessEqual(len(result.candidate_intents), 3)

    def test_falls_back_on_timeout(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen", side_effect=socket.timeout()):
            result = backend.clarify_request("help", ["doctor"])
        self.assertIsNotNone(result.clarification_question)
        self.assertIn("unavailable", result.clarification_question.lower())

    def test_falls_back_on_invalid_json(self):
        backend = self._make_backend()
        resp = _make_http_response("not json")
        with patch("urllib.request.urlopen", return_value=resp):
            result = backend.clarify_request("help", ["doctor"])
        self.assertIn("unavailable", result.clarification_question.lower())

    def test_falls_back_on_connection_error(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            result = backend.clarify_request("help", ["doctor"])
        self.assertEqual(result.candidate_intents, [])


# ── LlamaCppBackend.suggest_intent (safety gate) ──────────────────────────────

class TestLlamaCppSuggestIntent(unittest.TestCase):

    def _make_backend(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        return LlamaCppBackend(server_url="http://localhost:8080", timeout_seconds=10)

    def _mock_chat(self, payload):
        resp = _make_http_response(_chat_response(payload))
        return patch("urllib.request.urlopen", return_value=resp)

    def test_returns_whitelisted_intent(self):
        backend = self._make_backend()
        payload = {"intent": "doctor", "confidence": 0.85, "explanation": "checks env"}
        allowed = ["doctor", "backup_folder"]
        with self._mock_chat(payload):
            result = backend.suggest_intent("check my setup", allowed)
        self.assertEqual(result.intent, "doctor")
        self.assertAlmostEqual(result.confidence, 0.85, places=2)

    def test_safety_gate_rejects_non_whitelisted_intent(self):
        backend = self._make_backend()
        payload = {"intent": "rm_all_files", "confidence": 0.9, "explanation": "delete everything"}
        allowed = ["doctor", "backup_folder"]
        with self._mock_chat(payload):
            result = backend.suggest_intent("delete everything", allowed)
        self.assertIsNone(result.intent)
        self.assertEqual(result.confidence, 0.0)
        self.assertIn("whitelist", result.explanation.lower())

    def test_safety_gate_rejects_shell_command(self):
        backend = self._make_backend()
        payload = {"intent": "rm -rf /", "confidence": 0.99, "explanation": "unsafe"}
        allowed = ["doctor"]
        with self._mock_chat(payload):
            result = backend.suggest_intent("delete everything", allowed)
        self.assertIsNone(result.intent)

    def test_null_intent_allowed(self):
        backend = self._make_backend()
        payload = {"intent": None, "confidence": 0.0, "explanation": "no match found"}
        allowed = ["doctor", "backup_folder"]
        with self._mock_chat(payload):
            result = backend.suggest_intent("xyzzy", allowed)
        self.assertIsNone(result.intent)
        self.assertEqual(result.confidence, 0.0)

    def test_falls_back_on_timeout(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen", side_effect=socket.timeout()):
            result = backend.suggest_intent("check", ["doctor"])
        self.assertIsNone(result.intent)
        self.assertEqual(result.confidence, 0.0)
        self.assertIn("unavailable", result.explanation.lower())

    def test_falls_back_on_connection_error(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            result = backend.suggest_intent("check", ["doctor"])
        self.assertIsNone(result.intent)

    def test_falls_back_on_invalid_json(self):
        backend = self._make_backend()
        resp = _make_http_response("not json")
        with patch("urllib.request.urlopen", return_value=resp):
            result = backend.suggest_intent("check", ["doctor"])
        self.assertIsNone(result.intent)

    def test_low_confidence_gate_rejects_below_threshold(self):
        """Spec §12: low-confidence results must not be treated as valid commands."""
        backend = self._make_backend()
        payload = {"intent": "doctor", "confidence": 0.2, "explanation": "weak match"}
        with self._mock_chat(payload):
            result = backend.suggest_intent("vague request", ["doctor"])
        self.assertIsNone(result.intent)
        self.assertAlmostEqual(result.confidence, 0.2)
        self.assertIn("threshold", result.explanation.lower())

    def test_low_confidence_gate_boundary_exactly_at_threshold(self):
        """Confidence exactly == 0.4 is not 'below 0.4' — it passes through."""
        backend = self._make_backend()
        payload = {"intent": "doctor", "confidence": 0.4, "explanation": "borderline"}
        with self._mock_chat(payload):
            result = backend.suggest_intent("check", ["doctor"])
        self.assertEqual(result.intent, "doctor")
        self.assertAlmostEqual(result.confidence, 0.4)

    def test_low_confidence_gate_boundary_just_below_threshold(self):
        """Confidence 0.39 is below 0.4 → rejected."""
        backend = self._make_backend()
        payload = {"intent": "doctor", "confidence": 0.39, "explanation": "too weak"}
        with self._mock_chat(payload):
            result = backend.suggest_intent("vague", ["doctor"])
        self.assertIsNone(result.intent)

    def test_high_confidence_passes_gate(self):
        """Confidence above threshold with whitelisted intent → accepted."""
        backend = self._make_backend()
        payload = {"intent": "doctor", "confidence": 0.75, "explanation": "checks env"}
        with self._mock_chat(payload):
            result = backend.suggest_intent("check my setup", ["doctor"])
        self.assertEqual(result.intent, "doctor")
        self.assertAlmostEqual(result.confidence, 0.75)

    def test_low_confidence_non_whitelisted_still_whitelist_rejected(self):
        """Whitelist check fires first — non-whitelisted intent with any confidence → None."""
        backend = self._make_backend()
        payload = {"intent": "rm_all_files", "confidence": 0.9, "explanation": "very sure"}
        with self._mock_chat(payload):
            result = backend.suggest_intent("delete everything", ["doctor"])
        self.assertIsNone(result.intent)
        self.assertEqual(result.confidence, 0.0)   # whitelist gate resets confidence to 0.0

    def test_null_intent_with_any_confidence_passes_through(self):
        """intent=null means 'no match' — always valid regardless of confidence."""
        backend = self._make_backend()
        payload = {"intent": None, "confidence": 0.1, "explanation": "no match"}
        with self._mock_chat(payload):
            result = backend.suggest_intent("xyzzy", ["doctor"])
        self.assertIsNone(result.intent)
        self.assertAlmostEqual(result.confidence, 0.1)


# ── AIAssistSkill — llama_cpp backend selection ───────────────────────────────

class TestAIAssistSkillLlamaCppConfig(unittest.TestCase):

    def setUp(self):
        import skills.registry as reg_mod
        reg_mod._registry = None

    def _skill_with_config(self, config: dict):
        from skills.ai_assist_skill import AIAssistSkill
        with patch("skills.ai_assist_skill._load_config", return_value=config):
            return AIAssistSkill()

    def test_backend_local_selected_by_default(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "local",
            "mode": "assist_only", "fallback_intent_suggestion": False,
        })
        from llm.local_backend import LocalBackend
        self.assertIsInstance(skill._get_backend(), LocalBackend)

    def test_backend_llama_cpp_selected(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"server_url": "http://localhost:8080",
                          "timeout_seconds": 15, "model_name": "test-model"},
        })
        from llm.llama_cpp_backend import LlamaCppBackend
        self.assertIsInstance(skill._get_backend(), LlamaCppBackend)

    def test_llama_cpp_config_propagated(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"server_url": "http://192.168.1.1:9999",
                          "timeout_seconds": 45, "model_name": "my-model"},
        })
        backend = skill._get_backend()
        self.assertEqual(backend._server_url, "http://192.168.1.1:9999")
        self.assertEqual(backend._timeout, 45)
        self.assertEqual(backend._model, "my-model")

    def test_trailing_slash_stripped_from_server_url(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"server_url": "http://localhost:8080/",
                          "timeout_seconds": 10},
        })
        backend = skill._get_backend()
        self.assertFalse(backend._server_url.endswith("/"))

    def test_disabled_with_llama_cpp_backend_still_raises(self):
        skill = self._skill_with_config({
            "enabled": False, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"server_url": "http://localhost:8080",
                          "timeout_seconds": 10},
        })
        with self.assertRaises(RuntimeError) as ctx:
            skill.suggest_command("check my phone")
        self.assertIn("disabled", str(ctx.exception).lower())


# ── get_backend_status ────────────────────────────────────────────────────────

class TestGetBackendStatus(unittest.TestCase):

    def setUp(self):
        import skills.registry as reg_mod
        reg_mod._registry = None

    def _skill_with_config(self, config: dict):
        from skills.ai_assist_skill import AIAssistSkill
        with patch("skills.ai_assist_skill._load_config", return_value=config):
            return AIAssistSkill()

    def test_local_backend_status_always_available(self):
        skill = self._skill_with_config({
            "enabled": False, "backend": "local",
            "mode": "assist_only", "fallback_intent_suggestion": False,
        })
        status = skill.get_backend_status()
        self.assertEqual(status["backend"], "local")
        self.assertTrue(status["available"])
        self.assertIn("detail", status)

    def test_llama_cpp_status_when_server_up(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"server_url": "http://localhost:8080",
                          "timeout_seconds": 5},
        })
        resp = _make_http_response({"status": "ok"})
        with patch("urllib.request.urlopen", return_value=resp):
            status = skill.get_backend_status()
        self.assertEqual(status["backend"], "llama_cpp")
        self.assertTrue(status["available"])
        self.assertIn("server_url", status)
        self.assertIn("Connected", status["detail"])

    def test_llama_cpp_status_when_server_down(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"server_url": "http://localhost:8080",
                          "timeout_seconds": 5},
        })
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = skill.get_backend_status()
        self.assertEqual(status["backend"], "llama_cpp")
        self.assertFalse(status["available"])
        self.assertIn("not responding", status["detail"].lower())

    def test_status_has_enabled_field(self):
        skill = self._skill_with_config({
            "enabled": False, "backend": "local",
            "mode": "assist_only", "fallback_intent_suggestion": False,
        })
        status = skill.get_backend_status()
        self.assertFalse(status["enabled"])

    def test_status_safe_when_ai_disabled(self):
        skill = self._skill_with_config({
            "enabled": False, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"server_url": "http://localhost:8080",
                          "timeout_seconds": 3},
        })
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = skill.get_backend_status()
        self.assertIsInstance(status, dict)
        self.assertIn("backend", status)


# ── ai_backend_status intent — full pipeline ──────────────────────────────────

class TestAIBackendStatusIntent(unittest.TestCase):

    def setUp(self):
        import skills.registry as reg_mod
        reg_mod._registry = None

    def _parse(self, cmd):
        from agent.parser import parse_command
        return parse_command(cmd)

    def test_parser_ai_backend_status(self):
        self.assertEqual(self._parse("ai backend status").intent, "ai_backend_status")

    def test_parser_show_ai_backend(self):
        self.assertEqual(self._parse("show ai backend").intent, "ai_backend_status")

    def test_parser_which_ai_backend(self):
        self.assertEqual(self._parse("which ai backend").intent, "ai_backend_status")

    def test_parser_ai_status(self):
        self.assertEqual(self._parse("ai status").intent, "ai_backend_status")

    def test_parser_backend_status(self):
        self.assertEqual(self._parse("backend status").intent, "ai_backend_status")

    def test_safety_read_only(self):
        from agent.safety import READ_ONLY_INTENTS
        self.assertIn("ai_backend_status", READ_ONLY_INTENTS)

    def test_safety_not_path_required(self):
        from agent.safety import PATH_REQUIRED_INTENTS
        self.assertNotIn("ai_backend_status", PATH_REQUIRED_INTENTS)

    def test_planner_tool_name_is_skill(self):
        from agent.planner import plan
        p = plan(self._parse("ai backend status"))
        self.assertEqual(p.actions[0].tool_name, "skill")
        self.assertEqual(p.actions[0].function_name, "backend_status")

    def test_planner_no_confirmation(self):
        from agent.planner import plan
        p = plan(self._parse("ai backend status"))
        self.assertFalse(p.requires_confirmation)

    def test_executor_returns_dict(self):
        from agent.parser import parse_command
        from agent.planner import plan
        from agent.executor import execute
        p = plan(parse_command("ai backend status"))
        result = execute(p, confirmed=False)
        from agent.models import OperationStatus
        self.assertEqual(result.status, OperationStatus.SUCCESS)
        raw = result.raw_results[0]
        self.assertIn("backend", raw)
        self.assertIn("available", raw)

    def test_reporter_formats_local_backend(self):
        from agent.parser import parse_command
        from agent.planner import plan
        from agent.executor import execute
        from agent.reporter import report_result
        parsed = parse_command("ai backend status")
        p = plan(parsed)
        result = execute(p, confirmed=False)
        output = report_result(result, parsed.intent, False)
        self.assertIn("local", output)
        self.assertIn("Backend", output)

    def test_reporter_shows_server_url_for_llama_cpp(self):
        with patch("skills.ai_assist_skill._load_config",
                   return_value={"enabled": True, "backend": "llama_cpp",
                                 "mode": "assist_only", "fallback_intent_suggestion": False,
                                 "llama_cpp": {"server_url": "http://localhost:8080",
                                               "timeout_seconds": 5}}):
            import skills.registry as reg_mod
            reg_mod._registry = None
            with patch("urllib.request.urlopen",
                       side_effect=urllib.error.URLError("refused")):
                from agent.parser import parse_command
                from agent.planner import plan
                from agent.executor import execute
                from agent.reporter import report_result
                parsed = parse_command("ai backend status")
                p = plan(parsed)
                result = execute(p, confirmed=False)
                output = report_result(result, parsed.intent, False)
        self.assertIn("llama_cpp", output)
        self.assertIn("localhost:8080", output)

    def test_reporter_shows_startup_hint_when_llama_cpp_down(self):
        with patch("skills.ai_assist_skill._load_config",
                   return_value={"enabled": True, "backend": "llama_cpp",
                                 "mode": "assist_only", "fallback_intent_suggestion": False,
                                 "llama_cpp": {"server_url": "http://localhost:8080",
                                               "timeout_seconds": 5}}):
            import skills.registry as reg_mod
            reg_mod._registry = None
            with patch("urllib.request.urlopen",
                       side_effect=urllib.error.URLError("refused")):
                from agent.parser import parse_command
                from agent.planner import plan
                from agent.executor import execute
                from agent.reporter import report_result
                parsed = parse_command("ai backend status")
                p = plan(parsed)
                result = execute(p, confirmed=False)
                output = report_result(result, parsed.intent, False)
        self.assertIn("./server", output)


# ── No auto-execution ─────────────────────────────────────────────────────────

class TestNoAutoExecutionV7(unittest.TestCase):

    def setUp(self):
        import skills.registry as reg_mod
        reg_mod._registry = None

    def test_llama_cpp_backend_never_requires_confirmation(self):
        from agent.parser import parse_command
        from agent.planner import plan
        for cmd in [
            "suggest command for backup my files",
            "explain last result",
            "help me with media files",
            "ai backend status",
        ]:
            parsed = parse_command(cmd)
            if parsed.intent == "ai_explain_last_result":
                parsed.options["last_command"] = ""
                parsed.options["last_result"] = ""
            p = plan(parsed)
            self.assertFalse(
                p.requires_confirmation,
                f"'{cmd}' must not require confirmation"
            )

    def test_llama_cpp_backend_risk_level_is_low(self):
        from agent.parser import parse_command
        from agent.planner import plan
        from agent.models import RiskLevel
        for cmd in ["suggest command for organize", "ai backend status"]:
            p = plan(parse_command(cmd))
            self.assertEqual(p.risk_level, RiskLevel.LOW)

    def test_ai_skill_actions_never_use_system_tool(self):
        from agent.parser import parse_command
        from agent.planner import plan
        for cmd in ["suggest command for backup", "ai backend status", "help me with files"]:
            p = plan(parse_command(cmd))
            for action in p.actions:
                self.assertNotEqual(
                    action.tool_name, "system",
                    f"AI command '{cmd}' must not use system tool"
                )


# ── Config file ───────────────────────────────────────────────────────────────

class TestConfigV7(unittest.TestCase):

    def _config_path(self):
        return os.path.join(os.path.dirname(__file__), "..", "config", "ai_assist.json")

    def test_config_has_llama_cpp_section(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertIn("llama_cpp", config)

    def test_llama_cpp_section_has_endpoint(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertIn("endpoint", config["llama_cpp"])

    def test_llama_cpp_section_has_transport(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertIn("transport", config["llama_cpp"])
        self.assertIn(config["llama_cpp"]["transport"], ("server", "cli"))

    def test_llama_cpp_section_has_binary_path(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertIn("binary_path", config["llama_cpp"])

    def test_llama_cpp_section_has_model_path(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertIn("model_path", config["llama_cpp"])

    def test_llama_cpp_section_has_max_tokens(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertIn("max_tokens", config["llama_cpp"])
        self.assertIsInstance(config["llama_cpp"]["max_tokens"], int)

    def test_llama_cpp_section_has_temperature(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertIn("temperature", config["llama_cpp"])
        self.assertIsInstance(config["llama_cpp"]["temperature"], float)

    def test_llama_cpp_section_has_timeout_seconds(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertIn("timeout_seconds", config["llama_cpp"])
        self.assertIsInstance(config["llama_cpp"]["timeout_seconds"], int)

    def test_default_backend_is_local(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertEqual(config["backend"], "local")

    def test_enabled_is_false_by_default(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertFalse(config["enabled"])


# ── BackendStatus dataclass ───────────────────────────────────────────────────

class TestBackendStatusSchema(unittest.TestCase):

    def test_backend_status_fields(self):
        from llm.schemas import BackendStatus
        s = BackendStatus(
            available=True, backend_name="local",
            transport=None, healthy=True, detail="ok"
        )
        self.assertTrue(s.available)
        self.assertEqual(s.backend_name, "local")
        self.assertIsNone(s.transport)
        self.assertTrue(s.healthy)
        self.assertEqual(s.detail, "ok")

    def test_backend_status_unhealthy(self):
        from llm.schemas import BackendStatus
        s = BackendStatus(
            available=False, backend_name="llama_cpp",
            transport="server", healthy=False, detail="server not responding"
        )
        self.assertFalse(s.available)
        self.assertFalse(s.healthy)
        self.assertEqual(s.transport, "server")

    def test_backend_status_cli_transport(self):
        from llm.schemas import BackendStatus
        s = BackendStatus(
            available=True, backend_name="llama_cpp",
            transport="cli", healthy=True, detail="CLI ready"
        )
        self.assertEqual(s.transport, "cli")


# ── get_status() — abstract method implemented ────────────────────────────────

class TestGetStatusMethod(unittest.TestCase):

    def test_local_backend_get_status(self):
        from llm.local_backend import LocalBackend
        from llm.schemas import BackendStatus
        b = LocalBackend()
        status = b.get_status()
        self.assertIsInstance(status, BackendStatus)
        self.assertTrue(status.available)
        self.assertEqual(status.backend_name, "local")
        self.assertIsNone(status.transport)
        self.assertTrue(status.healthy)

    def test_llama_cpp_server_get_status_up(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        from llm.schemas import BackendStatus
        b = LlamaCppBackend(endpoint="http://localhost:8080", transport="server")
        resp = _make_http_response({"status": "ok"})
        with patch("urllib.request.urlopen", return_value=resp):
            status = b.get_status()
        self.assertIsInstance(status, BackendStatus)
        self.assertTrue(status.available)
        self.assertEqual(status.backend_name, "llama_cpp")
        self.assertEqual(status.transport, "server")
        self.assertTrue(status.healthy)

    def test_llama_cpp_server_get_status_down(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        from llm.schemas import BackendStatus
        b = LlamaCppBackend(endpoint="http://localhost:8080", transport="server")
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = b.get_status()
        self.assertIsInstance(status, BackendStatus)
        self.assertFalse(status.available)
        self.assertFalse(status.healthy)
        self.assertIn("not responding", status.detail.lower())

    def test_llama_cpp_cli_get_status_missing_binary(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        from llm.schemas import BackendStatus
        b = LlamaCppBackend(transport="cli", binary_path="/nonexistent/llama-cli",
                            model_path="/nonexistent/model.gguf")
        status = b.get_status()
        self.assertIsInstance(status, BackendStatus)
        self.assertFalse(status.available)
        self.assertEqual(status.transport, "cli")
        self.assertIn("not found", status.detail.lower())

    def test_llama_cpp_cli_get_status_missing_model(self, tmp_path=None):
        from llm.llama_cpp_backend import LlamaCppBackend
        from llm.schemas import BackendStatus
        import tempfile, os
        with tempfile.NamedTemporaryFile(delete=False) as f:
            binary = f.name
        try:
            b = LlamaCppBackend(transport="cli", binary_path=binary,
                                model_path="/nonexistent/model.gguf")
            status = b.get_status()
        finally:
            os.unlink(binary)
        self.assertFalse(status.available)
        self.assertIn("not found", status.detail.lower())

    def test_llama_cpp_cli_get_status_both_present(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        from llm.schemas import BackendStatus
        import tempfile, os
        with tempfile.NamedTemporaryFile(delete=False) as fb:
            binary = fb.name
        with tempfile.NamedTemporaryFile(delete=False) as fm:
            model = fm.name
        try:
            b = LlamaCppBackend(transport="cli", binary_path=binary, model_path=model)
            status = b.get_status()
        finally:
            os.unlink(binary)
            os.unlink(model)
        self.assertTrue(status.available)
        self.assertTrue(status.healthy)
        self.assertEqual(status.transport, "cli")

    def test_get_status_never_raises(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(transport="server", endpoint="http://localhost:9999")
        with patch("urllib.request.urlopen", side_effect=RuntimeError("unexpected")):
            status = b.get_status()
        self.assertIsNotNone(status)
        self.assertFalse(status.available)


# ── LlamaCppBackend — transport config ────────────────────────────────────────

class TestLlamaCppTransportConfig(unittest.TestCase):

    def test_default_transport_is_server(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend()
        self.assertEqual(b._transport, "server")

    def test_cli_transport_set(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(transport="cli", binary_path="/bin/llama", model_path="/m.gguf")
        self.assertEqual(b._transport, "cli")

    def test_endpoint_used_as_primary(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(endpoint="http://myserver:9090")
        self.assertEqual(b._endpoint, "http://myserver:9090")

    def test_server_url_kwarg_still_works(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(server_url="http://legacy:8888")
        self.assertEqual(b._server_url, "http://legacy:8888")

    def test_trailing_slash_stripped_from_endpoint(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(endpoint="http://localhost:8080/")
        self.assertFalse(b._endpoint.endswith("/"))

    def test_max_tokens_stored(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(max_tokens=512)
        self.assertEqual(b._max_tokens, 512)

    def test_temperature_stored(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(temperature=0.5)
        self.assertAlmostEqual(b._temperature, 0.5)

    def test_binary_path_stored(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(transport="cli", binary_path="/usr/bin/llama-cli")
        self.assertEqual(b._binary_path, "/usr/bin/llama-cli")

    def test_model_path_stored(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(transport="cli", model_path="/sdcard/models/model.gguf")
        self.assertEqual(b._model_path, "/sdcard/models/model.gguf")


# ── max_tokens and temperature propagated in server request ───────────────────

class TestLlamaCppServerPayload(unittest.TestCase):

    def test_max_tokens_in_request_payload(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(endpoint="http://localhost:8080", max_tokens=128)
        payload_resp = _make_http_response(
            _chat_response({"suggested_command": "doctor", "rationale": "ok", "confidence": 0.8})
        )
        captured = {}
        def fake_urlopen(req, timeout):
            captured["body"] = json.loads(req.data.decode())
            return payload_resp
        with patch("urllib.request.urlopen", side_effect=fake_urlopen):
            b.suggest_command("check", ["doctor"])
        self.assertEqual(captured["body"]["max_tokens"], 128)

    def test_temperature_in_request_payload(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(endpoint="http://localhost:8080", temperature=0.3)
        payload_resp = _make_http_response(
            _chat_response({"suggested_command": "doctor", "rationale": "ok", "confidence": 0.8})
        )
        captured = {}
        def fake_urlopen(req, timeout):
            captured["body"] = json.loads(req.data.decode())
            return payload_resp
        with patch("urllib.request.urlopen", side_effect=fake_urlopen):
            b.suggest_command("check", ["doctor"])
        self.assertAlmostEqual(captured["body"]["temperature"], 0.3)

    def test_response_format_json_object_in_payload(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(endpoint="http://localhost:8080")
        payload_resp = _make_http_response(
            _chat_response({"suggested_command": "doctor", "rationale": "ok", "confidence": 0.8})
        )
        captured = {}
        def fake_urlopen(req, timeout):
            captured["body"] = json.loads(req.data.decode())
            return payload_resp
        with patch("urllib.request.urlopen", side_effect=fake_urlopen):
            b.suggest_command("check", ["doctor"])
        self.assertEqual(
            captured["body"].get("response_format", {}).get("type"), "json_object"
        )


# ── CLI mode — is_available ───────────────────────────────────────────────────

class TestLlamaCppCLIAvailability(unittest.TestCase):

    def test_cli_available_when_both_files_exist(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        import tempfile, os
        with tempfile.NamedTemporaryFile(delete=False) as fb:
            binary = fb.name
        with tempfile.NamedTemporaryFile(delete=False) as fm:
            model = fm.name
        try:
            b = LlamaCppBackend(transport="cli", binary_path=binary, model_path=model)
            self.assertTrue(b.is_available())
        finally:
            os.unlink(binary)
            os.unlink(model)

    def test_cli_unavailable_when_binary_missing(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(transport="cli",
                            binary_path="/nonexistent/llama-cli",
                            model_path="/nonexistent/model.gguf")
        self.assertFalse(b.is_available())

    def test_cli_unavailable_when_model_missing(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        import tempfile, os
        with tempfile.NamedTemporaryFile(delete=False) as fb:
            binary = fb.name
        try:
            b = LlamaCppBackend(transport="cli", binary_path=binary,
                                model_path="/nonexistent/model.gguf")
            self.assertFalse(b.is_available())
        finally:
            os.unlink(binary)

    def test_cli_unavailable_when_paths_empty(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(transport="cli", binary_path="", model_path="")
        self.assertFalse(b.is_available())


# ── CLI mode — subprocess safety ──────────────────────────────────────────────

class TestLlamaCppCLISubprocess(unittest.TestCase):

    def _make_cli_backend(self, binary="/usr/bin/llama-cli", model="/sdcard/model.gguf"):
        from llm.llama_cpp_backend import LlamaCppBackend
        return LlamaCppBackend(
            transport="cli",
            binary_path=binary,
            model_path=model,
            timeout_seconds=10,
            max_tokens=128,
            temperature=0.2,
        )

    def _cli_proc(self, stdout: str, returncode: int = 0) -> MagicMock:
        proc = MagicMock()
        proc.stdout = stdout
        proc.stderr = ""
        proc.returncode = returncode
        return proc

    def test_cli_chat_returns_valid_dict(self):
        b = self._make_cli_backend()
        payload = {"suggested_command": "doctor", "rationale": "ok", "confidence": 0.9}
        proc = self._cli_proc(json.dumps(payload))
        with patch("os.path.isfile", return_value=True), \
             patch("subprocess.run", return_value=proc):
            result = b._chat_cli("sys", "user")
        self.assertEqual(result["suggested_command"], "doctor")

    def test_cli_chat_extracts_json_from_mixed_output(self):
        b = self._make_cli_backend()
        payload = {"suggested_command": "doctor", "rationale": "ok", "confidence": 0.7}
        mixed = f"some preamble text\n{json.dumps(payload)}\nsome trailing text"
        proc = self._cli_proc(mixed)
        with patch("os.path.isfile", return_value=True), \
             patch("subprocess.run", return_value=proc):
            result = b._chat_cli("sys", "user")
        self.assertEqual(result["suggested_command"], "doctor")

    def test_cli_uses_fixed_arg_list_not_shell(self):
        b = self._make_cli_backend()
        payload = {"intent": "doctor", "confidence": 0.8, "explanation": "ok"}
        proc = self._cli_proc(json.dumps(payload))
        call_args = {}
        def fake_run(cmd, **kwargs):
            call_args["cmd"] = cmd
            call_args["shell"] = kwargs.get("shell", False)
            return proc
        with patch("os.path.isfile", return_value=True), \
             patch("subprocess.run", side_effect=fake_run):
            b._chat_cli("sys", "user")
        self.assertIsInstance(call_args["cmd"], list)
        self.assertFalse(call_args["shell"])

    def test_cli_timeout_raises_timeout_error(self):
        b = self._make_cli_backend()
        import subprocess as sp
        with patch("os.path.isfile", return_value=True), \
             patch("subprocess.run", side_effect=sp.TimeoutExpired(["llama-cli"], 10)):
            with self.assertRaises(TimeoutError):
                b._chat_cli("sys", "user")

    def test_cli_missing_binary_raises_connection_error(self):
        b = self._make_cli_backend(binary="/missing/llama-cli")
        with patch("os.path.isfile", return_value=False):
            with self.assertRaises(ConnectionError) as ctx:
                b._chat_cli("sys", "user")
        self.assertIn("not found", str(ctx.exception).lower())

    def test_cli_missing_model_raises_connection_error(self):
        b = self._make_cli_backend()
        def isfile(p):
            return "binary" not in p and "llama-cli" not in p.lower() or p == "/usr/bin/llama-cli"
        with patch("os.path.isfile", side_effect=lambda p: (
            p == "/usr/bin/llama-cli"  # binary exists
        )):
            with self.assertRaises(ConnectionError):
                b._chat_cli("sys", "user")

    def test_cli_nonzero_exit_raises_connection_error(self):
        b = self._make_cli_backend()
        proc = self._cli_proc("error output", returncode=1)
        with patch("os.path.isfile", return_value=True), \
             patch("subprocess.run", return_value=proc):
            with self.assertRaises(ConnectionError) as ctx:
                b._chat_cli("sys", "user")
        self.assertIn("exited with code 1", str(ctx.exception))

    def test_cli_falls_back_gracefully_on_timeout(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        import subprocess as sp
        b = LlamaCppBackend(transport="cli", binary_path="/bin/llama",
                            model_path="/m.gguf", timeout_seconds=5)
        with patch("os.path.isfile", return_value=True), \
             patch("subprocess.run", side_effect=sp.TimeoutExpired(["llama-cli"], 5)):
            result = b.suggest_command("check", ["doctor"])
        self.assertEqual(result.suggested_command, "doctor")
        self.assertEqual(result.confidence, 0.0)
        self.assertIn("timed out", result.rationale.lower())

    def test_cli_falls_back_gracefully_on_missing_binary(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        b = LlamaCppBackend(transport="cli", binary_path="/nonexistent/llama",
                            model_path="/nonexistent/model.gguf")
        result = b.suggest_command("check", ["doctor"])
        self.assertEqual(result.suggested_command, "doctor")
        self.assertEqual(result.confidence, 0.0)


# ── Reporter — transport field ────────────────────────────────────────────────

class TestReporterTransportField(unittest.TestCase):

    def _report(self, raw: dict) -> str:
        from agent.reporter import report_result
        from agent.models import OperationStatus
        result = MagicMock()
        result.status = OperationStatus.SUCCESS
        result.message = "AI Backend Status"
        result.errors = []
        result.raw_results = [raw]
        return report_result(result, "ai_backend_status", False)

    def test_transport_shown_for_server(self):
        raw = {
            "backend": "llama_cpp", "available": True, "enabled": True,
            "transport": "server", "healthy": True,
            "endpoint": "http://localhost:8080",
            "timeout_seconds": 20, "max_tokens": 256, "temperature": 0.2,
            "detail": "Connected",
        }
        output = self._report(raw)
        self.assertIn("server", output)
        self.assertIn("Transport", output)

    def test_transport_shown_for_cli(self):
        raw = {
            "backend": "llama_cpp", "available": False, "enabled": True,
            "transport": "cli", "healthy": False,
            "binary_path": "/usr/bin/llama-cli",
            "model_path": "/sdcard/models/model.gguf",
            "timeout_seconds": 20,
            "detail": "binary not found",
        }
        output = self._report(raw)
        self.assertIn("cli", output)
        self.assertIn("Binary", output)

    def test_endpoint_shown_for_server_transport(self):
        raw = {
            "backend": "llama_cpp", "available": True, "enabled": True,
            "transport": "server", "healthy": True,
            "endpoint": "http://192.168.1.5:8080",
            "timeout_seconds": 30, "max_tokens": 256, "temperature": 0.2,
            "detail": "Connected",
        }
        output = self._report(raw)
        self.assertIn("192.168.1.5:8080", output)
        self.assertIn("Endpoint", output)

    def test_cli_hint_shown_when_cli_unavailable(self):
        raw = {
            "backend": "llama_cpp", "available": False, "enabled": True,
            "transport": "cli", "healthy": False,
            "binary_path": "", "model_path": "",
            "timeout_seconds": 20,
            "detail": "binary_path not set",
        }
        output = self._report(raw)
        self.assertIn("binary_path", output)

    def test_server_hint_shown_when_server_unavailable(self):
        raw = {
            "backend": "llama_cpp", "available": False, "enabled": True,
            "transport": "server", "healthy": False,
            "endpoint": "http://localhost:8080",
            "timeout_seconds": 20, "max_tokens": 256, "temperature": 0.2,
            "detail": "not responding",
        }
        output = self._report(raw)
        self.assertIn("./server", output)

    def test_local_backend_no_transport_line(self):
        raw = {
            "backend": "local", "available": True, "enabled": False,
            "transport": None, "healthy": True,
            "detail": "deterministic keyword matching",
        }
        output = self._report(raw)
        self.assertNotIn("Transport:", output)


# ── AIAssistSkill — endpoint config propagated ────────────────────────────────

class TestAIAssistSkillEndpointConfig(unittest.TestCase):

    def setUp(self):
        import skills.registry as reg_mod
        reg_mod._registry = None

    def _skill_with_config(self, config: dict):
        from skills.ai_assist_skill import AIAssistSkill
        with patch("skills.ai_assist_skill._load_config", return_value=config):
            return AIAssistSkill()

    def test_endpoint_used_from_config(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"endpoint": "http://192.168.1.1:9001",
                          "transport": "server", "timeout_seconds": 15,
                          "max_tokens": 128, "temperature": 0.1},
        })
        backend = skill._get_backend()
        self.assertEqual(backend._endpoint, "http://192.168.1.1:9001")

    def test_server_url_legacy_key_still_works(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"server_url": "http://old-style:8080",
                          "timeout_seconds": 10},
        })
        backend = skill._get_backend()
        self.assertEqual(backend._endpoint, "http://old-style:8080")

    def test_max_tokens_propagated(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"endpoint": "http://localhost:8080",
                          "transport": "server", "timeout_seconds": 20,
                          "max_tokens": 512, "temperature": 0.3},
        })
        backend = skill._get_backend()
        self.assertEqual(backend._max_tokens, 512)

    def test_temperature_propagated(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"endpoint": "http://localhost:8080",
                          "transport": "server", "timeout_seconds": 20,
                          "max_tokens": 256, "temperature": 0.15},
        })
        backend = skill._get_backend()
        self.assertAlmostEqual(backend._temperature, 0.15)

    def test_cli_transport_propagated(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"transport": "cli",
                          "binary_path": "/usr/bin/llama-cli",
                          "model_path": "/sdcard/model.gguf",
                          "timeout_seconds": 30,
                          "max_tokens": 256, "temperature": 0.2},
        })
        backend = skill._get_backend()
        self.assertEqual(backend._transport, "cli")
        self.assertEqual(backend._binary_path, "/usr/bin/llama-cli")
        self.assertEqual(backend._model_path, "/sdcard/model.gguf")

    def test_get_backend_status_has_transport_field(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"endpoint": "http://localhost:8080",
                          "transport": "server", "timeout_seconds": 5,
                          "max_tokens": 256, "temperature": 0.2},
        })
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = skill.get_backend_status()
        self.assertIn("transport", status)

    def test_get_backend_status_has_max_tokens(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"endpoint": "http://localhost:8080",
                          "transport": "server", "timeout_seconds": 5,
                          "max_tokens": 400, "temperature": 0.2},
        })
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = skill.get_backend_status()
        self.assertEqual(status["max_tokens"], 400)

    def test_get_backend_status_has_temperature(self):
        skill = self._skill_with_config({
            "enabled": True, "backend": "llama_cpp",
            "mode": "assist_only", "fallback_intent_suggestion": False,
            "llama_cpp": {"endpoint": "http://localhost:8080",
                          "transport": "server", "timeout_seconds": 5,
                          "max_tokens": 256, "temperature": 0.25},
        })
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = skill.get_backend_status()
        self.assertAlmostEqual(status["temperature"], 0.25)


# ── JSON prompts sanity checks ────────────────────────────────────────────────

class TestJsonPrompts(unittest.TestCase):

    def test_system_prompt_is_string(self):
        from llm.prompts import LLAMA_SYSTEM_PROMPT
        self.assertIsInstance(LLAMA_SYSTEM_PROMPT, str)
        self.assertGreater(len(LLAMA_SYSTEM_PROMPT), 50)

    def test_suggest_command_template_has_json_schema(self):
        from llm.prompts import SUGGEST_COMMAND_JSON_TEMPLATE
        self.assertIn("suggested_command", SUGGEST_COMMAND_JSON_TEMPLATE)
        self.assertIn("rationale", SUGGEST_COMMAND_JSON_TEMPLATE)
        self.assertIn("confidence", SUGGEST_COMMAND_JSON_TEMPLATE)

    def test_explain_result_template_has_json_schema(self):
        from llm.prompts import EXPLAIN_RESULT_JSON_TEMPLATE
        self.assertIn("summary", EXPLAIN_RESULT_JSON_TEMPLATE)
        self.assertIn("safety_note", EXPLAIN_RESULT_JSON_TEMPLATE)
        self.assertIn("suggested_next_step", EXPLAIN_RESULT_JSON_TEMPLATE)

    def test_clarify_request_template_has_json_schema(self):
        from llm.prompts import CLARIFY_REQUEST_JSON_TEMPLATE
        self.assertIn("clarification_needed", CLARIFY_REQUEST_JSON_TEMPLATE)
        self.assertIn("clarification_question", CLARIFY_REQUEST_JSON_TEMPLATE)
        self.assertIn("candidate_intents", CLARIFY_REQUEST_JSON_TEMPLATE)

    def test_suggest_intent_template_has_json_schema(self):
        from llm.prompts import SUGGEST_INTENT_JSON_TEMPLATE
        self.assertIn("intent", SUGGEST_INTENT_JSON_TEMPLATE)
        self.assertIn("confidence", SUGGEST_INTENT_JSON_TEMPLATE)
        self.assertIn("explanation", SUGGEST_INTENT_JSON_TEMPLATE)

    def test_templates_are_formattable(self):
        from llm.prompts import (
            SUGGEST_COMMAND_JSON_TEMPLATE,
            EXPLAIN_RESULT_JSON_TEMPLATE,
            CLARIFY_REQUEST_JSON_TEMPLATE,
            SUGGEST_INTENT_JSON_TEMPLATE,
        )
        SUGGEST_COMMAND_JSON_TEMPLATE.format(
            user_text="test", available_intents="- doctor\n- backup_folder"
        )
        EXPLAIN_RESULT_JSON_TEMPLATE.format(
            last_command="doctor", last_result="ok"
        )
        CLARIFY_REQUEST_JSON_TEMPLATE.format(
            user_text="test", available_intents="- doctor"
        )
        SUGGEST_INTENT_JSON_TEMPLATE.format(
            user_text="test", allowed_intents="- doctor"
        )

    def test_system_prompt_mentions_advisory_only(self):
        from llm.prompts import LLAMA_SYSTEM_PROMPT
        self.assertIn("advisory", LLAMA_SYSTEM_PROMPT.lower())

    def test_system_prompt_forbids_execution(self):
        from llm.prompts import LLAMA_SYSTEM_PROMPT
        self.assertIn("execute", LLAMA_SYSTEM_PROMPT.lower())

    def test_suggest_intent_template_warns_against_inventing_intents(self):
        from llm.prompts import SUGGEST_INTENT_JSON_TEMPLATE
        self.assertIn("Never", SUGGEST_INTENT_JSON_TEMPLATE)


# ── Existing commands unaffected ──────────────────────────────────────────────

class TestExistingCommandsUnaffectedV7(unittest.TestCase):

    def _parse(self, cmd):
        from agent.parser import parse_command
        return parse_command(cmd)

    def test_doctor_unchanged(self):
        self.assertEqual(self._parse("doctor").intent, "doctor")

    def test_storage_report_unchanged(self):
        self.assertEqual(self._parse("storage report /sdcard").intent, "storage_report")

    def test_find_duplicates_unchanged(self):
        self.assertEqual(
            self._parse("find duplicates /sdcard/Download").intent, "find_duplicates"
        )

    def test_backup_requires_confirmation(self):
        from agent.planner import plan
        p = plan(self._parse("back up /sdcard/Documents to /sdcard/Backup"))
        self.assertTrue(p.requires_confirmation)

    def test_show_files_unchanged(self):
        p = self._parse("show files in /sdcard/Download")
        self.assertEqual(p.intent, "show_files")

    def test_show_skills_unchanged(self):
        self.assertEqual(self._parse("show skills").intent, "show_skills")

    def test_skill_info_unchanged(self):
        self.assertEqual(self._parse("skill info ai_assist").intent, "skill_info")


# ── main.py version ───────────────────────────────────────────────────────────

class TestMainV07(unittest.TestCase):

    def test_banner_says_v10(self):
        from main import BANNER
        self.assertIn("v1.0", BANNER)

    def test_help_text_says_v10(self):
        from main import HELP_TEXT
        self.assertIn("v1.0", HELP_TEXT)

    def test_help_text_mentions_ai_backend_status(self):
        from main import HELP_TEXT
        self.assertIn("ai backend status", HELP_TEXT)

    def test_help_text_mentions_llama_cpp(self):
        from main import HELP_TEXT
        self.assertIn("llama_cpp", HELP_TEXT)

    def test_help_text_mentions_backends(self):
        from main import HELP_TEXT
        self.assertIn("Backends", HELP_TEXT)

    def test_ai_backend_status_not_in_session_update(self):
        import main
        from agent.context import ContextMemory
        # v1.0: _session dict replaced by ContextMemory (_ctx)
        self.assertTrue(hasattr(main, "_ctx"))
        self.assertIsInstance(main._ctx, ContextMemory)


# ── Audit: M2 — CLI stderr capped at 80 chars ────────────────────────────────

class TestCliStderrCapped(unittest.TestCase):

    def _make_cli_backend(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        return LlamaCppBackend(
            transport="cli",
            binary_path="/usr/bin/llama-cli",
            model_path="/sdcard/models/model.gguf",
        )

    def _cli_proc(self, stderr: str, returncode: int = 0, stdout: str = ""):
        import subprocess
        proc = MagicMock(spec=subprocess.CompletedProcess)
        proc.returncode = returncode
        proc.stdout = stdout
        proc.stderr = stderr
        return proc

    def test_stderr_over_80_chars_is_truncated(self):
        b = self._make_cli_backend()
        long_stderr = "x" * 200
        proc = self._cli_proc(long_stderr, returncode=1)
        with patch("os.path.isfile", return_value=True), \
             patch("subprocess.run", return_value=proc):
            with self.assertRaises(ConnectionError) as ctx:
                b._chat_cli("sys", "user")
        msg = str(ctx.exception)
        self.assertIn("exited with code 1", msg)
        self.assertIn("…", msg)
        self.assertLessEqual(len(msg), 300)

    def test_stderr_exactly_80_chars_is_not_truncated(self):
        b = self._make_cli_backend()
        stderr_80 = "e" * 80
        proc = self._cli_proc(stderr_80, returncode=1)
        with patch("os.path.isfile", return_value=True), \
             patch("subprocess.run", return_value=proc):
            with self.assertRaises(ConnectionError) as ctx:
                b._chat_cli("sys", "user")
        msg = str(ctx.exception)
        self.assertNotIn("…", msg)
        self.assertIn("e" * 80, msg)

    def test_stderr_under_80_chars_is_not_truncated(self):
        b = self._make_cli_backend()
        proc = self._cli_proc("short error", returncode=1)
        with patch("os.path.isfile", return_value=True), \
             patch("subprocess.run", return_value=proc):
            with self.assertRaises(ConnectionError) as ctx:
                b._chat_cli("sys", "user")
        self.assertIn("short error", str(ctx.exception))
        self.assertNotIn("…", str(ctx.exception))

    def test_empty_stderr_shows_no_stderr_message(self):
        b = self._make_cli_backend()
        proc = self._cli_proc("", returncode=1)
        with patch("os.path.isfile", return_value=True), \
             patch("subprocess.run", return_value=proc):
            with self.assertRaises(ConnectionError) as ctx:
                b._chat_cli("sys", "user")
        self.assertIn("no stderr", str(ctx.exception))

    def test_stderr_content_in_fallback_rationale_is_short(self):
        """End-to-end: oversized stderr must not make the rationale unreadably long."""
        b = self._make_cli_backend()
        proc = self._cli_proc("z" * 500, returncode=1)
        with patch("os.path.isfile", return_value=True), \
             patch("subprocess.run", return_value=proc):
            result = b.suggest_command("back up my files", ["backup_folder"])
        self.assertLessEqual(len(result.rationale), 600)


# ── Audit: M3 — Reporter distinguishes 0%-confidence fallback from real suggestion ──

class TestReporterSuggestCommandFallback(unittest.TestCase):
    """
    M3 audit fix: reporter must distinguish backend-unavailable fallback
    (confidence==0.0 AND "unavailable" in rationale) from a genuine 0%-confidence
    model response (which should NOT show the "AI unavailable" warning).
    """

    _FALLBACK_RATIONALE = (
        "AI backend unavailable (timed out). "
        "'doctor' checks your environment and is always a safe starting point."
    )

    def _make_report(self, confidence: float, rationale: str = "checks your setup") -> str:
        from agent.reporter import report_result
        from agent.models import ExecutionResult, OperationStatus
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="",
            raw_results=[{
                "success": True,
                "type": "suggest_command",
                "suggested_command": "doctor",
                "rationale": rationale,
                "confidence": confidence,
            }],
        )
        return report_result(result, "ai_suggest_command", confirmed=False)

    def test_explicit_fallback_shows_unavailable_warning(self):
        """confidence==0.0 + 'unavailable' in rationale → show warning."""
        output = self._make_report(0.0, self._FALLBACK_RATIONALE)
        self.assertIn("unavailable", output.lower())
        self.assertIn("safe default", output.lower())

    def test_explicit_fallback_still_shows_command_and_confidence(self):
        output = self._make_report(0.0, self._FALLBACK_RATIONALE)
        self.assertIn("doctor", output)
        self.assertIn("0%", output)

    def test_zero_confidence_without_unavailable_in_rationale_no_warning(self):
        """
        A model legitimately returning 0.0 confidence must NOT trigger the warning.
        Only the explicit fallback (rationale contains 'unavailable') should.
        """
        output = self._make_report(0.0, "very uncertain match")
        self.assertNotIn("safe default", output.lower())

    def test_nonzero_confidence_does_not_show_unavailable_warning(self):
        output = self._make_report(0.75, "checks your environment")
        self.assertNotIn("safe default", output.lower())
        self.assertIn("75%", output)

    def test_low_but_nonzero_confidence_no_unavailable_warning(self):
        output = self._make_report(0.1, "weak match")
        self.assertNotIn("safe default", output.lower())
        self.assertIn("10%", output)

    def test_unavailable_warning_mentions_ai_backend_status(self):
        output = self._make_report(0.0, self._FALLBACK_RATIONALE)
        self.assertIn("ai backend status", output.lower())

    def test_advisory_header_always_present_for_fallback(self):
        output = self._make_report(0.0, self._FALLBACK_RATIONALE)
        self.assertIn("advisory only", output.lower())

    def test_advisory_header_always_present_for_real_suggestion(self):
        output = self._make_report(0.85, "checks your environment")
        self.assertIn("advisory only", output.lower())


class TestNullFieldCoercion(unittest.TestCase):
    """
    Null JSON fields (JSON null → Python None) must never surface as the
    string "None" in user-facing output.  Each method must either use a
    safe default or fall back gracefully.
    """

    def _make_backend(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        return LlamaCppBackend(endpoint="http://127.0.0.1:8080")

    # ── suggest_command ────────────────────────────────────────────────────────

    @patch("llm.llama_cpp_backend.LlamaCppBackend._chat")
    def test_null_suggested_command_triggers_fallback(self, mock_chat):
        """null suggested_command must NOT produce the string 'None' as a command."""
        mock_chat.return_value = {"suggested_command": None, "rationale": "fine", "confidence": 0.9}
        result = self._make_backend().suggest_command("do something", ["doctor"])
        self.assertNotEqual(result.suggested_command, "None")
        # Must fall back to the safe default
        self.assertEqual(result.suggested_command, "doctor")
        self.assertEqual(result.confidence, 0.0)

    @patch("llm.llama_cpp_backend.LlamaCppBackend._chat")
    def test_null_rationale_uses_default_text(self, mock_chat):
        """null rationale must NOT produce the string 'None' — uses 'No rationale provided.'"""
        mock_chat.return_value = {"suggested_command": "doctor", "rationale": None, "confidence": 0.8}
        result = self._make_backend().suggest_command("do something", ["doctor"])
        self.assertNotEqual(result.rationale, "None")
        self.assertEqual(result.rationale, "No rationale provided.")

    @patch("llm.llama_cpp_backend.LlamaCppBackend._chat")
    def test_null_confidence_in_suggest_command_falls_back(self, mock_chat):
        """null confidence cannot be float()-coerced — must fall back gracefully."""
        mock_chat.return_value = {"suggested_command": "doctor", "rationale": "ok", "confidence": None}
        result = self._make_backend().suggest_command("do something", ["doctor"])
        # confidence=None → float(None or 0.0) = 0.0 — still produces a valid result
        # (this path is NOT a fallback since 'or 0.0' handles None safely)
        self.assertIsInstance(result.confidence, float)
        self.assertNotIn("None", result.rationale)

    # ── explain_result ─────────────────────────────────────────────────────────

    @patch("llm.llama_cpp_backend.LlamaCppBackend._chat")
    def test_null_summary_triggers_fallback(self, mock_chat):
        """null summary must NOT produce 'None' — must fall back to safe message."""
        mock_chat.return_value = {
            "summary": None, "safety_note": None, "suggested_next_step": None
        }
        result = self._make_backend().explain_result("doctor", "ok")
        self.assertNotEqual(result.summary, "None")
        self.assertNotIn("None", result.summary)

    @patch("llm.llama_cpp_backend.LlamaCppBackend._chat")
    def test_null_safety_note_becomes_none_not_string(self, mock_chat):
        """null safety_note must be Python None, not the string 'None'."""
        mock_chat.return_value = {
            "summary": "All good.", "safety_note": None, "suggested_next_step": None
        }
        result = self._make_backend().explain_result("doctor", "ok")
        self.assertIsNone(result.safety_note)

    @patch("llm.llama_cpp_backend.LlamaCppBackend._chat")
    def test_null_next_step_becomes_none_not_string(self, mock_chat):
        """null suggested_next_step must be Python None, not the string 'None'."""
        mock_chat.return_value = {
            "summary": "All good.", "safety_note": None, "suggested_next_step": None
        }
        result = self._make_backend().explain_result("doctor", "ok")
        self.assertIsNone(result.suggested_next_step)

    # ── clarify_request ────────────────────────────────────────────────────────

    @patch("llm.llama_cpp_backend.LlamaCppBackend._chat")
    def test_null_clarification_question_becomes_none(self, mock_chat):
        """null clarification_question must be Python None, not the string 'None'."""
        mock_chat.return_value = {
            "clarification_needed": True,
            "clarification_question": None,
            "candidate_intents": [],
        }
        result = self._make_backend().clarify_request("hmm", ["doctor"])
        self.assertIsNone(result.clarification_question)

    @patch("llm.llama_cpp_backend.LlamaCppBackend._chat")
    def test_null_clarification_needed_defaults_to_true(self, mock_chat):
        """null clarification_needed must default to True (ask for clarification), not False."""
        mock_chat.return_value = {
            "clarification_needed": None,
            "clarification_question": "What do you mean?",
            "candidate_intents": [],
        }
        result = self._make_backend().clarify_request("hmm", ["doctor"])
        self.assertTrue(result.clarification_needed)

    # ── suggest_intent ─────────────────────────────────────────────────────────

    @patch("llm.llama_cpp_backend.LlamaCppBackend._chat")
    def test_null_explanation_uses_default_text(self, mock_chat):
        """null explanation must NOT produce the string 'None' — uses 'No explanation provided.'"""
        mock_chat.return_value = {
            "intent": "doctor", "confidence": 0.8, "explanation": None
        }
        result = self._make_backend().suggest_intent("check my phone", ["doctor"])
        self.assertNotEqual(result.explanation, "None")
        self.assertEqual(result.explanation, "No explanation provided.")

    @patch("llm.llama_cpp_backend.LlamaCppBackend._chat")
    def test_null_intent_treated_as_no_match(self, mock_chat):
        """null intent from model must be treated as no confident match."""
        mock_chat.return_value = {
            "intent": None, "confidence": 0.8, "explanation": "unclear"
        }
        result = self._make_backend().suggest_intent("check my phone", ["doctor"])
        self.assertIsNone(result.intent)

    # ── _coerce_str helper directly ────────────────────────────────────────────

    def test_coerce_str_with_none_returns_default(self):
        from llm.llama_cpp_backend import _coerce_str
        self.assertEqual(_coerce_str(None), "")

    def test_coerce_str_with_string_returns_stripped(self):
        from llm.llama_cpp_backend import _coerce_str
        self.assertEqual(_coerce_str("  hello  "), "hello")

    def test_coerce_str_with_int_returns_str(self):
        from llm.llama_cpp_backend import _coerce_str
        self.assertEqual(_coerce_str(42), "42")

    def test_coerce_str_custom_default_returned_for_none(self):
        from llm.llama_cpp_backend import _coerce_str
        self.assertEqual(_coerce_str(None, default="fallback"), "fallback")


class TestServerStatusDetail(unittest.TestCase):
    """
    _server_status() detail message must be concise.
    The reporter footer already prints the startup command — the detail
    field must NOT duplicate it.
    """

    def _make_backend(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        return LlamaCppBackend(endpoint="http://127.0.0.1:8080")

    @patch("llm.llama_cpp_backend.LlamaCppBackend.is_available", return_value=False)
    def test_unreachable_detail_does_not_contain_start_with(self, _):
        """Detail must not repeat the startup command — reporter footer already shows it."""
        backend = self._make_backend()
        status = backend.get_status()
        self.assertNotIn("Start with:", status.detail)
        self.assertNotIn("./server", status.detail)

    @patch("llm.llama_cpp_backend.LlamaCppBackend.is_available", return_value=False)
    def test_unreachable_detail_contains_endpoint(self, _):
        """Detail must still identify which endpoint is not responding."""
        backend = self._make_backend()
        status = backend.get_status()
        self.assertIn("127.0.0.1:8080", status.detail)

    @patch("llm.llama_cpp_backend.LlamaCppBackend.is_available", return_value=True)
    def test_reachable_detail_contains_connected(self, _):
        """When server is up, detail says 'Connected to <endpoint>'."""
        backend = self._make_backend()
        status = backend.get_status()
        self.assertIn("Connected to", status.detail)


if __name__ == "__main__":
    unittest.main()
