"""Tests for agent/safety.py — path safety and intent validation."""

import sys
import os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import pytest
from unittest.mock import patch

from agent.safety import validate_path_safety, validate_intent_safety
from agent.models import ParsedIntent, RiskLevel
from core.exceptions import (
    PathNotAllowedError,
    PathTraversalError,
    ValidationError,
)

ALLOWED_ROOTS = ["/sdcard/Download", "/sdcard/Documents", "/sdcard/Music", "/sdcard/Pictures"]


class TestValidatePathSafety:
    def test_allowed_path_passes(self):
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            result = validate_path_safety("/sdcard/Download")
            assert "Download" in result

    def test_nested_allowed_path_passes(self):
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            result = validate_path_safety("/sdcard/Download/subfolder")
            assert "subfolder" in result

    def test_deeply_nested_allowed_path_passes(self):
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            result = validate_path_safety("/sdcard/Download/a/b/c/file.txt")
            assert result is not None

    def test_path_outside_roots_blocked(self):
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(PathNotAllowedError):
                validate_path_safety("/etc/passwd")

    def test_path_traversal_dotdot_blocked(self):
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(PathTraversalError):
                validate_path_safety("/sdcard/Download/../../../etc/passwd")

    def test_double_slash_blocked(self):
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(PathTraversalError):
                validate_path_safety("//etc/passwd")

    def test_null_byte_blocked(self):
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(PathTraversalError):
                validate_path_safety("/sdcard/Download/\x00malicious")

    def test_empty_path_raises_validation_error(self):
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(ValidationError):
                validate_path_safety("")

    def test_whitespace_only_path_raises(self):
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(ValidationError):
                validate_path_safety("   ")

    def test_root_path_blocked(self):
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(PathNotAllowedError):
                validate_path_safety("/")

    def test_home_path_blocked(self):
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(PathNotAllowedError):
                validate_path_safety("/home/user")

    def test_partial_match_blocked(self):
        """'/sdcard/Downloads' should NOT match root '/sdcard/Download'."""
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(PathNotAllowedError):
                validate_path_safety("/sdcard/Downloads")

    def test_url_encoded_traversal_blocked(self):
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(PathTraversalError):
                validate_path_safety("/sdcard/Download/%2e%2e/etc")


class TestValidateIntentSafety:
    def test_storage_report_no_path_ok(self):
        intent = ParsedIntent(
            intent="storage_report",
            source_path=None,
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            validate_intent_safety(intent)

    def test_find_duplicates_no_path_ok(self):
        intent = ParsedIntent(
            intent="find_duplicates",
            source_path=None,
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            validate_intent_safety(intent)

    def test_organize_without_path_raises(self):
        intent = ParsedIntent(
            intent="organize_folder_by_type",
            source_path=None,
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(ValidationError):
                validate_intent_safety(intent)

    def test_organize_with_valid_path_passes(self):
        intent = ParsedIntent(
            intent="organize_folder_by_type",
            source_path="/sdcard/Download",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            validate_intent_safety(intent)

    def test_backup_without_target_raises(self):
        intent = ParsedIntent(
            intent="backup_folder",
            source_path="/sdcard/Documents",
            target_path=None,
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(ValidationError):
                validate_intent_safety(intent)

    def test_backup_with_valid_paths_passes(self):
        intent = ParsedIntent(
            intent="backup_folder",
            source_path="/sdcard/Documents",
            target_path="/sdcard/Download",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            validate_intent_safety(intent)

    def test_backup_source_outside_allowed_blocked(self):
        intent = ParsedIntent(
            intent="backup_folder",
            source_path="/etc/passwd",
            target_path="/sdcard/Download",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(PathNotAllowedError):
                validate_intent_safety(intent)

    def test_backup_target_outside_allowed_blocked(self):
        intent = ParsedIntent(
            intent="backup_folder",
            source_path="/sdcard/Download",
            target_path="/tmp/evil",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(PathNotAllowedError):
                validate_intent_safety(intent)

    def test_move_without_target_raises(self):
        intent = ParsedIntent(
            intent="safe_move_files",
            source_path="/sdcard/Download/file.txt",
            target_path=None,
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
        )
        with patch("agent.safety.get_allowed_roots", return_value=ALLOWED_ROOTS):
            with pytest.raises(ValidationError):
                validate_intent_safety(intent)
