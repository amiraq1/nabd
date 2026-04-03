"""
Tests for Nabd v0.6 — AI Assist Skill + Skills Registry

Covers:
  - Skills registry (registration, list, get, info)
  - Parser detection of all 5 new AI/skill intents
  - Query extraction for ai_suggest_command, ai_clarify_request, skill_info
  - Safety: all new intents are READ_ONLY, no path/URL needed
  - Planner: correct tool_name and function_name for each new intent
  - Executor: skill and ai_skill handler routing
  - Reporter: formatted output for show_skills, skill_info, AI results
  - LocalBackend: suggest_command, explain_result, clarify_request, suggest_intent
  - AIAssistSkill: disabled blocks cleanly, enabled works correctly
  - Fallback intent suggestion never invents non-whitelisted intents
  - Low-confidence suggestions do not auto-execute (requires_confirmation=False)
  - Existing non-AI commands continue to work unchanged
"""

import sys
import os
import json
import unittest
from unittest.mock import patch

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))


# ── Skills Registry ────────────────────────────────────────────────────────────

class TestSkillsRegistry(unittest.TestCase):

    def setUp(self):
        # Reset singleton so each test starts fresh
        import skills.registry as reg_mod
        reg_mod._registry = None

    def test_registry_creates_on_first_access(self):
        from skills.registry import get_registry
        reg = get_registry()
        self.assertIsNotNone(reg)

    def test_ai_assist_is_registered(self):
        from skills.registry import get_registry
        reg = get_registry()
        self.assertIn("ai_assist", reg.list_names())

    def test_get_returns_ai_assist_skill(self):
        from skills.registry import get_registry
        reg = get_registry()
        skill = reg.get("ai_assist")
        self.assertIsNotNone(skill)
        self.assertEqual(skill.name, "ai_assist")

    def test_get_unknown_returns_none(self):
        from skills.registry import get_registry
        reg = get_registry()
        self.assertIsNone(reg.get("nonexistent_skill"))

    def test_list_skills_returns_skill_info_objects(self):
        from skills.registry import get_registry
        from skills.base import SkillInfo
        reg = get_registry()
        skills = reg.list_skills()
        self.assertTrue(len(skills) >= 1)
        self.assertIsInstance(skills[0], SkillInfo)

    def test_skill_info_fields_populated(self):
        from skills.registry import get_registry
        reg = get_registry()
        info = reg.get("ai_assist").get_info()
        self.assertEqual(info.name, "ai_assist")
        self.assertIsInstance(info.version, str)
        self.assertIn("ai", info.tags)
        self.assertIsInstance(info.description, str)
        self.assertGreater(len(info.description), 10)

    def test_is_enabled_false_by_default(self):
        from skills.registry import get_registry
        reg = get_registry()
        # Default config has enabled=false
        self.assertFalse(reg.is_enabled("ai_assist"))

    def test_is_enabled_unknown_skill_returns_false(self):
        from skills.registry import get_registry
        reg = get_registry()
        self.assertFalse(reg.is_enabled("no_such_skill"))


# ── Parser — new intent detection ─────────────────────────────────────────────

