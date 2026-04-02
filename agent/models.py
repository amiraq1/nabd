from dataclasses import dataclass, field
from enum import Enum
from typing import Any


class RiskLevel(str, Enum):
    LOW = "low"
    MEDIUM = "medium"
    HIGH = "high"


class OperationStatus(str, Enum):
    SUCCESS = "success"
    FAILURE = "failure"
    PARTIAL = "partial"
    SKIPPED = "skipped"
    CANCELLED = "cancelled"


@dataclass
class ParsedIntent:
    intent: str
    source_path: str | None = None
    target_path: str | None = None
    url: str | None = None
    app_name: str | None = None
    query: str | None = None
    options: dict[str, Any] = field(default_factory=dict)
    risk_level: RiskLevel = RiskLevel.LOW
    requires_confirmation: bool = False
    raw_command: str = ""


@dataclass
class ToolAction:
    tool_name: str
    function_name: str
    arguments: dict[str, Any] = field(default_factory=dict)


@dataclass
class ExecutionPlan:
    intent: str
    risk_level: RiskLevel
    requires_confirmation: bool
    dry_run: bool
    actions: list[ToolAction] = field(default_factory=list)
    preview_summary: str = ""


@dataclass
class ExecutionResult:
    status: OperationStatus
    message: str
    details: list[str] = field(default_factory=list)
    affected_paths: list[str] = field(default_factory=list)
    errors: list[str] = field(default_factory=list)
    raw_results: list[dict[str, Any]] = field(default_factory=list)
    opened_target: str | None = None
    extracted_text_summary: str | None = None
    listed_links: list[str] = field(default_factory=list)
