"""
v1.1 — Backend status tests.

Covers:
  - local backend status: always available, correct fields
  - llama.cpp server status: available + unavailable formatting
  - llama.cpp CLI status: available + unavailable formatting
  - Ollama status: available + unavailable, troubleshooting guidance
  - BackendStatus v1.1 extended fields: endpoint, timeout_seconds, model_name,
    capabilities, troubleshooting
  - AIAssistSkill.get_backend_status() dict shape for all backends
  - reporter formatting for ai_backend_status intent
  - failure handling: timeout, invalid JSON, empty output, unsupported intent,
    low-confidence suggestion, malformed config
"""
import sys
import os
import unittest
from unittest.mock import patch, MagicMock
import urllib.error

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from llm.schemas import BackendStatus


# ── Helper ────────────────────────────────────────────────────────────────────

def _make_http_response(body: bytes, status: int = 200):
    resp = MagicMock()
    resp.status = status
    resp.read.return_value = body
    resp.__enter__ = lambda s: s
    resp.__exit__ = MagicMock(return_value=False)
    return resp


# ── BackendStatus v1.1 extended schema ────────────────────────────────────────

class TestBackendStatusExtendedFields(unittest.TestCase):

    def test_v10_fields_still_work_positionally(self):
        s = BackendStatus(
            available=True, backend_name="local",
            transport=None, healthy=True, detail="ok"
        )
        self.assertTrue(s.available)
        self.assertEqual(s.backend_name, "local")
        self.assertIsNone(s.transport)

    def test_v11_endpoint_field_default_none(self):
        s = BackendStatus(available=True, backend_name="local",
                          transport=None, healthy=True, detail="ok")
        self.assertIsNone(s.endpoint)

    def test_v11_timeout_field_default_none(self):
        s = BackendStatus(available=True, backend_name="local",
                          transport=None, healthy=True, detail="ok")
        self.assertIsNone(s.timeout_seconds)

    def test_v11_model_name_field_default_none(self):
        s = BackendStatus(available=True, backend_name="local",
                          transport=None, healthy=True, detail="ok")
        self.assertIsNone(s.model_name)

    def test_v11_capabilities_field_default_empty_list(self):
        s = BackendStatus(available=True, backend_name="local",
                          transport=None, healthy=True, detail="ok")
        self.assertEqual(s.capabilities, [])

    def test_v11_troubleshooting_field_default_none(self):
        s = BackendStatus(available=True, backend_name="local",
                          transport=None, healthy=True, detail="ok")
        self.assertIsNone(s.troubleshooting)

    def test_v11_all_fields_set(self):
        s = BackendStatus(
            available=True, backend_name="ollama",
            transport="server", healthy=True, detail="running",
            endpoint="http://127.0.0.1:11434",
            timeout_seconds=30,
            model_name="llama3",
            capabilities=["suggest_command", "explain_result"],
            troubleshooting=None,
        )
        self.assertEqual(s.endpoint, "http://127.0.0.1:11434")
        self.assertEqual(s.timeout_seconds, 30)
        self.assertEqual(s.model_name, "llama3")
        self.assertIn("suggest_command", s.capabilities)
        self.assertIsNone(s.troubleshooting)

    def test_v11_troubleshooting_set_when_unavailable(self):
        s = BackendStatus(
            available=False, backend_name="ollama",
            transport="server", healthy=False,
            detail="not responding",
            troubleshooting="Run: ollama serve",
        )
        self.assertIsNotNone(s.troubleshooting)
        self.assertIn("ollama serve", s.troubleshooting)


# ── Local backend status ──────────────────────────────────────────────────────

class TestLocalBackendStatus(unittest.TestCase):

    def _get_status(self) -> BackendStatus:
        from llm.local_backend import LocalBackend
        return LocalBackend().get_status()

    def test_local_always_available(self):
        self.assertTrue(self._get_status().available)

    def test_local_always_healthy(self):
        self.assertTrue(self._get_status().healthy)

    def test_local_backend_name(self):
        self.assertEqual(self._get_status().backend_name, "local")

    def test_local_transport_is_none(self):
        self.assertIsNone(self._get_status().transport)

    def test_local_detail_is_str(self):
        self.assertIsInstance(self._get_status().detail, str)

    def test_local_detail_not_empty(self):
        self.assertTrue(self._get_status().detail.strip())