class TestParserNewIntents(unittest.TestCase):

    def _parse(self, cmd):
        from agent.parser import parse_command
        return parse_command(cmd)

    # show_skills
    def test_show_skills_basic(self):
        self.assertEqual(self._parse("show skills").intent, "show_skills")

    def test_show_skills_list_variant(self):
        self.assertEqual(self._parse("list skills").intent, "show_skills")

    def test_show_skills_what_variant(self):
        self.assertEqual(self._parse("what skills are available").intent, "show_skills")

    def test_show_skills_available_variant(self):
        self.assertEqual(self._parse("available skills").intent, "show_skills")

    # skill_info
    def test_skill_info_basic(self):
        p = self._parse("skill info ai_assist")
        self.assertEqual(p.intent, "skill_info")
        self.assertEqual(p.query, "ai_assist")

    def test_skill_info_details_variant(self):
        p = self._parse("skill details ai_assist")
        self.assertEqual(p.intent, "skill_info")

    def test_skill_info_query_extracted(self):
        p = self._parse("skill info ai_assist")
        self.assertEqual(p.query, "ai_assist")

    def test_skill_info_unknown_skill_name_extracted(self):
        p = self._parse("skill info something")
        self.assertEqual(p.intent, "skill_info")
        self.assertEqual(p.query, "something")

    # ai_suggest_command
    def test_ai_suggest_command_basic(self):
        p = self._parse("suggest command for check my phone")
        self.assertEqual(p.intent, "ai_suggest_command")

    def test_ai_suggest_command_query_extracted(self):
        p = self._parse("suggest command for check my phone setup")
        self.assertIsNotNone(p.query)
        self.assertIn("check", p.query)

    def test_ai_suggest_command_what_variant(self):
        p = self._parse("what command should I use for find duplicate files")
        self.assertEqual(p.intent, "ai_suggest_command")

    def test_ai_suggest_command_which_variant(self):
        p = self._parse("which command for backup")
        self.assertEqual(p.intent, "ai_suggest_command")

    # ai_explain_last_result
    def test_ai_explain_last_result_basic(self):
        p = self._parse("explain last result")
        self.assertEqual(p.intent, "ai_explain_last_result")

    def test_ai_explain_last_result_short_form(self):
        p = self._parse("explain result")
        self.assertEqual(p.intent, "ai_explain_last_result")

    def test_ai_explain_that(self):
        p = self._parse("explain that")
        self.assertEqual(p.intent, "ai_explain_last_result")

    def test_ai_what_did_that_do(self):
        p = self._parse("what did that do")
        self.assertEqual(p.intent, "ai_explain_last_result")

    def test_ai_what_did_that_mean(self):
        p = self._parse("what did that mean")
        self.assertEqual(p.intent, "ai_explain_last_result")

    # ai_clarify_request
    def test_ai_clarify_help_me_with(self):
        p = self._parse("help me with finding duplicate files")
        self.assertEqual(p.intent, "ai_clarify_request")

    def test_ai_clarify_query_extracted(self):
        p = self._parse("help me with organizing my downloads")
        self.assertIsNotNone(p.query)
        self.assertIn("organiz", p.query)

    def test_ai_clarify_help_me_find(self):
        p = self._parse("help me find large files")
        self.assertEqual(p.intent, "ai_clarify_request")

    def test_ai_clarify_help_me_understand(self):
        p = self._parse("help me understand storage report")
        self.assertEqual(p.intent, "ai_clarify_request")


# ── Safety — all new intents are read-only ─────────────────────────────────────

class TestSafetyNewIntents(unittest.TestCase):

    def _validate(self, cmd):
        from agent.parser import parse_command
        from agent.safety import validate_intent_safety
        parsed = parse_command(cmd)
        validate_intent_safety(parsed)
        return parsed

    def test_show_skills_passes_safety(self):
        self._validate("show skills")

    def test_skill_info_passes_safety(self):
        self._validate("skill info ai_assist")

    def test_ai_suggest_command_passes_safety(self):
        self._validate("suggest command for backup")

    def test_ai_explain_last_result_passes_safety(self):
        self._validate("explain last result")

    def test_ai_clarify_request_passes_safety(self):
        self._validate("help me with organizing files")

    def test_new_intents_are_read_only(self):
        from agent.safety import READ_ONLY_INTENTS
        for intent in ("show_skills", "skill_info", "ai_suggest_command",
                       "ai_explain_last_result", "ai_clarify_request"):
            self.assertIn(intent, READ_ONLY_INTENTS, f"{intent} not in READ_ONLY_INTENTS")

    def test_new_intents_not_in_path_required(self):
        from agent.safety import PATH_REQUIRED_INTENTS
        for intent in ("show_skills", "skill_info", "ai_suggest_command",
                       "ai_explain_last_result", "ai_clarify_request"):
            self.assertNotIn(intent, PATH_REQUIRED_INTENTS)

    def test_new_intents_not_in_url_required(self):
        from agent.safety import URL_REQUIRED_INTENTS
        for intent in ("show_skills", "skill_info", "ai_suggest_command",
                       "ai_explain_last_result", "ai_clarify_request"):
            self.assertNotIn(intent, URL_REQUIRED_INTENTS)


# ── Planner — correct tool_name + function_name ────────────────────────────────

