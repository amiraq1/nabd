"""
Base class and info struct for all Nabd skills.
Skills are optional capability modules. They never execute tool actions directly.
"""
from __future__ import annotations
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Any


@dataclass
class SkillInfo:
    name: str
    description: str
    version: str
    enabled: bool = True
    tags: list[str] = field(default_factory=list)
    path: str = ""
    author: str | None = None
    requires_python: bool = False
    has_python_logic: bool = False
    entrypoint: str | None = None
    usage: str | None = None
    instructions: str | None = None
    source: str = "filesystem"
    load_error: str | None = None


class SkillBase(ABC):
    """
    Abstract base class for Nabd skills.
    Subclasses must declare: name, description, version, enabled.
    """

    name: str
    description: str
    version: str
    enabled: bool = False

    @abstractmethod
    def get_info(self) -> SkillInfo:
        """Return a SkillInfo describing this skill."""
        ...

    def is_enabled(self) -> bool:
        return self.enabled

    def can_execute(self) -> bool:
        return False

    def execute(self, arguments: dict[str, Any] | None = None) -> dict[str, Any]:
        raise RuntimeError(f"Skill '{self.name}' does not expose an executable entrypoint.")
