import unittest
from unittest.mock import patch


class TestConfigCaching(unittest.TestCase):
    def setUp(self):
        from core.config import clear_config_cache
        clear_config_cache()

    def tearDown(self):
        from core.config import clear_config_cache
        clear_config_cache()

    def test_get_settings_uses_cache_but_returns_copy(self):
        from core.config import get_settings

        with patch("core.config._load_json", return_value={"max_large_files": 20}) as mock_load:
            first = get_settings()
            first["max_large_files"] = 99
            second = get_settings()

        self.assertEqual(mock_load.call_count, 1)
        self.assertEqual(second["max_large_files"], 20)

    def test_get_allowed_roots_uses_cache_but_returns_copy(self):
        from core.config import get_allowed_roots

        with patch(
            "core.config._load_json",
            return_value={"allowed_roots": ["/sdcard/Download"]},
        ) as mock_load:
            first = get_allowed_roots()
            first.append("/tmp/extra")
            second = get_allowed_roots()

        self.assertEqual(mock_load.call_count, 1)
        self.assertEqual(second, ["/sdcard/Download"])


class TestSkillRegistryReset(unittest.TestCase):
    def test_reset_registry_creates_fresh_singleton(self):
        from skills.registry import get_registry, reset_registry

        reset_registry()
        first = get_registry()
        reset_registry()
        second = get_registry()

        self.assertIsNot(first, second)


class TestPromptTemplateSync(unittest.TestCase):
    def test_intent_template_includes_recent_intents(self):
        from agent.prompts import INTENT_EXTRACTION_TEMPLATE

        self.assertIn("browser_search", INTENT_EXTRACTION_TEMPLATE)
        self.assertIn("schedule_create", INTENT_EXTRACTION_TEMPLATE)
        self.assertIn("show_skills", INTENT_EXTRACTION_TEMPLATE)


class TestParserRegressions(unittest.TestCase):
    def test_search_for_local_media_path_prefers_list_media(self):
        from agent.parser import detect_intent

        self.assertEqual(
            detect_intent("search for photos in /sdcard/Pictures"),
            "list_media",
        )

    def test_search_for_local_media_with_quoted_path_prefers_list_media(self):
        from agent.parser import detect_intent

        self.assertEqual(
            detect_intent('search for screenshots in "/sdcard/Pictures"'),
            "list_media",
        )

    def test_search_for_podcast_folder_prefers_list_media(self):
        from agent.parser import detect_intent

        self.assertEqual(
            detect_intent("search for podcasts in /sdcard/Podcasts"),
            "list_media",
        )

    def test_search_for_general_query_stays_browser_search(self):
        from agent.parser import detect_intent

        self.assertEqual(
            detect_intent("search for photos in paris"),
            "browser_search",
        )

    def test_search_for_music_in_city_stays_browser_search(self):
        from agent.parser import detect_intent

        self.assertEqual(
            detect_intent("search for music in baghdad"),
            "browser_search",
        )

    def test_open_files_still_means_files_app(self):
        from agent.parser import detect_intent

        self.assertEqual(detect_intent("open files"), "open_app")


class TestContextItRegressions(unittest.TestCase):
    def test_it_in_hyphenated_term_is_not_resolved(self):
        from agent.context import ContextMemory

        ctx = ContextMemory()
        ctx.update(
            "show_files",
            "show files in /sdcard/Download",
            "ok",
            source_path="/sdcard/Download",
            success=True,
        )

        self.assertEqual(
            ctx.resolve("list files in it-projects"),
            "list files in it-projects",
        )

    def test_it_in_underscored_term_is_not_resolved(self):
        from agent.context import ContextMemory

        ctx = ContextMemory()
        ctx.update(
            "show_files",
            "show files in /sdcard/Download",
            "ok",
            source_path="/sdcard/Download",
            success=True,
        )

        self.assertEqual(
            ctx.resolve("list files in it_projects"),
            "list files in it_projects",
        )


if __name__ == "__main__":
    unittest.main()