class TestPlannerNewIntents(unittest.TestCase):

    def _plan(self, cmd):
        from agent.parser import parse_command
        from agent.planner import plan
        return plan(parse_command(cmd))

    def test_show_skills_plan(self):
        p = self._plan("show skills")
        self.assertEqual(p.actions[0].tool_name, "skill")
        self.assertEqual(p.actions[0].function_name, "list_skills")
        self.assertFalse(p.requires_confirmation)

    def test_skill_info_plan(self):
        p = self._plan("skill info ai_assist")
        self.assertEqual(p.actions[0].tool_name, "skill")
        self.assertEqual(p.actions[0].function_name, "skill_info")
        self.assertEqual(p.actions[0].arguments["skill_name"], "ai_assist")

    def test_ai_suggest_command_plan(self):
        p = self._plan("suggest command for backup my files")
        self.assertEqual(p.actions[0].tool_name, "ai_skill")
        self.assertEqual(p.actions[0].function_name, "suggest_command")
        self.assertIn("user_text", p.actions[0].arguments)

    def test_ai_explain_last_result_plan(self):
        from agent.parser import parse_command
        from agent.planner import plan
        parsed = parse_command("explain last result")
        parsed.options["last_command"] = "doctor"
        parsed.options["last_result"] = "success"
        p = plan(parsed)
        self.assertEqual(p.actions[0].tool_name, "ai_skill")
        self.assertEqual(p.actions[0].function_name, "explain_result")
        self.assertEqual(p.actions[0].arguments["last_command"], "doctor")

    def test_ai_clarify_request_plan(self):
        p = self._plan("help me with finding duplicates")
        self.assertEqual(p.actions[0].tool_name, "ai_skill")
        self.assertEqual(p.actions[0].function_name, "clarify_request")

    def test_all_new_plans_require_no_confirmation(self):
        cmds = [
            "show skills",
            "skill info ai_assist",
            "suggest command for organize",
            "help me with backup",
        ]
        for cmd in cmds:
            p = self._plan(cmd)
            self.assertFalse(p.requires_confirmation, f"plan for '{cmd}' requires confirmation")


# ── Executor — skill and ai_skill routing ─────────────────────────────────────

class TestExecutorSkillRouting(unittest.TestCase):

    def setUp(self):
        import skills.registry as reg_mod
        reg_mod._registry = None

    def _execute_plan(self, cmd, last_command="", last_result=""):
        from agent.parser import parse_command
        from agent.planner import plan
        from agent.executor import execute
        parsed = parse_command(cmd)
        if parsed.intent == "ai_explain_last_result":
            parsed.options["last_command"] = last_command
            parsed.options["last_result"] = last_result
        p = plan(parsed)
        return execute(p, confirmed=False)

    def test_show_skills_returns_success(self):
        result = self._execute_plan("show skills")
        from agent.models import OperationStatus
        self.assertEqual(result.status, OperationStatus.SUCCESS)

    def test_show_skills_raw_contains_skills_list(self):
        result = self._execute_plan("show skills")
        raw = result.raw_results[0]
        self.assertIn("skills", raw)
        self.assertIsInstance(raw["skills"], list)
        names = [s["name"] for s in raw["skills"]]
        self.assertIn("ai_assist", names)

    def test_skill_info_known_skill(self):
        result = self._execute_plan("skill info ai_assist")
        from agent.models import OperationStatus
        self.assertEqual(result.status, OperationStatus.SUCCESS)
        raw = result.raw_results[0]
        self.assertIsNotNone(raw.get("skill"))
        self.assertEqual(raw["skill"]["name"], "ai_assist")

    def test_skill_info_unknown_skill(self):
        result = self._execute_plan("skill info nonexistent")
        raw = result.raw_results[0]
        self.assertIn("error", raw)
        self.assertIn("nonexistent", raw["error"])

    def test_ai_suggest_command_disabled_returns_error(self):
        result = self._execute_plan("suggest command for backup")
        raw = result.raw_results[0]
        self.assertFalse(raw.get("success", True))
        self.assertIn("disabled", raw.get("error", "").lower())

    def test_ai_explain_result_disabled_returns_error(self):
        result = self._execute_plan("explain last result", "doctor", "success")
        raw = result.raw_results[0]
        self.assertFalse(raw.get("success", True))

    def test_ai_clarify_disabled_returns_error(self):
        result = self._execute_plan("help me with backup")
        raw = result.raw_results[0]
        self.assertFalse(raw.get("success", True))

    def test_ai_suggest_command_enabled(self):
        with patch("skills.ai_assist_skill._load_config",
                   return_value={"enabled": True, "backend": "local",
                                 "mode": "assist_only", "fallback_intent_suggestion": False}):
            import skills.registry as reg_mod
            reg_mod._registry = None
            result = self._execute_plan("suggest command for check my phone")
            raw = result.raw_results[0]
            self.assertTrue(raw.get("success"))
            self.assertEqual(raw.get("type"), "suggest_command")
            self.assertIn("suggested_command", raw)
            self.assertIsInstance(raw["confidence"], float)

    def test_ai_explain_enabled(self):
        with patch("skills.ai_assist_skill._load_config",
                   return_value={"enabled": True, "backend": "local",
                                 "mode": "assist_only", "fallback_intent_suggestion": False}):
            import skills.registry as reg_mod
            reg_mod._registry = None
            result = self._execute_plan("explain last result", "doctor", "success")
            raw = result.raw_results[0]
            self.assertTrue(raw.get("success"))
            self.assertEqual(raw.get("type"), "explain_result")
            self.assertIn("summary", raw)

    def test_ai_clarify_enabled(self):
        with patch("skills.ai_assist_skill._load_config",
                   return_value={"enabled": True, "backend": "local",
                                 "mode": "assist_only", "fallback_intent_suggestion": False}):
            import skills.registry as reg_mod
            reg_mod._registry = None
            result = self._execute_plan("help me with finding duplicate files")
            raw = result.raw_results[0]
            self.assertTrue(raw.get("success"))
            self.assertEqual(raw.get("type"), "clarify_request")
            self.assertIn("clarification_question", raw)