# ── llama.cpp server status ───────────────────────────────────────────────────

class TestLlamaCppServerStatus(unittest.TestCase):

    def _backend(self, endpoint="http://127.0.0.1:8080"):
        from llm.llama_cpp_backend import LlamaCppBackend
        return LlamaCppBackend(endpoint=endpoint, transport="server", timeout_seconds=5)

    def test_server_available_when_health_ok(self):
        resp = _make_http_response(b'{"status":"ok"}')
        with patch("urllib.request.urlopen", return_value=resp):
            status = self._backend().get_status()
        self.assertTrue(status.available)
        self.assertTrue(status.healthy)

    def test_server_detail_contains_endpoint_when_up(self):
        resp = _make_http_response(b'{"status":"ok"}')
        with patch("urllib.request.urlopen", return_value=resp):
            status = self._backend("http://127.0.0.1:8080").get_status()
        self.assertIn("127.0.0.1:8080", status.detail)

    def test_server_unavailable_when_connection_refused(self):
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = self._backend().get_status()
        self.assertFalse(status.available)
        self.assertFalse(status.healthy)

    def test_server_detail_mentions_not_responding_when_down(self):
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = self._backend().get_status()
        self.assertIn("not responding", status.detail.lower())

    def test_server_transport_field(self):
        resp = _make_http_response(b'{"status":"ok"}')
        with patch("urllib.request.urlopen", return_value=resp):
            status = self._backend().get_status()
        self.assertEqual(status.transport, "server")

    def test_get_status_never_raises(self):
        with patch("urllib.request.urlopen", side_effect=RuntimeError("boom")):
            try:
                status = self._backend().get_status()
            except Exception as e:
                self.fail(f"get_status() raised: {e}")


# ── llama.cpp CLI status ──────────────────────────────────────────────────────

class TestLlamaCppCLIStatus(unittest.TestCase):

    def _backend(self, binary="", model=""):
        from llm.llama_cpp_backend import LlamaCppBackend
        return LlamaCppBackend(
            transport="cli", binary_path=binary, model_path=model
        )

    def test_cli_unavailable_when_paths_missing(self):
        status = self._backend("", "").get_status()
        self.assertFalse(status.available)
        self.assertFalse(status.healthy)

    def test_cli_detail_mentions_binary_path_issue(self):
        status = self._backend("", "").get_status()
        self.assertIn("binary_path", status.detail.lower())

    def test_cli_transport_field(self):
        status = self._backend("", "").get_status()
        self.assertEqual(status.transport, "cli")

    def test_cli_available_when_files_exist(self):
        import tempfile
        with tempfile.NamedTemporaryFile() as bf, \
             tempfile.NamedTemporaryFile() as mf:
            status = self._backend(bf.name, mf.name).get_status()
        self.assertTrue(status.available)
        self.assertTrue(status.healthy)


# ── Ollama status ─────────────────────────────────────────────────────────────

