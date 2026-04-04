import unittest

from agent.reporter import _RAW_DETAIL_RENDERERS, _append_raw_details


class TestReporterRegistry(unittest.TestCase):
    def test_registry_contains_expected_intents(self):
        expected = {
            "doctor",
            "list_large_files",
            "schedule_list",
            "browser_page_title",
            "show_skills",
            "ai_backend_status",
        }
        self.assertTrue(expected.issubset(_RAW_DETAIL_RENDERERS))

    def test_unknown_intent_is_noop(self):
        lines = ["seed"]
        _append_raw_details(lines, {}, "unknown_intent", confirmed=False)
        self.assertEqual(lines, ["seed"])


if __name__ == "__main__":
    unittest.main()
