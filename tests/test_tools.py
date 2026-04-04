"""Tests for tools — storage, files, duplicates, backup, media."""

import sys
import os
import tempfile
import shutil
from unittest.mock import patch
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import pytest
from tools.storage import get_storage_report, list_large_files
from tools.files import organize_folder_by_type, safe_rename_files, safe_move_files
from tools.duplicates import find_duplicates
from tools.backup import backup_folder
from tools.utils import (
    human_readable_size, get_category, hash_file, unique_dest_path
)
from core.exceptions import ToolError


class TestHumanReadableSize:
    def test_bytes(self):
        assert human_readable_size(500) == "500 B"

    def test_kilobytes(self):
        assert human_readable_size(2048) == "2.0 KB"

    def test_megabytes(self):
        assert human_readable_size(5 * 1024 * 1024) == "5.0 MB"

    def test_gigabytes(self):
        assert human_readable_size(2 * 1024 ** 3) == "2.00 GB"

    def test_zero_bytes(self):
        assert human_readable_size(0) == "0 B"


class TestGetCategory:
    def test_jpg_is_image(self):
        assert get_category(".jpg") == "images"

    def test_mp4_is_video(self):
        assert get_category(".mp4") == "videos"

    def test_mp3_is_audio(self):
        assert get_category(".mp3") == "audio"

    def test_pdf_is_document(self):
        assert get_category(".pdf") == "documents"

    def test_txt_is_document(self):
        assert get_category(".txt") == "documents"

    def test_zip_is_archive(self):
        assert get_category(".zip") == "archives"

    def test_py_is_code(self):
        assert get_category(".py") == "code"

    def test_apk_is_apk(self):
        assert get_category(".apk") == "apks"

    def test_unknown_is_other(self):
        assert get_category(".xyz123") == "other"

    def test_empty_ext_is_other(self):
        assert get_category("") == "other"

    def test_case_insensitive_jpg(self):
        assert get_category(".JPG") == "images"

    def test_case_insensitive_mp4(self):
        assert get_category(".MP4") == "videos"


class TestHashFile:
    def test_hash_consistent(self, tmp_path):
        f = tmp_path / "test.txt"
        f.write_bytes(b"hello world")
        h1 = hash_file(str(f))
        h2 = hash_file(str(f))
        assert h1 == h2
        assert len(h1) == 64

    def test_different_content_different_hash(self, tmp_path):
        f1 = tmp_path / "a.txt"
        f2 = tmp_path / "b.txt"
        f1.write_bytes(b"aaa")
        f2.write_bytes(b"bbb")
        assert hash_file(str(f1)) != hash_file(str(f2))

    def test_same_content_same_hash(self, tmp_path):
        content = b"identical content"
        f1 = tmp_path / "a.bin"
        f2 = tmp_path / "b.bin"
        f1.write_bytes(content)
        f2.write_bytes(content)
        assert hash_file(str(f1)) == hash_file(str(f2))

    def test_nonexistent_file_returns_empty(self):
        assert hash_file("/nonexistent/path/file.txt") == ""

    def test_hash_prefix_mode_is_stable(self, tmp_path):
        f = tmp_path / "big.bin"
        f.write_bytes(b"a" * 4096 + b"b" * 4096)
        h1 = hash_file(str(f), chunk_size_kb=4, max_chunks=1)
        h2 = hash_file(str(f), chunk_size_kb=4, max_chunks=1)
        assert h1 == h2


class TestUniqueDestPath:
    def test_no_conflict_returns_original(self, tmp_path):
        target = str(tmp_path / "file.txt")
        assert unique_dest_path(target) == target

    def test_conflict_returns_incremented(self, tmp_path):
        original = tmp_path / "file.txt"
        original.write_bytes(b"x")
        result = unique_dest_path(str(original))
        assert result == str(tmp_path / "file_1.txt")

    def test_multiple_conflicts_increment(self, tmp_path):
        for name in ("file.txt", "file_1.txt", "file_2.txt"):
            (tmp_path / name).write_bytes(b"x")
        result = unique_dest_path(str(tmp_path / "file.txt"))
        assert result == str(tmp_path / "file_3.txt")

    def test_raises_when_no_unique_name_found(self, monkeypatch):
        monkeypatch.setattr("tools.utils.MAX_UNIQUE_DEST_ATTEMPTS", 3)
        monkeypatch.setattr("tools.utils.os.path.exists", lambda _path: True)
        with pytest.raises(ToolError):
            unique_dest_path("/tmp/file.txt")