class TestOllamaStatus(unittest.TestCase):

    def _backend(self, endpoint="http://127.0.0.1:11434", model="llama3"):
        from llm.ollama_backend import OllamaBackend
        return OllamaBackend(endpoint=endpoint, model=model, timeout_seconds=5)

    def test_ollama_available_when_server_up(self):
        resp = _make_http_response(b'{"models":[]}')
        with patch("urllib.request.urlopen", return_value=resp):
            status = self._backend().get_status()
        self.assertTrue(status.available)
        self.assertTrue(status.healthy)

    def test_ollama_unavailable_when_server_down(self):
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = self._backend().get_status()
        self.assertFalse(status.available)
        self.assertFalse(status.healthy)

    def test_ollama_detail_mentions_endpoint_when_up(self):
        resp = _make_http_response(b'{"models":[]}')
        with patch("urllib.request.urlopen", return_value=resp):
            status = self._backend("http://127.0.0.1:11434").get_status()
        self.assertIn("127.0.0.1:11434", status.detail)

    def test_ollama_detail_mentions_not_responding_when_down(self):
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = self._backend().get_status()
        self.assertIn("not responding", status.detail.lower())

    def test_ollama_backend_name(self):
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = self._backend().get_status()
        self.assertEqual(status.backend_name, "ollama")

    def test_ollama_transport_is_server(self):
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = self._backend().get_status()
        self.assertEqual(status.transport, "server")

    def test_ollama_endpoint_field_present(self):
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = self._backend("http://127.0.0.1:11434").get_status()
        self.assertEqual(status.endpoint, "http://127.0.0.1:11434")

    def test_ollama_model_name_field_present(self):
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = self._backend(model="mistral").get_status()
        self.assertEqual(status.model_name, "mistral")

    def test_ollama_capabilities_list_present(self):
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = self._backend().get_status()
        self.assertIsInstance(status.capabilities, list)
        self.assertIn("suggest_command", status.capabilities)

    def test_ollama_troubleshooting_present_when_down(self):
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = self._backend().get_status()
        self.assertIsNotNone(status.troubleshooting)
        self.assertIn("ollama serve", status.troubleshooting)

    def test_ollama_troubleshooting_none_when_up(self):
        resp = _make_http_response(b'{"models":[]}')
        with patch("urllib.request.urlopen", return_value=resp):
            status = self._backend().get_status()
        self.assertIsNone(status.troubleshooting)

    def test_ollama_get_status_never_raises(self):
        with patch("urllib.request.urlopen", side_effect=RuntimeError("boom")):
            try:
                status = self._backend().get_status()
            except Exception as e:
                self.fail(f"get_status() raised: {e}")


# ── AIAssistSkill.get_backend_status() dict shape ─────────────────────────────

class TestAIAssistSkillBackendStatusDict(unittest.TestCase):

    def _skill(self, backend: str, extra: dict | None = None):
        from skills.ai_assist_skill import AIAssistSkill
        cfg = {
            "enabled": False, "backend": backend,
            "mode": "assist_only", "fallback_intent_suggestion": False,
        }
        if extra:
            cfg.update(extra)
        with patch("skills.ai_assist_skill._load_config", return_value=cfg):
            return AIAssistSkill()

    def test_local_status_dict_keys(self):
        status = self._skill("local").get_backend_status()
        for key in ("backend", "available", "enabled", "transport", "detail"):
            self.assertIn(key, status)

    def test_local_status_always_available(self):
        status = self._skill("local").get_backend_status()
        self.assertTrue(status["available"])

    def test_ollama_status_dict_has_endpoint(self):
        cfg = {"ollama": {"endpoint": "http://127.0.0.1:11434", "model": "llama3"}}
        skill = self._skill("ollama", cfg)
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = skill.get_backend_status()
        self.assertIn("endpoint", status)

    def test_ollama_status_has_troubleshooting_when_down(self):
        cfg = {"ollama": {"endpoint": "http://127.0.0.1:11434", "model": "llama3"}}
        skill = self._skill("ollama", cfg)
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            status = skill.get_backend_status()
        self.assertIn("troubleshooting", status)

    def test_invalid_backend_returns_error_dict(self):
        skill = self._skill("badbackend")
        status = skill.get_backend_status()
        # Should not raise — should return a safe dict with "detail" describing the error
        self.assertIsInstance(status, dict)
        self.assertIn("detail", status)
        self.assertFalse(status.get("available", True))


# ── Failure handling ──────────────────────────────────────────────────────────

