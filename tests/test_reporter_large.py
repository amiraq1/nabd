import unittest

from agent.models import ExecutionResult, OperationStatus
from agent.reporter import _append_raw_details


class DummyLines(list):
    pass


class TestReporterLargeOutputs(unittest.TestCase):
    def test_list_large_files_truncates(self):
        files = {
            "directory": "/sdcard/Download",
            "files": [{"path": f"/file{i}", "size_human": f"{i}KB"} for i in range(30)],
        }
        lines = DummyLines()
        _append_raw_details(lines, files, "list_large_files", confirmed=False)
        self.assertIn("... and", "".join(lines))

    def test_show_files_truncates(self):
        entries = [{"name": f"file{i}", "is_dir": False, "size_human": f"{i}KB"} for i in range(20)]
        raw = {
            "directory": "/sdcard/Download",
            "entries": entries,
            "total_entries": 30,
            "file_count": 30,
            "dir_count": 0,
            "truncated": 10,
            "sort_by": "name",
        }
        lines = DummyLines()
        _append_raw_details(lines, raw, "show_files", confirmed=False)
        self.assertIn("... and", "".join(lines))

    def test_list_media_truncates_each_category(self):
        raw = {
            "directory": "/sdcard/Media",
            "summary": {"images": {"count": 20}},
            "groups": {"images": [{"name": f"img{i}", "size_human": f"{i}KB"} for i in range(20)]},
            "total_media_count": 20,
            "total_size_human": "20MB",
        }
        lines = DummyLines()
        _append_raw_details(lines, raw, "list_media", confirmed=False)
        self.assertIn("... and", "".join(lines))

    def test_find_duplicates_truncates_groups(self):
        groups = [{"file_size_human": "1MB", "paths": [f"/dup{idx}/file"] * 10} for idx in range(8)]
        raw = {
            "duplicate_groups": groups,
            "total_wasted_human": "8MB",
            "total_groups": len(groups),
            "total_wasted_human": "8MB",
        }
        lines = DummyLines()
        _append_raw_details(lines, raw, "find_duplicates", confirmed=False)
        self.assertIn("... and", "".join(lines))