class TestStorageReport:
    def test_report_on_temp_dir(self, tmp_path):
        (tmp_path / "file1.txt").write_bytes(b"hello")
        (tmp_path / "image.jpg").write_bytes(b"x" * 100)
        result = get_storage_report(str(tmp_path))
        assert result["file_count"] == 2
        assert result["total_size_bytes"] == 105
        assert "total_size_human" in result

    def test_category_breakdown_present(self, tmp_path):
        (tmp_path / "photo.jpg").write_bytes(b"x" * 200)
        (tmp_path / "doc.pdf").write_bytes(b"x" * 100)
        result = get_storage_report(str(tmp_path))
        breakdown = result["category_breakdown"]
        assert "images" in breakdown
        assert "documents" in breakdown

    def test_nonexistent_dir_raises(self):
        with pytest.raises(ToolError):
            get_storage_report("/nonexistent/directory/abc")

    def test_empty_dir(self, tmp_path):
        result = get_storage_report(str(tmp_path))
        assert result["file_count"] == 0
        assert result["total_size_bytes"] == 0

    def test_nested_files_counted(self, tmp_path):
        sub = tmp_path / "sub"
        sub.mkdir()
        (sub / "nested.txt").write_bytes(b"x")
        result = get_storage_report(str(tmp_path))
        assert result["file_count"] == 1


class TestListLargeFiles:
    def test_finds_large_file(self, tmp_path):
        large = tmp_path / "big.bin"
        large.write_bytes(b"x" * (1024 * 1024 * 5))
        small = tmp_path / "small.txt"
        small.write_bytes(b"hi")
        result = list_large_files(str(tmp_path), top_n=10, threshold_mb=1.0)
        paths = [r["path"] for r in result["files"]]
        assert result["directory"] == str(tmp_path)
        assert str(large) in paths
        assert str(small) not in paths

    def test_top_n_respected(self, tmp_path):
        for i in range(5):
            (tmp_path / f"file{i}.bin").write_bytes(b"x" * 1024)
        result = list_large_files(str(tmp_path), top_n=3, threshold_mb=0)
        assert len(result["files"]) <= 3

    def test_sorted_descending(self, tmp_path):
        (tmp_path / "small.bin").write_bytes(b"x" * 100)
        (tmp_path / "large.bin").write_bytes(b"x" * 10000)
        (tmp_path / "medium.bin").write_bytes(b"x" * 1000)
        result = list_large_files(str(tmp_path), top_n=10, threshold_mb=0)
        sizes = [r["size_bytes"] for r in result["files"]]
        assert sizes == sorted(sizes, reverse=True)

    def test_nonexistent_dir_raises(self):
        with pytest.raises(ToolError):
            list_large_files("/nonexistent/path")