class TestOllamaFailureHandling(unittest.TestCase):

    def _backend(self):
        from llm.ollama_backend import OllamaBackend
        return OllamaBackend(endpoint="http://127.0.0.1:11434", model="llama3",
                             timeout_seconds=5)

    def test_timeout_returns_fallback_suggestion(self):
        with patch.object(self._backend().__class__, "_chat",
                          side_effect=TimeoutError("timed out")):
            backend = self._backend()
            result = backend.suggest_command("check storage", ["storage_report"])
        self.assertEqual(result.suggested_command, "doctor")
        self.assertEqual(result.confidence, 0.0)

    def test_connection_error_returns_fallback_suggestion(self):
        with patch.object(self._backend().__class__, "_chat",
                          side_effect=ConnectionError("refused")):
            backend = self._backend()
            result = backend.suggest_command("check storage", ["storage_report"])
        self.assertEqual(result.suggested_command, "doctor")

    def test_invalid_json_returns_fallback_suggestion(self):
        with patch.object(self._backend().__class__, "_chat",
                          side_effect=ValueError("bad json")):
            backend = self._backend()
            result = backend.suggest_command("check storage", ["storage_report"])
        self.assertEqual(result.suggested_command, "doctor")
        self.assertEqual(result.confidence, 0.0)

    def test_unsupported_intent_discarded(self):
        fake_data = {
            "intent": "rm_rf_everything",
            "confidence": 0.9,
            "explanation": "some explanation",
        }
        with patch.object(self._backend().__class__, "_chat",
                          return_value=fake_data):
            backend = self._backend()
            result = backend.suggest_intent("delete everything", ["storage_report"])
        self.assertIsNone(result.intent)
        self.assertEqual(result.confidence, 0.0)

    def test_low_confidence_intent_discarded(self):
        fake_data = {
            "intent": "storage_report",
            "confidence": 0.2,
            "explanation": "weak match",
        }
        with patch.object(self._backend().__class__, "_chat",
                          return_value=fake_data):
            backend = self._backend()
            result = backend.suggest_intent("do something", ["storage_report"])
        self.assertIsNone(result.intent)
        self.assertLess(result.confidence, 0.4)

    def test_empty_suggest_command_falls_back(self):
        with patch.object(self._backend().__class__, "_chat",
                          return_value={"suggested_command": "", "rationale": "", "confidence": 0.8}):
            backend = self._backend()
            result = backend.suggest_command("check storage", ["storage_report"])
        self.assertEqual(result.suggested_command, "doctor")

    def test_explain_result_empty_command_handled(self):
        backend = self._backend()
        result = backend.explain_result("", "")
        self.assertIn("No previous command", result.summary)

    def test_fallback_suggest_command_is_advisory_only(self):
        with patch.object(self._backend().__class__, "_chat",
                          side_effect=TimeoutError("x")):
            backend = self._backend()
            result = backend.suggest_command("x", ["doctor"])
        # Advisory only — should not suggest execution
        self.assertIsInstance(result.suggested_command, str)
        self.assertEqual(result.confidence, 0.0)


# ── Reporter formatting ───────────────────────────────────────────────────────

class TestReporterAIBackendStatusOllama(unittest.TestCase):

    def _report(self, raw: dict) -> str:
        from agent.reporter import report_result
        from agent.models import OperationStatus
        result = MagicMock()
        result.status = OperationStatus.SUCCESS
        result.message = "AI Backend Status"
        result.errors = []
        result.raw_results = [raw]
        return report_result(result, "ai_backend_status", confirmed=False)

    def test_ollama_backend_name_shown(self):
        raw = {
            "backend": "ollama", "available": False, "enabled": True,
            "transport": "server", "healthy": False,
            "detail": "not responding",
            "endpoint": "http://127.0.0.1:11434",
            "model_name": "llama3",
            "timeout_seconds": 30,
            "troubleshooting": "Start Ollama: ollama serve\n  Pull model: ollama pull llama3",
        }
        out = self._report(raw)
        self.assertIn("ollama", out)

    def test_ollama_endpoint_shown(self):
        raw = {
            "backend": "ollama", "available": False, "enabled": True,
            "transport": "server", "healthy": False,
            "detail": "not responding",
            "endpoint": "http://127.0.0.1:11434",
        }
        out = self._report(raw)
        self.assertIn("11434", out)

    def test_ollama_troubleshooting_shown_when_down(self):
        raw = {
            "backend": "ollama", "available": False, "enabled": True,
            "transport": "server", "healthy": False,
            "detail": "not responding",
            "troubleshooting": "Start Ollama: ollama serve",
        }
        out = self._report(raw)
        self.assertIn("ollama serve", out)

    def test_local_backend_no_endpoint_shown(self):
        raw = {
            "backend": "local", "available": True, "enabled": True,
            "transport": None, "healthy": True,
            "detail": "deterministic keyword matcher",
        }
        out = self._report(raw)
        self.assertIn("local", out)
        # No endpoint for local
        self.assertNotIn("Endpoint", out)

    def test_capabilities_shown_when_present(self):
        raw = {
            "backend": "ollama", "available": True, "enabled": True,
            "transport": "server", "healthy": True,
            "detail": "running",
            "capabilities": ["suggest_command", "explain_result"],
        }
        out = self._report(raw)
        self.assertIn("suggest_command", out)


if __name__ == "__main__":
    unittest.main()