# ── Reporter — formatting ──────────────────────────────────────────────────────

class TestReporterNewIntents(unittest.TestCase):

    def setUp(self):
        import skills.registry as reg_mod
        reg_mod._registry = None

    def _report(self, cmd, last_command="", last_result=""):
        from agent.parser import parse_command
        from agent.planner import plan
        from agent.executor import execute
        from agent.reporter import report_result
        parsed = parse_command(cmd)
        if parsed.intent == "ai_explain_last_result":
            parsed.options["last_command"] = last_command
            parsed.options["last_result"] = last_result
        p = plan(parsed)
        result = execute(p, confirmed=False)
        return report_result(result, parsed.intent, False)

    def test_show_skills_output_contains_ai_assist(self):
        output = self._report("show skills")
        self.assertIn("ai_assist", output)

    def test_show_skills_output_shows_status(self):
        output = self._report("show skills")
        self.assertIn("disabled", output.lower())

    def test_skill_info_output_contains_name(self):
        output = self._report("skill info ai_assist")
        self.assertIn("ai_assist", output)
        self.assertIn("Version", output)

    def test_skill_info_output_shows_commands(self):
        output = self._report("skill info ai_assist")
        self.assertIn("suggest command for", output)
        self.assertIn("explain last result", output)

    def test_skill_info_unknown_shows_error(self):
        output = self._report("skill info xyz")
        self.assertIn("Unknown skill", output)

    def test_ai_disabled_shows_helpful_message(self):
        output = self._report("suggest command for backup")
        self.assertIn("disabled", output.lower())
        self.assertIn("ai_assist.json", output)

    def test_ai_suggest_command_enabled_shows_suggestion(self):
        with patch("skills.ai_assist_skill._load_config",
                   return_value={"enabled": True, "backend": "local",
                                 "mode": "assist_only", "fallback_intent_suggestion": False}):
            import skills.registry as reg_mod
            reg_mod._registry = None
            output = self._report("suggest command for check my phone")
            self.assertIn("Suggested command", output)
            self.assertIn("Confidence", output)
            self.assertIn("advisory", output.lower())

    def test_ai_explain_enabled_shows_summary(self):
        with patch("skills.ai_assist_skill._load_config",
                   return_value={"enabled": True, "backend": "local",
                                 "mode": "assist_only", "fallback_intent_suggestion": False}):
            import skills.registry as reg_mod
            reg_mod._registry = None
            output = self._report("explain last result", "doctor", "success")
            self.assertIn("Summary", output)

    def test_ai_clarify_enabled_shows_question(self):
        with patch("skills.ai_assist_skill._load_config",
                   return_value={"enabled": True, "backend": "local",
                                 "mode": "assist_only", "fallback_intent_suggestion": False}):
            import skills.registry as reg_mod
            reg_mod._registry = None
            output = self._report("help me with media files")
            self.assertIn("Question", output)


# ── LocalBackend — deterministic keyword matching ─────────────────────────────

