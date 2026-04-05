import os
import tempfile
import textwrap
import unittest

from agent.context import ContextMemory
from agent.executor import execute
from agent.parser import parse_command
from agent.planner import plan
from agent.reporter import report_result
from agent.safety import validate_intent_safety
from core.exceptions import ValidationError
from skills import SkillRegistry, reset_registry


def _write_skill(
    root: str,
    name: str,
    *,
    description: str = "Test skill",
    version: str = "1.0.0",
    author: str | None = None,
    requires_python: bool = False,
    entrypoint: str | None = None,
    tags: str | None = None,
    instructions: str | None = "Use this skill carefully.",
    usage: str | None = None,
    raw_front_matter: str | None = None,
    logic_code: str | None = None,
) -> str:
    skill_dir = os.path.join(root, name)
    os.makedirs(skill_dir, exist_ok=True)

    if raw_front_matter is None:
        lines = [
            "---",
            f"name: {name}",
            f"description: {description}",
            f"version: {version}",
        ]
        if author:
            lines.append(f"author: {author}")
        if requires_python:
            lines.append("requires_python: true")
        if entrypoint:
            lines.append(f"entrypoint: {entrypoint}")
        if tags:
            lines.append(f"tags: {tags}")
        lines.append("---")
        lines.append("")
        lines.append(f"# {name}")
        if instructions is not None:
            lines.append("")
            lines.append("## Instructions")
            lines.append(instructions)
        if usage is not None:
            lines.append("")
            lines.append("## Usage")
            lines.append(usage)
        skill_md = "\n".join(lines) + "\n"
    else:
        skill_md = raw_front_matter

    with open(os.path.join(skill_dir, "SKILL.md"), "w", encoding="utf-8") as handle:
        handle.write(skill_md)

    if logic_code is not None:
        with open(os.path.join(skill_dir, "skill_logic.py"), "w", encoding="utf-8") as handle:
            handle.write(textwrap.dedent(logic_code).lstrip())

    return skill_dir


