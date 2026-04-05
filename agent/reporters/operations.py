from typing import Any

from tools.utils import truncate_list

from .registry import register_raw_detail


@register_raw_detail("backup_folder")
def render_backup_folder(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    lines.append(f"\n  Source      : {raw.get('source', '')}")
    lines.append(f"  Destination : {raw.get('destination', '')}")
    lines.append(f"  Files       : {raw.get('file_count', 0)}")
    lines.append(f"  Size        : {raw.get('total_size_human', '')}")
    if confirmed and raw.get("success"):
        lines.append("  Backup      : COMPLETED")
    elif not confirmed:
        lines.append(f"  Note        : {raw.get('message', '')}")


@register_raw_detail("convert_video_to_mp3")
def render_convert_video_to_mp3(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    lines.append(f"\n  Input  : {raw.get('video_path', '')}")
    lines.append(f"  Output : {raw.get('output_path', '')}")
    if not confirmed:
        lines.append(f"  Note   : {raw.get('message', '')}")


@register_raw_detail("compress_images")
def render_compress_images(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    planned = raw.get("planned", [])
    compressed = raw.get("compressed", [])
    lines.append(f"\n  Quality : {raw.get('quality', '?')}%")
    if confirmed:
        lines.append(f"  Compressed : {len(compressed)} image(s)")
        return

    lines.append(f"  Would compress : {len(planned)} image(s)")
    if planned:
        shown, extra = truncate_list(planned, 5)
        for path in shown:
            lines.append(f"    • {path.split('/')[-1]}")
        if extra:
            lines.append(f"    ... and {extra} more")


@register_raw_detail("safe_rename_files")
def render_safe_rename_files(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    planned = raw.get("planned_renames", [])
    renamed = raw.get("renamed", [])
    if confirmed:
        lines.append(f"\n  Renamed : {len(renamed)} file(s)")
        return

    lines.append(f"\n  Would rename : {len(planned)} file(s)")
    shown, extra = truncate_list(planned, 8)
    for rename in shown:
        src = rename.get("source", "").split("/")[-1]
        dst = rename.get("destination", "").split("/")[-1]
        lines.append(f"    {src}  →  {dst}")
    if extra:
        lines.append(f"    ... and {extra} more")


@register_raw_detail("safe_move_files")
def render_safe_move_files(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    moved = raw.get("moved", [])
    planned = raw.get("planned", {})
    if confirmed:
        lines.append(f"\n  Moved : {len(moved)} item(s)")
        return

    if isinstance(planned, dict):
        src = planned.get("source", "").split("/")[-1]
        dst = planned.get("destination", "")
        lines.append(f"\n  Would move : {src}  →  {dst}")


@register_raw_detail("schedule_create")
def render_schedule_create(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    schedule = raw.get("schedule", {})
    validation = schedule.get("creation_validation", {})
    lines.append(f"\n  Schedule ID : {schedule.get('id', '')}")
    lines.append(f"  Interval    : {schedule.get('interval', '')}")
    lines.append(f"  Command     : {schedule.get('target_command', '')}")
    if validation:
        status = "valid" if validation.get("ok") else "invalid"
        lines.append(f"  Validation  : {status}")
        if validation.get("intent"):
            lines.append(f"  Target      : {validation.get('intent')}")
        if validation.get("risk_level"):
            lines.append(f"  Risk        : {str(validation.get('risk_level')).upper()}")


@register_raw_detail("schedule_list")
def render_schedule_list(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    schedules = raw.get("schedules", [])
    invalid_count = raw.get("invalid_count", 0)
    lines.append(f"\n  Schedules   : {len(schedules)}")
    lines.append(f"  Invalid     : {invalid_count}")
    if not schedules:
        lines.append("\n  No schedules found.")
        return

    shown, extra = truncate_list(schedules, 10)
    for item in shown:
        runtime_validation = item.get("runtime_validation", {})
        marker = "✓" if runtime_validation.get("ok") else "✗"
        interval = item.get("interval", "?")
        command = item.get("target_command", "")
        lines.append(f"    {marker} {item.get('id', '')}  [{interval}]  {command}")
        if not runtime_validation.get("ok") and runtime_validation.get("error"):
            lines.append(f"      invalid: {runtime_validation.get('error')}")
    if extra:
        lines.append(f"    ... and {extra} more")


@register_raw_detail("schedule_delete")
def render_schedule_delete(lines: list[str], raw: dict[str, Any], confirmed: bool) -> None:
    if raw.get("success"):
        lines.append(f"\n  Deleted schedule : {raw.get('deleted_id', '')}")
    else:
        lines.append(f"\n  Delete failed    : {raw.get('error', 'Unknown error')}")
