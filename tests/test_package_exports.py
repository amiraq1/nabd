import unittest

from agent import ALL_INTENTS, detect_intent, execute, parse_command, plan, report_result
from core import ValidationError, get_allowed_roots, get_history_entry, get_settings, resolve_path, validate_path
from llm import BackendRegistry, KNOWN_BACKENDS, LLMBackend, LocalBackend
from skills import AIAssistSkill, SkillBase, SkillRegistry, get_registry, reset_registry
from tools import browser, schedule, storage


class TestPackageExports(unittest.TestCase):
    def test_agent_exports_resolve_to_public_api(self):
        self.assertTrue(callable(parse_command))
        self.assertTrue(callable(detect_intent))
        self.assertTrue(callable(plan))
        self.assertTrue(callable(execute))
        self.assertTrue(callable(report_result))
        self.assertIn("doctor", ALL_INTENTS)

    def test_core_exports_resolve_to_public_api(self):
        self.assertTrue(callable(get_settings))
        self.assertTrue(callable(get_allowed_roots))
        self.assertTrue(callable(resolve_path))
        self.assertTrue(callable(validate_path))
        self.assertTrue(callable(get_history_entry))
        self.assertTrue(issubclass(ValidationError, Exception))

    def test_tools_exports_expose_modules(self):
        self.assertTrue(hasattr(storage, "get_storage_report"))
        self.assertTrue(hasattr(browser, "browser_search"))
        self.assertTrue(hasattr(schedule, "list_schedules"))

    def test_skills_exports_expose_registry_surface(self):
        self.assertTrue(issubclass(AIAssistSkill, SkillBase))
        self.assertTrue(issubclass(SkillRegistry, object))
        reset_registry()
        registry = get_registry()
        self.assertIsInstance(registry, SkillRegistry)
        self.assertIn("ai_assist", registry.list_names())

    def test_llm_exports_expose_registry_and_backends(self):
        self.assertTrue(issubclass(LocalBackend, LLMBackend))
        self.assertIn("local", KNOWN_BACKENDS)
        registry = BackendRegistry({"backend": "local"})
        self.assertEqual(registry.get_active_name(), "local")


if __name__ == "__main__":
    unittest.main()
