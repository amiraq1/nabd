import unittest
from unittest.mock import patch

from agent.models import ParsedIntent, RiskLevel
from agent.safety import validate_intent_safety
from core.exceptions import SafetyError


ALLOWED_ROOTS = [
    "/sdcard/Download",
    "/sdcard/Documents",
    "/sdcard/Backup",
]


class TestSafetyPathRelationships(unittest.TestCase):
    def test_backup_to_same_source_folder_is_blocked(self):
        intent = ParsedIntent(
            intent="backup_folder",
            source_path="/sdcard/Documents",
            target_path="/sdcard/Documents",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with self.assertRaises(SafetyError):
                validate_intent_safety(intent)

    def test_backup_to_nested_destination_is_blocked(self):
        intent = ParsedIntent(
            intent="backup_folder",
            source_path="/sdcard/Documents",
            target_path="/sdcard/Documents/backups",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with self.assertRaises(SafetyError):
                validate_intent_safety(intent)

    def test_backup_to_separate_allowed_root_still_passes(self):
        intent = ParsedIntent(
            intent="backup_folder",
            source_path="/sdcard/Documents",
            target_path="/sdcard/Backup",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            validate_intent_safety(intent)

    def test_move_file_to_same_parent_directory_is_blocked(self):
        intent = ParsedIntent(
            intent="safe_move_files",
            source_path="/sdcard/Download/file.txt",
            target_path="/sdcard/Download",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with self.assertRaises(SafetyError):
                validate_intent_safety(intent)

    def test_move_directory_into_itself_is_blocked(self):
        intent = ParsedIntent(
            intent="safe_move_files",
            source_path="/sdcard/Documents/project",
            target_path="/sdcard/Documents/project",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with self.assertRaises(SafetyError):
                validate_intent_safety(intent)

    def test_move_directory_into_child_folder_is_blocked(self):
        intent = ParsedIntent(
            intent="safe_move_files",
            source_path="/sdcard/Documents/project",
            target_path="/sdcard/Documents/project/archive",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with self.assertRaises(SafetyError):
                validate_intent_safety(intent)

    def test_move_file_to_different_allowed_directory_still_passes(self):
        intent = ParsedIntent(
            intent="safe_move_files",
            source_path="/sdcard/Download/file.txt",
            target_path="/sdcard/Documents",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            validate_intent_safety(intent)


if __name__ == "__main__":
    unittest.main()