class TestOrganizeFolder:
    def test_dry_run_no_actual_moves(self, tmp_path):
        (tmp_path / "photo.jpg").write_bytes(b"img")
        (tmp_path / "doc.pdf").write_bytes(b"pdf")
        result = organize_folder_by_type(str(tmp_path), dry_run=True)
        assert result["dry_run"] is True
        assert len(result["planned_moves"]) == 2
        assert len(result["moved"]) == 0
        assert (tmp_path / "photo.jpg").exists()

    def test_actual_move(self, tmp_path):
        (tmp_path / "photo.jpg").write_bytes(b"img")
        result = organize_folder_by_type(str(tmp_path), dry_run=False)
        assert result["dry_run"] is False
        assert not (tmp_path / "photo.jpg").exists()
        assert (tmp_path / "images" / "photo.jpg").exists()

    def test_collision_handled(self, tmp_path):
        """Two files with same name in same category resolve without overwriting."""
        (tmp_path / "photo.jpg").write_bytes(b"original")
        images_dir = tmp_path / "images"
        images_dir.mkdir()
        (images_dir / "photo.jpg").write_bytes(b"existing")
        result = organize_folder_by_type(str(tmp_path), dry_run=False)
        assert (images_dir / "photo.jpg").read_bytes() == b"existing"
        assert (images_dir / "photo_1.jpg").exists()
        assert (images_dir / "photo_1.jpg").read_bytes() == b"original"

    def test_nonexistent_dir_raises(self):
        with pytest.raises(ToolError):
            organize_folder_by_type("/nonexistent/dir")

    def test_plans_correct_categories(self, tmp_path):
        (tmp_path / "video.mp4").write_bytes(b"v")
        (tmp_path / "song.mp3").write_bytes(b"a")
        (tmp_path / "notes.txt").write_bytes(b"t")
        result = organize_folder_by_type(str(tmp_path), dry_run=True)
        categories = {m["category"] for m in result["planned_moves"]}
        assert "videos" in categories
        assert "audio" in categories
        assert "documents" in categories

    def test_already_organized_files_skipped(self, tmp_path):
        images_dir = tmp_path / "images"
        images_dir.mkdir()
        (images_dir / "photo.jpg").write_bytes(b"x")
        result = organize_folder_by_type(str(tmp_path), dry_run=True)
        assert len(result["planned_moves"]) == 0

    def test_other_category_for_unknown_ext(self, tmp_path):
        (tmp_path / "file.xyz").write_bytes(b"x")
        result = organize_folder_by_type(str(tmp_path), dry_run=True)
        assert result["planned_moves"][0]["category"] == "other"


class TestFindDuplicates:
    def test_finds_identical_files(self, tmp_path):
        content = b"duplicate content here"
        (tmp_path / "a.txt").write_bytes(content)
        (tmp_path / "b.txt").write_bytes(content)
        (tmp_path / "unique.txt").write_bytes(b"unique")
        result = find_duplicates(str(tmp_path), recursive=False)
        assert result["total_groups"] == 1
        assert len(result["duplicate_groups"][0]["paths"]) == 2

    def test_three_copies_one_group(self, tmp_path):
        content = b"same content"
        for name in ("a.txt", "b.txt", "c.txt"):
            (tmp_path / name).write_bytes(content)
        result = find_duplicates(str(tmp_path), recursive=False)
        assert result["total_groups"] == 1
        assert result["duplicate_groups"][0]["duplicate_count"] == 2

    def test_no_duplicates(self, tmp_path):
        (tmp_path / "a.txt").write_bytes(b"aaa")
        (tmp_path / "b.txt").write_bytes(b"bbb")
        result = find_duplicates(str(tmp_path), recursive=False)
        assert result["total_groups"] == 0

    def test_nonexistent_dir_raises(self):
        with pytest.raises(ToolError):
            find_duplicates("/nonexistent/dir")

    def test_wasted_space_reported(self, tmp_path):
        content = b"x" * 1000
        (tmp_path / "copy1.bin").write_bytes(content)
        (tmp_path / "copy2.bin").write_bytes(content)
        result = find_duplicates(str(tmp_path))
        assert result["total_groups"] == 1
        assert "B" in result["total_wasted_human"]

    def test_empty_files_ignored(self, tmp_path):
        (tmp_path / "empty1.txt").write_bytes(b"")
        (tmp_path / "empty2.txt").write_bytes(b"")
        result = find_duplicates(str(tmp_path), recursive=False)
        assert result["total_groups"] == 0

    def test_recursive_finds_nested_duplicates(self, tmp_path):
        content = b"same bytes"
        (tmp_path / "root.txt").write_bytes(content)
        sub = tmp_path / "subdir"
        sub.mkdir()
        (sub / "nested.txt").write_bytes(content)
        result = find_duplicates(str(tmp_path), recursive=True)
        assert result["total_groups"] == 1

    def test_prefix_hash_prefilter_skips_full_hash_for_distinct_prefixes(self, tmp_path):
        a = tmp_path / "a.bin"
        b = tmp_path / "b.bin"
        a.write_bytes(b"a" * 8192)
        b.write_bytes(b"b" * 8192)

        calls: list[tuple[str, int, int | None]] = []

        def fake_hash(path: str, chunk_size_kb: int = 64, max_chunks: int | None = None) -> str:
            calls.append((os.path.basename(path), chunk_size_kb, max_chunks))
            if max_chunks == 1:
                return f"prefix-{os.path.basename(path)}"
            raise AssertionError("Full hash should not run when prefix hashes differ.")

        with patch("tools.duplicates.hash_file", side_effect=fake_hash):
            result = find_duplicates(str(tmp_path), recursive=False)

        assert result["total_groups"] == 0
        assert calls == [("a.bin", 4, 1), ("b.bin", 4, 1)]


