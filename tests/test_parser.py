"""Tests for agent/parser.py — intent detection and path extraction."""

import sys
import os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import pytest
from agent.parser import parse_command, detect_intent, _extract_source_target
from agent.models import RiskLevel
from core.exceptions import UnknownIntentError


class TestDetectIntent:
    def test_storage_report_basic(self):
        assert detect_intent("storage report /sdcard/Download") == "storage_report"

    def test_storage_report_disk_usage(self):
        assert detect_intent("disk usage /sdcard") == "storage_report"

    def test_storage_report_how_much_space(self):
        assert detect_intent("how much space is left") == "storage_report"

    def test_storage_report_check(self):
        assert detect_intent("check storage") == "storage_report"

    def test_list_large_files_basic(self):
        assert detect_intent("list large files") == "list_large_files"

    def test_list_large_files_biggest(self):
        assert detect_intent("biggest files in /sdcard") == "list_large_files"

    def test_list_large_files_top_n(self):
        assert detect_intent("show top 10 files") == "list_large_files"

    def test_list_large_files_what_taking_space(self):
        assert detect_intent("what is taking up space") == "list_large_files"

    def test_organize_folder_basic(self):
        assert detect_intent("organize my downloads folder") == "organize_folder_by_type"

    def test_organize_folder_sort(self):
        assert detect_intent("sort files in /sdcard/Download") == "organize_folder_by_type"

    def test_organize_tidy_up(self):
        assert detect_intent("tidy up files in /sdcard/Download") == "organize_folder_by_type"

    def test_organize_arrange(self):
        assert detect_intent("arrange files by type") == "organize_folder_by_type"

    def test_find_duplicates_basic(self):
        assert detect_intent("find duplicate files") == "find_duplicates"

    def test_find_duplicates_show_identical(self):
        assert detect_intent("show identical files") == "find_duplicates"

    def test_find_duplicates_repeated(self):
        assert detect_intent("find repeated files") == "find_duplicates"

    def test_find_duplicates_redundant(self):
        assert detect_intent("find redundant files") == "find_duplicates"

    def test_backup_folder_back_up(self):
        assert detect_intent("back up my Documents folder") == "backup_folder"

    def test_backup_folder_one_word(self):
        assert detect_intent("backup /sdcard/Documents") == "backup_folder"

    def test_backup_folder_copy(self):
        assert detect_intent("copy folder /sdcard/Docs to /sdcard/Backup") == "backup_folder"

    def test_backup_folder_mirror(self):
        assert detect_intent("mirror folder /sdcard/Photos") == "backup_folder"

    def test_convert_video_to_mp3_basic(self):
        assert detect_intent("convert video.mp4 to mp3") == "convert_video_to_mp3"

    def test_convert_video_to_mp3_mkv(self):
        assert detect_intent("convert film.mkv to mp3") == "convert_video_to_mp3"

    def test_convert_video_extract_audio(self):
        assert detect_intent("extract audio from video.mp4") == "convert_video_to_mp3"

    def test_convert_video_rip_audio(self):
        assert detect_intent("rip audio from movie.mp4") == "convert_video_to_mp3"

    def test_compress_images_basic(self):
        assert detect_intent("compress images in folder") == "compress_images"

    def test_compress_images_resize(self):
        assert detect_intent("resize images /sdcard/Pictures") == "compress_images"

    def test_compress_images_optimize(self):
        assert detect_intent("optimize images /sdcard/Photos") == "compress_images"

    def test_compress_images_reduce(self):
        assert detect_intent("reduce image size /sdcard/Pictures") == "compress_images"

    def test_rename_files_basic(self):
        assert detect_intent("rename files in /sdcard/Download prefix old_") == "safe_rename_files"

    def test_rename_files_batch(self):
        assert detect_intent("batch rename files") == "safe_rename_files"

    def test_rename_files_bulk(self):
        assert detect_intent("bulk rename files in /sdcard/Download") == "safe_rename_files"

    def test_move_file_basic(self):
        assert detect_intent("move /sdcard/file.txt to /sdcard/Docs") == "safe_move_files"

    def test_move_folder(self):
        assert detect_intent("move folder /sdcard/Old to /sdcard/Archive") == "safe_move_files"

    def test_move_transfer(self):
        assert detect_intent("transfer files to /sdcard/Archive") == "safe_move_files"

    def test_unknown_intent_raises(self):
        with pytest.raises(UnknownIntentError):
            detect_intent("do something random xyz123")

    def test_unknown_intent_greeting(self):
        with pytest.raises(UnknownIntentError):
            detect_intent("hello how are you")

    def test_unknown_intent_empty(self):
        with pytest.raises(UnknownIntentError):
            detect_intent("   ")

    def test_unknown_intent_short_gibberish(self):
        with pytest.raises(UnknownIntentError):
            detect_intent("zzz aaa bbb")


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

    def test_convert_single_path(self):
        src, tgt = _extract_source_target("convert /sdcard/Movies/film.mp4 to mp3")
        assert src == "/sdcard/Movies/film.mp4"

    def test_move_two_paths_with_to(self):
        src, tgt = _extract_source_target(
            "move /sdcard/Download/file.txt to /sdcard/Documents"
        )
        assert src == "/sdcard/Download/file.txt"
        assert tgt == "/sdcard/Documents"


