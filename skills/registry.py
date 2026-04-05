"""
Skill registry — module-level singleton that holds all registered skills.
Bootstrap happens lazily on first access via get_registry().
"""
from __future__ import annotations
import os

from skills.base import SkillBase, SkillInfo
from skills.discovery import SkillValidationResult, validate_skill

_DEFAULT_SKILLS_ROOT = os.path.realpath(os.path.dirname(__file__))


class SkillRegistry:
    def __init__(self, skill_root: str | None = None, *, include_builtins: bool = True) -> None:
        self._skill_root = os.path.realpath(skill_root or _DEFAULT_SKILLS_ROOT)
        self._include_builtins = include_builtins
        self._skills: dict[str, SkillBase] = {}
        self._load_errors: dict[str, str] = {}
        self.reload_skills()

    def register(self, skill: SkillBase) -> None:
        if skill.name in self._skills:
            raise ValueError(f"Duplicate skill name: '{skill.name}'")
        self._skills[skill.name] = skill

    def get(self, name: str) -> SkillBase | None:
        return self._skills.get(name)

    def get_skill(self, name: str) -> SkillBase | None:
        return self.get(name)

    def list_skills(self) -> list[SkillInfo]:
        return [self._skills[name].get_info() for name in sorted(self._skills)]

    def is_enabled(self, name: str) -> bool:
        skill = self._skills.get(name)
        return skill.is_enabled() if skill else False

    def list_names(self) -> list[str]:
        return sorted(self._skills.keys())

    def list_errors(self) -> dict[str, str]:
        return dict(self._load_errors)

    def validate_skill(self, skill_dir: str) -> SkillValidationResult:
        return validate_skill(skill_dir, self._skill_root)

    def reload_skills(self) -> None:
        self._skills.clear()
        self._load_errors.clear()

        if self._include_builtins:
            self._bootstrap_builtins()

        try:
            entries = sorted(os.scandir(self._skill_root), key=lambda entry: entry.name)
        except FileNotFoundError:
            return

        for entry in entries:
            if not entry.is_dir(follow_symlinks=False):
                continue
            if entry.name.startswith("__"):
                continue
            result = self.validate_skill(entry.path)
            if not result.valid or result.skill is None:
                self._load_errors[entry.name] = result.error or "Unknown skill validation error."
                continue
            try:
                self.register(result.skill)
            except ValueError as exc:
                self._load_errors[entry.name] = str(exc)

    def execute_skill(self, name: str) -> dict:
        skill = self.get(name)
        if skill is None:
            raise RuntimeError(f"Unknown skill: '{name}'")
        return skill.execute()

    def _bootstrap_builtins(self) -> None:
        from skills.ai_assist_skill import AIAssistSkill

        try:
            self.register(AIAssistSkill())
        except ValueError as exc:
            self._load_errors["ai_assist"] = str(exc)


_registry: SkillRegistry | None = None


def get_registry() -> SkillRegistry:
    global _registry
    if _registry is None:
        _registry = SkillRegistry()
    return _registry


def reset_registry() -> None:
    """Reset the module-level singleton so tests can isolate registry state."""
    global _registry
    _registry = None