class TestSafeRenameFiles:
    def test_dry_run_no_changes(self, tmp_path):
        (tmp_path / "file.txt").write_bytes(b"x")
        result = safe_rename_files(str(tmp_path), prefix="new_", dry_run=True)
        assert result["dry_run"] is True
        assert len(result["planned_renames"]) == 1
        assert len(result["renamed"]) == 0
        assert (tmp_path / "file.txt").exists()

    def test_actual_rename_with_prefix(self, tmp_path):
        (tmp_path / "file.txt").write_bytes(b"x")
        result = safe_rename_files(str(tmp_path), prefix="new_", dry_run=False)
        assert (tmp_path / "new_file.txt").exists()
        assert not (tmp_path / "file.txt").exists()

    def test_actual_rename_with_suffix(self, tmp_path):
        (tmp_path / "photo.jpg").write_bytes(b"x")
        result = safe_rename_files(str(tmp_path), suffix="_edited", dry_run=False)
        assert (tmp_path / "photo_edited.jpg").exists()

    def test_no_prefix_or_suffix_raises(self, tmp_path):
        with pytest.raises(ToolError):
            safe_rename_files(str(tmp_path), dry_run=True)

    def test_collision_skipped_with_error(self, tmp_path):
        (tmp_path / "file.txt").write_bytes(b"original")
        (tmp_path / "new_file.txt").write_bytes(b"existing")
        result = safe_rename_files(str(tmp_path), prefix="new_", dry_run=False)
        assert len(result["errors"]) > 0
        assert (tmp_path / "file.txt").exists()


class TestSafeMoveFiles:
    def test_dry_run_no_move(self, tmp_path):
        src = tmp_path / "file.txt"
        src.write_bytes(b"x")
        dest_dir = tmp_path / "dest"
        result = safe_move_files(str(src), str(dest_dir), dry_run=True)
        assert result["dry_run"] is True
        assert src.exists()

    def test_actual_move(self, tmp_path):
        src = tmp_path / "file.txt"
        src.write_bytes(b"x")
        dest_dir = tmp_path / "dest"
        result = safe_move_files(str(src), str(dest_dir), dry_run=False)
        assert not src.exists()
        assert (dest_dir / "file.txt").exists()

    def test_creates_target_dir(self, tmp_path):
        src = tmp_path / "file.txt"
        src.write_bytes(b"x")
        new_dir = tmp_path / "new" / "deep"
        safe_move_files(str(src), str(new_dir), dry_run=False)
        assert (new_dir / "file.txt").exists()

    def test_nonexistent_source_raises(self, tmp_path):
        with pytest.raises(ToolError):
            safe_move_files(str(tmp_path / "nonexistent.txt"), str(tmp_path))


class TestBackupFolder:
    def test_dry_run_no_copy(self, tmp_path):
        src = tmp_path / "source"
        src.mkdir()
        (src / "file.txt").write_bytes(b"content")
        dest_root = tmp_path / "backups"
        result = backup_folder(str(src), str(dest_root), dry_run=True)
        assert result["dry_run"] is True
        assert result["file_count"] == 1
        assert not dest_root.exists()

    def test_actual_backup_copies_files(self, tmp_path):
        src = tmp_path / "source"
        src.mkdir()
        (src / "data.txt").write_bytes(b"important data")
        (src / "photo.jpg").write_bytes(b"image bytes")
        dest_root = tmp_path / "backups"
        result = backup_folder(str(src), str(dest_root), dry_run=False)
        assert result["success"] is True
        assert result["file_count"] == 2
        backup_dir = dest_root / os.path.basename(result["destination"])
        assert (backup_dir / "data.txt").read_bytes() == b"important data"
        assert (backup_dir / "photo.jpg").read_bytes() == b"image bytes"

    def test_backup_name_has_timestamp(self, tmp_path):
        src = tmp_path / "source"
        src.mkdir()
        (src / "f.txt").write_bytes(b"x")
        dest_root = tmp_path / "backups"
        result = backup_folder(str(src), str(dest_root), dry_run=True)
        assert "backup_" in result["destination"]
        assert "source" in result["destination"]

    def test_nonexistent_source_raises(self, tmp_path):
        with pytest.raises(ToolError):
            backup_folder("/nonexistent/source", str(tmp_path), dry_run=True)

    def test_nested_files_backed_up(self, tmp_path):
        src = tmp_path / "source"
        src.mkdir()
        sub = src / "subdir"
        sub.mkdir()
        (sub / "nested.txt").write_bytes(b"nested")
        dest_root = tmp_path / "backups"
        result = backup_folder(str(src), str(dest_root), dry_run=False)
        assert result["success"] is True
        backup_path = result["destination"]
        assert os.path.isfile(os.path.join(backup_path, "subdir", "nested.txt"))