class TestParseCommand:
    def test_risk_level_low_for_storage_report(self):
        result = parse_command("storage report /sdcard/Download")
        assert result.risk_level == RiskLevel.LOW
        assert result.requires_confirmation is False

    def test_risk_level_low_for_list_large_files(self):
        result = parse_command("list large files /sdcard/Download")
        assert result.risk_level == RiskLevel.LOW
        assert result.requires_confirmation is False

    def test_risk_level_low_for_find_duplicates(self):
        result = parse_command("find duplicates /sdcard/Download")
        assert result.risk_level == RiskLevel.LOW
        assert result.requires_confirmation is False

    def test_risk_level_medium_for_organize(self):
        result = parse_command("organize /sdcard/Download")
        assert result.risk_level == RiskLevel.MEDIUM
        assert result.requires_confirmation is True

    def test_risk_level_medium_for_backup(self):
        result = parse_command("back up /sdcard/Docs to /sdcard/Backup")
        assert result.risk_level == RiskLevel.MEDIUM
        assert result.requires_confirmation is True

    def test_risk_level_medium_for_move(self):
        result = parse_command("move /sdcard/file.txt to /sdcard/Docs")
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

    def test_top_n_extracted(self):
        result = parse_command("show top 5 large files")
        assert result.options.get("top_n") == 5

    def test_top_n_extracted_different_value(self):
        result = parse_command("top 20 files /sdcard/Download")
        assert result.options.get("top_n") == 20

    def test_raw_command_preserved(self):
        cmd = "storage report /sdcard"
        result = parse_command(cmd)
        assert result.raw_command == cmd

    def test_intent_field_find_duplicates(self):
        result = parse_command("find duplicates /sdcard/Download")
        assert result.intent == "find_duplicates"

    def test_intent_field_storage_report(self):
        result = parse_command("storage report /sdcard/Download")
        assert result.intent == "storage_report"

    def test_backup_two_paths_parsed(self):
        result = parse_command("back up /sdcard/Documents to /sdcard/Backup")
        assert result.source_path == "/sdcard/Documents"
        assert result.target_path == "/sdcard/Backup"

    def test_organize_single_path_parsed(self):
        result = parse_command("organize /sdcard/Download")
        assert result.source_path == "/sdcard/Download"
        assert result.target_path is None

    def test_quality_clamped_high(self):
        result = parse_command("compress images /sdcard/Pictures quality 200")
        assert result.options.get("quality") == 95

    def test_quality_clamped_low(self):
        result = parse_command("compress images /sdcard/Pictures quality 0")
        assert result.options.get("quality") == 1

    def test_quality_extracted_normal(self):
        result = parse_command("compress images /sdcard/Pictures quality 60")
        assert result.options.get("quality") == 60

    def test_prefix_extracted(self):
        result = parse_command("rename files /sdcard/Download prefix bak_")
        assert result.options.get("prefix") == "bak_"

    def test_suffix_extracted(self):
        result = parse_command("rename files /sdcard/Download suffix _old")
        assert result.options.get("suffix") == "_old"

    def test_strip_whitespace(self):
        result = parse_command("  storage report /sdcard/Download  ")
        assert result.intent == "storage_report"
