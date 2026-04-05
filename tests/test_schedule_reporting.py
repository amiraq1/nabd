import unittest

from agent.models import ExecutionResult, OperationStatus
from agent.reporter import report_result


class TestScheduleReporting(unittest.TestCase):
    def test_schedule_create_shows_validation_summary(self):
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="Operation 'schedule_create' completed successfully.",
            raw_results=[
                {
                    "success": True,
                    "schedule": {
                        "id": "abc12345",
                        "interval": "weekly",
                        "target_command": "doctor",
                        "creation_validation": {
                            "ok": True,
                            "intent": "doctor",
                            "risk_level": "low",
                        },
                    },
                }
            ],
        )

        output = report_result(result, "schedule_create", confirmed=True)
        self.assertIn("Schedule ID : abc12345", output)
        self.assertIn("Validation  : valid", output)
        self.assertIn("Target      : doctor", output)

    def test_schedule_list_shows_invalid_runtime_entries(self):
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="Operation 'schedule_list' completed successfully.",
            raw_results=[
                {
                    "success": True,
                    "invalid_count": 1,
                    "schedules": [
                        {
                            "id": "ok123",
                            "interval": "daily",
                            "target_command": "doctor",
                            "runtime_validation": {"ok": True},
                        },
                        {
                            "id": "bad456",
                            "interval": "weekly",
                            "target_command": "list schedules",
                            "runtime_validation": {
                                "ok": False,
                                "error": "Cannot schedule a scheduling command.",
                            },
                        },
                    ],
                }
            ],
        )

        output = report_result(result, "schedule_list", confirmed=True)
        self.assertIn("Schedules   : 2", output)
        self.assertIn("Invalid     : 1", output)
        self.assertIn("invalid: Cannot schedule a scheduling command.", output)

    def test_schedule_delete_shows_deleted_id(self):
        result = ExecutionResult(
            status=OperationStatus.SUCCESS,
            message="Operation 'schedule_delete' completed successfully.",
            raw_results=[{"success": True, "deleted_id": "abc12345"}],
        )

        output = report_result(result, "schedule_delete", confirmed=True)
        self.assertIn("Deleted schedule : abc12345", output)


if __name__ == "__main__":
    unittest.main()