class TestLocalBackend(unittest.TestCase):

    def setUp(self):
        from llm.local_backend import LocalBackend
        self.backend = LocalBackend()
        from skills.ai_assist_skill import AVAILABLE_INTENTS
        self.intents = AVAILABLE_INTENTS

    def test_is_always_available(self):
        self.assertTrue(self.backend.is_available())

    def test_suggest_command_doctor_keywords(self):
        result = self.backend.suggest_command("check my phone setup", self.intents)
        self.assertEqual(result.suggested_command, "doctor")
        self.assertGreater(result.confidence, 0.0)
        self.assertLessEqual(result.confidence, 1.0)

    def test_suggest_command_backup_keywords(self):
        result = self.backend.suggest_command("back up my documents folder", self.intents)
        self.assertEqual(result.suggested_command, "back up /sdcard/Documents to /sdcard/Backup")

    def test_suggest_command_storage_keywords(self):
        result = self.backend.suggest_command("how much disk space is left", self.intents)
        self.assertEqual(result.suggested_command, "storage report /sdcard/Download")

    def test_suggest_command_fallback_doctor(self):
        result = self.backend.suggest_command("xxxxunknownxxx", self.intents)
        self.assertEqual(result.suggested_command, "doctor")
        self.assertLess(result.confidence, 0.2)

    def test_suggest_command_confidence_is_float(self):
        result = self.backend.suggest_command("organize my files", self.intents)
        self.assertIsInstance(result.confidence, float)

    def test_suggest_command_respects_available_intents(self):
        result = self.backend.suggest_command("organize", ["organize_folder_by_type"])
        # Should still find a match
        self.assertIsNotNone(result.suggested_command)

    def test_explain_result_no_previous_command(self):
        result = self.backend.explain_result("", "")
        self.assertIn("No previous command", result.summary)

    def test_explain_result_doctor_command(self):
        result = self.backend.explain_result("doctor", "ok")
        self.assertIn("environment", result.summary.lower())

    def test_explain_result_storage_command(self):
        result = self.backend.explain_result("storage report /sdcard/Download", "success")
        self.assertIn("storage", result.summary.lower())

    def test_explain_result_backup_command(self):
        result = self.backend.explain_result("back up /sdcard/Documents to /sdcard/Backup", "")
        self.assertIn("backup", result.summary.lower())

    def test_explain_result_organize_includes_safety_note(self):
        result = self.backend.explain_result("organize /sdcard/Download", "")
        self.assertIsNotNone(result.safety_note)

    def test_explain_result_compress_includes_safety_note(self):
        result = self.backend.explain_result("compress images /sdcard/Pictures", "")
        self.assertIsNotNone(result.safety_note)

    def test_clarify_request_media_keywords(self):
        result = self.backend.clarify_request("show my media", self.intents)
        self.assertTrue(result.clarification_needed)
        self.assertIsNotNone(result.clarification_question)
        self.assertIn("folder", result.clarification_question.lower())

    def test_clarify_request_backup_keywords(self):
        result = self.backend.clarify_request("I want to back up my files", self.intents)
        self.assertTrue(result.clarification_needed)
        self.assertIsNotNone(result.clarification_question)

    def test_clarify_request_candidates_are_valid_intents(self):
        result = self.backend.clarify_request("find duplicate photos", self.intents)
        for candidate in result.candidate_intents:
            self.assertIn(candidate, self.intents)

    def test_clarify_request_max_three_candidates(self):
        result = self.backend.clarify_request("show organize backup media", self.intents)
        self.assertLessEqual(len(result.candidate_intents), 3)

    def test_suggest_intent_returns_allowed_intent_only(self):
        allowed = ["doctor", "storage_report", "backup_folder"]
        result = self.backend.suggest_intent("check my setup", allowed)
        if result.intent is not None:
            self.assertIn(result.intent, allowed)

    def test_suggest_intent_never_invents_intents(self):
        allowed = ["doctor", "backup_folder"]
        # Run many different queries and check all stay in allowed
        queries = [
            "organize files", "find duplicates", "compress images",
            "search the web", "open chrome", "battery status",
        ]
        for q in queries:
            result = self.backend.suggest_intent(q, allowed)
            if result.intent is not None:
                self.assertIn(result.intent, allowed, f"Invented intent '{result.intent}' for '{q}'")


# ── AIAssistSkill — disabled vs enabled behaviour ────────────────────────────

