"""
Base class and info struct for all Nabd skills.
Skills are optional capability modules. They never execute tool actions directly.
"""
from __future__ import annotations
from abc import ABC, abstractmethod
from dataclasses import dataclass, field


@dataclass
class SkillInfo:
    name: str
    description: str
    version: str
    enabled: bool
    tags: list[str] = field(default_factory=list)


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