class TestSkillDiscovery(unittest.TestCase):
    def tearDown(self) -> None:
        reset_registry()

    def test_valid_metadata_only_skill_is_discovered(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            _write_skill(
                tmpdir,
                "notes_helper",
                description="Metadata-only skill.",
                usage="skill info notes_helper",
            )

            registry = SkillRegistry(skill_root=tmpdir, include_builtins=False)

            self.assertEqual(registry.list_names(), ["notes_helper"])
            info = registry.get("notes_helper").get_info()
            self.assertFalse(info.requires_python)
            self.assertFalse(info.has_python_logic)
            self.assertIsNone(info.entrypoint)
            self.assertEqual(info.source, "filesystem")
            self.assertIn("notes_helper", info.usage)

    def test_valid_python_backed_skill_is_discovered(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            _write_skill(
                tmpdir,
                "runner",
                description="Python-backed skill.",
                requires_python=True,
                entrypoint="run",
                usage="run skill runner",
                logic_code="""
                def run():
                    return {"message": "runner ok"}
                """,
            )

            registry = SkillRegistry(skill_root=tmpdir, include_builtins=False)

            self.assertEqual(registry.list_names(), ["runner"])
            info = registry.get("runner").get_info()
            self.assertTrue(info.requires_python)
            self.assertTrue(info.has_python_logic)
            self.assertEqual(info.entrypoint, "run")

    def test_malformed_skill_is_ignored_safely(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            _write_skill(tmpdir, "good_skill", usage="skill info good_skill")
            _write_skill(
                tmpdir,
                "broken_skill",
                raw_front_matter="""---
name: broken_skill
version: 1.0.0
---

# broken_skill
""",
            )

            registry = SkillRegistry(skill_root=tmpdir, include_builtins=False)

            self.assertEqual(registry.list_names(), ["good_skill"])
            self.assertIn("broken_skill", registry.list_errors())
            self.assertIn("Missing required metadata field(s): description", registry.list_errors()["broken_skill"])

    def test_duplicate_skill_name_against_builtin_is_reported(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            _write_skill(
                tmpdir,
                "ai_assist",
                description="Conflicts with the built-in skill name.",
            )

            registry = SkillRegistry(skill_root=tmpdir, include_builtins=True)

            self.assertIn("ai_assist", registry.list_names())
            self.assertIn("ai_assist", registry.list_errors())
            self.assertIn("Duplicate skill name", registry.list_errors()["ai_assist"])

    def test_discovery_does_not_execute_python_code(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            marker = os.path.join(tmpdir, "marker.txt")
            _write_skill(
                tmpdir,
                "lazy_runner",
                requires_python=True,
                entrypoint="run",
                logic_code=f"""
                from pathlib import Path

                Path(r\"{marker}\").write_text(\"imported\", encoding=\"utf-8\")

                def run():
                    return {{"message": "executed"}}
                """,
            )

            registry = SkillRegistry(skill_root=tmpdir, include_builtins=False)
            self.assertIn("lazy_runner", registry.list_names())
            self.assertFalse(os.path.exists(marker))

            result = registry.execute_skill("lazy_runner")
            self.assertTrue(result["success"])
            self.assertTrue(os.path.exists(marker))


class TestSkillExecutionSafety(unittest.TestCase):
    def setUp(self) -> None:
        reset_registry()

    def tearDown(self) -> None:
        reset_registry()

    def test_invalid_entrypoint_is_blocked(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            _write_skill(
                tmpdir,
                "bad_runner",
                requires_python=True,
                entrypoint="launch",
                logic_code="""
                def launch():
                    return {"message": "nope"}
                """,
            )

            registry = SkillRegistry(skill_root=tmpdir, include_builtins=False)

            self.assertEqual(registry.list_names(), [])
            self.assertIn("bad_runner", registry.list_errors())
            self.assertIn("Entrypoint 'launch' is not allowed", registry.list_errors()["bad_runner"])

    def test_run_skill_rejects_extra_arguments(self):
        parsed = parse_command("run skill duplicate_helper with /sdcard/Download")

        self.assertEqual(parsed.intent, "run_skill")
        self.assertEqual(parsed.query, "duplicate_helper")
        self.assertIn("skill_argument_text", parsed.options)

        with self.assertRaises(ValidationError):
            validate_intent_safety(parsed)

    def test_run_skill_unknown_skill_is_rejected(self):
        parsed = parse_command("run skill no_such_skill")

        with self.assertRaises(ValidationError):
            validate_intent_safety(parsed)

    def test_run_skill_pipeline_executes_only_whitelisted_entrypoint(self):
        parsed = parse_command("run skill duplicate_helper")
        validate_intent_safety(parsed)

        execution_plan = plan(parsed)
        result = execute(execution_plan, confirmed=False)

        self.assertEqual(result.status.value, "success")
        self.assertTrue(result.raw_results)
        raw = result.raw_results[0]
        self.assertEqual(raw["skill_name"], "duplicate_helper")
        self.assertEqual(raw["entrypoint"], "run")
        self.assertIn("Duplicate Helper", raw["message"])

    def test_run_skill_does_not_update_context(self):
        ctx = ContextMemory()
        ctx.update(
            intent="run_skill",
            command="run skill duplicate_helper",
            result_msg="ok",
            success=True,
        )

        self.assertIsNone(ctx.last_intent)
        self.assertEqual(ctx.last_command, "")
        self.assertEqual(ctx.last_result_msg, "")


class TestSkillReporterAndHelp(unittest.TestCase):
    def setUp(self) -> None:
        reset_registry()

    def tearDown(self) -> None:
        reset_registry()

    def test_show_skills_output_includes_skill_types(self):
        import skills.registry as registry_module

        with tempfile.TemporaryDirectory() as tmpdir:
            _write_skill(tmpdir, "notes_helper", usage="skill info notes_helper")
            _write_skill(
                tmpdir,
                "runner",
                requires_python=True,
                entrypoint="run",
                usage="run skill runner",
                logic_code="""
                def run():
                    return {"message": "runner ok"}
                """,
            )
            registry_module._registry = SkillRegistry(skill_root=tmpdir, include_builtins=False)

            parsed = parse_command("show skills")
            result = execute(plan(parsed), confirmed=False)
            output = report_result(result, parsed.intent, confirmed=False)

            self.assertIn("notes_helper", output)
            self.assertIn("runner", output)
            self.assertIn("[Metadata-only]", output)
            self.assertIn("[Python-backed]", output)

    def test_skill_info_output_shows_usage_and_entrypoint(self):
        parsed = parse_command("skill info duplicate_helper")
        validate_intent_safety(parsed)

        result = execute(plan(parsed), confirmed=False)
        output = report_result(result, parsed.intent, confirmed=False)

        self.assertIn("duplicate_helper", output)
        self.assertIn("Entrypoint : run", output)
        self.assertIn("Instructions:", output)
        self.assertIn("Usage:", output)
        self.assertIn("run skill duplicate_helper", output)

    def test_invalid_skill_lookup_output_is_clear(self):
        parsed = parse_command("skill info no_such_skill")
        validate_intent_safety(parsed)

        result = execute(plan(parsed), confirmed=False)
        output = report_result(result, parsed.intent, confirmed=False)

        self.assertIn("Unknown skill", output)
        self.assertIn("show skills", output)

    def test_run_skill_output_is_formatted(self):
        parsed = parse_command("run skill duplicate_helper")
        validate_intent_safety(parsed)

        result = execute(plan(parsed), confirmed=False)
        output = report_result(result, parsed.intent, confirmed=False)

        self.assertIn("Skill      : duplicate_helper", output)
        self.assertIn("Entrypoint : run", output)
        self.assertIn("Details:", output)

    def test_help_text_documents_skills_surface(self):
        from main import HELP_TEXT

        self.assertIn("show skills", HELP_TEXT)
        self.assertIn("skill info duplicate_helper", HELP_TEXT)
        self.assertIn("run skill duplicate_helper", HELP_TEXT)
        self.assertIn("skills/<name>/SKILL.md", HELP_TEXT)


if __name__ == "__main__":
    unittest.main()
