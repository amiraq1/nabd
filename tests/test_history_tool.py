import unittest
from unittest.mock import patch

from tools import history as history_tool


class TestHistoryTool(unittest.TestCase):
    SAMPLE = [
        {"id": 1, "command": "storage report /sdcard/Download", "intent": "storage_report", "status": "success"},
        {"id": 2, "command": "doctor", "intent": "doctor", "status": "success"},
        {"id": 3, "command": "organize /sdcard/Download", "intent": "organize_folder_by_type", "status": "partial"},
    ]

    @patch("tools.history.get_history", return_value=SAMPLE)
    def test_search_history(self, mock_get):
        result = history_tool.search_history("doctor")
        self.assertEqual(result["count"], 1)
        self.assertEqual(result["entries"][0]["intent"], "doctor")
        mock_get.assert_called_once_with(limit=100)

    @patch("tools.history.get_history", return_value=SAMPLE)
    def test_history_by_intent(self, mock_get):
        result = history_tool.history_by_intent("organize_folder_by_type")
        self.assertEqual(result["count"], 1)
        self.assertEqual(result["entries"][0]["id"], 3)
        mock_get.assert_called_once_with(limit=200)

    @patch("tools.history.get_history_entry", return_value=SAMPLE[1])
    def test_show_history_entry_found(self, mock_get):
        result = history_tool.show_history_entry(2)
        self.assertEqual(result["entry"]["intent"], "doctor")
        mock_get.assert_called_once_with(2)

    @patch("tools.history.get_history_entry", return_value=None)
    def test_show_history_entry_missing(self, mock_get):
        result = history_tool.show_history_entry(99)
        self.assertIsNone(result["entry"])
        self.assertIn("No history entry found", result.get("message", ""))
        mock_get.assert_called_once_with(99)
