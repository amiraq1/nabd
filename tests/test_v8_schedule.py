import pytest
import os
import tempfile
import uuid
import json

from agent.parser import parse_command
from agent.models import ParsedIntent
from agent.safety import validate_intent_safety
from agent.planner import plan
from agent.executor import execute
from agent.reporter import report_result
from core.exceptions import ValidationError, SafetyError
from agent.models import OperationStatus

def test_parse_schedule_create():
    cmd = "schedule storage report /sdcard/Download every daily"
    parsed = parse_command(cmd)
    assert parsed.intent == "schedule_create"
    assert parsed.options["target_command"] == "storage report /sdcard/Download"
    assert parsed.options["interval"] == "daily"

def test_parse_schedule_list():
    assert parse_command("list schedules").intent == "schedule_list"
    assert parse_command("show schedules").intent == "schedule_list"

def test_parse_schedule_delete():
    parsed = parse_command("delete schedule abc123_")
    assert parsed.intent == "schedule_delete"
    assert parsed.options["schedule_id"] == "abc123_"

def test_safety_schedule_create_validates_target():
    # Valid
    intent = parse_command("schedule doctor every weekly")
    validate_intent_safety(intent)
    
    # Invalid: path traversal
    intent = parse_command("schedule storage report /sdcard/../etc every daily")
    with pytest.raises(Exception):
        validate_intent_safety(intent)

    # Invalid: scheduling a schedule
    intent = parse_command("schedule list schedules every daily")
    with pytest.raises(SafetyError, match="Cannot schedule a scheduling command"):
        validate_intent_safety(intent)

def test_safety_schedule_delete_requires_id():
    # This shouldn't normally happen since the regex requires an ID, but if options were tampered:
    intent = ParsedIntent(intent="schedule_delete", source_path=None)
    with pytest.raises(ValidationError, match="Please specify the schedule ID to delete"):
        validate_intent_safety(intent)

def test_planner_schedule():
    intent = parse_command("schedule doctor every weekly")
    execution_plan = plan(intent)
    assert execution_plan.requires_confirmation is True
    
    list_intent = parse_command("list schedules")
    list_plan = plan(list_intent)
    assert list_plan.requires_confirmation is False

@pytest.fixture
def mock_schedule_file(monkeypatch):
    import tools.schedule
    with tempfile.NamedTemporaryFile(delete=False) as tf:
        tf.close()
    
    monkeypatch.setattr(tools.schedule, "_get_schedule_file", lambda: tf.name)
    yield tf.name
    os.unlink(tf.name)

def test_executor_schedule_flow(mock_schedule_file):
    # Create
    intent = parse_command("schedule doctor every weekly")
    p = plan(intent)
    res = execute(p, confirmed=True)
    if res.status != OperationStatus.SUCCESS:
        print("ERRORS:", res.errors)
    assert res.status == OperationStatus.SUCCESS
    assert "schedule" in res.raw_results[0]
    schedule_id = res.raw_results[0]["schedule"]["id"]
    
    # List
    list_p = plan(parse_command("list schedules"))
    res_list = execute(list_p, confirmed=True)
    assert len(res_list.raw_results[0]["schedules"]) == 1
    
    # Delete
    del_p = plan(parse_command(f"delete schedule {schedule_id}"))
    res_del = execute(del_p, confirmed=True)
    assert res_del.status == OperationStatus.SUCCESS
    
    # List empty
    res_list_empty = execute(list_p, confirmed=True)
    assert len(res_list_empty.raw_results[0]["schedules"]) == 0
