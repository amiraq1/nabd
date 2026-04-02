from typing import Any

from agent.models import ExecutionPlan, OperationStatus, ParsedIntent
from tools.utils import truncate_list


SEPARATOR = "─" * 55


def _section(title: str) -> str:
    return f"\n{SEPARATOR}\n  {title}\n{SEPARATOR}"


def report_parsed_intent(intent: ParsedIntent) -> str:
    lines = [_section("PARSED INTENT")]
    lines.append(f"  Intent       : {intent.intent}")
    lines.append(f"  Source Path  : {intent.source_path or '(not specified)'}")
    lines.append(f"  Target Path  : {intent.target_path or '(not specified)'}")
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
    lines.append(f"  Dry Run    : {'Yes' if plan.dry_run else 'No'}")
    lines.append(f"  Preview    : {plan.preview_summary}")
    lines.append(f"  Actions    : {len(plan.actions)}")
    for i, action in enumerate(plan.actions, 1):
        lines.append(f"    [{i}] {action.tool_name}.{action.function_name}({_fmt_args(action.arguments)})")
    return "\n".join(lines)


def _fmt_args(args: dict) -> str:
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
        for p in shown:
            lines.append(f"    • {p}")
        if extra:
            lines.append(f"    ... and {extra} more")

    if result.errors:
        lines.append("\n  Errors:")
        for e in result.errors:
            lines.append(f"    ! {e}")

    return "\n".join(lines)


def _append_raw_details(lines: list, raw: dict, intent: str, confirmed: bool) -> None:
    if intent == "storage_report":
        lines.append(f"\n  Directory    : {raw.get('directory', '')}")
        lines.append(f"  Total Size   : {raw.get('total_size_human', '')}")
        lines.append(f"  Files        : {raw.get('file_count', 0)}")
        lines.append(f"  Directories  : {raw.get('directory_count', 0)}")
        lines.append(f"  Free Space   : {raw.get('free_space_human', 'N/A')}")
        breakdown = raw.get("category_breakdown", {})
        if breakdown:
            lines.append("  By Category  :")
            for cat, sz in breakdown.items():
                lines.append(f"    • {cat:<14}: {sz}")

    elif intent == "list_large_files":
        files = raw if isinstance(raw, list) else raw.get("files", [])
        lines.append(f"\n  Found {len(files)} large file(s):")
        shown, extra = truncate_list(files, 20)
        for item in shown:
            if isinstance(item, dict):
                lines.append(f"    {item.get('size_human', '?'):>10}  {item.get('path', '')}")
        if extra:
            lines.append(f"    ... and {extra} more")

    elif intent == "organize_folder_by_type":
        moves = raw.get("planned_moves", [])
        skipped = raw.get("skipped", [])
        moved = raw.get("moved", [])
        errors = raw.get("errors", [])
        if confirmed:
            lines.append(f"\n  Moved  : {len(moved)} file(s)")
            lines.append(f"  Errors : {len(errors)}")
        else:
            lines.append(f"\n  Would move  : {len(moves)} file(s)")
            lines.append(f"  Would skip  : {len(skipped)} (already in correct folder)")
            shown, extra = truncate_list(moves, 10)
            for m in shown:
                lines.append(f"    {m.get('source', '')} → {m.get('destination', '')}")
            if extra:
                lines.append(f"    ... and {extra} more")

    elif intent == "find_duplicates":
        groups = raw.get("duplicate_groups", [])
        total_wasted = raw.get("total_wasted_human", "0 B")
        lines.append(f"\n  Duplicate groups : {len(groups)}")
        lines.append(f"  Wasted space     : {total_wasted}")
        shown, extra = truncate_list(groups, 5)
        for g in shown:
            lines.append(f"\n  Group ({g.get('file_size_human', '?')} × {len(g.get('paths', []))} files):")
            for p in g.get("paths", []):
                lines.append(f"    • {p}")
        if extra:
            lines.append(f"  ... and {extra} more groups")

    elif intent == "backup_folder":
        lines.append(f"\n  Source      : {raw.get('source', '')}")
        lines.append(f"  Destination : {raw.get('destination', '')}")
        lines.append(f"  Files       : {raw.get('file_count', 0)}")
        lines.append(f"  Size        : {raw.get('total_size_human', '')}")
        if confirmed and raw.get("success"):
            lines.append("  Backup      : COMPLETED")
        elif not confirmed:
            lines.append(f"  Note        : {raw.get('message', '')}")

    elif intent == "convert_video_to_mp3":
        lines.append(f"\n  Input  : {raw.get('video_path', '')}")
        lines.append(f"  Output : {raw.get('output_path', '')}")
        if not confirmed:
            lines.append(f"  Note   : {raw.get('message', '')}")

    elif intent == "compress_images":
        planned = raw.get("planned", [])
        compressed = raw.get("compressed", [])
        lines.append(f"\n  Quality : {raw.get('quality', '?')}%")
        if confirmed:
            lines.append(f"  Compressed : {len(compressed)} image(s)")
        else:
            lines.append(f"  Would compress : {len(planned)} image(s)")

    elif intent == "safe_rename_files":
        planned = raw.get("planned_renames", [])
        renamed = raw.get("renamed", [])
        if confirmed:
            lines.append(f"\n  Renamed : {len(renamed)} file(s)")
        else:
            lines.append(f"\n  Would rename : {len(planned)} file(s)")
            shown, extra = truncate_list(planned, 10)
            for r in shown:
                lines.append(f"    {r.get('source', '')} → {r.get('destination', '')}")
            if extra:
                lines.append(f"    ... and {extra} more")

    elif intent == "safe_move_files":
        moved = raw.get("moved", [])
        planned = raw.get("planned", {})
        if confirmed:
            lines.append(f"\n  Moved : {len(moved)} item(s)")
        else:
            if isinstance(planned, dict):
                lines.append(f"\n  Would move : {planned.get('source', '')} → {planned.get('destination', '')}")
