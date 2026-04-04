from typing import Any

from agent.models import ExecutionPlan, OperationStatus, ParsedIntent
from agent.reporters import (
    _RAW_DETAIL_RENDERERS,
    append_ai_result as _append_ai_result,
    get_raw_detail_renderer,
    register_raw_detail as _register_raw_detail,
    tls_fallback_lines as _tls_fallback_lines,
)
from tools.utils import truncate_list

SEPARATOR = "─" * 55


def _section(title: str) -> str:
    return f"\n{SEPARATOR}\n  {title}\n{SEPARATOR}"


def report_parsed_intent(intent: ParsedIntent) -> str:
    lines = [_section("PARSED INTENT")]
    lines.append(f"  Intent       : {intent.intent}")
    lines.append(f"  Source Path  : {intent.source_path or '(not specified)'}")
    if intent.target_path:
        lines.append(f"  Target Path  : {intent.target_path}")
    lines.append(f"  Risk Level   : {intent.risk_level.value.upper()}")
    lines.append(f"  Needs Confirm: {'Yes' if intent.requires_confirmation else 'No'}")
    if intent.options:
        for k, v in intent.options.items():
            lines.append(f"  Option [{k}] : {v}")
    return "\n".join(lines)


def report_plan(plan: ExecutionPlan) -> str:
    lines = [_section("EXECUTION PLAN")]
    lines.append(f"  Intent     : {plan.intent}")
    lines.append(f"  Risk Level : {plan.risk_level.value.upper()}")
    lines.append(f"  Dry Run    : {'Yes — preview only, no changes' if plan.dry_run else 'No'}")
    lines.append(f"  Preview    : {plan.preview_summary}")
    lines.append(f"  Actions    : {len(plan.actions)}")
    for i, action in enumerate(plan.actions, 1):
        lines.append(f"    [{i}] {action.tool_name}.{action.function_name}({_fmt_args(action.arguments)})")
    return "\n".join(lines)


def _fmt_args(args: dict[str, Any]) -> str:
    parts = []
    for k, v in args.items():
        if isinstance(v, str) and len(v) > 40:
            v = v[:37] + "..."
        parts.append(f"{k}={v!r}")
    return ", ".join(parts)


def report_result(result: Any, intent_name: str, confirmed: bool) -> str:
    lines = [_section("RESULT")]
    status_icon = {
        OperationStatus.SUCCESS: "✓",
        OperationStatus.FAILURE: "✗",
        OperationStatus.PARTIAL: "~",
        OperationStatus.SKIPPED: "-",
        OperationStatus.CANCELLED: "⊘",
    }.get(result.status, "?")

    lines.append(f"  Status  : {status_icon} {result.status.value.upper()}")
    lines.append(f"  Message : {result.message}")

    raw_results = getattr(result, "raw_results", [])
    for raw in raw_results:
        _append_raw_details(lines, raw, intent_name, confirmed)

    if result.affected_paths:
        shown, extra = truncate_list(result.affected_paths, 10)
        lines.append("\n  Affected Paths:")
        for path in shown:
            lines.append(f"    • {path}")
        if extra:
            lines.append(f"    ... and {extra} more")

    if result.errors:
        lines.append("\n  Errors:")
        for error in result.errors:
            lines.append(f"    ! {error}")

    return "\n".join(lines)


def _append_raw_details(lines: list[str], raw: dict[str, Any], intent: str, confirmed: bool) -> None:
    renderer = get_raw_detail_renderer(intent)
    if renderer is not None:
        renderer(lines, raw, confirmed)