class TestAIAssistSkillDisabled(unittest.TestCase):

    def setUp(self):
        import skills.registry as reg_mod
        reg_mod._registry = None

    def _get_skill(self, enabled=False):
        from skills.ai_assist_skill import AIAssistSkill
        with patch("skills.ai_assist_skill._load_config",
                   return_value={"enabled": enabled, "backend": "local",
                                 "mode": "assist_only", "fallback_intent_suggestion": False}):
            return AIAssistSkill()

    def test_disabled_suggest_command_raises(self):
        skill = self._get_skill(enabled=False)
        with self.assertRaises(RuntimeError) as ctx:
            skill.suggest_command("check my setup")
        self.assertIn("disabled", str(ctx.exception).lower())

    def test_disabled_explain_result_raises(self):
        skill = self._get_skill(enabled=False)
        with self.assertRaises(RuntimeError):
            skill.explain_result("doctor", "ok")

    def test_disabled_clarify_request_raises(self):
        skill = self._get_skill(enabled=False)
        with self.assertRaises(RuntimeError):
            skill.clarify_request("help me")

    def test_enabled_suggest_command_returns_result(self):
        skill = self._get_skill(enabled=True)
        result = skill.suggest_command("check my phone setup")
        self.assertIsNotNone(result.suggested_command)
        self.assertIsInstance(result.confidence, float)

    def test_enabled_explain_result_returns_summary(self):
        skill = self._get_skill(enabled=True)
        result = skill.explain_result("doctor", "ok")
        self.assertIsNotNone(result.summary)

    def test_enabled_clarify_request_returns_question(self):
        skill = self._get_skill(enabled=True)
        result = skill.clarify_request("show my media")
        self.assertIsNotNone(result.clarification_question)

    def test_error_message_mentions_config_file(self):
        skill = self._get_skill(enabled=False)
        with self.assertRaises(RuntimeError) as ctx:
            skill.suggest_command("anything")
        self.assertIn("ai_assist.json", str(ctx.exception))


# ── Fallback intent suggestion — safety constraints ───────────────────────────

class TestFallbackIntentSuggestion(unittest.TestCase):

    def setUp(self):
        import skills.registry as reg_mod
        reg_mod._registry = None

    def _get_skill(self, fallback=False):
        from skills.ai_assist_skill import AIAssistSkill
        with patch("skills.ai_assist_skill._load_config",
                   return_value={"enabled": True, "backend": "local",
                                 "mode": "assist_only", "fallback_intent_suggestion": fallback}):
            return AIAssistSkill()

    def test_fallback_disabled_returns_none_intent(self):
        skill = self._get_skill(fallback=False)
        result = skill.suggest_intent("organize my files")
        self.assertIsNone(result.intent)
        self.assertEqual(result.confidence, 0.0)
        self.assertIn("disabled", result.explanation.lower())

    def test_fallback_enabled_returns_whitelisted_intent(self):
        from skills.ai_assist_skill import AVAILABLE_INTENTS
        skill = self._get_skill(fallback=True)
        result = skill.suggest_intent("check my phone setup")
        if result.intent is not None:
            self.assertIn(result.intent, AVAILABLE_INTENTS,
                          f"Returned non-whitelisted intent: '{result.intent}'")

    def test_safety_gate_rejects_invented_intents(self):
        from skills.ai_assist_skill import AIAssistSkill, AVAILABLE_INTENTS
        from llm.schemas import IntentSuggestion
        with patch("skills.ai_assist_skill._load_config",
                   return_value={"enabled": True, "backend": "local",
                                 "mode": "assist_only", "fallback_intent_suggestion": True}):
            skill = AIAssistSkill()
            # Monkey-patch backend to return a fake intent
            class FakeBackend:
                def suggest_intent(self, text, allowed):
                    return IntentSuggestion(intent="rm_files", confidence=0.9,
                                           explanation="delete files")
                def is_available(self):
                    return True
            skill._backend = FakeBackend()
            result = skill.suggest_intent("delete my files")
            # Safety gate must discard it
            self.assertIsNone(result.intent)
            self.assertEqual(result.confidence, 0.0)


# ── Low-confidence suggestions do not auto-execute ────────────────────────────

class TestNoAutoExecution(unittest.TestCase):

    def setUp(self):
        import skills.registry as reg_mod
        reg_mod._registry = None

    def test_ai_intents_never_require_confirmation(self):
        from agent.parser import parse_command
        from agent.planner import plan
        cmds = [
            "suggest command for backup",
            "explain last result",
            "help me with finding large files",
        ]
        for cmd in cmds:
            parsed = parse_command(cmd)
            if parsed.intent == "ai_explain_last_result":
                parsed.options["last_command"] = ""
                parsed.options["last_result"] = ""
            p = plan(parsed)
            self.assertFalse(p.requires_confirmation,
                             f"'{cmd}' should not require confirmation")

    def test_ai_intents_risk_level_is_low(self):
        from agent.parser import parse_command
        from agent.planner import plan
        from agent.models import RiskLevel
        cmds = [
            "suggest command for organize files",
            "help me with backup",
        ]
        for cmd in cmds:
            p = plan(parse_command(cmd))
            self.assertEqual(p.risk_level, RiskLevel.LOW,
                             f"'{cmd}' should have LOW risk")


# ── Existing non-AI commands are unaffected ────────────────────────────────────

