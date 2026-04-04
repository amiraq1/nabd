"""
Skill registry — module-level singleton that holds all registered skills.
Bootstrap happens lazily on first access via get_registry().
"""
from __future__ import annotations
from skills.base import SkillBase, SkillInfo


class SkillRegistry:
    def __init__(self) -> None:
        self._skills: dict[str, SkillBase] = {}

    def register(self, skill: SkillBase) -> None:
        self._skills[skill.name] = skill

    def get(self, name: str) -> SkillBase | None:
        return self._skills.get(name)

    def list_skills(self) -> list[SkillInfo]:
        return [skill.get_info() for skill in self._skills.values()]

    def is_enabled(self, name: str) -> bool:
        skill = self._skills.get(name)
        return skill.is_enabled() if skill else False

    def list_names(self) -> list[str]:
        return list(self._skills.keys())


_registry: SkillRegistry | None = None


def get_registry() -> SkillRegistry:
    global _registry
    if _registry is None:
        _registry = SkillRegistry()
        _bootstrap(_registry)
    return _registry


def reset_registry() -> None:
    """Reset the module-level singleton so tests can isolate registry state."""
    global _registry
    _registry = None


def _bootstrap(registry: SkillRegistry) -> None:
    from skills.ai_assist_skill import AIAssistSkill
    registry.register(AIAssistSkill())
