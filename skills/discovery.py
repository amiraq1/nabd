"""
Safe filesystem discovery for Nabd skills.

Discovery reads metadata only. It never imports or executes skill code.
"""
from __future__ import annotations

import importlib.util
import os
import re
from dataclasses import dataclass
from types import ModuleType
from typing import Any

from skills.base import SkillBase, SkillInfo

_SAFE_SKILL_NAME_RE = re.compile(r"^[a-z0-9][a-z0-9_-]*$")
_SAFE_ENTRYPOINT_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")
_ALLOWED_ENTRYPOINTS = {"run"}


@dataclass
class SkillValidationResult:
    valid: bool
    skill: SkillBase | None = None
    error: str | None = None


class DiscoveredSkill(SkillBase):
    """
    Filesystem-backed skill discovered from skills/<name>/SKILL.md.

    The module is imported only when execute() is called, never during discovery.
    """

    def __init__(self, info: SkillInfo) -> None:
        self._info = info
        self.name = info.name
        self.description = info.description
        self.version = info.version
        self.enabled = info.enabled

    def get_info(self) -> SkillInfo:
        return self._info

    def can_execute(self) -> bool:
        return self._info.has_python_logic and bool(self._info.entrypoint)

    def execute(self, arguments: dict[str, Any] | None = None) -> dict[str, Any]:
        if not self.can_execute():
            raise RuntimeError(f"Skill '{self.name}' is metadata-only and cannot be executed.")

        logic_path = os.path.join(self._info.path, "skill_logic.py")
        module = _load_skill_module(self.name, logic_path)
        entrypoint = self._info.entrypoint or ""
        fn = getattr(module, entrypoint, None)
        if fn is None or not callable(fn):
            raise RuntimeError(
                f"Skill '{self.name}' entrypoint '{entrypoint}' was not found in skill_logic.py."
            )

        if arguments:
            raise RuntimeError(
                "This phase only supports explicit skill execution without free-form arguments."
            )

        result = fn()
        if isinstance(result, dict):
            payload = dict(result)
        else:
            payload = {"result": result}

        payload.setdefault("success", True)
        payload.setdefault("skill_name", self.name)
        payload.setdefault("entrypoint", entrypoint)
        return payload


def validate_skill(skill_dir: str, skills_root: str) -> SkillValidationResult:
    """
    Validate a single skill directory without importing any Python code.
    """
    safe_root = os.path.realpath(skills_root)
    safe_dir = os.path.realpath(skill_dir)

    if not _is_same_or_descendant(safe_dir, safe_root):
        return SkillValidationResult(False, error="Skill directory is outside the allowed skills root.")

    dir_name = os.path.basename(safe_dir)
    if not _SAFE_SKILL_NAME_RE.fullmatch(dir_name):
        return SkillValidationResult(False, error=f"Unsafe skill directory name: '{dir_name}'.")

    skill_md_path = os.path.join(safe_dir, "SKILL.md")
    if not os.path.isfile(skill_md_path):
        return SkillValidationResult(False, error="Missing required SKILL.md.")

    try:
        metadata, body = parse_skill_markdown(skill_md_path)
    except ValueError as exc:
        return SkillValidationResult(False, error=str(exc))

    required_fields = ("name", "description", "version")
    missing = [field for field in required_fields if not metadata.get(field)]
    if missing:
        return SkillValidationResult(
            False,
            error=f"Missing required metadata field(s): {', '.join(missing)}.",
        )

    skill_name = str(metadata["name"]).strip().lower()
    if skill_name != dir_name:
        return SkillValidationResult(
            False,
            error=f"Skill name '{skill_name}' must exactly match folder name '{dir_name}'.",
        )

    if not _SAFE_SKILL_NAME_RE.fullmatch(skill_name):
        return SkillValidationResult(False, error=f"Unsafe skill name: '{skill_name}'.")

    logic_path = os.path.join(safe_dir, "skill_logic.py")
    has_python_logic = os.path.isfile(logic_path)
    requires_python = _coerce_bool(metadata.get("requires_python", False))
    entrypoint = str(metadata.get("entrypoint", "")).strip() or None

    if has_python_logic != requires_python:
        if has_python_logic:
            return SkillValidationResult(
                False,
                error="skill_logic.py exists but metadata does not declare requires_python: true.",
            )
        return SkillValidationResult(
            False,
            error="requires_python: true declared but skill_logic.py is missing.",
        )

    if requires_python:
        if entrypoint is None:
            return SkillValidationResult(False, error="Python-backed skills must declare an entrypoint.")
        if not _SAFE_ENTRYPOINT_RE.fullmatch(entrypoint):
            return SkillValidationResult(False, error=f"Unsafe entrypoint name: '{entrypoint}'.")
        if entrypoint not in _ALLOWED_ENTRYPOINTS:
            return SkillValidationResult(
                False,
                error=f"Entrypoint '{entrypoint}' is not allowed. Allowed entrypoints: {sorted(_ALLOWED_ENTRYPOINTS)}.",
            )
    elif entrypoint is not None:
        return SkillValidationResult(
            False,
            error="Metadata-only skills must not declare an entrypoint.",
        )

    info = SkillInfo(
        name=skill_name,
        description=str(metadata["description"]).strip(),
        version=str(metadata["version"]).strip(),
        enabled=True,
        tags=_parse_tags(metadata.get("tags", "")),
        path=safe_dir,
        author=_optional_text(metadata.get("author")),
        requires_python=requires_python,
        has_python_logic=has_python_logic,
        entrypoint=entrypoint,
        usage=_extract_markdown_section(body, "Usage"),
        instructions=_extract_markdown_section(body, "Instructions"),
        source="filesystem",
    )
    return SkillValidationResult(True, skill=DiscoveredSkill(info))