class TestExistingCommandsUnaffected(unittest.TestCase):

    def _parse(self, cmd):
        from agent.parser import parse_command
        return parse_command(cmd)

    def _plan(self, cmd):
        from agent.planner import plan
        return plan(self._parse(cmd))

    def test_doctor_intent_unchanged(self):
        self.assertEqual(self._parse("doctor").intent, "doctor")

    def test_storage_report_intent_unchanged(self):
        self.assertEqual(self._parse("storage report /sdcard").intent, "storage_report")

    def test_show_files_intent_unchanged(self):
        p = self._parse("show files in /sdcard/Download")
        self.assertEqual(p.intent, "show_files")
        self.assertEqual(p.source_path, "/sdcard/Download")

    def test_show_folders_intent_unchanged(self):
        p = self._parse("show folders in /sdcard")
        self.assertEqual(p.intent, "show_folders")

    def test_find_duplicates_intent_unchanged(self):
        self.assertEqual(self._parse("find duplicates /sdcard/Download").intent, "find_duplicates")

    def test_browser_search_intent_unchanged(self):
        p = self._parse("search for python tips")
        self.assertEqual(p.intent, "browser_search")
        self.assertEqual(p.query, "python tips")

    def test_doctor_planner_uses_system_tool(self):
        p = self._plan("doctor")
        self.assertEqual(p.actions[0].tool_name, "system")
        self.assertEqual(p.actions[0].function_name, "run_doctor")

    def test_backup_requires_confirmation(self):
        p = self._plan("back up /sdcard/Documents to /sdcard/Backup")
        self.assertTrue(p.requires_confirmation)

    def test_ai_intents_dont_use_system_tool(self):
        for cmd in ("show skills", "skill info ai_assist", "suggest command for backup",
                    "explain last result", "help me with files"):
            p = self._parse(cmd)
            if p.intent == "ai_explain_last_result":
                from agent.parser import parse_command
                parsed = parse_command(cmd)
                parsed.options["last_command"] = ""
                parsed.options["last_result"] = ""
                from agent.planner import plan
                ep = plan(parsed)
            else:
                from agent.planner import plan
                ep = plan(p)
            for action in ep.actions:
                self.assertNotEqual(action.tool_name, "system",
                                    f"AI/skill intent '{p.intent}' must not use system tool")


# ── LLM schemas — typed dataclasses ───────────────────────────────────────────

class TestLLMSchemas(unittest.TestCase):

    def test_command_suggestion_fields(self):
        from llm.schemas import CommandSuggestion
        s = CommandSuggestion(suggested_command="doctor", rationale="checks env", confidence=0.8)
        self.assertEqual(s.suggested_command, "doctor")
        self.assertEqual(s.rationale, "checks env")
        self.assertEqual(s.confidence, 0.8)

    def test_clarification_fields(self):
        from llm.schemas import Clarification
        c = Clarification(clarification_needed=True, clarification_question="Which folder?",
                          candidate_intents=["show_files"])
        self.assertTrue(c.clarification_needed)
        self.assertEqual(c.clarification_question, "Which folder?")

    def test_intent_suggestion_fields(self):
        from llm.schemas import IntentSuggestion
        s = IntentSuggestion(intent="backup_folder", confidence=0.75,
                             explanation="matches backup keywords")
        self.assertEqual(s.intent, "backup_folder")

    def test_result_explanation_fields(self):
        from llm.schemas import ResultExplanation
        r = ResultExplanation(summary="All ok", safety_note=None, suggested_next_step="run doctor")
        self.assertEqual(r.summary, "All ok")
        self.assertIsNone(r.safety_note)

    def test_intent_suggestion_none_intent(self):
        from llm.schemas import IntentSuggestion
        s = IntentSuggestion(intent=None, confidence=0.0, explanation="no match")
        self.assertIsNone(s.intent)
        self.assertEqual(s.confidence, 0.0)


# ── Main.py — version and help text ───────────────────────────────────────────

