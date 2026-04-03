"""
v1.1 — BackendRegistry tests.

Covers:
  - valid backend selection (local, llama_cpp, ollama)
  - invalid backend name rejected with clear error
  - unavailable backend handled safely by caller
  - list_backends() returns all known names
  - is_known() recognises valid names and rejects typos
  - get_active_name() reflects config value
  - registry is the single instantiation point (no inline backend logic)
"""
import sys
import os
import unittest
from unittest.mock import patch

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from llm.backend_registry import BackendRegistry, KNOWN_BACKENDS


def _registry(backend: str = "local", extra: dict | None = None) -> BackendRegistry:
    cfg: dict = {"backend": backend}
    if extra:
        cfg.update(extra)
    return BackendRegistry(cfg)


class TestKnownBackends(unittest.TestCase):

    def test_known_backends_frozenset(self):
        self.assertIsInstance(KNOWN_BACKENDS, frozenset)

    def test_known_backends_contains_local(self):
        self.assertIn("local", KNOWN_BACKENDS)

    def test_known_backends_contains_llama_cpp(self):
        self.assertIn("llama_cpp", KNOWN_BACKENDS)

    def test_known_backends_contains_ollama(self):
        self.assertIn("ollama", KNOWN_BACKENDS)


class TestListBackends(unittest.TestCase):

    def test_list_backends_returns_sorted_list(self):
        names = BackendRegistry.list_backends()
        self.assertIsInstance(names, list)
        self.assertEqual(names, sorted(names))

    def test_list_backends_contains_all_known(self):
        names = BackendRegistry.list_backends()
        for n in KNOWN_BACKENDS:
            self.assertIn(n, names)

    def test_list_backends_is_static(self):
        r = _registry("local")
        self.assertEqual(r.list_backends(), BackendRegistry.list_backends())


class TestIsKnown(unittest.TestCase):

    def test_local_is_known(self):
        self.assertTrue(BackendRegistry.is_known("local"))

    def test_llama_cpp_is_known(self):
        self.assertTrue(BackendRegistry.is_known("llama_cpp"))

    def test_ollama_is_known(self):
        self.assertTrue(BackendRegistry.is_known("ollama"))

    def test_unknown_name_not_known(self):
        self.assertFalse(BackendRegistry.is_known("gpt4"))

    def test_empty_string_not_known(self):
        self.assertFalse(BackendRegistry.is_known(""))

    def test_none_not_known(self):
        self.assertFalse(BackendRegistry.is_known(None))

    def test_typo_not_known(self):
        self.assertFalse(BackendRegistry.is_known("llama-cpp"))

    def test_capitalised_not_known(self):
        self.assertFalse(BackendRegistry.is_known("Local"))


class TestGetActiveName(unittest.TestCase):

    def test_local_active_name(self):
        self.assertEqual(_registry("local").get_active_name(), "local")

    def test_llama_cpp_active_name(self):
        self.assertEqual(_registry("llama_cpp").get_active_name(), "llama_cpp")

    def test_ollama_active_name(self):
        self.assertEqual(_registry("ollama").get_active_name(), "ollama")

    def test_name_normalised_to_lowercase(self):
        r = BackendRegistry({"backend": "LOCAL"})
        self.assertEqual(r.get_active_name(), "local")

    def test_name_stripped(self):
        r = BackendRegistry({"backend": "  local  "})
        self.assertEqual(r.get_active_name(), "local")

    def test_missing_backend_defaults_to_local(self):
        r = BackendRegistry({})
        self.assertEqual(r.get_active_name(), "local")

    def test_none_backend_defaults_to_local(self):
        r = BackendRegistry({"backend": None})
        self.assertEqual(r.get_active_name(), "local")