def parse_skill_markdown(skill_md_path: str) -> tuple[dict[str, Any], str]:
    with open(skill_md_path, encoding="utf-8") as handle:
        raw = handle.read().replace("\r\n", "\n")

    if not raw.startswith("---\n"):
        raise ValueError("SKILL.md must begin with '---' front matter.")

    closing_marker = raw.find("\n---", 4)
    if closing_marker == -1:
        raise ValueError("SKILL.md front matter is missing a closing '---'.")

    metadata_block = raw[4:closing_marker]
    body = raw[closing_marker + 4 :].lstrip("\n")
    metadata: dict[str, Any] = {}

    for line_number, line in enumerate(metadata_block.splitlines(), start=1):
        stripped = line.strip()
        if not stripped:
            continue
        if ":" not in stripped:
            raise ValueError(f"Malformed metadata line {line_number}: '{line}'.")
        key, value = stripped.split(":", 1)
        key = key.strip().lower()
        value = value.strip()
        if not key:
            raise ValueError(f"Malformed metadata line {line_number}: '{line}'.")
        if key in metadata:
            raise ValueError(f"Duplicate metadata field: '{key}'.")
        metadata[key] = _strip_quotes(value)

    return metadata, body


def _load_skill_module(skill_name: str, logic_path: str) -> ModuleType:
    module_name = f"nabd_skill_{skill_name.replace('-', '_')}"
    spec = importlib.util.spec_from_file_location(module_name, logic_path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"Could not load module for skill '{skill_name}'.")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _strip_quotes(value: str) -> str:
    if len(value) >= 2 and value[0] == value[-1] and value[0] in {"'", '"'}:
        return value[1:-1]
    return value


def _coerce_bool(value: Any) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        return value.strip().lower() in {"1", "true", "yes", "on"}
    return bool(value)


def _parse_tags(value: Any) -> list[str]:
    if isinstance(value, list):
        return [str(item).strip() for item in value if str(item).strip()]
    if not value:
        return []
    raw = str(value).strip()
    if raw.startswith("[") and raw.endswith("]"):
        raw = raw[1:-1]
    tags = []
    for item in raw.split(","):
        cleaned = _strip_quotes(item.strip())
        if cleaned:
            tags.append(cleaned)
    return tags


def _optional_text(value: Any) -> str | None:
    if value is None:
        return None
    cleaned = str(value).strip()
    return cleaned or None


def _extract_markdown_section(body: str, heading: str) -> str | None:
    pattern = re.compile(
        rf"^##\s+{re.escape(heading)}\s*$([\s\S]*?)(?=^##\s+|\Z)",
        re.MULTILINE,
    )
    match = pattern.search(body)
    if not match:
        return None
    cleaned = match.group(1).strip()
    return cleaned or None


def _is_same_or_descendant(path: str, base: str) -> bool:
    return path == base or path.startswith(base + os.sep)