class TestExecutorIntegration:
    """Integration tests: planner → executor pipeline."""

    def test_storage_report_executed(self, tmp_path):
        from agent.models import ExecutionPlan, RiskLevel, ToolAction
        from agent.executor import execute, OperationStatus
        action = ToolAction(
            tool_name="storage",
            function_name="get_storage_report",
            arguments={"directory": str(tmp_path)},
        )
        plan = ExecutionPlan(
            intent="storage_report",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            dry_run=False,
            actions=[action],
        )
        (tmp_path / "a.txt").write_bytes(b"hello")
        result = execute(plan, confirmed=False)
        assert result.status == OperationStatus.SUCCESS
        assert result.message == "Operation 'storage_report' completed successfully."
        assert len(result.raw_results) == 1
        assert result.raw_results[0]["file_count"] == 1

    def test_organize_dry_run_no_changes(self, tmp_path):
        from agent.models import ExecutionPlan, RiskLevel, ToolAction
        from agent.executor import execute, OperationStatus
        (tmp_path / "photo.jpg").write_bytes(b"x")
        action = ToolAction(
            tool_name="files",
            function_name="organize_folder_by_type",
            arguments={"directory": str(tmp_path), "dry_run": True},
        )
        plan = ExecutionPlan(
            intent="organize_folder_by_type",
            risk_level=RiskLevel.MEDIUM,
            requires_confirmation=True,
            dry_run=True,
            actions=[action],
        )
        result = execute(plan, confirmed=False)
        assert result.status == OperationStatus.SUCCESS
        assert "[DRY RUN]" in result.message
        assert (tmp_path / "photo.jpg").exists()

    def test_whitelisted_function_enforced(self, tmp_path):
        from agent.models import ExecutionPlan, RiskLevel, ToolAction
        from agent.executor import execute, OperationStatus
        from core.exceptions import ExecutionError
        action = ToolAction(
            tool_name="storage",
            function_name="delete_everything",
            arguments={},
        )
        plan = ExecutionPlan(
            intent="storage_report",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            dry_run=False,
            actions=[action],
        )
        result = execute(plan, confirmed=False)
        assert result.status in (OperationStatus.FAILURE, OperationStatus.PARTIAL)
        assert any("not whitelisted" in e or "delete_everything" in e for e in result.errors)

    def test_unknown_tool_blocked(self, tmp_path):
        from agent.models import ExecutionPlan, RiskLevel, ToolAction
        from agent.executor import execute, OperationStatus
        action = ToolAction(
            tool_name="shellexec",
            function_name="run",
            arguments={"cmd": "rm -rf /"},
        )
        plan = ExecutionPlan(
            intent="storage_report",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            dry_run=False,
            actions=[action],
        )
        result = execute(plan, confirmed=False)
        assert result.status in (OperationStatus.FAILURE, OperationStatus.PARTIAL)
        assert any("not whitelisted" in e or "shellexec" in e for e in result.errors)

    def test_empty_plan_returns_skipped(self):
        from agent.models import ExecutionPlan, RiskLevel
        from agent.executor import execute, OperationStatus
        plan = ExecutionPlan(
            intent="storage_report",
            risk_level=RiskLevel.LOW,
            requires_confirmation=False,
            dry_run=False,
            actions=[],
        )
        result = execute(plan, confirmed=False)
        assert result.status == OperationStatus.SKIPPED
