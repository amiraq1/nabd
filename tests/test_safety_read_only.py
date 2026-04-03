import unittest
from unittest.mock import patch

from agent.models import ParsedIntent, RiskLevel
from agent.safety import validate_intent_safety
from core.exceptions import PathNotAllowedError


ALLOWED_ROOTS = [
    "/sdcard/Download",
    "/sdcard/Documents",
    "/sdcard/Music",
    "/sdcard/Pictures",
]


class TestReadOnlyPathAllowlist(unittest.TestCase):
    def test_storage_report_outside_allowed_roots_is_blocked(self):
        intent = ParsedIntent(
            intent="storage_report",
            source_path="/etc",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with self.assertRaises(PathNotAllowedError):
                validate_intent_safety(intent)

    def test_list_large_files_outside_allowed_roots_is_blocked(self):
        intent = ParsedIntent(
            intent="list_large_files",
            source_path="/data/local/tmp",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with self.assertRaises(PathNotAllowedError):
                validate_intent_safety(intent)

    def test_find_duplicates_outside_allowed_roots_is_blocked(self):
        intent = ParsedIntent(
            intent="find_duplicates",
            source_path="/storage/emulated/0/Secrets",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with self.assertRaises(PathNotAllowedError):
                validate_intent_safety(intent)

    def test_storage_report_without_path_still_allowed(self):
        intent = ParsedIntent(
            intent="storage_report",
            source_path=None,
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            validate_intent_safety(intent)

    def test_find_duplicates_inside_allowed_roots_still_allowed(self):
        intent = ParsedIntent(
            intent="find_duplicates",
            source_path="/sdcard/Download",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            validate_intent_safety(intent)


if __name__ == "__main__":
    unittest.main()
