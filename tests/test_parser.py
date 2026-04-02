"""Tests for agent/parser.py — intent detection and path extraction."""

import sys
import os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import pytest
from agent.parser import parse_command, detect_intent, _extract_source_target
from agent.models import RiskLevel
from core.exceptions import UnknownIntentError


class TestDetectIntent:
    def test_storage_report_english(self):
        assert detect_intent("storage report /sdcard/Download") == "storage_report"

    def test_storage_report_arabic(self):
        assert detect_intent("تقرير التخزين") == "storage_report"

    def test_storage_report_disk_usage(self):
        assert detect_intent("disk usage /sdcard") == "storage_report"

    def test_list_large_files_english(self):
        assert detect_intent("list large files") == "list_large_files"

    def test_list_large_files_arabic(self):
        assert detect_intent("اعرض أكبر الملفات") == "list_large_files"

    def test_list_large_files_biggest(self):
        assert detect_intent("biggest files in /sdcard") == "list_large_files"

    def test_organize_folder_english(self):
        assert detect_intent("organize my downloads folder") == "organize_folder_by_type"

    def test_organize_folder_arabic(self):
        assert detect_intent("رتّب مجلد التنزيلات") == "organize_folder_by_type"

    def test_organize_tidy_up(self):
        assert detect_intent("tidy up files in /sdcard/Download") == "organize_folder_by_type"

    def test_find_duplicates_english(self):
        assert detect_intent("find duplicate files") == "find_duplicates"

    def test_find_duplicates_arabic(self):
        assert detect_intent("ابحث عن الملفات المكررة") == "find_duplicates"

    def test_find_duplicates_identical(self):
        assert detect_intent("show identical files") == "find_duplicates"

    def test_backup_english(self):
        assert detect_intent("back up my Documents folder") == "backup_folder"

    def test_backup_one_word(self):
        assert detect_intent("backup /sdcard/Documents") == "backup_folder"

    def test_backup_arabic(self):
        assert detect_intent("انسخ مجلد المستندات احتياطيًا") == "backup_folder"

    def test_convert_video_english(self):
        assert detect_intent("convert video.mp4 to mp3") == "convert_video_to_mp3"

    def test_convert_video_to_mp3_short(self):
        assert detect_intent("convert film.mkv to mp3") == "convert_video_to_mp3"

    def test_convert_video_arabic(self):
        assert detect_intent("حوّل الفيديو إلى mp3") == "convert_video_to_mp3"

    def test_extract_audio(self):
        assert detect_intent("extract audio from video.mp4") == "convert_video_to_mp3"

    def test_compress_images_english(self):
        assert detect_intent("compress images in folder") == "compress_images"

    def test_compress_images_arabic(self):
        assert detect_intent("اضغط صور المجلد") == "compress_images"

    def test_rename_files(self):
        assert detect_intent("rename files in /sdcard/Download prefix old_") == "safe_rename_files"

    def test_batch_rename(self):
        assert detect_intent("batch rename files") == "safe_rename_files"

    def test_move_file(self):
        assert detect_intent("move /sdcard/file.txt to /sdcard/Docs") == "safe_move_files"

    def test_move_arabic(self):
        assert detect_intent("انقل الملف إلى مجلد آخر") == "safe_move_files"

    def test_unknown_intent_raises(self):
        with pytest.raises(UnknownIntentError):
            detect_intent("do something random xyz123")

    def test_unknown_intent_greeting(self):
        with pytest.raises(UnknownIntentError):
            detect_intent("hello how are you")

    def test_unknown_intent_empty(self):
        with pytest.raises(UnknownIntentError):
            detect_intent("   ")


class TestSourceTargetExtraction:
    def test_single_path_source_only(self):
        src, tgt = _extract_source_target("organize /sdcard/Download")
        assert src == "/sdcard/Download"
        assert tgt is None

    def test_two_paths_with_to(self):
        src, tgt = _extract_source_target(
            "back up /sdcard/Documents to /sdcard/Backup"
        )
        assert src == "/sdcard/Documents"
        assert tgt == "/sdcard/Backup"

    def test_two_paths_arabic_to(self):
        src, tgt = _extract_source_target(
            "انسخ /sdcard/Documents إلى /sdcard/Backup"
        )
        assert src == "/sdcard/Documents"
        assert tgt == "/sdcard/Backup"

    def test_two_bare_paths(self):
        src, tgt = _extract_source_target("move /sdcard/a.txt /sdcard/Docs")
        assert src == "/sdcard/a.txt"
        assert tgt == "/sdcard/Docs"

    def test_no_paths(self):
        src, tgt = _extract_source_target("list large files")
        assert src is None
        assert tgt is None

    def test_source_not_same_as_target_with_to(self):
        src, tgt = _extract_source_target(
            "back up /sdcard/Docs to /sdcard/Backup"
        )
        assert src != tgt

    def test_quoted_path(self):
        src, tgt = _extract_source_target('organize "/sdcard/My Files"')
        assert src == "/sdcard/My Files"


class TestParseCommand:
    def test_risk_level_low_for_storage_report(self):
        result = parse_command("storage report /sdcard/Download")
        assert result.risk_level == RiskLevel.LOW
        assert result.requires_confirmation is False

    def test_risk_level_medium_for_organize(self):
        result = parse_command("organize /sdcard/Download")
        assert result.risk_level == RiskLevel.MEDIUM
        assert result.requires_confirmation is True

    def test_risk_level_high_for_compress(self):
        result = parse_command("compress images /sdcard/Pictures")
        assert result.risk_level == RiskLevel.HIGH
        assert result.requires_confirmation is True

    def test_risk_level_high_for_rename(self):
        result = parse_command("rename files /sdcard/Download")
        assert result.risk_level == RiskLevel.HIGH
        assert result.requires_confirmation is True

    def test_risk_level_medium_for_backup(self):
        result = parse_command("back up /sdcard/Docs to /sdcard/Backup")
        assert result.risk_level == RiskLevel.MEDIUM
        assert result.requires_confirmation is True

    def test_top_n_extracted(self):
        result = parse_command("show top 5 large files")
        assert result.options.get("top_n") == 5

    def test_raw_command_preserved(self):
        cmd = "storage report /sdcard"
        result = parse_command(cmd)
        assert result.raw_command == cmd

    def test_intent_field(self):
        result = parse_command("find duplicates /sdcard")
        assert result.intent == "find_duplicates"

    def test_backup_two_paths_parsed(self):
        result = parse_command("back up /sdcard/Documents to /sdcard/Backup")
        assert result.source_path == "/sdcard/Documents"
        assert result.target_path == "/sdcard/Backup"

    def test_quality_clamped(self):
        result = parse_command("compress images /sdcard/Pictures quality 200")
        assert result.options.get("quality") == 95

    def test_quality_extracted(self):
        result = parse_command("compress images /sdcard/Pictures quality 60")
        assert result.options.get("quality") == 60

    def test_prefix_extracted(self):
        result = parse_command("rename files /sdcard/Download prefix bak_")
        assert result.options.get("prefix") == "bak_"