class TestGetBackendValid(unittest.TestCase):

    def test_local_returns_local_backend(self):
        from llm.local_backend import LocalBackend
        backend = _registry("local").get_backend()
        self.assertIsInstance(backend, LocalBackend)

    def test_llama_cpp_returns_llama_cpp_backend(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        backend = _registry("llama_cpp").get_backend()
        self.assertIsInstance(backend, LlamaCppBackend)

    def test_ollama_returns_ollama_backend(self):
        from llm.ollama_backend import OllamaBackend
        backend = _registry("ollama").get_backend()
        self.assertIsInstance(backend, OllamaBackend)

    def test_llama_cpp_config_passed_through(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        cfg = {
            "backend": "llama_cpp",
            "llama_cpp": {
                "endpoint": "http://10.0.0.1:9090",
                "timeout_seconds": 45,
                "transport": "server",
            },
        }
        backend = BackendRegistry(cfg).get_backend()
        self.assertIsInstance(backend, LlamaCppBackend)
        self.assertEqual(backend._endpoint, "http://10.0.0.1:9090")
        self.assertEqual(backend._timeout, 45)

    def test_ollama_config_passed_through(self):
        from llm.ollama_backend import OllamaBackend
        cfg = {
            "backend": "ollama",
            "ollama": {
                "endpoint": "http://10.0.0.1:11434",
                "model": "mistral",
                "timeout_seconds": 60,
            },
        }
        backend = BackendRegistry(cfg).get_backend()
        self.assertIsInstance(backend, OllamaBackend)
        self.assertEqual(backend._endpoint, "http://10.0.0.1:11434")
        self.assertEqual(backend._model, "mistral")
        self.assertEqual(backend._timeout, 60)

    def test_llama_cpp_legacy_server_url_key_accepted(self):
        from llm.llama_cpp_backend import LlamaCppBackend
        cfg = {
            "backend": "llama_cpp",
            "llama_cpp": {"server_url": "http://localhost:8888"},
        }
        backend = BackendRegistry(cfg).get_backend()
        self.assertIsInstance(backend, LlamaCppBackend)
        self.assertEqual(backend._endpoint, "http://localhost:8888")


class TestGetBackendInvalid(unittest.TestCase):

    def test_unknown_name_raises_value_error(self):
        with self.assertRaises(ValueError) as ctx:
            _registry("gpt4").get_backend()
        self.assertIn("Unknown AI backend", str(ctx.exception))
        self.assertIn("gpt4", str(ctx.exception))

    def test_error_message_lists_known_backends(self):
        with self.assertRaises(ValueError) as ctx:
            _registry("llamacpp").get_backend()
        msg = str(ctx.exception)
        self.assertIn("local", msg)
        self.assertIn("llama_cpp", msg)
        self.assertIn("ollama", msg)

    def test_empty_string_treated_as_local(self):
        # empty string falsy → normalised to "local" by (name or "local")
        from llm.local_backend import LocalBackend
        backend = _registry("").get_backend()
        self.assertIsInstance(backend, LocalBackend)

    def test_hyphenated_name_rejected(self):
        with self.assertRaises(ValueError):
            _registry("llama-cpp").get_backend()

    def test_uppercase_normalised_before_lookup(self):
        # "LOCAL" → "local" → valid
        from llm.local_backend import LocalBackend
        r = BackendRegistry({"backend": "LOCAL"})
        self.assertIsInstance(r.get_backend(), LocalBackend)


class TestUnavailableBackendHandledSafely(unittest.TestCase):
    """
    Registry raises ValueError for unknown names.
    Callers (AIAssistSkill) must handle this and degrade safely.
    Verify that get_status() on an unreachable backend never raises.
    """

    def test_llama_cpp_get_status_never_raises_when_server_down(self):
        import urllib.error
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            backend = _registry("llama_cpp").get_backend()
            status = backend.get_status()
        self.assertFalse(status.available)
        self.assertIsNotNone(status.detail)

    def test_ollama_get_status_never_raises_when_server_down(self):
        import urllib.error
        with patch("urllib.request.urlopen",
                   side_effect=urllib.error.URLError("refused")):
            backend = _registry("ollama").get_backend()
            status = backend.get_status()
        self.assertFalse(status.available)
        self.assertIsNotNone(status.detail)

    def test_local_get_status_always_available(self):
        status = _registry("local").get_backend().get_status()
        self.assertTrue(status.available)
        self.assertTrue(status.healthy)


class TestRegistryDoesNotInstantiateEagerly(unittest.TestCase):
    """Registry should not build the backend in __init__."""

    def test_init_with_bad_name_does_not_raise(self):
        # ValueError only raised when get_backend() is called, not at construction
        try:
            r = BackendRegistry({"backend": "nonexistent"})
        except Exception as e:
            self.fail(f"BackendRegistry.__init__ raised unexpectedly: {e}")

    def test_get_active_name_works_with_bad_name(self):
        r = BackendRegistry({"backend": "nonexistent"})
        self.assertEqual(r.get_active_name(), "nonexistent")


if __name__ == "__main__":
    unittest.main()