class TestMainV06(unittest.TestCase):

    def test_banner_says_current_version(self):
        from main import BANNER
        self.assertIn("v1.0", BANNER)

    def test_help_text_says_current_version(self):
        from main import HELP_TEXT
        self.assertIn("v1.0", HELP_TEXT)

    def test_help_text_has_ai_assist_section(self):
        from main import HELP_TEXT
        self.assertIn("AI ASSIST", HELP_TEXT)

    def test_help_text_has_suggest_command(self):
        from main import HELP_TEXT
        self.assertIn("suggest command for", HELP_TEXT)

    def test_help_text_has_explain_last_result(self):
        from main import HELP_TEXT
        self.assertIn("explain last result", HELP_TEXT)

    def test_help_text_has_help_me_with(self):
        from main import HELP_TEXT
        self.assertIn("help me with", HELP_TEXT)

    def test_help_text_has_skills_section(self):
        from main import HELP_TEXT
        self.assertIn("SKILLS", HELP_TEXT)
        self.assertIn("show skills", HELP_TEXT)

    def test_help_text_advisory_disclaimer(self):
        from main import HELP_TEXT
        self.assertIn("advisory only", HELP_TEXT.lower())

    def test_context_memory_exists(self):
        import main
        from agent.context import ContextMemory
        self.assertTrue(hasattr(main, "_ctx"))
        self.assertIsInstance(main._ctx, ContextMemory)
        self.assertTrue(hasattr(main._ctx, "last_command"))
        self.assertTrue(hasattr(main._ctx, "last_result_msg"))


# ── Config files ───────────────────────────────────────────────────────────────

class TestConfigFiles(unittest.TestCase):

    def _config_path(self, filename):
        return os.path.join(os.path.dirname(__file__), "..", "config", filename)

    def test_ai_assist_json_exists(self):
        self.assertTrue(os.path.isfile(self._config_path("ai_assist.json")))

    def test_ai_assist_json_valid(self):
        with open(self._config_path("ai_assist.json")) as f:
            config = json.load(f)
        self.assertIn("enabled", config)
        self.assertIn("backend", config)
        self.assertIn("mode", config)

    def test_ai_assist_default_disabled(self):
        with open(self._config_path("ai_assist.json")) as f:
            config = json.load(f)
        self.assertFalse(config["enabled"], "Default config must have enabled=false (safest default)")

    def test_ai_assist_backend_is_local(self):
        with open(self._config_path("ai_assist.json")) as f:
            config = json.load(f)
        self.assertEqual(config["backend"], "local")

    def test_skills_json_exists(self):
        self.assertTrue(os.path.isfile(self._config_path("skills.json")))

    def test_skills_json_valid(self):
        with open(self._config_path("skills.json")) as f:
            config = json.load(f)
        self.assertIn("skills", config)
        self.assertIsInstance(config["skills"], list)


# ── Audit: backend validation ──────────────────────────────────────────────────

class TestAIAssistBackendValidation(unittest.TestCase):
    """M1 audit fix: unknown backend name must raise, not silently use local."""

    def _make_skill(self, backend_name: str):
        from skills.ai_assist_skill import AIAssistSkill
        skill = AIAssistSkill.__new__(AIAssistSkill)
        skill.enabled = True
        skill.mode = "assist_only"
        skill.backend_name = backend_name
        skill.fallback_intent_suggestion = False
        skill._llama_cfg = {}
        skill._backend = None
        return skill

    def test_unknown_backend_raises_runtime_error(self):
        skill = self._make_skill("llamacpp")
        with self.assertRaises(RuntimeError) as ctx:
            skill._get_backend()
        self.assertIn("Unknown AI backend", str(ctx.exception))
        self.assertIn("llamacpp", str(ctx.exception))

    def test_unknown_backend_typo_raises_runtime_error(self):
        skill = self._make_skill("llama-cpp")
        with self.assertRaises(RuntimeError) as ctx:
            skill._get_backend()
        self.assertIn("Unknown AI backend", str(ctx.exception))

    def test_empty_string_backend_defaults_to_local(self):
        # v1.1: empty string is treated as falsy → defaults to "local" (safer than raising)
        from llm.local_backend import LocalBackend
        skill = self._make_skill("")
        backend = skill._get_backend()
        self.assertIsInstance(backend, LocalBackend)

    def test_valid_backend_local_does_not_raise(self):
        skill = self._make_skill("local")
        backend = skill._get_backend()
        from llm.local_backend import LocalBackend
        self.assertIsInstance(backend, LocalBackend)

    def test_valid_backend_llama_cpp_does_not_raise(self):
        skill = self._make_skill("llama_cpp")
        from llm.llama_cpp_backend import LlamaCppBackend
        backend = skill._get_backend()
        self.assertIsInstance(backend, LlamaCppBackend)

    def test_error_message_lists_allowed_values(self):
        skill = self._make_skill("gpt4")
        with self.assertRaises(RuntimeError) as ctx:
            skill._get_backend()
        msg = str(ctx.exception)
        self.assertIn("local", msg)
        self.assertIn("llama_cpp", msg)

    def test_backend_cached_after_first_get(self):
        skill = self._make_skill("local")
        b1 = skill._get_backend()
        b2 = skill._get_backend()
        self.assertIs(b1, b2)


if __name__ == "__main__":
    unittest.main()
