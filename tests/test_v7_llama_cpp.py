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

    def test_chat_raises_timeout_error(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen", side_effect=socket.timeout()):
            with self.assertRaises(TimeoutError) as ctx:
                backend._chat("sys", "user")
        self.assertIn("timed out", str(ctx.exception).lower())

    def test_chat_raises_connection_error(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            with self.assertRaises(ConnectionError) as ctx:
                backend._chat("sys", "user")
        self.assertIn("unavailable", str(ctx.exception).lower())

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

    def test_falls_back_on_timeout(self):
        backend = self._make_backend()
        with patch("urllib.request.urlopen", side_effect=socket.timeout()):
            result = backend.suggest_command("check my setup", ["doctor"])
        self.assertEqual(result.suggested_command, "doctor")
        self.assertEqual(result.confidence, 0.0)
        self.assertIn("timed out", result.rationale.lower())

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

    def test_llama_cpp_section_has_server_url(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertIn("server_url", config["llama_cpp"])

    def test_llama_cpp_section_has_timeout_seconds(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertIn("timeout_seconds", config["llama_cpp"])
        self.assertIsInstance(config["llama_cpp"]["timeout_seconds"], int)

    def test_llama_cpp_section_has_model_name(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertIn("model_name", config["llama_cpp"])

    def test_default_backend_is_local(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertEqual(config["backend"], "local")

    def test_enabled_is_false_by_default(self):
        with open(self._config_path()) as f:
            config = json.load(f)
        self.assertFalse(config["enabled"])


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

    def test_banner_says_v07(self):
        from main import BANNER
        self.assertIn("v0.7", BANNER)

    def test_help_text_says_v07(self):
        from main import HELP_TEXT
        self.assertIn("v0.7", HELP_TEXT)

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
        self.assertIn("_session", dir(main))


if __name__ == "__main__":
    unittest.main()
